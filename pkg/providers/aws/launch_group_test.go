package aws

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLaunchTemplates struct {
	createInput *ec2.CreateLaunchTemplateInput
	createErr   error
	deleteErr   error
	deleted     []string
	vpcs        []types.Vpc
	// existing is what DescribeLaunchTemplates returns (none when nil).
	existing *types.LaunchTemplate
}

func (f *fakeLaunchTemplates) CreateLaunchTemplate(_ context.Context, input *ec2.CreateLaunchTemplateInput, _ ...func(*ec2.Options)) (*ec2.CreateLaunchTemplateOutput, error) {
	f.createInput = input
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &ec2.CreateLaunchTemplateOutput{LaunchTemplate: &types.LaunchTemplate{
		LaunchTemplateId: aws.String("lt-1"), LatestVersionNumber: aws.Int64(1),
	}}, nil
}

func (f *fakeLaunchTemplates) DeleteLaunchTemplate(_ context.Context, input *ec2.DeleteLaunchTemplateInput, _ ...func(*ec2.Options)) (*ec2.DeleteLaunchTemplateOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(input.LaunchTemplateId))
	return &ec2.DeleteLaunchTemplateOutput{}, f.deleteErr
}

func (f *fakeLaunchTemplates) DescribeLaunchTemplates(_ context.Context, _ *ec2.DescribeLaunchTemplatesInput, _ ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
	if f.existing == nil {
		return nil, &smithy.GenericAPIError{Code: "InvalidLaunchTemplateName.NotFoundException"}
	}
	return &ec2.DescribeLaunchTemplatesOutput{LaunchTemplates: []types.LaunchTemplate{*f.existing}}, nil
}

func (f *fakeLaunchTemplates) DescribeVpcs(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}

type fakeGroups struct {
	createInput  *autoscaling.CreateAutoScalingGroupInput
	createErr    error
	suspendInput *autoscaling.SuspendProcessesInput
	// deleteErrs are returned by successive DeleteAutoScalingGroup calls.
	deleteErrs  []error
	deleteCalls int
	forceDelete bool
	// describes are returned by successive DescribeAutoScalingGroups calls;
	// the last one repeats.
	describes     [][]asgtypes.AutoScalingGroup
	describeCalls int
	activities    []asgtypes.Activity
}

func (f *fakeGroups) CreateAutoScalingGroup(_ context.Context, input *autoscaling.CreateAutoScalingGroupInput, _ ...func(*autoscaling.Options)) (*autoscaling.CreateAutoScalingGroupOutput, error) {
	f.createInput = input
	return &autoscaling.CreateAutoScalingGroupOutput{}, f.createErr
}

func (f *fakeGroups) DeleteAutoScalingGroup(_ context.Context, input *autoscaling.DeleteAutoScalingGroupInput, _ ...func(*autoscaling.Options)) (*autoscaling.DeleteAutoScalingGroupOutput, error) {
	f.forceDelete = aws.ToBool(input.ForceDelete)
	call := f.deleteCalls
	f.deleteCalls++
	if call < len(f.deleteErrs) {
		return nil, f.deleteErrs[call]
	}
	return &autoscaling.DeleteAutoScalingGroupOutput{}, nil
}

func (f *fakeGroups) SuspendProcesses(_ context.Context, input *autoscaling.SuspendProcessesInput, _ ...func(*autoscaling.Options)) (*autoscaling.SuspendProcessesOutput, error) {
	f.suspendInput = input
	return &autoscaling.SuspendProcessesOutput{}, nil
}

func (f *fakeGroups) DescribeAutoScalingGroups(_ context.Context, _ *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	if len(f.describes) == 0 {
		return &autoscaling.DescribeAutoScalingGroupsOutput{}, nil
	}
	idx := min(f.describeCalls, len(f.describes)-1)
	f.describeCalls++
	return &autoscaling.DescribeAutoScalingGroupsOutput{AutoScalingGroups: f.describes[idx]}, nil
}

func (f *fakeGroups) DescribeScalingActivities(_ context.Context, _ *autoscaling.DescribeScalingActivitiesInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeScalingActivitiesOutput, error) {
	return &autoscaling.DescribeScalingActivitiesOutput{Activities: f.activities}, nil
}

func testLaunchGroupSpec() LaunchGroupSpec {
	return LaunchGroupSpec{
		Name:               "dev01",
		SubnetIDs:          []string{"subnet-a", "subnet-b"},
		SecurityGroupIDs:   []string{"sg-1"},
		ImageID:            "ami-1",
		InstanceType:       "t3a.medium",
		IAMInstanceProfile: "etcd-infra-ssm",
		UserData:           "#!/bin/bash\necho hi\n",
		DataVolumeSizeGB:   32,
		Tags:               map[string]string{"etcd-infra.cluster": "dev01", "etcd-infra.role": "dev"},
		Count:              3,
	}
}

