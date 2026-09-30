package aws

// Launch groups: a fleet of identical instances defined by one EC2 launch
// template and launched by one Auto Scaling group. Launch templates are
// AWS's recommended launch specification for Auto Scaling (launch
// configurations are legacy); the group pins an explicit template version,
// so every instance, including later scale-outs, gets exactly the same spec.
//
// The group and the template share one name, which is also the ownership
// lock: EC2 and Auto Scaling reject a second template or group with the same
// name in an account and region. Callers record the name before creating
// anything so teardown can always find both.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// launchTemplateAPI is the EC2 surface for launch templates and VPC lookup.
type launchTemplateAPI interface {
	CreateLaunchTemplate(ctx context.Context, input *ec2.CreateLaunchTemplateInput, optFns ...func(*ec2.Options)) (*ec2.CreateLaunchTemplateOutput, error)
	DeleteLaunchTemplate(ctx context.Context, input *ec2.DeleteLaunchTemplateInput, optFns ...func(*ec2.Options)) (*ec2.DeleteLaunchTemplateOutput, error)
	DescribeLaunchTemplates(ctx context.Context, input *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error)
	DescribeVpcs(ctx context.Context, input *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
}

// groupAPI is the Auto Scaling surface for creating and deleting groups.
type groupAPI interface {
	CreateAutoScalingGroup(ctx context.Context, input *autoscaling.CreateAutoScalingGroupInput, optFns ...func(*autoscaling.Options)) (*autoscaling.CreateAutoScalingGroupOutput, error)
	DeleteAutoScalingGroup(ctx context.Context, input *autoscaling.DeleteAutoScalingGroupInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DeleteAutoScalingGroupOutput, error)
	SuspendProcesses(ctx context.Context, input *autoscaling.SuspendProcessesInput, optFns ...func(*autoscaling.Options)) (*autoscaling.SuspendProcessesOutput, error)
	DescribeAutoScalingGroups(ctx context.Context, input *autoscaling.DescribeAutoScalingGroupsInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
	DescribeScalingActivities(ctx context.Context, input *autoscaling.DescribeScalingActivitiesInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeScalingActivitiesOutput, error)
}

// ErrAlreadyExists reports a launch template or Auto Scaling group name that
// is already taken in the account and region.
var ErrAlreadyExists = errors.New("already exists")

// LaunchGroupSpec describes a fleet of identical instances.
type LaunchGroupSpec struct {
	// Name names both the launch template and the Auto Scaling group.
	Name string
	// SubnetIDs are the subnets (availability zones) the group spreads
	// instances across.
	SubnetIDs        []string
	SecurityGroupIDs []string
	ImageID          string
	InstanceType     string
	// IAMInstanceProfile is a profile name or ARN.
	IAMInstanceProfile string
	UserData           string
	// DataVolumeSizeGB adds an encrypted gp3 data volume that EC2 deletes
	// with the instance, so terminating an instance never leaks storage.
	DataVolumeSizeGB int32
	// Tags go on the template, the group, and every instance and volume the
	// group launches.
	Tags  map[string]string
	Count int32
}

// suspendedGroupProcesses keeps the group from terminating instances on its
// own: AZRebalance terminates instances to even out zones, and
// ReplaceUnhealthy replaces a stopped or impaired instance with a blank one.
// Either would silently destroy the data on a box someone is testing on.
// Launch and Terminate stay enabled so resizing works.
var suspendedGroupProcesses = []string{"AZRebalance", "ReplaceUnhealthy"}

// launchGroupHopLimit lets containers on the instance reach IMDSv2 (AWS
// recommends 2 for containerized workloads; 1 blocks them).
const launchGroupHopLimit = 2

// DefaultVPCID returns the region's default VPC.
func (m *Manager) DefaultVPCID(ctx context.Context) (string, error) {
	if m.lt == nil {
		return "", errors.New("aws: launch template client is nil")
	}
	out, err := m.lt.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return "", fmt.Errorf("aws: describe default VPC: %w", err)
	}
	for _, vpc := range out.Vpcs {
		if id := aws.ToString(vpc.VpcId); id != "" {
			return id, nil
		}
	}
	return "", errors.New("aws: the region has no default VPC; pass a VPC ID")
}

// DefaultSecurityGroupID returns the VPC's "default" security group.
func (m *Manager) DefaultSecurityGroupID(ctx context.Context, vpcID string) (string, error) {
	out, err := m.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
			{Name: aws.String("group-name"), Values: []string{"default"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("aws: describe security groups: %w", err)
	}
	for _, group := range out.SecurityGroups {
		if id := aws.ToString(group.GroupId); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("aws: no default security group found in vpc %s", vpcID)
}

// CreateLaunchTemplate creates the group's launch template and returns its ID
// and version (always 1: the template is never modified, so the spec cannot
// drift). If the name is taken by a template carrying all of spec.Tags (so a
// unique owner tag makes it ours, e.g. an SDK retry of a create that had
// succeeded), that template is returned; any other taken name returns
// ErrAlreadyExists.
func (m *Manager) CreateLaunchTemplate(ctx context.Context, spec LaunchGroupSpec) (string, int64, error) {
	if m.lt == nil {
		return "", 0, errors.New("aws: launch template client is nil")
	}
	data := &types.RequestLaunchTemplateData{
		ImageId:          aws.String(spec.ImageID),
		InstanceType:     types.InstanceType(spec.InstanceType),
		SecurityGroupIds: spec.SecurityGroupIDs,
		MetadataOptions: &types.LaunchTemplateInstanceMetadataOptionsRequest{
			HttpEndpoint:            types.LaunchTemplateInstanceMetadataEndpointStateEnabled,
			HttpTokens:              types.LaunchTemplateHttpTokensStateRequired,
			HttpPutResponseHopLimit: aws.Int32(launchGroupHopLimit),
		},
		TagSpecifications: []types.LaunchTemplateTagSpecificationRequest{
			{ResourceType: types.ResourceTypeInstance, Tags: ec2Tags(spec.Tags, spec.Name)},
			{ResourceType: types.ResourceTypeVolume, Tags: ec2Tags(spec.Tags, spec.Name)},
		},
	}
	if spec.UserData != "" {
		data.UserData = aws.String(base64.StdEncoding.EncodeToString([]byte(spec.UserData)))
	}
	if profile := strings.TrimSpace(spec.IAMInstanceProfile); strings.HasPrefix(profile, "arn:") {
		data.IamInstanceProfile = &types.LaunchTemplateIamInstanceProfileSpecificationRequest{Arn: aws.String(profile)}
	} else if profile != "" {
		data.IamInstanceProfile = &types.LaunchTemplateIamInstanceProfileSpecificationRequest{Name: aws.String(profile)}
	}
	if spec.DataVolumeSizeGB > 0 {
		data.BlockDeviceMappings = []types.LaunchTemplateBlockDeviceMappingRequest{{
			DeviceName: aws.String(dataVolumeDeviceName),
			Ebs: &types.LaunchTemplateEbsBlockDeviceRequest{
				VolumeType:          types.VolumeTypeGp3,
				VolumeSize:          aws.Int32(spec.DataVolumeSizeGB),
				Encrypted:           aws.Bool(true),
				DeleteOnTermination: aws.Bool(true),
			},
		}}
	}
	out, err := m.lt.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(spec.Name),
		VersionDescription: aws.String("etcd-infra launch group spec"),
		LaunchTemplateData: data,
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeLaunchTemplate, Tags: ec2Tags(spec.Tags, spec.Name)},
		},
	})
	if err != nil {
		if apiErrorCode(err) != "InvalidLaunchTemplateName.AlreadyExistsException" {
			return "", 0, fmt.Errorf("aws: create launch template %s: %w", spec.Name, err)
		}
		existing, exists, lookupErr := m.LaunchTemplateByName(ctx, spec.Name)
		if lookupErr != nil {
			return "", 0, fmt.Errorf("aws: launch template %s: %w (%w)", spec.Name, ErrAlreadyExists, lookupErr)
		}
		if !exists || !hasAllTags(existing.Tags, spec.Tags) {
			return "", 0, fmt.Errorf("aws: launch template %s: %w", spec.Name, ErrAlreadyExists)
		}
		return existing.ID, existing.Version, nil
	}
	if out.LaunchTemplate == nil || aws.ToString(out.LaunchTemplate.LaunchTemplateId) == "" {
		return "", 0, fmt.Errorf("aws: create launch template %s returned no ID", spec.Name)
	}
	return aws.ToString(out.LaunchTemplate.LaunchTemplateId), aws.ToInt64(out.LaunchTemplate.LatestVersionNumber), nil
}

