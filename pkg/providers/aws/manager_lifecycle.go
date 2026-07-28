package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/providers/compute"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

	out, err := m.ec2.RunInstances(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("aws: run instances: %w", err)
	}
	if len(out.Instances) == 0 {
		return nil, errors.New("aws: run instances returned no instances")
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

// ReplaceMachine terminates an ASG-owned EC2 instance without decrementing
// desired capacity. The ASG starts its replacement asynchronously.
func (m *Manager) ReplaceMachine(ctx context.Context, req compute.ReplaceRequest) (compute.ReplaceResult, error) {
	if m.asg == nil {
		return compute.ReplaceResult{}, fmt.Errorf("aws: machine replacement requires an ASG client: %w", compute.ErrNotSupported)
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return compute.ReplaceResult{}, errors.New("aws: instance id is required")
	}
	membership, err := m.asg.DescribeAutoScalingInstances(ctx, &autoscaling.DescribeAutoScalingInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return compute.ReplaceResult{}, fmt.Errorf("aws: describe ASG membership for instance %s: %w", id, err)
	}
	if len(membership.AutoScalingInstances) != 1 ||
		strings.TrimSpace(aws.ToString(membership.AutoScalingInstances[0].InstanceId)) != id ||
		strings.TrimSpace(aws.ToString(membership.AutoScalingInstances[0].AutoScalingGroupName)) == "" {
		return compute.ReplaceResult{}, fmt.Errorf("aws: instance %s is not managed by an autoscaling group: %w", id, compute.ErrNotSupported)
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
	return []types.TagSpecification{{ResourceType: types.ResourceTypeInstance, Tags: tags}}
}