func tagMap(tags []types.Tag) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		out[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

func TestCreateLaunchTemplateSpec(t *testing.T) {
	t.Parallel()

	lt := &fakeLaunchTemplates{}
	id, version, err := (&Manager{lt: lt}).CreateLaunchTemplate(context.Background(), testLaunchGroupSpec())
	require.NoError(t, err)
	assert.Equal(t, "lt-1", id)
	assert.Equal(t, int64(1), version)

	in := lt.createInput
	assert.Equal(t, "dev01", aws.ToString(in.LaunchTemplateName))
	data := in.LaunchTemplateData
	assert.Equal(t, "ami-1", aws.ToString(data.ImageId))
	assert.Equal(t, []string{"sg-1"}, data.SecurityGroupIds)
	assert.Equal(t, "etcd-infra-ssm", aws.ToString(data.IamInstanceProfile.Name))
	// IMDSv2 only, reachable from containers.
	assert.Equal(t, types.LaunchTemplateHttpTokensStateRequired, data.MetadataOptions.HttpTokens)
	assert.Equal(t, int32(2), aws.ToInt32(data.MetadataOptions.HttpPutResponseHopLimit))
	userData, err := base64.StdEncoding.DecodeString(aws.ToString(data.UserData))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/bash\necho hi\n", string(userData))

	// The data volume must never outlive its instance: a scale-in or
	// teardown would otherwise leak it.
	require.Len(t, data.BlockDeviceMappings, 1)
	ebs := data.BlockDeviceMappings[0].Ebs
	assert.True(t, aws.ToBool(ebs.DeleteOnTermination))
	assert.True(t, aws.ToBool(ebs.Encrypted))
	assert.Equal(t, types.VolumeTypeGp3, ebs.VolumeType)
	assert.Equal(t, int32(32), aws.ToInt32(ebs.VolumeSize))

	// Instances and volumes carry the group tags (IAM gates on them and
	// teardown sweeps by them), plus Name.
	byType := map[types.ResourceType]map[string]string{}
	for _, spec := range data.TagSpecifications {
		byType[spec.ResourceType] = tagMap(spec.Tags)
	}
	want := map[string]string{"etcd-infra.cluster": "dev01", "etcd-infra.role": "dev", "Name": "dev01"}
	assert.Equal(t, want, byType[types.ResourceTypeInstance])
	assert.Equal(t, want, byType[types.ResourceTypeVolume])
	require.Len(t, in.TagSpecifications, 1)
	assert.Equal(t, types.ResourceTypeLaunchTemplate, in.TagSpecifications[0].ResourceType)
	assert.Equal(t, want, tagMap(in.TagSpecifications[0].Tags))

	spec := testLaunchGroupSpec()
	spec.IAMInstanceProfile = "arn:aws:iam::1:instance-profile/p"
	_, _, err = (&Manager{lt: lt}).CreateLaunchTemplate(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::1:instance-profile/p", aws.ToString(lt.createInput.LaunchTemplateData.IamInstanceProfile.Arn))
}

// A taken name is ours only when the existing resource carries all of our
// tags (the owner token among them): that is an SDK retry of a create that
// had succeeded, and treating it as foreign would drop state for live
// resources. Anything else is someone else's and must be left alone.
func TestLaunchGroupNameConflicts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	spec := testLaunchGroupSpec()
	alreadyExists := &smithy.GenericAPIError{Code: "InvalidLaunchTemplateName.AlreadyExistsException"}
	var ours, foreign []types.Tag
	for key, value := range spec.Tags {
		ours = append(ours, types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	foreign = []types.Tag{{Key: aws.String("etcd-infra.cluster"), Value: aws.String("dev01")}}

	lt := &fakeLaunchTemplates{createErr: alreadyExists, existing: &types.LaunchTemplate{
		LaunchTemplateId: aws.String("lt-mine"), LatestVersionNumber: aws.Int64(1), Tags: ours,
	}}
	id, version, err := (&Manager{lt: lt}).CreateLaunchTemplate(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, "lt-mine", id)
	assert.Equal(t, int64(1), version)

	lt = &fakeLaunchTemplates{createErr: alreadyExists, existing: &types.LaunchTemplate{LaunchTemplateId: aws.String("lt-theirs"), Tags: foreign}}
	_, _, err = (&Manager{lt: lt}).CreateLaunchTemplate(ctx, spec)
	require.ErrorIs(t, err, ErrAlreadyExists)

	lt = &fakeLaunchTemplates{createErr: &smithy.GenericAPIError{Code: "UnauthorizedOperation"}}
	_, _, err = (&Manager{lt: lt}).CreateLaunchTemplate(ctx, spec)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAlreadyExists)

	groupWith := func(tags map[string]string) [][]asgtypes.AutoScalingGroup {
		var asgTags []asgtypes.TagDescription
		for key, value := range tags {
			asgTags = append(asgTags, asgtypes.TagDescription{Key: aws.String(key), Value: aws.String(value)})
		}
		return [][]asgtypes.AutoScalingGroup{{{AutoScalingGroupName: aws.String("dev01"), Tags: asgTags}}}
	}
	groups := &fakeGroups{createErr: &smithy.GenericAPIError{Code: "AlreadyExists"}, describes: groupWith(map[string]string{"etcd-infra.cluster": "dev01"})}
	err = (&Manager{groups: groups}).CreateAutoScalingGroup(ctx, spec, "lt-1", 1)
	require.ErrorIs(t, err, ErrAlreadyExists)
	assert.Nil(t, groups.suspendInput, "a group we did not create must not be modified")

	groups = &fakeGroups{createErr: &smithy.GenericAPIError{Code: "AlreadyExists"}, describes: groupWith(spec.Tags)}
	require.NoError(t, (&Manager{groups: groups}).CreateAutoScalingGroup(ctx, spec, "lt-1", 1))
	require.NotNil(t, groups.suspendInput, "our own group still gets its processes suspended")
}

func TestCreateAutoScalingGroupSpec(t *testing.T) {
	t.Parallel()

	groups := &fakeGroups{}
	require.NoError(t, (&Manager{groups: groups}).CreateAutoScalingGroup(context.Background(), testLaunchGroupSpec(), "lt-1", 1))
	in := groups.createInput
	assert.Equal(t, "dev01", aws.ToString(in.AutoScalingGroupName))
	// An explicit version, never $Latest/$Default: the spec cannot drift.
	assert.Equal(t, "lt-1", aws.ToString(in.LaunchTemplate.LaunchTemplateId))
	assert.Equal(t, "1", aws.ToString(in.LaunchTemplate.Version))
	assert.Equal(t, int32(3), aws.ToInt32(in.MinSize))
	assert.Equal(t, int32(3), aws.ToInt32(in.DesiredCapacity))
	assert.Equal(t, int32(3), aws.ToInt32(in.MaxSize))
	assert.Equal(t, "subnet-a,subnet-b", aws.ToString(in.VPCZoneIdentifier))
	require.Len(t, in.Tags, 2)
	for _, tag := range in.Tags {
		assert.False(t, aws.ToBool(tag.PropagateAtLaunch), "instance tags come from the launch template")
	}
	// The group must never terminate a box on its own.
	require.NotNil(t, groups.suspendInput)
	assert.ElementsMatch(t, []string{"AZRebalance", "ReplaceUnhealthy"}, groups.suspendInput.ScalingProcesses)
}

func TestDeleteLaunchTemplateIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, code := range []string{"InvalidLaunchTemplateName.NotFoundException", "InvalidLaunchTemplateId.NotFound"} {
		lt := &fakeLaunchTemplates{deleteErr: &smithy.GenericAPIError{Code: code}}
		require.NoError(t, (&Manager{lt: lt}).DeleteLaunchTemplate(ctx, "dev01"), code)
	}
	lt := &fakeLaunchTemplates{deleteErr: &smithy.GenericAPIError{Code: "UnauthorizedOperation"}}
	require.Error(t, (&Manager{lt: lt}).DeleteLaunchTemplate(ctx, "dev01"))
}

func TestDeleteAutoScalingGroup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	group := asgtypes.AutoScalingGroup{AutoScalingGroupName: aws.String("dev01")}

	// Busy group: retried, force-deleted, and waited on until it is gone.
	groups := &fakeGroups{
		deleteErrs: []error{&smithy.GenericAPIError{Code: "ScalingActivityInProgress"}},
		describes:  [][]asgtypes.AutoScalingGroup{{group}, {group}, {}},
	}
	require.NoError(t, (&Manager{groups: groups}).DeleteAutoScalingGroup(ctx, "dev01", time.Minute, time.Millisecond))
	assert.Equal(t, 2, groups.deleteCalls)
	assert.True(t, groups.forceDelete, "force delete terminates the group's instances")
	assert.Equal(t, 3, groups.describeCalls, "must wait until the group no longer exists")

	// Already gone.
	groups = &fakeGroups{deleteErrs: []error{&smithy.GenericAPIError{Code: "ValidationError", Message: "AutoScalingGroup name not found - dev01"}}}
	require.NoError(t, (&Manager{groups: groups}).DeleteAutoScalingGroup(ctx, "dev01", time.Minute, time.Millisecond))

	// Other validation errors are real failures.
	groups = &fakeGroups{deleteErrs: []error{&smithy.GenericAPIError{Code: "ValidationError", Message: "bad request"}}}
	require.Error(t, (&Manager{groups: groups}).DeleteAutoScalingGroup(ctx, "dev01", time.Minute, time.Millisecond))

	// A group that never disappears times out instead of reporting success.
	groups = &fakeGroups{describes: [][]asgtypes.AutoScalingGroup{{group}}}
	require.ErrorContains(t, (&Manager{groups: groups}).DeleteAutoScalingGroup(ctx, "dev01", 5*time.Millisecond, time.Millisecond), "timed out")
}

