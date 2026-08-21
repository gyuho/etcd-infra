package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/providers/compute"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// Create provisions an EC2 instance.
func (m *Manager) Create(ctx context.Context, req compute.CreateRequest) (compute.Instance, error) {
	if m.ec2 == nil {
		return nil, errors.New("aws: ec2 client is nil")
	}

	op := req.Op
	if err := validateCreateOp(op); err != nil {
		return nil, err
	}

	input, err := m.buildRunInstancesInput(ctx, op)
	if err != nil {
		return nil, err
	}

	// InsufficientInstanceCapacity is per-AZ and transient: alternate between
	// the VPC's subnets on each attempt, and keep retrying for a few minutes —
	// AZ capacity replenishes on that order, while the SDK's internal retries
	// exhaust in seconds.
	var out *ec2.RunInstancesOutput
	capacityDeadline := time.Now().Add(3 * time.Minute)
	alternatesLoaded := false
	var alternates []string
	for {
		out, err = m.ec2.RunInstances(ctx, input)
		if err == nil {
			break
		}
		if !isInsufficientCapacityError(err) || time.Now().After(capacityDeadline) {
			return nil, fmt.Errorf("aws: run instances: %w", err)
		}
		if !alternatesLoaded {
			alternatesLoaded = true
			alternates = m.otherSubnetIDs(ctx, op.VPCID, aws.ToString(input.SubnetId))
		}
		if len(alternates) > 0 {
			alt := alternates[0]
			alternates = append(alternates[1:], alt)
			logutil.S().Infow("aws: insufficient capacity, retrying in another subnet", "subnet", alt)
			op.SubnetID = alt
			input, err = m.buildRunInstancesInput(ctx, op)
			if err != nil {
				return nil, err
			}
			continue
		}
		logutil.S().Infow("aws: insufficient capacity, retrying", "error", err)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("aws: run instances: %w", ctx.Err())
		case <-time.After(30 * time.Second):
		}
	}
	if len(out.Instances) == 0 {
		return nil, errors.New("aws: run instances returned no instances")
	}

	if cfg, ok := op.ProviderConfig.(CreateConfig); ok && cfg.DataVolumeID != "" {
		if _, err := m.ec2.AttachVolume(ctx, &ec2.AttachVolumeInput{
			InstanceId: out.Instances[0].InstanceId,
			VolumeId:   aws.String(cfg.DataVolumeID),
			Device:     aws.String(dataVolumeDeviceName),
		}); err != nil {
			return nil, fmt.Errorf("aws: attach data volume %s to %s: %w",
				cfg.DataVolumeID, aws.ToString(out.Instances[0].InstanceId), err)
		}
	}

	return m.instanceInfoFromLaunch(out.Instances[0], op), nil
}

// validateCreateOp checks that the required fields are set on the compute Op.
func validateCreateOp(op compute.Op) error {
	if op.VPCID == "" {
		return errors.New("aws: vpc id is required")
	}
	if op.Name == "" {
		return errors.New("aws: name is required")
	}
	if op.Image == "" {
		return errors.New("aws: image is required")
	}
	if op.Size == "" {
		return errors.New("aws: size is required")
	}
	return nil
}