// LaunchTemplate identifies an existing launch template.
type LaunchTemplate struct {
	ID      string
	Version int64
	Tags    map[string]string
}

// LaunchTemplateByName looks up a launch template; exists is false when there
// is none with that name.
func (m *Manager) LaunchTemplateByName(ctx context.Context, name string) (LaunchTemplate, bool, error) {
	if m.lt == nil {
		return LaunchTemplate{}, false, errors.New("aws: launch template client is nil")
	}
	out, err := m.lt.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{LaunchTemplateNames: []string{name}})
	if err != nil {
		if isLaunchTemplateNotFound(err) {
			return LaunchTemplate{}, false, nil
		}
		return LaunchTemplate{}, false, fmt.Errorf("aws: describe launch template %s: %w", name, err)
	}
	if len(out.LaunchTemplates) == 0 {
		return LaunchTemplate{}, false, nil
	}
	lt := out.LaunchTemplates[0]
	return LaunchTemplate{
		ID:      aws.ToString(lt.LaunchTemplateId),
		Version: aws.ToInt64(lt.LatestVersionNumber),
		Tags:    extractTags(lt.Tags),
	}, true, nil
}

// DeleteLaunchTemplate deletes a launch template by ID (an ID, unlike a name,
// can never refer to someone else's later template). A missing template is
// the desired end state and not an error.
func (m *Manager) DeleteLaunchTemplate(ctx context.Context, id string) error {
	if m.lt == nil {
		return errors.New("aws: launch template client is nil")
	}
	_, err := m.lt.DeleteLaunchTemplate(ctx, &ec2.DeleteLaunchTemplateInput{LaunchTemplateId: aws.String(id)})
	if err == nil || isLaunchTemplateNotFound(err) {
		return nil
	}
	return fmt.Errorf("aws: delete launch template %s: %w", id, err)
}

