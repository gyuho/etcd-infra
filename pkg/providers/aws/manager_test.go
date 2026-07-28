package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

type fakeEC2 struct {
	runInput                  *ec2.RunInstancesInput
	terminateIDs              []string
	stopIDs                   []string
	startIDs                  []string
	describeInstancesInput    *ec2.DescribeInstancesInput
	subnets                   []types.Subnet
	securityGroups            []types.SecurityGroup
	runOutput                 *ec2.RunInstancesOutput
	describeInstancesOutput   *ec2.DescribeInstancesOutput
	describeInstancesPages    []*ec2.DescribeInstancesOutput // paginated responses (used when set)
	describeInstancesPageIdx  int
	describeSubnetsCalled     bool
	describeSecurityGroupsRun bool

	runErr                    error
	terminateErr              error
	stopErr                   error
	startErr                  error
	describeInstancesErr      error
	describeSubnetsErr        error
	describeSecurityGroupsErr error
}

func (f *fakeEC2) RunInstances(_ context.Context, input *ec2.RunInstancesInput, _ ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	f.runInput = input
	if f.runErr != nil {
		return nil, f.runErr
	}
	if f.runOutput != nil {
		return f.runOutput, nil
	}
	return &ec2.RunInstancesOutput{Instances: []types.Instance{{
		InstanceId: aws.String("i-123"), PublicIpAddress: aws.String("203.0.113.10"),
	}}}, nil
}

func (f *fakeEC2) TerminateInstances(_ context.Context, input *ec2.TerminateInstancesInput, _ ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminateIDs = append(f.terminateIDs, input.InstanceIds...)
	if f.terminateErr != nil {
		return nil, f.terminateErr
	}
	return &ec2.TerminateInstancesOutput{}, nil
}

func (f *fakeEC2) StopInstances(_ context.Context, input *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	f.stopIDs = append(f.stopIDs, input.InstanceIds...)
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	return &ec2.StopInstancesOutput{}, nil
}

func (f *fakeEC2) StartInstances(_ context.Context, input *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	f.startIDs = append(f.startIDs, input.InstanceIds...)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &ec2.StartInstancesOutput{}, nil
}

func (f *fakeEC2) DescribeSubnets(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	f.describeSubnetsCalled = true
	if f.describeSubnetsErr != nil {
		return nil, f.describeSubnetsErr
	}
	return &ec2.DescribeSubnetsOutput{Subnets: f.subnets}, nil
}

func (f *fakeEC2) DescribeSecurityGroups(_ context.Context, _ *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	f.describeSecurityGroupsRun = true
	if f.describeSecurityGroupsErr != nil {
		return nil, f.describeSecurityGroupsErr
	}
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.securityGroups}, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, input *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.describeInstancesInput = input
	if f.describeInstancesErr != nil {
		return nil, f.describeInstancesErr
	}
	if len(f.describeInstancesPages) > 0 {
		if f.describeInstancesPageIdx >= len(f.describeInstancesPages) {
			return &ec2.DescribeInstancesOutput{}, nil
		}
		out := f.describeInstancesPages[f.describeInstancesPageIdx]
		f.describeInstancesPageIdx++
		return out, nil
	}
	if f.describeInstancesOutput != nil {
		return f.describeInstancesOutput, nil
	}
	return &ec2.DescribeInstancesOutput{}, nil
}

type fakeSSM struct {
	sendInput  *ssm.SendCommandInput
	getInputs  []*ssm.GetCommandInvocationInput
	sendOutput *ssm.SendCommandOutput
	sendErr    error
	getErr     error
	invokes    []*ssm.GetCommandInvocationOutput
	callIndex  int
}

func (f *fakeSSM) SendCommand(_ context.Context, input *ssm.SendCommandInput, _ ...func(*ssm.Options)) (*ssm.SendCommandOutput, error) {
	f.sendInput = input
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if f.sendOutput != nil {
		return f.sendOutput, nil
	}
	return &ssm.SendCommandOutput{
		Command: &ssmtypes.Command{
			CommandId: aws.String("cmd-123"),
		},
	}, nil
}