// buildRunInstancesInput resolves network dependencies and constructs the EC2
// RunInstancesInput.
func (m *Manager) buildRunInstancesInput(ctx context.Context, op compute.Op) (*ec2.RunInstancesInput, error) {
	subnetID, err := m.resolveSubnet(ctx, op)
	if err != nil {
		return nil, err
	}
	securityGroupIDs, err := m.resolveSecurityGroups(ctx, op)
	if err != nil {
		return nil, err
	}

	input := &ec2.RunInstancesInput{
		ImageId:      aws.String(op.Image),
		InstanceType: types.InstanceType(op.Size),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		SubnetId:     aws.String(subnetID),
		SecurityGroupIds: func() []string {
			if len(securityGroupIDs) == 0 {
				return nil
			}
			return securityGroupIDs
		}(),
		TagSpecifications: buildTagSpecifications(op),
	}

	if cfg, ok := op.ProviderConfig.(CreateConfig); ok {
		if cfg.PrivateIPAddress != "" {
			// Pinned IP preserves the member's identity (advertise URLs embed
			// the private IP) across standalone replacement.
			input.PrivateIpAddress = aws.String(cfg.PrivateIPAddress)
		}
		if cfg.DataVolumeSizeGB > 0 && cfg.DataVolumeID == "" {
			// A dedicated data volume that survives termination: replacement
			// reattaches it instead of losing /var/lib/etcd with the root
			// volume.
			input.BlockDeviceMappings = append(input.BlockDeviceMappings, types.BlockDeviceMapping{
				DeviceName: aws.String(dataVolumeDeviceName),
				Ebs: &types.EbsBlockDevice{
					VolumeType:          types.VolumeTypeGp3,
					VolumeSize:          aws.Int32(int32(cfg.DataVolumeSizeGB)), //nolint:gosec // bounded by flag validation
					DeleteOnTermination: aws.Bool(false),
				},
			})
		}
	}

	if op.UserData != "" {
		input.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(op.UserData)))
	}
	if len(op.SSHKeys) > 0 {
		input.KeyName = aws.String(op.SSHKeys[0])
	}
	if cfg, ok := op.ProviderConfig.(CreateConfig); ok {
		switch profile := strings.TrimSpace(cfg.IAMInstanceProfile); {
		case strings.HasPrefix(profile, "arn:"):
			input.IamInstanceProfile = &types.IamInstanceProfileSpecification{Arn: aws.String(profile)}
		case profile != "":
			input.IamInstanceProfile = &types.IamInstanceProfileSpecification{Name: aws.String(profile)}
		}
	}

	return input, nil
}

// instanceInfoFromLaunch converts a freshly launched EC2 instance into an instanceInfo.
func (m *Manager) instanceInfoFromLaunch(instance types.Instance, op compute.Op) *instanceInfo {
	instanceID := aws.ToString(instance.InstanceId)
	publicIPv4 := aws.ToString(instance.PublicIpAddress)
	privateIPv4 := aws.ToString(instance.PrivateIpAddress)

	var az string
	if instance.Placement != nil {
		az = aws.ToString(instance.Placement.AvailabilityZone)
	}

	sshUser := op.SSHUser
	if sshUser == "" {
		sshUser = defaultSSHUser
	}
	sshPort := op.SSHPort
	if sshPort == 0 {
		sshPort = 22
	}

	logutil.S().Infow("created aws instance", "instance_id", instanceID, "public_ipv4", publicIPv4)

	return &instanceInfo{
		id:               instanceID,
		publicIPv4:       publicIPv4,
		privateIPv4:      privateIPv4,
		state:            mapEC2State(instance.State),
		tags:             extractTags(instance.Tags),
		availabilityZone: az,
		instanceType:     string(instance.InstanceType),
		ec2:              m.ec2,
		ssm:              m.ssm,
		ssh: compute.SSHConfig{
			User:           sshUser,
			Port:           sshPort,
			PrivateKeyPath: op.SSHPrivateKeyPath,
		},
	}
}

// Delete terminates an EC2 instance.
func (m *Manager) Delete(ctx context.Context, req compute.DeleteRequest) (compute.DeleteResult, error) {
	if m.ec2 == nil {
		return compute.DeleteResult{}, errors.New("aws: ec2 client is nil")
	}
	id := req.ID
	if strings.TrimSpace(id) == "" {
		return compute.DeleteResult{}, errors.New("aws: instance id is required")
	}
	_, err := m.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return compute.DeleteResult{}, fmt.Errorf("aws: terminate instance %s: %w", id, err)
	}
	return compute.DeleteResult{ID: id, Deleted: true}, nil
}