func isLaunchTemplateNotFound(err error) bool {
	switch apiErrorCode(err) {
	case "InvalidLaunchTemplateName.NotFoundException", "InvalidLaunchTemplateId.NotFound", "InvalidLaunchTemplateId.Malformed":
		return true
	}
	return false
}

// hasAllTags reports whether have contains every key/value of want.
func hasAllTags(have, want map[string]string) bool {
	for key, value := range want {
		if got, ok := have[key]; !ok || got != value {
			return false
		}
	}
	return true
}

// CreateAutoScalingGroup creates the group with min = desired = max =
// spec.Count from the given template version, and suspends the processes
// that would terminate instances on their own. A taken name is accepted only
// when the existing group carries all of spec.Tags (ours, e.g. an SDK retry
// of a create that had succeeded); otherwise it returns ErrAlreadyExists.
func (m *Manager) CreateAutoScalingGroup(ctx context.Context, spec LaunchGroupSpec, templateID string, version int64) error {
	if m.groups == nil {
		return errors.New("aws: auto scaling client is nil")
	}
	tagKeys := slices.Sorted(maps.Keys(spec.Tags))
	tags := make([]asgtypes.Tag, 0, len(tagKeys))
	for _, key := range tagKeys {
		// Instances and volumes get their tags from the launch template, so
		// the group's tags are not propagated (that would duplicate them).
		tags = append(tags, asgtypes.Tag{
			Key:               aws.String(key),
			Value:             aws.String(spec.Tags[key]),
			PropagateAtLaunch: aws.Bool(false),
			ResourceId:        aws.String(spec.Name),
			ResourceType:      aws.String("auto-scaling-group"),
		})
	}
	_, err := m.groups.CreateAutoScalingGroup(ctx, &autoscaling.CreateAutoScalingGroupInput{
		AutoScalingGroupName: aws.String(spec.Name),
		LaunchTemplate: &asgtypes.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(templateID),
			Version:          aws.String(fmt.Sprint(version)),
		},
		MinSize:           aws.Int32(spec.Count),
		MaxSize:           aws.Int32(spec.Count),
		DesiredCapacity:   aws.Int32(spec.Count),
		VPCZoneIdentifier: aws.String(strings.Join(spec.SubnetIDs, ",")),
		HealthCheckType:   aws.String("EC2"),
		Tags:              tags,
	})
	if err != nil {
		if apiErrorCode(err) != "AlreadyExists" {
			return fmt.Errorf("aws: create auto scaling group %s: %w", spec.Name, err)
		}
		existing, exists, lookupErr := m.AutoScalingGroupTags(ctx, spec.Name)
		if lookupErr != nil {
			return fmt.Errorf("aws: auto scaling group %s: %w (%w)", spec.Name, ErrAlreadyExists, lookupErr)
		}
		if !exists || !hasAllTags(existing, spec.Tags) {
			return fmt.Errorf("aws: auto scaling group %s: %w", spec.Name, ErrAlreadyExists)
		}
	}
	if _, err := m.groups.SuspendProcesses(ctx, &autoscaling.SuspendProcessesInput{
		AutoScalingGroupName: aws.String(spec.Name),
		ScalingProcesses:     suspendedGroupProcesses,
	}); err != nil {
		return fmt.Errorf("aws: suspend %v on auto scaling group %s: %w", suspendedGroupProcesses, spec.Name, err)
	}
	return nil
}