func (f *fakeSSM) GetCommandInvocation(_ context.Context, input *ssm.GetCommandInvocationInput, _ ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error) {
	f.getInputs = append(f.getInputs, input)
	if f.getErr != nil {
		return nil, f.getErr
	}
	if len(f.invokes) == 0 {
		return &ssm.GetCommandInvocationOutput{
			Status:       ssmtypes.CommandInvocationStatusSuccess,
			ResponseCode: 0,
		}, nil
	}
	if f.callIndex >= len(f.invokes) {
		return f.invokes[len(f.invokes)-1], nil
	}
	out := f.invokes[f.callIndex]
	f.callIndex++
	return out, nil
}

type fakeASG struct {
	describeOutput          *autoscaling.DescribeAutoScalingGroupsOutput
	describeInstancesOutput *autoscaling.DescribeAutoScalingInstancesOutput
	describeInstancesInput  *autoscaling.DescribeAutoScalingInstancesInput
	terminateInput          *autoscaling.TerminateInstanceInAutoScalingGroupInput
	describeErr             error
	describeInstancesErr    error
	updateErr               error
	terminateInstanceErr    error
}

func (f *fakeASG) DescribeAutoScalingGroups(_ context.Context, _ *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if f.describeOutput != nil {
		return f.describeOutput, nil
	}
	return &autoscaling.DescribeAutoScalingGroupsOutput{}, nil
}

func (f *fakeASG) DescribeAutoScalingInstances(_ context.Context, input *autoscaling.DescribeAutoScalingInstancesInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingInstancesOutput, error) {
	f.describeInstancesInput = input
	if f.describeInstancesErr != nil {
		return nil, f.describeInstancesErr
	}
	if f.describeInstancesOutput != nil {
		return f.describeInstancesOutput, nil
	}
	return &autoscaling.DescribeAutoScalingInstancesOutput{}, nil
}

func (f *fakeASG) UpdateAutoScalingGroup(_ context.Context, _ *autoscaling.UpdateAutoScalingGroupInput, _ ...func(*autoscaling.Options)) (*autoscaling.UpdateAutoScalingGroupOutput, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &autoscaling.UpdateAutoScalingGroupOutput{}, nil
}

func (f *fakeASG) TerminateInstanceInAutoScalingGroup(_ context.Context, input *autoscaling.TerminateInstanceInAutoScalingGroupInput, _ ...func(*autoscaling.Options)) (*autoscaling.TerminateInstanceInAutoScalingGroupOutput, error) {
	f.terminateInput = input
	if f.terminateInstanceErr != nil {
		return nil, f.terminateInstanceErr
	}
	return &autoscaling.TerminateInstanceInAutoScalingGroupOutput{}, nil
}

func TestNew(t *testing.T) {
	t.Parallel()
	mgr := New(aws.Config{Region: "us-east-1"})
	require.NotNil(t, mgr)
	require.NotNil(t, mgr.ec2)
	require.NotNil(t, mgr.ssm)
	require.NotNil(t, mgr.asg)
}