func TestWaitForGroupSize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	member := func(id string, state asgtypes.LifecycleState) asgtypes.Instance {
		return asgtypes.Instance{InstanceId: aws.String(id), LifecycleState: state, HealthStatus: aws.String("Healthy")}
	}
	groupOf := func(instances ...asgtypes.Instance) []asgtypes.AutoScalingGroup {
		return []asgtypes.AutoScalingGroup{{AutoScalingGroupName: aws.String("dev01"), Instances: instances}}
	}

	groups := &fakeGroups{describes: [][]asgtypes.AutoScalingGroup{
		groupOf(member("i-2", asgtypes.LifecycleStateInService), member("i-1", asgtypes.LifecycleStatePending)),
		// Scale-in leftovers are not counted as done either.
		groupOf(member("i-2", asgtypes.LifecycleStateInService), member("i-1", asgtypes.LifecycleStateInService), member("i-3", asgtypes.LifecycleStateTerminating)),
		groupOf(member("i-2", asgtypes.LifecycleStateInService), member("i-1", asgtypes.LifecycleStateInService)),
	}}
	ids, err := (&Manager{groups: groups}).WaitForGroupSize(ctx, "dev01", 2, time.Minute, time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, []string{"i-1", "i-2"}, ids)
	assert.Equal(t, 3, groups.describeCalls)

	// Zero is a valid size (a scaled-to-zero group).
	groups = &fakeGroups{describes: [][]asgtypes.AutoScalingGroup{groupOf()}}
	ids, err = (&Manager{groups: groups}).WaitForGroupSize(ctx, "dev01", 0, time.Minute, time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, ids)

	// On timeout the failed activity explains why, e.g. a missing permission.
	groups = &fakeGroups{
		describes: [][]asgtypes.AutoScalingGroup{groupOf()},
		activities: []asgtypes.Activity{
			{StatusCode: asgtypes.ScalingActivityStatusCodeSuccessful, StatusMessage: aws.String("fine")},
			{StatusCode: asgtypes.ScalingActivityStatusCodeFailed, StatusMessage: aws.String("You are not authorized to use launch template")},
		},
	}
	_, err = (&Manager{groups: groups}).WaitForGroupSize(ctx, "dev01", 1, 5*time.Millisecond, time.Millisecond)
	require.ErrorContains(t, err, "not authorized to use launch template")

	groups = &fakeGroups{}
	_, err = (&Manager{groups: groups}).WaitForGroupSize(ctx, "dev01", 1, time.Minute, time.Millisecond)
	require.ErrorContains(t, err, "does not exist")
}