// ReplaceMachine replaces one machine while preserving its identity. For an
// ASG-owned instance it terminates the member without decrementing desired
// capacity, and the ASG starts the replacement asynchronously. For a
// standalone instance it captures the launch spec, terminates the instance,
// recreates it with the same private IP and tags, and reattaches the data
// volume recorded at launch, so the member keeps its identity and its data.
func (m *Manager) ReplaceMachine(ctx context.Context, req compute.ReplaceRequest) (compute.ReplaceResult, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return compute.ReplaceResult{}, errors.New("aws: instance id is required")
	}
	if m.asg == nil {
		return m.replaceStandalone(ctx, id)
	}
	membership, err := m.asg.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		if m.ec2 != nil {
			// The caller cannot query ASG (for example the least-privilege
			// policy grants no autoscaling actions); standalone replacement
			// is the only path available to it.
			logutil.S().Infow("aws: ASG membership lookup failed, using standalone replacement", "instance_id", id, "error", err)
			return m.replaceStandalone(ctx, id)
		}
		return compute.ReplaceResult{}, fmt.Errorf("aws: describe ASG membership for instance %s: %w", id, err)
	}
	if len(membership.AutoScalingInstances) != 1 ||
		strings.TrimSpace(aws.ToString(membership.AutoScalingInstances[0].InstanceId)) != id ||
		strings.TrimSpace(aws.ToString(membership.AutoScalingInstances[0].AutoScalingGroupName)) == "" {
		// Not ASG-managed: standalone replacement preserves identity
		// (pinned private IP, same tags) and the preserved data volume.
		return m.replaceStandalone(ctx, id)
	}
	lifecycleState := strings.TrimSpace(aws.ToString(membership.AutoScalingInstances[0].LifecycleState))
	if !strings.EqualFold(lifecycleState, "InService") {
		return compute.ReplaceResult{}, fmt.Errorf("aws: instance %s lifecycle state %q cannot be replaced: %w", id, lifecycleState, compute.ErrNotSupported)
	}
	group := strings.TrimSpace(aws.ToString(membership.AutoScalingInstances[0].AutoScalingGroupName))
	_, err = m.asg.TerminateInstanceInAutoScalingGroup(ctx, &autoscaling.TerminateInstanceInAutoScalingGroupInput{
		InstanceId:                     aws.String(id),
		ShouldDecrementDesiredCapacity: aws.Bool(false),
	})
	if err != nil {
		return compute.ReplaceResult{}, fmt.Errorf("aws: replace ASG instance %s: %w", id, err)
	}
	return compute.ReplaceResult{PreviousID: id, Group: group}, nil
}

// replaceStandalone replaces a standalone (non-ASG) instance while
// preserving identity: same private IP, subnet, security groups, AMI,
// instance profile, key, and tags. The data volume recorded at launch
// survives termination (DeleteOnTermination=false) and is reattached, so the
// member's data dir is intact when the replacement boots.
func (m *Manager) replaceStandalone(ctx context.Context, id string) (compute.ReplaceResult, error) {
	if m.ec2 == nil {
		return compute.ReplaceResult{}, errors.New("aws: ec2 client is nil")
	}

	spec, err := m.launchSpecForReplace(ctx, id)
	if err != nil {
		return compute.ReplaceResult{}, err
	}

	if _, err := m.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}}); err != nil {
		return compute.ReplaceResult{}, fmt.Errorf("aws: terminate instance %s for replacement: %w", id, err)
	}
	if err := m.WaitForTerminated(ctx, id, 5*time.Minute); err != nil {
		return compute.ReplaceResult{}, err
	}
	if spec.dataVolumeID != "" {
		if err := m.waitForVolumeAvailable(ctx, spec.dataVolumeID, 5*time.Minute); err != nil {
			return compute.ReplaceResult{}, err
		}
	}

	// The terminated instance's network interface can hold the private IP
	// briefly after the instance reports terminated; retry while EC2 reports
	// the address in use.
	var runOut *ec2.RunInstancesOutput
	relaunchDeadline := time.Now().Add(3 * time.Minute)
	for {
		runOut, err = m.ec2.RunInstances(ctx, spec.input)
		if err == nil {
			break
		}
		if !isIPAddressInUseError(err) {
			return compute.ReplaceResult{}, fmt.Errorf("aws: relaunch replacement for %s: %w", id, err)
		}
		if time.Now().After(relaunchDeadline) {
			return compute.ReplaceResult{}, fmt.Errorf("aws: private IP %s still in use 3 minutes after %s terminated: %w",
				aws.ToString(spec.input.PrivateIpAddress), id, err)
		}
		select {
		case <-ctx.Done():
			return compute.ReplaceResult{}, fmt.Errorf("aws: relaunch replacement for %s: %w", id, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	if len(runOut.Instances) == 0 {
		return compute.ReplaceResult{}, errors.New("aws: run instances returned no instances")
	}
	newID := aws.ToString(runOut.Instances[0].InstanceId)

	if spec.dataVolumeID != "" {
		// AttachVolume requires a running instance; RunInstances returns it
		// pending. On any failure from here, terminate the replacement so it
		// cannot leak unrecorded.
		if err := m.waitForRunningState(ctx, newID, 5*time.Minute); err != nil {
			_, _ = m.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{newID}})
			return compute.ReplaceResult{}, err
		}
		if _, err := m.ec2.AttachVolume(ctx, &ec2.AttachVolumeInput{
			InstanceId: aws.String(newID),
			VolumeId:   aws.String(spec.dataVolumeID),
			Device:     aws.String(dataVolumeDeviceName),
		}); err != nil {
			_, _ = m.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{newID}})
			return compute.ReplaceResult{}, fmt.Errorf("aws: attach preserved data volume %s to %s: %w", spec.dataVolumeID, newID, err)
		}
	}
	return compute.ReplaceResult{PreviousID: id, ID: newID}, nil
}