func TestCreateRequiresVPC(t *testing.T) {
	t.Parallel()
	mgr := newWithEC2(&fakeEC2{})
	_, err := mgr.Create(context.Background(), compute.NewCreateRequest(
		compute.WithName("node-1"),
		compute.WithImage("ami-1"),
		compute.WithSize("t3.micro"),
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vpc")
}

func TestCreateBuildsRunInstancesRequest(t *testing.T) {
	t.Parallel()
	fake := &fakeEC2{
		subnets:        []types.Subnet{{SubnetId: aws.String("subnet-b")}, {SubnetId: aws.String("subnet-a")}},
		securityGroups: []types.SecurityGroup{{GroupId: aws.String("sg-1")}},
	}
	mgr := newWithEC2(fake)

	userData := "#!/bin/bash\necho hello"
	comp, err := mgr.Create(context.Background(), compute.NewCreateRequest(
		compute.WithVPCID("vpc-1"),
		compute.WithName("node-1"),
		compute.WithImage("ami-1"),
		compute.WithSize("t3.micro"),
		compute.WithUserData(userData),
		compute.WithSSHKeys([]string{"keypair-1"}),
		compute.WithTags(map[string]string{"env": "test"}),
		compute.WithSSHPrivateKeyPath("/tmp/key"),
	))
	require.NoError(t, err)
	require.NotNil(t, fake.runInput, "expected run instances input")
	assert.Equal(t, "subnet-a", aws.ToString(fake.runInput.SubnetId))
	assert.Equal(t, []string{"sg-1"}, fake.runInput.SecurityGroupIds)
	assert.Equal(t, "keypair-1", aws.ToString(fake.runInput.KeyName))
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(userData)), aws.ToString(fake.runInput.UserData))
	assert.Equal(t, "i-123", comp.ID())
	assert.Equal(t, "203.0.113.10", comp.PublicIPv4())
	sshInst, ok := comp.(compute.SSHInstance)
	require.True(t, ok, "expected instance to implement SSHInstance")
	ssh := sshInst.SSHInfo()
	assert.Equal(t, defaultSSHUser, ssh.User)
	assert.Equal(t, 22, ssh.Port)
	assert.Equal(t, "/tmp/key", ssh.PrivateKeyPath)
}

func TestCreateValidationAndErrorPaths(t *testing.T) {
	t.Parallel()
	mgr := newWithEC2(&fakeEC2{})

	_, err := mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1")))
	require.ErrorContains(t, err, "name")

	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n")))
	require.ErrorContains(t, err, "image")

	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami")))
	require.ErrorContains(t, err, "size")

	mgr = newWithEC2(nil)
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "ec2 client is nil")

	mgr = newWithEC2(&fakeEC2{describeSubnetsErr: errors.New("boom")})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "describe subnets")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{}, securityGroups: []types.SecurityGroup{{GroupId: aws.String("sg")}}})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "no subnets found")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{{}}, securityGroups: []types.SecurityGroup{{GroupId: aws.String("sg")}}})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "no subnets with ids")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{{SubnetId: aws.String("subnet-1")}}, describeSecurityGroupsErr: errors.New("sgboom")})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "describe security groups")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{{SubnetId: aws.String("subnet-1")}}, securityGroups: []types.SecurityGroup{}})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "no default security group")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{{SubnetId: aws.String("subnet-1")}}, securityGroups: []types.SecurityGroup{{}}})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "missing id")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{{SubnetId: aws.String("subnet-1")}}, securityGroups: []types.SecurityGroup{{GroupId: aws.String("sg-1")}}, runErr: errors.New("run")})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "run instances")

	mgr = newWithEC2(&fakeEC2{subnets: []types.Subnet{{SubnetId: aws.String("subnet-1")}}, securityGroups: []types.SecurityGroup{{GroupId: aws.String("sg-1")}}, runOutput: &ec2.RunInstancesOutput{Instances: nil}})
	_, err = mgr.Create(context.Background(), compute.NewCreateRequest(compute.WithVPCID("vpc-1"), compute.WithName("n"), compute.WithImage("ami"), compute.WithSize("t3.micro")))
	require.ErrorContains(t, err, "returned no instances")
}

func TestCreateUsesProvidedSubnetAndSecurityGroups(t *testing.T) {
	t.Parallel()
	fake := &fakeEC2{}
	mgr := newWithEC2(fake)
	inst, err := mgr.Create(context.Background(), compute.NewCreateRequest(
		compute.WithVPCID("vpc-1"),
		compute.WithName("node-1"),
		compute.WithImage("ami-1"),
		compute.WithSize("t3.micro"),
		compute.WithSubnetID("subnet-x"),
		compute.WithSecurityGroupIDs([]string{"sg-a", "sg-b"}),
		compute.WithSSHUser("ec2-user"),
		compute.WithSSHPort(2222),
		compute.WithProviderConfig(CreateConfig{IAMInstanceProfile: "etcd-infra-ssm"}),
	))
	require.NoError(t, err)
	require.NotNil(t, fake.runInput)
	assert.Equal(t, "subnet-x", aws.ToString(fake.runInput.SubnetId))
	assert.Equal(t, []string{"sg-a", "sg-b"}, fake.runInput.SecurityGroupIds)
	require.NotNil(t, fake.runInput.IamInstanceProfile)
	assert.Equal(t, "etcd-infra-ssm", aws.ToString(fake.runInput.IamInstanceProfile.Name))
	assert.False(t, fake.describeSubnetsCalled)
	assert.False(t, fake.describeSecurityGroupsRun)
	assert.Equal(t, "ec2-user", inst.(compute.SSHInstance).SSHInfo().User) //nolint:forcetypeassert // Test file - type assertion is safe
	assert.Equal(t, 2222, inst.(compute.SSHInstance).SSHInfo().Port)       //nolint:forcetypeassert // Test file - type assertion is safe
}

