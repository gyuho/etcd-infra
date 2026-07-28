package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"time"

	"git.tbd/etcd-infra/pkg/providers/compute"
	"git.tbd/etcd-infra/pkg/shell"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/smithy-go"
)

const (
	defaultSSHUser          = "root"
	defaultSSMExecTimeout   = 15 * time.Minute
	defaultSSMWaitInterval  = 2 * time.Second
	ssmDocumentRunShell     = "AWS-RunShellScript"
	ssmParameterCommands    = "commands"
	ssmParameterExecTimeout = "executionTimeout"
)

// Compile-time interface compliance checks.
var (
	_ compute.Provider           = (*Manager)(nil)
	_ compute.CapabilityReporter = (*Manager)(nil)
	_ compute.ReadinessWaiter    = (*Manager)(nil)
	_ compute.GroupScaler        = (*Manager)(nil)
	_ compute.Instance           = (*instanceInfo)(nil)
	_ compute.SSHInstance        = (*instanceInfo)(nil)
	_ compute.InstanceMetadata   = (*instanceInfo)(nil)
)

// Manager manages AWS EC2 instances and autoscaling groups.
type Manager struct {
	ec2 ec2API
	ssm ssmAPI
	asg asgAPI
}

type ec2API interface {
	RunInstances(ctx context.Context, input *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	TerminateInstances(ctx context.Context, input *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	StopInstances(ctx context.Context, input *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	StartInstances(ctx context.Context, input *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	DescribeSubnets(ctx context.Context, input *ec2.DescribeSubnetsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(ctx context.Context, input *ec2.DescribeSecurityGroupsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeInstances(ctx context.Context, input *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

type ssmAPI interface {
	SendCommand(ctx context.Context, input *ssm.SendCommandInput, optFns ...func(*ssm.Options)) (*ssm.SendCommandOutput, error)
	GetCommandInvocation(ctx context.Context, input *ssm.GetCommandInvocationInput, optFns ...func(*ssm.Options)) (*ssm.GetCommandInvocationOutput, error)
}

type asgAPI interface {
	DescribeAutoScalingGroups(ctx context.Context, input *autoscaling.DescribeAutoScalingGroupsInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
	DescribeAutoScalingInstances(ctx context.Context, input *autoscaling.DescribeAutoScalingInstancesInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingInstancesOutput, error)
	UpdateAutoScalingGroup(ctx context.Context, input *autoscaling.UpdateAutoScalingGroupInput, optFns ...func(*autoscaling.Options)) (*autoscaling.UpdateAutoScalingGroupOutput, error)
	TerminateInstanceInAutoScalingGroup(ctx context.Context, input *autoscaling.TerminateInstanceInAutoScalingGroupInput, optFns ...func(*autoscaling.Options)) (*autoscaling.TerminateInstanceInAutoScalingGroupOutput, error)
}

// New creates a Manager using the provided AWS configuration.
func New(cfg aws.Config) *Manager {
	return &Manager{
		ec2: ec2.NewFromConfig(cfg),
		ssm: ssm.NewFromConfig(cfg),
		asg: autoscaling.NewFromConfig(cfg),
	}
}

// newWithEC2 creates a Manager using only the provided EC2 client (test helper).
func newWithEC2(client ec2API) *Manager {
	return &Manager{
		ec2: client,
	}
}

// newWithClients creates a Manager using explicit EC2/SSM clients (test helper).
func newWithClients(ec2Client ec2API, ssmClient ssmAPI) *Manager { //nolint:unparam // ssmClient is always nil in current tests but kept for SSM test coverage
	return &Manager{
		ec2: ec2Client,
		ssm: ssmClient,
	}
}

// Capabilities reports the AWS compute manager capability surface.
func (m *Manager) Capabilities() compute.CapabilitySet {
	caps := []compute.Capability{
		compute.CapabilityLifecycleCreateDelete,
		compute.CapabilityPowerControl,
		compute.CapabilityInventoryRead,
		compute.CapabilityCommandExecution,
		compute.CapabilitySSHAccess,
		compute.CapabilityReadinessWait,
		compute.CapabilityInstanceMetadata,
	}
	if m.asg != nil {
		caps = append(caps, compute.CapabilityGroupScaling)
	}
	return compute.NewCapabilitySet(caps...)
}

// instanceInfo implements compute.Instance, compute.SSHInstance, and
// compute.InstanceMetadata for AWS EC2 instances.
type instanceInfo struct {
	id               string
	publicIPv4       string
	privateIPv4      string
	state            compute.InstanceState
	tags             map[string]string
	availabilityZone string
	instanceType     string
	ssh              compute.SSHConfig
	ec2              ec2API
	ssm              ssmAPI
}

func (i *instanceInfo) ID() string          { return i.id }
func (i *instanceInfo) PublicIPv4() string  { return i.publicIPv4 }
func (i *instanceInfo) PrivateIPv4() string { return i.privateIPv4 }
func (i *instanceInfo) State() compute.InstanceState {
	if i.state == "" {
		return compute.InstanceStateUnknown
	}
	return i.state
}

func (i *instanceInfo) SSHInfo() compute.SSHConfig {
	return i.ssh
}

// Tags returns EC2 instance tags as key-value pairs.
func (i *instanceInfo) Tags() map[string]string {
	if i.tags == nil {
		return make(map[string]string)
	}
	out := make(map[string]string, len(i.tags))
	maps.Copy(out, i.tags)
	return out
}

// AvailabilityZone returns the EC2 instance's placement AZ (e.g., "us-east-1a").
func (i *instanceInfo) AvailabilityZone() string {
	return i.availabilityZone
}

// InstanceType returns the EC2 instance type (e.g., "t3a.medium", "m5.xlarge").
func (i *instanceInfo) InstanceType() string {
	return i.instanceType
}

// RunCommand executes a command on the instance.
func (i *instanceInfo) RunCommand(ctx context.Context, command []string) (*compute.ExecuteResult, error) {
	return i.RunCommandWithOptions(ctx, command, nil)
}

// RunCommandWithOptions executes a command using AWS SSM RunCommand.
func (i *instanceInfo) RunCommandWithOptions(ctx context.Context, command []string, opts *compute.RunCommandOptions) (*compute.ExecuteResult, error) {
	if strings.TrimSpace(i.id) == "" {
		return nil, errors.New("aws: instance id is required")
	}
	if len(command) == 0 {
		return nil, errors.New("aws: command cannot be empty")
	}

	// Explicitly requested behavior: treat host shutdown as instance termination.
	if isShutdownCommand(command) {
		return i.terminateAsShutdown(ctx)
	}

	if i.ssm == nil {
		return nil, errors.New("aws: ssm client is nil")
	}

	commandString, err := buildSSMCommandString(command, opts)
	if err != nil {
		return nil, err
	}

	commandID, waitCtx, cancel, err := i.sendSSMCommand(ctx, commandString, opts)
	if err != nil {
		return nil, err
	}
	defer cancel()

	return i.waitForSSMResult(waitCtx, commandID)
}

// terminateAsShutdown handles shutdown commands by terminating the EC2 instance.
func (i *instanceInfo) terminateAsShutdown(ctx context.Context) (*compute.ExecuteResult, error) {
	if i.ec2 == nil {
		return nil, errors.New("aws: ec2 client is nil")
	}
	if _, err := i.ec2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{i.id},
	}); err != nil {
		return nil, fmt.Errorf("aws: terminate instance %s: %w", i.id, err)
	}
	return &compute.ExecuteResult{
		ExitCode: 0,
		Stdout:   "instance termination requested via shutdown command\n",
	}, nil
}

// buildSSMCommandString constructs the shell command string with optional workdir and stdin piping.
func buildSSMCommandString(command []string, opts *compute.RunCommandOptions) (string, error) {
	commandString := shell.JoinArgs(command)
	if commandString == "" {
		return "", errors.New("aws: command cannot be empty")
	}
	if opts != nil && strings.TrimSpace(opts.WorkDir) != "" {
		commandString = "cd " + shell.Quote(strings.TrimSpace(opts.WorkDir)) + " && " + commandString
	}
	if opts != nil && opts.Stdin != nil {
		stdinData, err := io.ReadAll(opts.Stdin)
		if err != nil {
			return "", fmt.Errorf("aws: read stdin: %w", err)
		}
		commandString = pipeStdinToCommand(commandString, string(stdinData))
	}
	return commandString, nil
}

// sendSSMCommand sends a command via SSM and returns the command ID and a
// context with a timeout covering the SSM execution window.
func (i *instanceInfo) sendSSMCommand(ctx context.Context, commandString string, opts *compute.RunCommandOptions) (string, context.Context, context.CancelFunc, error) {
	execTimeout := ssmExecTimeout(opts)
	waitCtx, cancel := context.WithTimeout(ctx, execTimeout+30*time.Second)

	sendOut, err := i.ssm.SendCommand(waitCtx, &ssm.SendCommandInput{
		InstanceIds:  []string{i.id},
		DocumentName: aws.String(ssmDocumentRunShell),
		Parameters: map[string][]string{
			ssmParameterCommands:    {commandString},
			ssmParameterExecTimeout: {strconv.FormatInt(int64(execTimeout/time.Second), 10)},
		},
	})
	if err != nil {
		cancel()
		return "", nil, nil, fmt.Errorf("aws: send ssm command on %s: %w", i.id, err)
	}
	if sendOut.Command == nil || sendOut.Command.CommandId == nil || *sendOut.Command.CommandId == "" {
		cancel()
		return "", nil, nil, errors.New("aws: ssm send command returned empty command id")
	}

	return *sendOut.Command.CommandId, waitCtx, cancel, nil
}

// waitForSSMResult polls SSM until the command reaches a terminal state.
func (i *instanceInfo) waitForSSMResult(waitCtx context.Context, commandID string) (*compute.ExecuteResult, error) {
	ticker := time.NewTicker(defaultSSMWaitInterval)
	defer ticker.Stop()

	for {
		inv, err := i.ssm.GetCommandInvocation(waitCtx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(commandID),
			InstanceId: aws.String(i.id),
		})
		if err != nil {
			if isInvocationPendingError(err) {
				if waitErr := waitForTickOrCancel(waitCtx, ticker, commandID, i.id); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, fmt.Errorf("aws: get ssm command invocation %s on %s: %w", commandID, i.id, err)
		}

		//nolint:exhaustive // Default handles terminal states (Success, Failed, etc.)
		switch inv.Status {
		case ssmtypes.CommandInvocationStatusPending,
			ssmtypes.CommandInvocationStatusInProgress,
			ssmtypes.CommandInvocationStatusDelayed:
			if waitErr := waitForTickOrCancel(waitCtx, ticker, commandID, i.id); waitErr != nil {
				return nil, waitErr
			}
			continue
		default:
			return buildSSMExecuteResult(inv), nil
		}
	}
}

// waitForTickOrCancel waits for the next ticker tick or context cancellation.
func waitForTickOrCancel(ctx context.Context, ticker *time.Ticker, commandID, instanceID string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("aws: wait for ssm command %s on %s: %w", commandID, instanceID, ctx.Err())
	case <-ticker.C:
		return nil
	}
}

// buildSSMExecuteResult converts an SSM invocation to an ExecuteResult.
func buildSSMExecuteResult(inv *ssm.GetCommandInvocationOutput) *compute.ExecuteResult {
	exitCode := int(inv.ResponseCode)
	if inv.Status == ssmtypes.CommandInvocationStatusSuccess && exitCode < 0 {
		exitCode = 0
	}
	if inv.Status != ssmtypes.CommandInvocationStatusSuccess && exitCode <= 0 {
		exitCode = 1
	}
	return &compute.ExecuteResult{
		ExitCode: exitCode,
		Stdout:   aws.ToString(inv.StandardOutputContent),
		Stderr:   aws.ToString(inv.StandardErrorContent),
	}
}

// instanceFromEC2 converts an EC2 types.Instance to an instanceInfo.
func (m *Manager) instanceFromEC2(instance types.Instance) *instanceInfo {
	var az string
	if instance.Placement != nil {
		az = aws.ToString(instance.Placement.AvailabilityZone)
	}
	return &instanceInfo{
		id:               aws.ToString(instance.InstanceId),
		publicIPv4:       aws.ToString(instance.PublicIpAddress),
		privateIPv4:      aws.ToString(instance.PrivateIpAddress),
		state:            mapEC2State(instance.State),
		tags:             extractTags(instance.Tags),
		availabilityZone: az,
		instanceType:     string(instance.InstanceType),
		ec2:              m.ec2,
		ssm:              m.ssm,
		ssh: compute.SSHConfig{
			User: defaultSSHUser,
			Port: 22,
		},
	}
}

func extractTags(tags []types.Tag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	result := make(map[string]string, len(tags))
	for _, tag := range tags {
		if tag.Key != nil && tag.Value != nil {
			result[*tag.Key] = *tag.Value
		}
	}
	return result
}

func mapEC2State(state *types.InstanceState) compute.InstanceState {
	if state == nil {
		return compute.InstanceStateUnknown
	}
	switch state.Name {
	case types.InstanceStateNamePending:
		return compute.InstanceStatePending
	case types.InstanceStateNameRunning:
		return compute.InstanceStateRunning
	case types.InstanceStateNameStopping:
		return compute.InstanceStateStopping
	case types.InstanceStateNameStopped:
		return compute.InstanceStateStopped
	case types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated:
		return compute.InstanceStateTerminated
	default:
		return compute.InstanceStateUnknown
	}
}

func ssmExecTimeout(opts *compute.RunCommandOptions) time.Duration {
	if opts != nil && opts.Timeout > 0 {
		return opts.Timeout
	}
	return defaultSSMExecTimeout
}

func pipeStdinToCommand(command, stdin string) string {
	if stdin == "" {
		return command
	}
	delimiter := "ETCD_TEST_STDIN_EOF"
	for strings.Contains(stdin, delimiter) {
		delimiter += "_1"
	}
	return fmt.Sprintf("cat <<'%s' | %s\n%s\n%s", delimiter, command, stdin, delimiter)
}

func isInvocationPendingError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvocationDoesNotExist" {
		return true
	}
	return strings.Contains(err.Error(), "InvocationDoesNotExist")
}

func isShutdownCommand(command []string) bool {
	if len(command) == 0 {
		return false
	}
	parts := make([]string, 0, len(command))
	for _, c := range command {
		c = strings.TrimSpace(c)
		if c != "" {
			parts = append(parts, c)
		}
	}
	if len(parts) == 0 {
		return false
	}
	lower := make([]string, len(parts))
	for i := range parts {
		lower[i] = strings.ToLower(parts[i])
	}

	// direct command path: sudo shutdown now
	if lower[0] == "sudo" && len(lower) > 1 {
		lower = lower[1:]
	}
	if len(lower) > 0 {
		switch lower[0] {
		case "shutdown", "poweroff", "halt", "reboot": //nolint:goconst // Contextual string usage
			return true
		}
	}

	// shell path: bash -c 'sudo shutdown -h now'
	if len(lower) >= 3 && (lower[0] == "bash" || lower[0] == "sh") && lower[1] == "-c" {
		script := strings.ToLower(strings.Join(lower[2:], " "))
		return containsShutdownWord(script)
	}

	return false
}

func containsShutdownWord(script string) bool {
	for _, tok := range strings.FieldsFunc(script, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		switch tok {
		case "shutdown", "poweroff", "halt", "reboot":
			return true
		}
	}
	return false
}
