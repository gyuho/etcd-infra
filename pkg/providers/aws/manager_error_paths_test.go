//nolint:all // Coverage-oriented tests.
package aws

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

// Get edge cases

func TestGetNilEC2(t *testing.T) {
	t.Parallel()
	mgr := &Manager{ec2: nil}
	_, err := mgr.Get(context.Background(), "i-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ec2 client is nil")
}

func TestGetEmptyID(t *testing.T) {
	t.Parallel()
	mgr := newWithClients(&fakeEC2{}, nil)
	_, err := mgr.Get(context.Background(), "  ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance id is required")
}

func TestGetDescribeError(t *testing.T) {
	t.Parallel()
	mgr := newWithClients(&fakeEC2{describeInstancesErr: errors.New("boom")}, nil)
	_, err := mgr.Get(context.Background(), "i-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "describe instance")
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()
	fake := &fakeEC2{
		describeInstancesOutput: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{Instances: []types.Instance{
					{InstanceId: aws.String("i-other")},
				}},
			},
		},
	}
	mgr := newWithClients(fake, nil)
	_, err := mgr.Get(context.Background(), "i-missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// List edge cases

func TestListNilEC2(t *testing.T) {
	t.Parallel()
	mgr := &Manager{ec2: nil}
	_, err := mgr.List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ec2 client is nil")
}

func TestListDescribeError(t *testing.T) {
	t.Parallel()
	mgr := newWithClients(&fakeEC2{describeInstancesErr: errors.New("boom")}, nil)
	_, err := mgr.List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list instances")
}

func TestListSkipsNilInstanceID(t *testing.T) {
	t.Parallel()
	fake := &fakeEC2{
		describeInstancesOutput: &ec2.DescribeInstancesOutput{
			Reservations: []types.Reservation{
				{Instances: []types.Instance{
					{InstanceId: nil},
					{InstanceId: aws.String("")},
					{InstanceId: aws.String("i-good"), PublicIpAddress: aws.String("1.2.3.4")},
				}},
			},
		},
	}
	mgr := newWithClients(fake, nil)
	list, err := mgr.List(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "i-good", list[0].ID())
}

// RunCommandWithOptions edge cases

func TestRunCommandEmptyID(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{id: "  "}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance id is required")
}

func TestRunCommandEmptyCommand(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{id: "i-1"}
	_, err := inst.RunCommandWithOptions(context.Background(), nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command cannot be empty")
}

func TestRunCommandShutdownNilEC2(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{id: "i-1", ec2: nil}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"shutdown"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ec2 client is nil")
}

func TestRunCommandShutdownTerminateError(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{id: "i-1", ec2: &fakeEC2{terminateErr: errors.New("terminate fail")}}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"poweroff"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminate instance")
}

func TestRunCommandNilSSM(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{id: "i-1", ssm: nil, ec2: &fakeEC2{}}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"echo", "hi"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssm client is nil")
}

func TestRunCommandSendError(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id:  "i-1",
		ssm: &fakeSSM{sendErr: errors.New("send fail")},
		ec2: &fakeEC2{},
	}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "send ssm command")
}

func TestRunCommandEmptyCommandID(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id: "i-1",
		ssm: &fakeSSM{
			sendOutput: &ssm.SendCommandOutput{Command: &ssmtypes.Command{CommandId: aws.String("")}},
		},
		ec2: &fakeEC2{},
	}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty command id")
}

func TestRunCommandNonPendingGetError(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id: "i-1",
		ssm: &fakeSSM{
			getErr: errors.New("some other error"),
		},
		ec2: &fakeEC2{},
	}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get ssm command invocation")
}

func TestRunCommandFailedStatus(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id: "i-1",
		ssm: &fakeSSM{
			invokes: []*ssm.GetCommandInvocationOutput{
				{
					Status:                ssmtypes.CommandInvocationStatusFailed,
					ResponseCode:          -1,
					StandardOutputContent: aws.String(""),
					StandardErrorContent:  aws.String("exit status 1"),
				},
			},
		},
		ec2: &fakeEC2{},
	}
	res, err := inst.RunCommandWithOptions(context.Background(), []string{"false"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.ExitCode)
}

func TestRunCommandSuccessNegativeCode(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id: "i-1",
		ssm: &fakeSSM{
			invokes: []*ssm.GetCommandInvocationOutput{
				{
					Status:                ssmtypes.CommandInvocationStatusSuccess,
					ResponseCode:          -1,
					StandardOutputContent: aws.String("ok"),
				},
			},
		},
		ec2: &fakeEC2{},
	}
	res, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
}

// pipeStdinToCommand

func TestPipeStdinToCommandEmpty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "echo hi", pipeStdinToCommand("echo hi", ""))
}

func TestPipeStdinToCommandDelimiterCollision(t *testing.T) {
	t.Parallel()
	// stdin contains the default delimiter, so it should append _1
	result := pipeStdinToCommand("cat", "ETCD_TEST_STDIN_EOF")
	assert.Contains(t, result, "ETCD_TEST_STDIN_EOF_1")
	assert.Contains(t, result, "cat")
}

// isInvocationPendingError

func TestIsInvocationPendingErrorNil(t *testing.T) {
	t.Parallel()
	assert.False(t, isInvocationPendingError(nil))
}