func TestDeleteRequiresID(t *testing.T) {
	t.Parallel()
	mgr := newWithEC2(&fakeEC2{})
	_, err := mgr.Delete(context.Background(), compute.NewDeleteRequest(""))
	require.Error(t, err)
}

func TestDeletePauseResumeErrorsAndNilClient(t *testing.T) {
	t.Parallel()
	mgr := newWithEC2(nil)
	_, err := mgr.Delete(context.Background(), compute.NewDeleteRequest("i-1"))
	require.ErrorContains(t, err, "ec2 client is nil")
	require.ErrorContains(t, mgr.Stop(context.Background(), compute.NewPowerRequest("i-1")), "ec2 client is nil")
	require.ErrorContains(t, mgr.Start(context.Background(), compute.NewPowerRequest("i-1")), "ec2 client is nil")

	fake := &fakeEC2{terminateErr: errors.New("term"), stopErr: errors.New("stop"), startErr: errors.New("start")}
	mgr = newWithEC2(fake)
	_, err = mgr.Delete(context.Background(), compute.NewDeleteRequest("i-1"))
	require.ErrorContains(t, err, "terminate instance")
	require.ErrorContains(t, mgr.Stop(context.Background(), compute.NewPowerRequest("i-1")), "stop instance")
	require.ErrorContains(t, mgr.Start(context.Background(), compute.NewPowerRequest("i-1")), "start instance")

	_, err = mgr.Delete(context.Background(), compute.NewDeleteRequest("  "))
	require.ErrorContains(t, err, "required")
	require.ErrorContains(t, mgr.Stop(context.Background(), compute.NewPowerRequest(" ")), "required")
	require.ErrorContains(t, mgr.Start(context.Background(), compute.NewPowerRequest("\t")), "required")
}

func TestPauseResume(t *testing.T) {
	t.Parallel()
	fake := &fakeEC2{}
	mgr := newWithEC2(fake)
	const instanceID = "i-1"
	require.NoError(t, mgr.Stop(context.Background(), compute.NewPowerRequest(instanceID)))
	require.NoError(t, mgr.Start(context.Background(), compute.NewPowerRequest(instanceID)))
	assert.Equal(t, []string{instanceID}, fake.stopIDs)
	assert.Equal(t, []string{instanceID}, fake.startIDs)
}

func TestDeleteSuccessAndInstanceInfoBasics(t *testing.T) {
	t.Parallel()
	mgr := newWithEC2(&fakeEC2{})
	res, err := mgr.Delete(context.Background(), compute.NewDeleteRequest("i-9"))
	require.NoError(t, err)
	assert.Equal(t, compute.DeleteResult{ID: "i-9", Deleted: true}, res)

	ii := &instanceInfo{id: "i-1", publicIPv4: "1.2.3.4", privateIPv4: "10.0.0.10", ssh: compute.SSHConfig{User: "root", Port: 22}}
	assert.Equal(t, "i-1", ii.ID())
	assert.Equal(t, "1.2.3.4", ii.PublicIPv4())
	assert.Equal(t, "10.0.0.10", ii.PrivateIPv4())
	assert.Equal(t, "root", ii.SSHInfo().User)
	_, err = ii.RunCommand(context.Background(), []string{"echo"})
	require.ErrorContains(t, err, "ssm client is nil")
	_, err = ii.RunCommandWithOptions(context.Background(), []string{"echo"}, &compute.RunCommandOptions{})
	require.ErrorContains(t, err, "ssm client is nil")
}