// waitForRunningState polls until the instance reaches the EC2 running state
// (AttachVolume requires running; SSM readiness is a separate, later concern
// handled by WaitForReady).
func (m *Manager) waitForRunningState(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		inst, err := m.Get(ctx, id)
		if err == nil && inst.State() == compute.InstanceStateRunning {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("aws: instance %s not running after %v", id, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("aws: wait for instance %s running: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// replaceLaunchSpec is the captured launch configuration of an instance
// being replaced.
type replaceLaunchSpec struct {
	input        *ec2.RunInstancesInput
	dataVolumeID string
}

// launchSpecForReplace describes the instance and derives the relaunch input.
func (m *Manager) launchSpecForReplace(ctx context.Context, id string) (replaceLaunchSpec, error) {
	out, err := m.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return replaceLaunchSpec{}, fmt.Errorf("aws: describe instance %s for replacement: %w", id, err)
	}
	var inst *types.Instance
	for i := range out.Reservations {
		for j := range out.Reservations[i].Instances {
			if aws.ToString(out.Reservations[i].Instances[j].InstanceId) == id {
				inst = &out.Reservations[i].Instances[j]
			}
		}
	}
	if inst == nil {
		return replaceLaunchSpec{}, fmt.Errorf("aws: instance %s not found for replacement", id)
	}
	if aws.ToString(inst.PrivateIpAddress) == "" {
		return replaceLaunchSpec{}, fmt.Errorf("aws: instance %s has no private IPv4 address to preserve", id)
	}

	securityGroupIDs := make([]string, 0, len(inst.SecurityGroups))
	for _, sg := range inst.SecurityGroups {
		if id := aws.ToString(sg.GroupId); id != "" {
			securityGroupIDs = append(securityGroupIDs, id)
		}
	}
	sort.Strings(securityGroupIDs)

	var dataVolumeID string
	for _, mapping := range inst.BlockDeviceMappings {
		if aws.ToString(mapping.DeviceName) == dataVolumeDeviceName && mapping.Ebs != nil {
			dataVolumeID = aws.ToString(mapping.Ebs.VolumeId)
		}
	}

	tags := make([]types.Tag, 0, len(inst.Tags))
	for _, tag := range inst.Tags {
		key := aws.ToString(tag.Key)
		if key == "" || strings.HasPrefix(key, "aws:") {
			continue
		}
		tags = append(tags, types.Tag{Key: aws.String(key), Value: tag.Value})
	}
	sort.Slice(tags, func(i, j int) bool { return aws.ToString(tags[i].Key) < aws.ToString(tags[j].Key) })

	input := &ec2.RunInstancesInput{
		ImageId:          inst.ImageId,
		InstanceType:     inst.InstanceType,
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		SubnetId:         inst.SubnetId,
		PrivateIpAddress: inst.PrivateIpAddress,
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tags},
		},
	}
	if len(securityGroupIDs) > 0 {
		input.SecurityGroupIds = securityGroupIDs
	}
	if keyName := aws.ToString(inst.KeyName); keyName != "" {
		input.KeyName = aws.String(keyName)
	}
	if inst.IamInstanceProfile != nil && aws.ToString(inst.IamInstanceProfile.Arn) != "" {
		input.IamInstanceProfile = &types.IamInstanceProfileSpecification{Arn: inst.IamInstanceProfile.Arn}
	}
	return replaceLaunchSpec{input: input, dataVolumeID: dataVolumeID}, nil
}

// WaitForTerminated polls until the instance reports terminated. Exported
// for teardown flows that must sequence behind termination (for example
// deleting detached data volumes).
func (m *Manager) WaitForTerminated(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		inst, err := m.Get(ctx, id)
		if err == nil && inst.State() == compute.InstanceStateTerminated {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("aws: instance %s not terminated after %v", id, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("aws: wait for instance %s terminated: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitForVolumeAvailable polls until the volume is detached and available.
func (m *Manager) waitForVolumeAvailable(ctx context.Context, volumeID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		out, err := m.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
		if err == nil && len(out.Volumes) == 1 && out.Volumes[0].State == types.VolumeStateAvailable {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("aws: volume %s not available after %v", volumeID, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("aws: wait for volume %s available: %w", volumeID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// DeleteClusterVolumes deletes the tagged data volumes a replaceable cluster
// leaves behind after its instances terminate. Missing volumes are ignored;
// volumes still attached are waited out.
func (m *Manager) DeleteClusterVolumes(ctx context.Context, cluster string) error {
	if m.ec2 == nil {
		return errors.New("aws: ec2 client is nil")
	}
	cluster = strings.TrimSpace(cluster)
	if cluster == "" {
		return errors.New("aws: cluster name is required")
	}
	out, err := m.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		Filters: []types.Filter{{Name: aws.String("tag:etcd-infra.cluster"), Values: []string{cluster}}},
	})
	if err != nil {
		return fmt.Errorf("aws: describe data volumes for cluster %s: %w", cluster, err)
	}
	var errs []error
	for _, volume := range out.Volumes {
		volumeID := aws.ToString(volume.VolumeId)
		if volumeID == "" || volume.State == types.VolumeStateDeleted {
			continue
		}
		if volume.State != types.VolumeStateAvailable {
			if err := m.waitForVolumeAvailable(ctx, volumeID, 5*time.Minute); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if _, err := m.ec2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)}); err != nil {
			// Already gone is the desired end state (a raced retry, or a volume
			// deleted between the describe and the delete).
			if !isVolumeNotFoundError(err) {
				errs = append(errs, fmt.Errorf("aws: delete data volume %s: %w", volumeID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// DataVolumeID returns the ID of the instance's dedicated data volume, or ""
// when the instance has none (not launched with a data volume).
func (m *Manager) DataVolumeID(ctx context.Context, id string) (string, error) {
	if m.ec2 == nil {
		return "", errors.New("aws: ec2 client is nil")
	}
	out, err := m.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return "", fmt.Errorf("aws: describe instance %s: %w", id, err)
	}
	for i := range out.Reservations {
		for j := range out.Reservations[i].Instances {
			inst := &out.Reservations[i].Instances[j]
			if aws.ToString(inst.InstanceId) != id {
				continue
			}
			for _, mapping := range inst.BlockDeviceMappings {
				if aws.ToString(mapping.DeviceName) == dataVolumeDeviceName && mapping.Ebs != nil {
					return aws.ToString(mapping.Ebs.VolumeId), nil
				}
			}
			return "", nil
		}
	}
	return "", fmt.Errorf("aws: instance %s not found", id)
}

// Stop stops an EC2 instance.
func (m *Manager) Stop(ctx context.Context, req compute.PowerRequest) error {
	if m.ec2 == nil {
		return errors.New("aws: ec2 client is nil")
	}
	id := req.ID
	if strings.TrimSpace(id) == "" {
		return errors.New("aws: instance id is required")
	}
	_, err := m.ec2.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return fmt.Errorf("aws: stop instance %s: %w", id, err)
	}
	return nil
}

// Start starts an EC2 instance.
func (m *Manager) Start(ctx context.Context, req compute.PowerRequest) error {
	if m.ec2 == nil {
		return errors.New("aws: ec2 client is nil")
	}
	id := req.ID
	if strings.TrimSpace(id) == "" {
		return errors.New("aws: instance id is required")
	}
	_, err := m.ec2.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return fmt.Errorf("aws: start instance %s: %w", id, err)
	}
	return nil
}

func (m *Manager) resolveSubnet(ctx context.Context, op compute.Op) (string, error) {
	if op.SubnetID != "" {
		return op.SubnetID, nil
	}

	out, err := m.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{op.VPCID}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("aws: describe subnets: %w", err)
	}
	if len(out.Subnets) == 0 {
		return "", fmt.Errorf("aws: no subnets found in vpc %s", op.VPCID)
	}
	subnetIDs := make([]string, 0, len(out.Subnets))
	for i := range out.Subnets {
		subnet := &out.Subnets[i]
		if subnet.SubnetId != nil {
			subnetIDs = append(subnetIDs, *subnet.SubnetId)
		}
	}
	if len(subnetIDs) == 0 {
		return "", fmt.Errorf("aws: no subnets with ids found in vpc %s", op.VPCID)
	}
	sort.Strings(subnetIDs)
	return subnetIDs[0], nil
}

func (m *Manager) resolveSecurityGroups(ctx context.Context, op compute.Op) ([]string, error) {
	if len(op.SecurityGroupIDs) > 0 {
		return op.SecurityGroupIDs, nil
	}

	out, err := m.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{op.VPCID}},
			{Name: aws.String("group-name"), Values: []string{"default"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("aws: describe security groups: %w", err)
	}
	if len(out.SecurityGroups) == 0 {
		return nil, fmt.Errorf("aws: no default security group found in vpc %s", op.VPCID)
	}
	groupID := aws.ToString(out.SecurityGroups[0].GroupId)
	if groupID == "" {
		return nil, fmt.Errorf("aws: default security group missing id in vpc %s", op.VPCID)
	}
	return []string{groupID}, nil
}

func buildTagSpecifications(op compute.Op) []types.TagSpecification {
	if op.Name == "" && len(op.Tags) == 0 {
		return nil
	}
	merged := make(map[string]string, len(op.Tags)+1)
	maps.Copy(merged, op.Tags)
	if op.Name != "" {
		if _, ok := merged["Name"]; !ok {
			merged["Name"] = op.Name
		}
	}
	tags := make([]types.Tag, 0, len(merged))
	for k, v := range merged {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	sort.Slice(tags, func(i, j int) bool { return aws.ToString(tags[i].Key) < aws.ToString(tags[j].Key) })
	specs := []types.TagSpecification{{ResourceType: types.ResourceTypeInstance, Tags: tags}}
	if cfg, ok := op.ProviderConfig.(CreateConfig); ok && cfg.DataVolumeSizeGB > 0 {
		// Tag the data volume too: teardown and replace find it by the
		// cluster tag, and the least-privilege policy gates volume operations
		// on it.
		specs = append(specs, types.TagSpecification{ResourceType: types.ResourceTypeVolume, Tags: tags})
	}
	return specs
}

// dataVolumeDeviceName is the block device name used for the dedicated etcd
// data volume at launch and reattach.
const dataVolumeDeviceName = "/dev/xvdf"

// otherSubnetIDs lists the VPC's subnets other than the given one, for
// capacity failover.
func (m *Manager) otherSubnetIDs(ctx context.Context, vpcID, current string) []string {
	out, err := m.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}},
	})
	if err != nil {
		return nil
	}
	var ids []string
	for _, subnet := range out.Subnets {
		id := aws.ToString(subnet.SubnetId)
		if id != "" && id != current {
			ids = append(ids, id)
		}
	}
	return ids
}

// isInsufficientCapacityError reports whether the error is EC2's
// InsufficientInstanceCapacity.
func isInsufficientCapacityError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InsufficientInstanceCapacity" {
		return true
	}
	return strings.Contains(err.Error(), "InsufficientInstanceCapacity")
}

// isVolumeNotFoundError reports whether the error is EC2's
// InvalidVolume.NotFound.
func isVolumeNotFoundError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidVolume.NotFound" {
		return true
	}
	return strings.Contains(err.Error(), "InvalidVolume.NotFound")
}

// isIPAddressInUseError reports whether the error is EC2's
// InvalidIPAddress.InUse, raised when a recently terminated instance's
// network interface still holds the address.
func isIPAddressInUseError(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidIPAddress.InUse" {
		return true
	}
	return strings.Contains(err.Error(), "InvalidIPAddress.InUse")
}