func TestIsInvocationPendingErrorAPIError(t *testing.T) {
	t.Parallel()
	apiErr := &smithy.GenericAPIError{Code: "InvocationDoesNotExist", Message: "pending"}
	assert.True(t, isInvocationPendingError(apiErr))
}

func TestIsInvocationPendingErrorStringMatch(t *testing.T) {
	t.Parallel()
	assert.True(t, isInvocationPendingError(errors.New("InvocationDoesNotExist")))
	assert.False(t, isInvocationPendingError(errors.New("SomethingElse")))
}

// containsShutdownWord

func TestContainsShutdownWordFalse(t *testing.T) {
	t.Parallel()
	assert.False(t, containsShutdownWord("echo hello"))
	assert.False(t, containsShutdownWord("ls -la"))
}

func TestContainsShutdownWordTrue(t *testing.T) {
	t.Parallel()
	assert.True(t, containsShutdownWord("sudo shutdown -h now"))
	assert.True(t, containsShutdownWord("poweroff"))
	assert.True(t, containsShutdownWord("halt"))
	assert.True(t, containsShutdownWord("reboot"))
}

// isShutdownCommand edge cases

func TestIsShutdownCommandEmpty(t *testing.T) {
	t.Parallel()
	assert.False(t, isShutdownCommand(nil))
	assert.False(t, isShutdownCommand([]string{}))
	assert.False(t, isShutdownCommand([]string{"", "  "}))
}

func TestIsShutdownCommandDirect(t *testing.T) {
	t.Parallel()
	assert.True(t, isShutdownCommand([]string{"halt"}))
	assert.True(t, isShutdownCommand([]string{"reboot"}))
	assert.True(t, isShutdownCommand([]string{"sudo", "halt"}))
}

func TestIsShutdownCommandShellPath(t *testing.T) {
	t.Parallel()
	assert.True(t, isShutdownCommand([]string{"sh", "-c", "sudo shutdown -h now"}))
	assert.False(t, isShutdownCommand([]string{"bash", "-c", "echo hello"}))
}

func TestIsShutdownCommandNonShutdown(t *testing.T) {
	t.Parallel()
	assert.False(t, isShutdownCommand([]string{"echo", "hello"}))
	assert.False(t, isShutdownCommand([]string{"sudo", "echo", "hello"}))
}

// instanceInfo accessors

func TestInstanceInfoAccessors(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id:          "i-xyz",
		publicIPv4:  "1.2.3.4",
		privateIPv4: "10.0.0.1",
		ssh:         compute.SSHConfig{User: "ubuntu", Port: 22},
	}
	assert.Equal(t, "i-xyz", inst.ID())
	assert.Equal(t, "1.2.3.4", inst.PublicIPv4())
	assert.Equal(t, "10.0.0.1", inst.PrivateIPv4())
	assert.Equal(t, "ubuntu", inst.SSHInfo().User)
}

// ssmExecTimeout

func TestSSMExecTimeoutDefault(t *testing.T) {
	t.Parallel()
	assert.Equal(t, defaultSSMExecTimeout, ssmExecTimeout(nil))
	assert.Equal(t, defaultSSMExecTimeout, ssmExecTimeout(&compute.RunCommandOptions{}))
}

func TestSSMExecTimeoutCustom(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 30*time.Second, ssmExecTimeout(&compute.RunCommandOptions{Timeout: 30 * time.Second}))
}

// RunCommandWithOptions delayed status then success

func TestRunCommandDelayedThenSuccess(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id: "i-1",
		ssm: &fakeSSM{
			invokes: []*ssm.GetCommandInvocationOutput{
				{Status: ssmtypes.CommandInvocationStatusDelayed, ResponseCode: -1},
				{Status: ssmtypes.CommandInvocationStatusInProgress, ResponseCode: -1},
				{
					Status:                ssmtypes.CommandInvocationStatusSuccess,
					ResponseCode:          0,
					StandardOutputContent: aws.String("done"),
				},
			},
		},
		ec2: &fakeEC2{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := inst.RunCommandWithOptions(ctx, []string{"echo"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "done", res.Stdout)
}

// RunCommand stdin read error

func TestRunCommandStdinReadError(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{id: "i-1", ssm: &fakeSSM{}, ec2: &fakeEC2{}}
	_, err := inst.RunCommandWithOptions(context.Background(), []string{"echo"}, &compute.RunCommandOptions{
		Stdin: &errReader{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read stdin")
}

type errReader struct{}

func (e *errReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }

// Timeout during pending status

func TestRunCommandPendingStatusTimeout(t *testing.T) {
	t.Parallel()
	inst := &instanceInfo{
		id: "i-1",
		ssm: &fakeSSM{
			invokes: []*ssm.GetCommandInvocationOutput{
				{Status: ssmtypes.CommandInvocationStatusPending, ResponseCode: -1},
				{Status: ssmtypes.CommandInvocationStatusPending, ResponseCode: -1},
				{Status: ssmtypes.CommandInvocationStatusPending, ResponseCode: -1},
			},
		},
		ec2: &fakeEC2{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := inst.RunCommandWithOptions(ctx, []string{"sleep", "999"}, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "wait for ssm command") || strings.Contains(err.Error(), "context"))
}