func TestReplaceMachineTerminatesASGInstance(t *testing.T) {
	t.Parallel()

	fake := &fakeASG{describeInstancesOutput: &autoscaling.DescribeAutoScalingInstancesOutput{
		AutoScalingInstances: []asgtypes.AutoScalingInstanceDetails{{
			InstanceId:           aws.String("i-asg-member"),
			AutoScalingGroupName: aws.String("etcd-asg"),
			LifecycleState:       aws.String("InService"),
		}},
	}}
	mgr := &Manager{asg: fake}
	result, err := mgr.ReplaceMachine(context.Background(), compute.NewReplaceRequest("i-asg-member"))
	require.NoError(t, err)
	require.Equal(t, compute.ReplaceResult{PreviousID: "i-asg-member", Group: "etcd-asg"}, result)
	require.Equal(t, []string{"i-asg-member"}, fake.describeInstancesInput.InstanceIds)
	require.Equal(t, "i-asg-member", aws.ToString(fake.terminateInput.InstanceId))
	require.False(t, aws.ToBool(fake.terminateInput.ShouldDecrementDesiredCapacity))

	_, err = newWithEC2(&fakeEC2{}).ReplaceMachine(context.Background(), compute.NewReplaceRequest("i-standalone"))
	require.ErrorIs(t, err, compute.ErrNotSupported)
	_, err = (&Manager{asg: &fakeASG{}}).ReplaceMachine(context.Background(), compute.NewReplaceRequest("i-standalone"))
	require.ErrorIs(t, err, compute.ErrNotSupported)
	warm := &fakeASG{describeInstancesOutput: &autoscaling.DescribeAutoScalingInstancesOutput{
		AutoScalingInstances: []asgtypes.AutoScalingInstanceDetails{{
			InstanceId:           aws.String("i-warm"),
			AutoScalingGroupName: aws.String("etcd-asg"),
			LifecycleState:       aws.String("Warmed:Running"),
		}},
	}}
	_, err = (&Manager{asg: warm}).ReplaceMachine(context.Background(), compute.NewReplaceRequest("i-warm"))
	require.ErrorIs(t, err, compute.ErrNotSupported)
	require.Nil(t, warm.terminateInput)
}

func TestGetAndList(t *testing.T) {
	t.Parallel()

	fake := &fakeEC2{
		describeInstancesOutput: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{
					Instances: []types.Instance{
						{
							InstanceId:       aws.String("i-b"),
							PublicIpAddress:  aws.String("203.0.113.2"),
							PrivateIpAddress: aws.String("10.0.0.2"),
						},
						{
							InstanceId:       aws.String("i-a"),
							PublicIpAddress:  aws.String("203.0.113.1"),
							PrivateIpAddress: aws.String("10.0.0.1"),
						},
					},
				},
			},
		},
	}
	mgr := newWithClients(fake, nil)

	got, err := mgr.Get(context.Background(), "i-a")
	require.NoError(t, err)
	require.NotNil(t, fake.describeInstancesInput)
	assert.Equal(t, []string{"i-a"}, fake.describeInstancesInput.InstanceIds)
	assert.Equal(t, "i-a", got.ID())
	assert.Equal(t, "203.0.113.1", got.PublicIPv4())
	assert.Equal(t, "10.0.0.1", got.PrivateIPv4())

	list, err := mgr.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "i-a", list[0].ID())
	assert.Equal(t, "i-b", list[1].ID())
}

func TestListPagination(t *testing.T) {
	t.Parallel()

	token := "page2-token"
	fake := &fakeEC2{
		describeInstancesPages: []*ec2.DescribeInstancesOutput{
			{
				Reservations: []types.Reservation{
					{Instances: []types.Instance{
						{InstanceId: aws.String("i-page1")},
					}},
				},
				NextToken: &token,
			},
			{
				Reservations: []types.Reservation{
					{Instances: []types.Instance{
						{InstanceId: aws.String("i-page2")},
					}},
				},
			},
		},
	}
	mgr := newWithClients(fake, nil)

	list, err := mgr.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "i-page1", list[0].ID())
	assert.Equal(t, "i-page2", list[1].ID())
}