// AutoScalingGroupTags returns the group's tags; exists is false when there
// is no group with that name.
func (m *Manager) AutoScalingGroupTags(ctx context.Context, name string) (map[string]string, bool, error) {
	if m.groups == nil {
		return nil, false, errors.New("aws: auto scaling client is nil")
	}
	out, err := m.groups.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{name},
	})
	if err != nil {
		return nil, false, fmt.Errorf("aws: describe auto scaling group %s: %w", name, err)
	}
	if len(out.AutoScalingGroups) == 0 {
		return nil, false, nil
	}
	tags := map[string]string{}
	for _, tag := range out.AutoScalingGroups[0].Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return tags, true, nil
}

// GroupMember is one instance of an Auto Scaling group.
type GroupMember struct {
	ID               string
	LifecycleState   string
	HealthStatus     string
	AvailabilityZone string
}

// InService reports whether the group considers the instance in service.
func (g GroupMember) InService() bool {
	return g.LifecycleState == string(asgtypes.LifecycleStateInService)
}

// GroupMembers returns the group's instances sorted by ID. exists is false
// when the group does not exist (or has been deleted).
func (m *Manager) GroupMembers(ctx context.Context, name string) (members []GroupMember, exists bool, err error) {
	if m.groups == nil {
		return nil, false, errors.New("aws: auto scaling client is nil")
	}
	out, err := m.groups.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{name},
	})
	if err != nil {
		return nil, false, fmt.Errorf("aws: describe auto scaling group %s: %w", name, err)
	}
	if len(out.AutoScalingGroups) == 0 {
		return nil, false, nil
	}
	for _, inst := range out.AutoScalingGroups[0].Instances {
		members = append(members, GroupMember{
			ID:               aws.ToString(inst.InstanceId),
			LifecycleState:   string(inst.LifecycleState),
			HealthStatus:     aws.ToString(inst.HealthStatus),
			AvailabilityZone: aws.ToString(inst.AvailabilityZone),
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return members, true, nil
}

// WaitForGroupSize waits until the group has exactly count instances, all in
// service, and returns their IDs sorted. On timeout the error carries the
// group's most recent failed scaling activity (for example a missing
// permission or insufficient capacity), which is otherwise only visible in
// the console.
func (m *Manager) WaitForGroupSize(ctx context.Context, name string, count int, timeout, poll time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		members, exists, err := m.GroupMembers(ctx, name)
		if err == nil && !exists {
			return nil, fmt.Errorf("aws: auto scaling group %s does not exist", name)
		}
		if err == nil && len(members) == count {
			ids := make([]string, 0, count)
			for _, member := range members {
				if member.InService() {
					ids = append(ids, member.ID)
				}
			}
			if len(ids) == count {
				return ids, nil
			}
		}
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("aws: timed out after %s waiting for %d in-service instances in auto scaling group %s", timeout, count, name)
			if reason := m.lastFailedScalingActivity(ctx, name); reason != "" {
				msg += "; last failed scaling activity: " + reason
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", msg, err)
			}
			return nil, errors.New(msg)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("aws: wait for auto scaling group %s: %w", name, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// lastFailedScalingActivity returns the status message of the most recent
// failed scaling activity, or "" when there is none or it cannot be read.
func (m *Manager) lastFailedScalingActivity(ctx context.Context, name string) string {
	out, err := m.groups.DescribeScalingActivities(ctx, &autoscaling.DescribeScalingActivitiesInput{
		AutoScalingGroupName: aws.String(name),
		MaxRecords:           aws.Int32(10),
	})
	if err != nil {
		return ""
	}
	for _, activity := range out.Activities {
		if activity.StatusCode == asgtypes.ScalingActivityStatusCodeFailed {
			return aws.ToString(activity.StatusMessage)
		}
	}
	return ""
}

// DeleteAutoScalingGroup force-deletes the group, which terminates all of its
// instances, and waits until the group no longer exists. A missing group is
// the desired end state and not an error.
func (m *Manager) DeleteAutoScalingGroup(ctx context.Context, name string, timeout, poll time.Duration) error {
	if m.groups == nil {
		return errors.New("aws: auto scaling client is nil")
	}
	deadline := time.Now().Add(timeout)
	for {
		_, err := m.groups.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
			AutoScalingGroupName: aws.String(name),
			ForceDelete:          aws.Bool(true),
		})
		if err == nil || isGroupNotFoundError(err) {
			break
		}
		// A scaling activity in flight (for example the launch this
		// teardown interrupted) blocks deletion briefly; retry.
		if code := apiErrorCode(err); code != "ScalingActivityInProgress" && code != "ResourceInUse" {
			return fmt.Errorf("aws: delete auto scaling group %s: %w", name, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("aws: delete auto scaling group %s: %w", name, err)
		}
		if err := sleepCtx(ctx, poll); err != nil {
			return fmt.Errorf("aws: delete auto scaling group %s: %w", name, err)
		}
	}
	for {
		_, exists, err := m.GroupMembers(ctx, name)
		if err == nil && !exists {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("aws: wait for auto scaling group %s deletion: %w", name, err)
			}
			return fmt.Errorf("aws: timed out after %s waiting for auto scaling group %s deletion", timeout, name)
		}
		if err := sleepCtx(ctx, poll); err != nil {
			return fmt.Errorf("aws: wait for auto scaling group %s deletion: %w", name, err)
		}
	}
}

// unterminatedInstanceStates are the EC2 states of instances that still
// exist (and may still hold volumes).
var unterminatedInstanceStates = []string{"pending", "running", "shutting-down", "stopping", "stopped"}

// UnterminatedInstancesTagged maps instance ID to EC2 state for instances
// carrying all the given tags that are not yet terminated. Instances in
// shutting-down are included: their volumes are deleted only once they are
// terminated.
func (m *Manager) UnterminatedInstancesTagged(ctx context.Context, tags map[string]string) (map[string]string, error) {
	if len(tags) == 0 {
		return nil, errors.New("aws: refusing to list instances without a tag filter")
	}
	filters := []types.Filter{{Name: aws.String("instance-state-name"), Values: unterminatedInstanceStates}}
	for _, key := range slices.Sorted(maps.Keys(tags)) {
		filters = append(filters, types.Filter{Name: aws.String("tag:" + key), Values: []string{tags[key]}})
	}
	states := map[string]string{}
	paginator := ec2.NewDescribeInstancesPaginator(m.ec2, &ec2.DescribeInstancesInput{Filters: filters})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws: describe instances by tag: %w", err)
		}
		for _, reservation := range page.Reservations {
			for _, inst := range reservation.Instances {
				state := ""
				if inst.State != nil {
					state = string(inst.State.Name)
				}
				states[aws.ToString(inst.InstanceId)] = state
			}
		}
	}
	return states, nil
}

func ec2Tags(tags map[string]string, name string) []types.Tag {
	merged := maps.Clone(tags)
	if merged == nil {
		merged = map[string]string{}
	}
	if _, ok := merged["Name"]; !ok && name != "" {
		merged["Name"] = name
	}
	out := make([]types.Tag, 0, len(merged))
	for _, key := range slices.Sorted(maps.Keys(merged)) {
		out = append(out, types.Tag{Key: aws.String(key), Value: aws.String(merged[key])})
	}
	return out
}

// apiErrorCode returns the AWS API error code of err, or "" for nil or
// non-API errors.
func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	if err != nil {
		return "unknown"
	}
	return ""
}

// isGroupNotFoundError reports Auto Scaling's missing-group error, which is a
// generic ValidationError ("AutoScalingGroup name not found").
func isGroupNotFoundError(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationError" &&
		strings.Contains(strings.ToLower(apiErr.ErrorMessage()), "not found")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