func TestUnterminatedInstancesTagged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := newWithEC2(&fakeEC2{}).UnterminatedInstancesTagged(ctx, nil)
	require.Error(t, err, "an unfiltered sweep would match every instance in the account")

	fake := &fakeEC2{describeInstancesOutput: &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{
		{InstanceId: aws.String("i-1"), State: &types.InstanceState{Name: types.InstanceStateNameRunning}},
		{InstanceId: aws.String("i-2"), State: &types.InstanceState{Name: types.InstanceStateNameShuttingDown}},
	}}}}}
	got, err := newWithEC2(fake).UnterminatedInstancesTagged(ctx, map[string]string{"etcd-infra.cluster": "dev01", "etcd-infra.role": "dev"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"i-1": "running", "i-2": "shutting-down"}, got)

	filters := map[string][]string{}
	for _, f := range fake.describeInstancesInput.Filters {
		filters[aws.ToString(f.Name)] = f.Values
	}
	assert.Equal(t, []string{"dev01"}, filters["tag:etcd-infra.cluster"])
	assert.Equal(t, []string{"dev"}, filters["tag:etcd-infra.role"])
	assert.NotContains(t, filters["instance-state-name"], "terminated")
	assert.Contains(t, filters["instance-state-name"], "stopped", "a stopped box still holds its volume")
}

func TestDefaultVPCID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	id, err := (&Manager{lt: &fakeLaunchTemplates{vpcs: []types.Vpc{{VpcId: aws.String("vpc-default")}}}}).DefaultVPCID(ctx)
	require.NoError(t, err)
	assert.Equal(t, "vpc-default", id)
	_, err = (&Manager{lt: &fakeLaunchTemplates{}}).DefaultVPCID(ctx)
	require.ErrorContains(t, err, "no default VPC")
	_, err = (&Manager{}).DefaultVPCID(ctx)
	require.ErrorContains(t, err, "client is nil")
}