func TestRunCommandWithOptionsSSM(t *testing.T) {
	t.Parallel()

	fakeSSM := &fakeSSM{
		invokes: []*ssm.GetCommandInvocationOutput{
			{
				Status:       ssmtypes.CommandInvocationStatusPending,
				ResponseCode: -1,
			},
			{
				Status:                ssmtypes.CommandInvocationStatusSuccess,
				ResponseCode:          0,
				StandardOutputContent: aws.String("ok\n"),
				StandardErrorContent:  aws.String(""),
			},
		},
	}
	inst := &instanceInfo{
		id:  "i-123",
		ssm: fakeSSM,
		ec2: &fakeEC2{},
	}

	res, err := inst.RunCommandWithOptions(context.Background(), []string{"echo", "hello world"}, &compute.RunCommandOptions{
		Timeout: 5 * time.Second,
		WorkDir: "/tmp/work",
		Stdin:   strings.NewReader("input-data"),
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, "ok\n", res.Stdout)

	require.NotNil(t, fakeSSM.sendInput)
	assert.Equal(t, ssmDocumentRunShell, aws.ToString(fakeSSM.sendInput.DocumentName))
	params := fakeSSM.sendInput.Parameters
	require.Contains(t, params, ssmParameterCommands)
	require.Contains(t, params, ssmParameterExecTimeout)
	require.NotEmpty(t, params[ssmParameterCommands])
	assert.Equal(t, "5", params[ssmParameterExecTimeout][0])
	assert.Contains(t, params[ssmParameterCommands][0], "cd /tmp/work &&")
	assert.Contains(t, params[ssmParameterCommands][0], "echo 'hello world'")
	assert.Contains(t, params[ssmParameterCommands][0], "input-data")
}

func TestRunCommandShutdownTerminatesInstance(t *testing.T) {
	t.Parallel()

	fake := &fakeEC2{}
	inst := &instanceInfo{
		id:  "i-shutdown",
		ec2: fake,
	}

	res, err := inst.RunCommand(context.Background(), []string{"sudo", "shutdown", "now"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.ExitCode)
	assert.Equal(t, []string{"i-shutdown"}, fake.terminateIDs)

	res, err = inst.RunCommand(context.Background(), []string{"bash", "-c", "sudo shutdown -h now"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []string{"i-shutdown", "i-shutdown"}, fake.terminateIDs)
}

func TestRunCommandWithPendingInvocationError(t *testing.T) {
	t.Parallel()

	fakeSSM := &fakeSSM{
		getErr: &smithy.GenericAPIError{
			Code:    "InvocationDoesNotExist",
			Message: "not ready",
		},
	}
	inst := &instanceInfo{id: "i-1", ssm: fakeSSM}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := inst.RunCommandWithOptions(ctx, []string{"echo", "ok"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait for ssm command")
}

func TestBuildTagSpecifications(t *testing.T) {
	t.Parallel()
	assert.Nil(t, buildTagSpecifications(compute.Op{}))

	tags := buildTagSpecifications(compute.Op{Name: "node-1", Tags: map[string]string{"env": "dev"}})
	require.Len(t, tags, 1)
	require.Equal(t, types.ResourceTypeInstance, tags[0].ResourceType)
	require.Len(t, tags[0].Tags, 2)
	assert.Equal(t, "Name", aws.ToString(tags[0].Tags[0].Key))
	assert.Equal(t, "node-1", aws.ToString(tags[0].Tags[0].Value))
	assert.Equal(t, "env", aws.ToString(tags[0].Tags[1].Key))

	tags = buildTagSpecifications(compute.Op{Name: "node-1", Tags: map[string]string{"Name": "override"}})
	require.Len(t, tags, 1)
	require.Len(t, tags[0].Tags, 1)
	assert.Equal(t, "override", aws.ToString(tags[0].Tags[0].Value))
}
