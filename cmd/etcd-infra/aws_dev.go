package main

// "aws dev" manages groups of ephemeral, identical Ubuntu boxes for manual
// and agent-driven testing of private branches. A dev group is one EC2
// launch template (the pinned spec) plus one Auto Scaling group that runs
// --count copies of it across the VPC's subnets. Every box is an empty
// instance (no etcd) with:
//
//   - a dedicated encrypted gp3 EBS data volume, formatted and mounted at
//     --mount-point (default /mnt/data), remounted across reboots via fstab;
//   - the AWS CLI and a verified write path to its own results prefix
//     s3://<bucket>/etcd-infra/dev/<name>/<instance-id>/;
//   - SSM command access: "aws dev run" executes commands as root over SSM
//     RunCommand, and "aws ssm start-session" opens an interactive shell.
//
// Boxes configure themselves at first boot (launch template user data), so
// scale-outs come up identical to the originals. Groups create no network
// resources: any number of groups can share one existing VPC.
//
// Workflow (every command is keyed by --name; state lives in
// ~/.etcd-infra/aws/<name>.json, the same store "aws up" uses). stdout of
// up/scale/status is only key=value lines; progress and hints go to stderr.
//
//	etcd-infra aws dev up --name dev01 --count 3 --instance-profile etcd-infra-ssm \
//	    --bucket etcd-infra-e2e-... --dry-run=false
//	etcd-infra aws dev run --name dev01 -- uname -a            # every box
//	etcd-infra aws dev run --name dev01 --instance i-0abc --script ./test.sh
//	etcd-infra aws dev etcd --name dev01 --binary ./bin/etcd   # single-member etcd per box
//	etcd-infra aws dev scale --name dev01 --count 5
//	etcd-infra aws dev status --name dev01
//	etcd-infra aws dev down --name dev01
//
// Cleanup contract: the launch template and group are named after --name
// and, with a random owner token that tags everything the group creates,
// recorded in the state file before either is created, so "dev down" can
// finish any partial "up". Data volumes are deleted with their instances.
// "dev down" refuses to run under another AWS account (where every lookup
// would answer "not found" for live resources), force-deletes the group
// (terminating its instances) and waits until it is gone, terminates and
// waits out any instance still tagged for the group, deletes the launch
// template, and only then removes the state file. It deletes only resources
// carrying the owner token, never by name alone. S3 results are kept.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
	"git.tbd/etcd-infra/pkg/providers/compute"
	"git.tbd/etcd-infra/pkg/shell"
)

const (
	defaultAWSDevName         = "etcd-infra-dev"
	defaultAWSDevMountPoint   = "/mnt/data"
	defaultAWSDevVolumeSizeGB = 32
	defaultAWSDevRunTimeout   = time.Hour
	// maxAWSDevCount bounds a group's size so a typo cannot launch a fleet.
	maxAWSDevCount        = 20
	awsDevCapacityTimeout = 10 * time.Minute
	awsDevSetupTimeout    = 15 * time.Minute
	awsDevTeardownTimeout = 15 * time.Minute
	awsDevPollInterval    = 10 * time.Second
	awsDevRole            = "dev"
	// awsDevEnvFile is sourced by every "aws dev run" command and by login
	// shells (start-session), so both see the same box environment. Setup
	// writes it last, so its presence means setup succeeded.
	awsDevEnvFile = "/etc/profile.d/etcd-infra-dev.sh"
	// awsDevSetupLog and awsDevSetupExitFile record the first-boot setup.
	awsDevSetupLog      = "/var/log/etcd-infra-dev-setup.log"
	awsDevSetupExitFile = "/var/lib/etcd-infra-dev/setup-exit-code"
	// ssmOutputLimit is the SSM GetCommandInvocation stdout/stderr cap.
	ssmOutputLimit = 24000
)

// awsDevState records a dev group; it marks an awsState as a dev group.
type awsDevState struct {
	// AccountID is the AWS account that created the group; teardown runs
	// only under the same account.
	AccountID        string   `json:"accountID"`
	VPCID            string   `json:"vpcID"`
	SubnetIDs        []string `json:"subnetIDs"`
	SecurityGroupIDs []string `json:"securityGroupIDs"`
	AMI              string   `json:"ami"`
	InstanceType     string   `json:"instanceType"`
	MountPoint       string   `json:"mountPoint"`
	VolumeSizeGB     int      `json:"volumeSizeGB"`
	Bucket           string   `json:"bucket"`
	// ResultsURI is the group's S3 prefix; each box writes under
	// <ResultsURI><instance-id>/, exported as $ETCD_INFRA_DEV_RESULTS.
	ResultsURI string `json:"resultsURI"`
	Count      int    `json:"count"`
	// AutoScalingGroup and LaunchTemplate are recorded before creation.
	AutoScalingGroup      string `json:"autoScalingGroup"`
	LaunchTemplate        string `json:"launchTemplate"`
	LaunchTemplateID      string `json:"launchTemplateID,omitempty"`
	LaunchTemplateVersion int64  `json:"launchTemplateVersion,omitempty"`
	// OwnerToken is random per "up" and tags everything the group creates;
	// teardown deletes only resources carrying it, never by name alone.
	OwnerToken string `json:"ownerToken,omitempty"`
}

type awsDevUpOptions struct {
	Name               string
	Region             string
	VPCID              string
	SubnetIDs          []string
	SecurityGroupIDs   []string
	AMI                string
	UbuntuRelease      string
	Arch               string
	InstanceType       string
	IAMInstanceProfile string
	VolumeSizeGB       int
	MountPoint         string
	Bucket             string
	Count              int
	DryRun             bool
}

func runAWSDev(ctx context.Context, args []string) error {
	const usage = "usage: etcd-infra aws dev <up|run|etcd|scale|status|down>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "up":
		return runAWSDevUp(ctx, args[1:])
	case "run":
		return runAWSDevRun(ctx, args[1:])
	case "etcd":
		return runAWSDevEtcd(ctx, args[1:])
	case "scale":
		return runAWSDevScale(ctx, args[1:])
	case "status":
		return runAWSDevStatus(ctx, args[1:])
	case "down":
		return runAWSDevDown(ctx, args[1:])
	default:
		return fmt.Errorf("unknown aws dev command %q; %s", args[0], usage)
	}
}

// runAWSDevUp creates the group: record state, create the launch template
// and Auto Scaling group, wait for --count in-service instances, and wait
// for each box's first-boot setup. State is saved before anything is
// created so any later failure is cleanable by "dev down".
func runAWSDevUp(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev up", flag.ContinueOnError)
	opts := awsDevUpOptions{}
	var subnets, securityGroups string
	flags.StringVar(&opts.Name, "name", defaultAWSDevName, "dev group name (state key, launch template and Auto Scaling group name, etcd-infra.cluster tag)")
	flags.IntVar(&opts.Count, "count", 1, fmt.Sprintf("number of identical boxes (1-%d); change later with 'aws dev scale'", maxAWSDevCount))
	flags.StringVar(&opts.Region, "region", os.Getenv("AWS_REGION"), "AWS region (defaults to AWS configuration)")
	flags.StringVar(&opts.VPCID, "vpc", os.Getenv("ETCD_INFRA_AWS_VPC"), "existing VPC ID, shareable by any number of groups (default: $ETCD_INFRA_AWS_VPC, else the region's default VPC)")
	flags.StringVar(&subnets, "subnets", "", "comma-separated subnet IDs to spread boxes across (default: every subnet in the VPC); each needs outbound internet or NAT")
	flags.StringVar(&securityGroups, "security-groups", "", "comma-separated security group IDs (default: the VPC default group); no inbound rules are needed")
	flags.StringVar(&opts.AMI, "ami", "", "AMI ID override (default: latest Canonical Ubuntu Server for --ubuntu-release/--arch)")
	flags.StringVar(&opts.UbuntuRelease, "ubuntu-release", awsprovider.DefaultUbuntuRelease, "Ubuntu Server release when --ami is unset (22.04, 24.04, 26.04)")
	flags.StringVar(&opts.Arch, "arch", "amd64", "CPU architecture (amd64 or arm64); must match the instance type")
	flags.StringVar(&opts.InstanceType, "instance-type", "", "EC2 instance type (default: t3a.medium for amd64, t4g.medium for arm64)")
	flags.StringVar(&opts.IAMInstanceProfile, "instance-profile", os.Getenv("ETCD_INFRA_AWS_INSTANCE_PROFILE"), "IAM instance profile with SSM core and S3 write on --bucket (default: $ETCD_INFRA_AWS_INSTANCE_PROFILE; required)")
	flags.IntVar(&opts.VolumeSizeGB, "volume-size-gb", defaultAWSDevVolumeSizeGB, "data volume size in GiB")
	flags.StringVar(&opts.MountPoint, "mount-point", defaultAWSDevMountPoint, "absolute path where the data volume is mounted")
	flags.StringVar(&opts.Bucket, "bucket", os.Getenv("ETCD_INFRA_AWS_S3_BUCKET"), "S3 bucket for results (default: $ETCD_INFRA_AWS_S3_BUCKET; required)")
	flags.BoolVar(&opts.DryRun, "dry-run", true, "show the plan without creating AWS resources")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	opts.SubnetIDs = splitCSV(subnets)
	opts.SecurityGroupIDs = splitCSV(securityGroups)
	if opts.InstanceType == "" {
		opts.InstanceType = defaultBastionInstanceType(opts.Arch)
	}
	if err := validateAWSDevUpOptions(opts); err != nil {
		return err
	}
	amiParameter := ""
	if opts.AMI == "" {
		var err error
		if amiParameter, err = awsprovider.UbuntuAMIParameter(opts.UbuntuRelease, opts.Arch); err != nil {
			return err
		}
	}
	if opts.DryRun {
		printAWSDevPlan(opts, amiParameter)
		return nil
	}

	statePath, err := awsStatePath(opts.Name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(statePath); err == nil {
		return fmt.Errorf("AWS state already exists at %s; pick another --name or run 'etcd-infra aws dev down --name %s'", statePath, opts.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check AWS state: %w", err)
	}

	cfg, err := awsprovider.LoadDefaultConfig(ctx, opts.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		return errors.New("AWS region is required via --region or AWS configuration")
	}
	opts.Region = cfg.Region
	manager := awsprovider.New(cfg)
	accountID, err := manager.AccountID(ctx)
	if err != nil {
		return fmt.Errorf("identify AWS account: %w", err)
	}
	if err := resolveAWSDevNetwork(ctx, manager, &opts); err != nil {
		return err
	}
	if amiParameter != "" {
		// Resolved once here, not via "resolve:ssm:" in the template: a
		// template pinned to one AMI keeps later scale-outs identical.
		ami, resolveErr := manager.ResolveSSMParameter(ctx, amiParameter)
		if resolveErr != nil {
			return fmt.Errorf("resolve Ubuntu AMI (grant ssm:GetParameters on the public parameter, or pass --ami): %w", resolveErr)
		}
		opts.AMI = ami
	}

	ownerToken, err := newAWSDevOwnerToken()
	if err != nil {
		return err
	}
	state := awsState{
		Name:   opts.Name,
		Region: opts.Region,
		Arch:   opts.Arch,
		Dev: &awsDevState{
			AccountID:        accountID,
			VPCID:            opts.VPCID,
			SubnetIDs:        opts.SubnetIDs,
			SecurityGroupIDs: opts.SecurityGroupIDs,
			AMI:              opts.AMI,
			InstanceType:     opts.InstanceType,
			MountPoint:       opts.MountPoint,
			VolumeSizeGB:     opts.VolumeSizeGB,
			Bucket:           opts.Bucket,
			ResultsURI:       awsDevResultsURI(opts.Bucket, opts.Name),
			Count:            opts.Count,
			AutoScalingGroup: opts.Name,
			LaunchTemplate:   opts.Name,
			OwnerToken:       ownerToken,
		},
	}
	if err := writeAWSState(statePath, state); err != nil {
		return fmt.Errorf("save AWS state: %w", err)
	}
	spec := awsDevLaunchGroupSpec(state, opts.IAMInstanceProfile)

	fmt.Fprintf(os.Stderr, "creating launch template and Auto Scaling group %s (%d x %s, %s, %s, VPC %s)\n",
		opts.Name, opts.Count, opts.InstanceType, opts.AMI, opts.Region, opts.VPCID)
	templateID, version, err := manager.CreateLaunchTemplate(ctx, spec)
	if errors.Is(err, awsprovider.ErrAlreadyExists) {
		// The name belongs to another group (another machine or user; ours
		// would carry our owner token): nothing was created, and "down"
		// must never touch it.
		return errors.Join(
			fmt.Errorf("launch template %s already exists in %s; pick another --name", opts.Name, opts.Region),
			removeAWSState(statePath))
	}
	if err != nil {
		return awsDevSetupError(statePath, state, err)
	}
	state.Dev.LaunchTemplateID = templateID
	state.Dev.LaunchTemplateVersion = version
	if err := writeAWSState(statePath, state); err != nil {
		return awsDevSetupError(statePath, state, fmt.Errorf("save AWS state: %w", err))
	}
	if err := manager.CreateAutoScalingGroup(ctx, spec, templateID, version); err != nil {
		if errors.Is(err, awsprovider.ErrAlreadyExists) {
			// Someone else's group: remove only the template created above.
			if delErr := manager.DeleteLaunchTemplate(ctx, templateID); delErr != nil {
				return awsDevSetupError(statePath, state, errors.Join(err, delErr))
			}
			return errors.Join(
				fmt.Errorf("auto scaling group %s already exists in %s; pick another --name", opts.Name, opts.Region),
				removeAWSState(statePath))
		}
		return awsDevSetupError(statePath, state, err)
	}

	if err := awsDevAwaitCapacity(ctx, manager, state); err != nil {
		return awsDevSetupError(statePath, state, err)
	}
	fmt.Fprintf(os.Stderr, "dev group %s is ready\n", opts.Name)
	return printAWSDevStatus(ctx, manager, statePath, state)
}

// resolveAWSDevNetwork fills in the VPC (flag, $ETCD_INFRA_AWS_VPC, or the
// default VPC), its subnets, and its default security group. Nothing is
// created: the group only launches instances into existing networking.
func resolveAWSDevNetwork(ctx context.Context, manager *awsprovider.Manager, opts *awsDevUpOptions) error {
	if opts.VPCID == "" {
		vpc, err := manager.DefaultVPCID(ctx)
		if err != nil {
			return fmt.Errorf("resolve VPC (pass --vpc or set $ETCD_INFRA_AWS_VPC): %w", err)
		}
		opts.VPCID = vpc
	}
	if len(opts.SubnetIDs) == 0 {
		subnets, err := manager.SubnetsInVPC(ctx, opts.VPCID)
		if err != nil {
			return err
		}
		if len(subnets) == 0 {
			return fmt.Errorf("VPC %s has no subnets", opts.VPCID)
		}
		slices.Sort(subnets)
		opts.SubnetIDs = subnets
	}
	if len(opts.SecurityGroupIDs) == 0 {
		group, err := manager.DefaultSecurityGroupID(ctx, opts.VPCID)
		if err != nil {
			return err
		}
		opts.SecurityGroupIDs = []string{group}
	}
	return nil
}

func awsDevLaunchGroupSpec(state awsState, instanceProfile string) awsprovider.LaunchGroupSpec {
	dev := state.Dev
	return awsprovider.LaunchGroupSpec{
		Name:               dev.LaunchTemplate,
		SubnetIDs:          dev.SubnetIDs,
		SecurityGroupIDs:   dev.SecurityGroupIDs,
		ImageID:            dev.AMI,
		InstanceType:       dev.InstanceType,
		IAMInstanceProfile: instanceProfile,
		UserData:           awsDevUserData(state),
		DataVolumeSizeGB:   int32(dev.VolumeSizeGB), //nolint:gosec // bounded by flag validation
		Tags:               awsDevTags(state.Name, dev.OwnerToken),
		Count:              int32(dev.Count), //nolint:gosec // bounded by flag validation
	}
}

// awsDevOwnerTag carries the per-"up" owner token.
const awsDevOwnerTag = "etcd-infra.dev-owner"

// awsDevTags identify everything a dev group creates. The cluster tag is
// what the least-privilege IAM policy gates on; the owner token proves a
// same-named resource was created by this state, and scopes teardown's
// instance sweep to this group alone.
func awsDevTags(name, ownerToken string) map[string]string {
	return map[string]string{"etcd-infra.cluster": name, "etcd-infra.role": awsDevRole, awsDevOwnerTag: ownerToken}
}

// newAWSDevOwnerToken returns 128 random bits, hex-encoded.
func newAWSDevOwnerToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate owner token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// awsDevAwaitCapacity waits for the group to reach its recorded count, then
// for every box to be reachable over SSM and finish first-boot setup.
func awsDevAwaitCapacity(ctx context.Context, manager *awsprovider.Manager, state awsState) error {
	fmt.Fprintf(os.Stderr, "waiting for %d in-service instances in %s\n", state.Dev.Count, state.Dev.AutoScalingGroup)
	ids, err := manager.WaitForGroupSize(ctx, state.Dev.AutoScalingGroup, state.Dev.Count, awsDevCapacityTimeout, awsDevPollInterval)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "waiting for SSM and first-boot setup on %s\n", strings.Join(ids, ", "))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			if err := awsDevAwaitSetup(ctx, manager, id); err != nil {
				errs[i] = fmt.Errorf("%s: %w", id, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func awsDevAwaitSetup(ctx context.Context, manager *awsprovider.Manager, id string) error {
	ready, err := manager.WaitForReady(ctx, id, awsReadyTimeout)
	if err != nil {
		return fmt.Errorf("wait for SSM: %w", err)
	}
	result, err := ready.RunCommandWithOptions(ctx,
		[]string{"bash", "-c", awsDevSetupWaitScript()},
		&compute.RunCommandOptions{Timeout: awsDevSetupTimeout},
	)
	if err != nil {
		return fmt.Errorf("check first-boot setup: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("first-boot setup: %s", strings.TrimSpace(result.Stderr+"\n"+result.Stdout))
	}
	return nil
}

// runAWSDevScale changes the group's size. New boxes come from the same
// launch template version, so they are identical to the existing ones; on
// scale-in, Auto Scaling picks which boxes to terminate (spreading across
// availability zones first), and their data volumes are deleted with them.
func runAWSDevScale(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev scale", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev group name")
	count := flags.Int("count", -1, fmt.Sprintf("new number of boxes (0-%d; required)", maxAWSDevCount))
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *count < 0 || *count > maxAWSDevCount {
		return fmt.Errorf("--count must be between 0 and %d, got %d", maxAWSDevCount, *count)
	}
	state, statePath, err := readAWSDevGroupState(*name)
	if err != nil {
		return err
	}
	manager, err := awsDevManager(ctx, state)
	if err != nil {
		return err
	}
	if err := manager.SetDesiredCapacity(ctx, state.Dev.AutoScalingGroup, *count, *count, *count); err != nil {
		return err
	}
	state.Dev.Count = *count
	if err := writeAWSState(statePath, state); err != nil {
		return fmt.Errorf("save AWS state: %w", err)
	}
	if err := awsDevAwaitCapacity(ctx, manager, state); err != nil {
		return err
	}
	return printAWSDevStatus(ctx, manager, statePath, state)
}

// runAWSDevRun executes one command or local script as root over SSM on
// every in-service box of the group (or the --instance subset), in
// parallel, from $ETCD_INFRA_DEV_DIR with the box environment sourced.
// Output is printed after completion; any non-zero exit fails the command.
func runAWSDevRun(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev run", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev group name")
	instances := flags.String("instance", "", "comma-separated instance IDs to run on (default: every in-service box)")
	scriptPath := flags.String("script", "", "local bash script to run instead of trailing arguments")
	timeout := flags.Duration("timeout", defaultAWSDevRunTimeout, "remote execution timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if (*scriptPath == "") == (len(command) == 0) {
		return errors.New("usage: etcd-infra aws dev run --name NAME [--instance ID,...] [--timeout D] (--script FILE | -- COMMAND [ARGS...])")
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	var userScript string
	if *scriptPath != "" {
		data, err := os.ReadFile(*scriptPath)
		if err != nil {
			return fmt.Errorf("read --script: %w", err)
		}
		userScript = string(data)
	} else {
		userScript = shell.JoinArgs(command)
	}

	state, _, err := readAWSDevGroupState(*name)
	if err != nil {
		return err
	}
	manager, err := awsDevManager(ctx, state)
	if err != nil {
		return err
	}
	members, exists, err := manager.GroupMembers(ctx, state.Dev.AutoScalingGroup)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("auto scaling group %s does not exist; run 'etcd-infra aws dev down --name %s' to clean up", state.Dev.AutoScalingGroup, state.Name)
	}
	targets, err := awsDevRunTargets(members, splitCSV(*instances))
	if err != nil {
		return err
	}

	results := make([]*compute.ExecuteResult, len(targets))
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, id := range targets {
		wg.Go(func() {
			instance, err := manager.Get(ctx, id)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = instance.RunCommandWithOptions(ctx,
				[]string{"bash", "-c", awsDevRunScript(userScript)},
				&compute.RunCommandOptions{
					Timeout: *timeout,
					// Forwarded scripts may legitimately mention or run
					// reboot and friends; never turn them into termination.
					ProviderConfig: awsprovider.CommandConfig{GuestShutdown: true},
				},
			)
		})
	}
	wg.Wait()

	var failed []string
	for i, id := range targets {
		if len(targets) > 1 {
			// One header per box keeps multi-box output attributable.
			code := "error"
			if errs[i] == nil {
				code = fmt.Sprint(results[i].ExitCode)
			}
			fmt.Printf("=== %s exit=%s ===\n", id, code)
			fmt.Fprintf(os.Stderr, "=== %s ===\n", id)
		}
		if errs[i] != nil {
			fmt.Fprintf(os.Stderr, "run on %s: %v\n", id, errs[i])
			failed = append(failed, id)
			continue
		}
		fmt.Fprint(os.Stdout, results[i].Stdout)
		fmt.Fprint(os.Stderr, results[i].Stderr)
		if len(results[i].Stdout) >= ssmOutputLimit || len(results[i].Stderr) >= ssmOutputLimit {
			fmt.Fprintf(os.Stderr, "warning: SSM caps output at %d characters; write large output under $ETCD_INFRA_DEV_DIR and upload it to $ETCD_INFRA_DEV_RESULTS\n", ssmOutputLimit)
		}
		if results[i].ExitCode != 0 {
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("dev group %s: remote command failed on %d of %d boxes: %s", state.Name, len(failed), len(targets), strings.Join(failed, ", "))
	}
	return nil
}

// awsDevRunTargets selects the in-service members to run on: all of them,
// or exactly the requested ones (each must be an in-service member).
func awsDevRunTargets(members []awsprovider.GroupMember, requested []string) ([]string, error) {
	var inService []string
	for _, member := range members {
		if member.InService() {
			inService = append(inService, member.ID)
		}
	}
	if len(requested) == 0 {
		if len(inService) == 0 {
			return nil, errors.New("the group has no in-service boxes; see 'aws dev status' or 'aws dev scale'")
		}
		return inService, nil
	}
	targets := slices.Clone(requested)
	slices.Sort(targets)
	targets = slices.Compact(targets)
	for _, id := range targets {
		if !slices.Contains(inService, id) {
			return nil, fmt.Errorf("instance %s is not an in-service box of this group (in service: %s)", id, strings.Join(inService, ", "))
		}
	}
	return targets, nil
}

func runAWSDevStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev status", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev group name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	state, statePath, err := readAWSDevGroupState(*name)
	if err != nil {
		return err
	}
	manager, err := awsDevManager(ctx, state)
	if err != nil {
		return err
	}
	return printAWSDevStatus(ctx, manager, statePath, state)
}

// runAWSDevDown deletes everything the group created and waits for AWS to
// confirm, then removes the state file. It refuses non-dev state so a
// mistyped name can never tear down an etcd cluster.
func runAWSDevDown(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev down", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev group name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	state, statePath, err := readAWSDevState(*name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("no AWS state for %s; nothing to do\n", *name)
			return nil
		}
		return err
	}
	manager, err := awsDevManager(ctx, state)
	if err != nil {
		return err
	}
	keep := fmt.Sprintf("state kept at %s; rerun to retry", statePath)
	// Every lookup below treats "not found" as "already deleted", which is
	// only true in the account (and region, from state) that created it.
	account, err := manager.AccountID(ctx)
	if err != nil {
		return fmt.Errorf("identify AWS account: %w; %s", err, keep)
	}
	if account != state.Dev.AccountID {
		return fmt.Errorf("dev group %s was created in account %q but the credentials are for %s; use the creating account; %s", state.Name, state.Dev.AccountID, account, keep)
	}

	if awsDevIsLegacy(state) {
		if err := awsDevDownLegacy(ctx, manager, state); err != nil {
			return fmt.Errorf("%w; %s", err, keep)
		}
	} else if err := awsDevDownGroup(ctx, manager, state); err != nil {
		return fmt.Errorf("%w; %s", err, keep)
	}
	if err := removeAWSState(statePath); err != nil {
		return err
	}
	fmt.Printf("deleted dev group %s (Auto Scaling group, launch template, instances, and data volumes); results kept at %s\n", state.Name, state.Dev.ResultsURI)
	return nil
}

// awsDevDownGroup deletes the group's resources, but only those carrying
// this state's owner token: the name alone is not proof of ownership (after
// a failed "up", another user may have created a group with the same name).
func awsDevDownGroup(ctx context.Context, manager *awsprovider.Manager, state awsState) error {
	dev := state.Dev
	owned := func(tags map[string]string) bool { return tags[awsDevOwnerTag] == dev.OwnerToken }

	groupTags, exists, err := manager.AutoScalingGroupTags(ctx, dev.AutoScalingGroup)
	if err != nil {
		return err
	}
	switch {
	case exists && owned(groupTags):
		fmt.Fprintf(os.Stderr, "deleting Auto Scaling group %s and its instances\n", dev.AutoScalingGroup)
		if err := manager.DeleteAutoScalingGroup(ctx, dev.AutoScalingGroup, awsDevTeardownTimeout, awsDevPollInterval); err != nil {
			return err
		}
	case exists:
		fmt.Fprintf(os.Stderr, "Auto Scaling group %s belongs to another dev group (owner tag differs); leaving it alone\n", dev.AutoScalingGroup)
	}
	if err := awsDevSweepInstances(ctx, manager, awsDevTags(state.Name, dev.OwnerToken)); err != nil {
		return err
	}
	template, exists, err := manager.LaunchTemplateByName(ctx, dev.LaunchTemplate)
	if err != nil {
		return err
	}
	switch {
	case exists && owned(template.Tags):
		return manager.DeleteLaunchTemplate(ctx, template.ID)
	case exists:
		fmt.Fprintf(os.Stderr, "launch template %s belongs to another dev group (owner tag differs); leaving it alone\n", dev.LaunchTemplate)
	}
	return nil
}

// awsDevIsLegacy reports state written by the single-instance "aws dev" of
// commit 64f3eb8: one recorded instance, no Auto Scaling group.
func awsDevIsLegacy(state awsState) bool {
	return state.Dev.OwnerToken == "" && len(state.Instances) > 0
}

// awsDevDownLegacy terminates a single-instance dev box (its data volume is
// DeleteOnTermination) and waits for EC2 to confirm. The caller verified the
// account, so a NotFound means the instance was purged long ago.
func awsDevDownLegacy(ctx context.Context, manager *awsprovider.Manager, state awsState) error {
	for _, box := range state.Instances {
		_, err := manager.Delete(ctx, compute.NewDeleteRequest(box.ID))
		if errors.Is(err, awsprovider.ErrInstanceNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("terminate %s: %w", box.ID, err)
		}
		if err := manager.WaitForTerminated(ctx, box.ID, awsDevTeardownTimeout); err != nil {
			return fmt.Errorf("wait for %s to terminate: %w", box.ID, err)
		}
	}
	return nil
}

// awsDevSweepInstances terminates any instance still carrying the group's
// tags, owner token included (for example one launched while the group was
// being deleted, or one detached from it by hand), and waits until every
// such instance is terminated, which is also when EC2 deletes their data
// volumes.
func awsDevSweepInstances(ctx context.Context, manager *awsprovider.Manager, tags map[string]string) error {
	deadline := time.Now().Add(awsDevTeardownTimeout)
	terminated := map[string]bool{}
	for {
		remaining, err := manager.UnterminatedInstancesTagged(ctx, tags)
		if err != nil {
			return err
		}
		if len(remaining) == 0 {
			return nil
		}
		for id, state := range remaining {
			if state == "shutting-down" || terminated[id] {
				continue
			}
			if _, err := manager.Delete(ctx, compute.NewDeleteRequest(id)); err != nil && !errors.Is(err, awsprovider.ErrInstanceNotFound) {
				return fmt.Errorf("terminate leftover instance %s: %w", id, err)
			}
			terminated[id] = true
		}
		if time.Now().After(deadline) {
			ids := slices.Sorted(maps.Keys(remaining))
			return fmt.Errorf("timed out after %s waiting for instances to terminate: %s", awsDevTeardownTimeout, strings.Join(ids, ", "))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(awsDevPollInterval):
		}
	}
}

func awsDevManager(ctx context.Context, state awsState) (*awsprovider.Manager, error) {
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return awsprovider.New(cfg), nil
}

// readAWSDevState loads the named state and requires it to be a dev group.
func readAWSDevState(name string) (awsState, string, error) {
	if err := validateClusterName(name); err != nil {
		return awsState{}, "", err
	}
	statePath, err := awsStatePath(name)
	if err != nil {
		return awsState{}, "", err
	}
	state, err := readAWSState(statePath)
	if err != nil {
		return awsState{}, "", err
	}
	if state.Dev == nil {
		return awsState{}, "", fmt.Errorf("%s is an etcd cluster, not a dev group; use the 'aws' cluster commands", name)
	}
	if state.Dev.AccountID == "" {
		return awsState{}, "", fmt.Errorf("invalid dev group state %s: missing account", statePath)
	}
	if !awsDevIsLegacy(state) && (state.Dev.AutoScalingGroup == "" || state.Dev.LaunchTemplate == "" || state.Dev.OwnerToken == "") {
		return awsState{}, "", fmt.Errorf("invalid dev group state %s: missing Auto Scaling group, launch template, or owner token", statePath)
	}
	return state, statePath, nil
}

// readAWSDevGroupState is readAWSDevState for commands that need a live
// group; state from the single-instance release can only be torn down.
func readAWSDevGroupState(name string) (awsState, string, error) {
	state, statePath, err := readAWSDevState(name)
	if err == nil && awsDevIsLegacy(state) {
		return awsState{}, "", fmt.Errorf("%s is a single-instance dev box from an older etcd-infra; run 'etcd-infra aws dev down --name %s' and recreate it with 'aws dev up'", name, name)
	}
	return state, statePath, err
}

func removeAWSState(statePath string) error {
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove AWS state: %w", err)
	}
	return nil
}

func awsDevSetupError(statePath string, state awsState, err error) error {
	return fmt.Errorf("%w; state saved at %s; clean up with 'etcd-infra aws dev down --name %s'", err, statePath, state.Name)
}

var (
	// awsDevMountPointPattern keeps the mount point safe to embed in shell
	// scripts and fstab.
	awsDevMountPointPattern = regexp.MustCompile(`^(/[A-Za-z0-9_-][A-Za-z0-9._-]*)+$`)
	s3BucketPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// awsDevMountPointAllowed accepts only locations that cannot shadow OS
// state: a blank volume over /var/lib or /usr/local would hide snapd and
// dpkg state and break the box on every boot.
func awsDevMountPointAllowed(mountPoint string) bool {
	if mountPoint == "/data" || mountPoint == "/var/lib/etcd" {
		return true
	}
	for _, root := range []string{"/mnt/", "/data/", "/srv/"} {
		if strings.HasPrefix(mountPoint, root) {
			return true
		}
	}
	return false
}

func validateAWSDevUpOptions(opts awsDevUpOptions) error {
	if err := validateClusterName(opts.Name); err != nil {
		return err
	}
	if len(opts.Name) < 3 {
		// Launch template names need at least 3 characters.
		return fmt.Errorf("--name must be at least 3 characters, got %q", opts.Name)
	}
	if opts.Count < 1 || opts.Count > maxAWSDevCount {
		return fmt.Errorf("--count must be between 1 and %d, got %d", maxAWSDevCount, opts.Count)
	}
	for _, field := range []struct{ flag, value string }{
		{"--instance-profile", opts.IAMInstanceProfile},
		{"--bucket", opts.Bucket},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.flag)
		}
	}
	if opts.Arch != "amd64" && opts.Arch != "arm64" {
		return fmt.Errorf("--arch must be amd64 or arm64, got %q", opts.Arch)
	}
	if opts.VolumeSizeGB < 1 || opts.VolumeSizeGB > 16384 {
		return fmt.Errorf("--volume-size-gb must be between 1 and 16384, got %d", opts.VolumeSizeGB)
	}
	if !awsDevMountPointPattern.MatchString(opts.MountPoint) {
		return fmt.Errorf("--mount-point must be an absolute path of [A-Za-z0-9._-] segments not starting with '.', got %q", opts.MountPoint)
	}
	if !awsDevMountPointAllowed(opts.MountPoint) {
		return fmt.Errorf("--mount-point %s must be /data, /var/lib/etcd, or under /mnt/, /data/, or /srv/", opts.MountPoint)
	}
	if !s3BucketPattern.MatchString(opts.Bucket) {
		return fmt.Errorf("--bucket %q is not a valid S3 bucket name", opts.Bucket)
	}
	return nil
}

// awsDevResultsURI is the group's results prefix; each box writes under
// <prefix><instance-id>/. It sits under etcd-infra/ so the stock
// instance-role policy (S3 put on etcd-infra-e2e-*/etcd-infra/*) covers it.
func awsDevResultsURI(bucket, name string) string {
	return fmt.Sprintf("s3://%s/etcd-infra/dev/%s/", bucket, name)
}

// awsDevUserData configures a box at first boot (cloud-init, as root): make
// sure the SSM agent runs, mount the data volume, install the AWS CLI,
// prove the instance role can write the box's results prefix, and write the
// box environment file last. The exit code lands in awsDevSetupExitFile
// and the trace in awsDevSetupLog, which "up" and "scale" read over SSM.
func awsDevUserData(state awsState) string {
	dev := state.Dev
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/bash
# etcd-infra dev box first-boot setup (dev group %[1]s).
exec >>%[2]s 2>&1
mkdir -p "$(dirname %[3]s)"
rm -f %[3]s
trap 'code=$?; echo "$code" > %[3]s.tmp && mv %[3]s.tmp %[3]s' EXIT
set -euxo pipefail
export PATH="$PATH:/snap/bin"
# Not every Ubuntu AMI ships the SSM agent; without it the box is unreachable.
if ! pgrep -f amazon-ssm-agent >/dev/null 2>&1; then
    snap wait system seed.loaded
    for _ in $(seq 1 30); do
        snap install amazon-ssm-agent --classic && break
        sleep 5
    done
fi
`, shell.Quote(state.Name), awsDevSetupLog, awsDevSetupExitFile)
	b.WriteString(awsDataVolumeSetupScript(dev.MountPoint) + "\n")
	// Ubuntu 24.04 dropped the awscli deb; the snap is the supported route
	// (retried: first-boot snap refreshes cause change conflicts), with
	// AWS's official bundle as the fallback (waiting out first-boot apt).
	b.WriteString(`if ! command -v aws >/dev/null 2>&1; then
    snap wait system seed.loaded || true
    for _ in $(seq 1 12); do
        snap install aws-cli --classic && break
        sleep 5
    done
    if ! command -v aws >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get -o DPkg::Lock::Timeout=300 update -q
        apt-get -o DPkg::Lock::Timeout=300 install -y -q unzip curl
        tmp="$(mktemp -d)"
        curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o "$tmp/awscliv2.zip"
        unzip -q "$tmp/awscliv2.zip" -d "$tmp"
        "$tmp/aws/install" --update
        rm -rf "$tmp"
    fi
fi
aws --version
`)
	fmt.Fprintf(&b, `instance_id="$(cat /var/lib/cloud/data/instance-id)"
[ -n "$instance_id" ]
results=%[1]s"${instance_id}/"
export AWS_REGION=%[2]s AWS_DEFAULT_REGION=%[2]s
echo "ready $(date -u)" | aws s3 cp - "${results}.dev-ready"
findmnt %[3]s
cat > %[4]s.tmp <<EOF
export ETCD_INFRA_DEV_NAME=%[5]s
export ETCD_INFRA_DEV_INSTANCE_ID=${instance_id}
export ETCD_INFRA_DEV_DIR=%[3]s
export ETCD_INFRA_DEV_RESULTS=${results}
export AWS_REGION=%[2]s
export AWS_DEFAULT_REGION=%[2]s
case ":\$PATH:" in *:/snap/bin:*) ;; *) export PATH="\$PATH:/snap/bin" ;; esac
EOF
chmod 0644 %[4]s.tmp
mv %[4]s.tmp %[4]s
`, shell.Quote(dev.ResultsURI), shell.Quote(state.Region), shell.Quote(dev.MountPoint), awsDevEnvFile, shell.Quote(state.Name))
	return b.String()
}

// awsDevSetupWaitScript waits (within the SSM command timeout) for
// first-boot setup to finish and fails with the setup log's tail if it
// failed or is still running.
func awsDevSetupWaitScript() string {
	polls := int((awsDevSetupTimeout - time.Minute) / (5 * time.Second))
	return fmt.Sprintf(`for _ in $(seq 1 %[1]d); do [ -e %[2]s ] && break; sleep 5; done
if [ ! -e %[2]s ]; then echo "setup still running; last lines of %[3]s:" >&2; tail -n 40 %[3]s >&2; exit 1; fi
code="$(cat %[2]s)"
if [ "$code" != 0 ]; then echo "setup failed (exit $code); last lines of %[3]s:" >&2; tail -n 40 %[3]s >&2; exit 1; fi
`, polls, awsDevSetupExitFile, awsDevSetupLog)
}

// awsDevRunScript wraps a user command or script with the box environment.
// The user script runs in the same shell, so "exit N" propagates; a missing
// environment (setup unfinished or failed) fails fast instead of running in
// $HOME.
func awsDevRunScript(userScript string) string {
	return fmt.Sprintf(`if ! . %[1]s 2>/dev/null; then echo "etcd-infra: %[1]s missing: first-boot setup has not finished or failed; see %[2]s" >&2; exit 97; fi
cd "$ETCD_INFRA_DEV_DIR" || exit 97
%[3]s
`, awsDevEnvFile, awsDevSetupLog, userScript)
}

func printAWSDevPlan(opts awsDevUpOptions, amiParameter string) {
	ami := opts.AMI
	if ami == "" {
		ami = "latest Ubuntu " + opts.UbuntuRelease + " (" + amiParameter + ", pinned at creation)"
	}
	vpc := opts.VPCID
	if vpc == "" {
		vpc = "the region's default VPC"
	}
	subnets := strings.Join(opts.SubnetIDs, ",")
	if subnets == "" {
		subnets = "every subnet in the VPC"
	}
	fmt.Printf("AWS dev group dry run: %s\n", opts.Name)
	fmt.Printf("  launch template + Auto Scaling group %s: %d x %s (%s), AMI %s\n", opts.Name, opts.Count, opts.InstanceType, opts.Arch, ami)
	fmt.Printf("  network: existing VPC %s, subnets: %s (nothing is created in the VPC)\n", vpc, subnets)
	fmt.Printf("  data volume per box: %d GiB encrypted gp3 mounted at %s (deleted with the box)\n", opts.VolumeSizeGB, opts.MountPoint)
	fmt.Printf("  results: %s<instance-id>/\n", awsDevResultsURI(opts.Bucket, opts.Name))
	fmt.Println("rerun with --dry-run=false to create the group")
}

// printAWSDevStatus prints stable key=value lines on stdout for scripts and
// agents (one "instance=" line per box), and the suggested next commands on
// stderr.
func printAWSDevStatus(ctx context.Context, manager *awsprovider.Manager, statePath string, state awsState) error {
	dev := state.Dev
	members, exists, err := manager.GroupMembers(ctx, dev.AutoScalingGroup)
	if err != nil {
		return err
	}
	groupState := "active"
	if !exists {
		groupState = "missing"
	}
	fmt.Printf("name=%s\n", state.Name)
	fmt.Printf("region=%s\n", state.Region)
	fmt.Printf("account_id=%s\n", dev.AccountID)
	fmt.Printf("vpc_id=%s\n", dev.VPCID)
	fmt.Printf("subnet_ids=%s\n", strings.Join(dev.SubnetIDs, ","))
	fmt.Printf("security_group_ids=%s\n", strings.Join(dev.SecurityGroupIDs, ","))
	fmt.Printf("auto_scaling_group=%s\n", dev.AutoScalingGroup)
	fmt.Printf("auto_scaling_group_state=%s\n", groupState)
	fmt.Printf("launch_template_id=%s\n", dev.LaunchTemplateID)
	fmt.Printf("launch_template_version=%d\n", dev.LaunchTemplateVersion)
	fmt.Printf("instance_type=%s\n", dev.InstanceType)
	fmt.Printf("ami=%s\n", dev.AMI)
	fmt.Printf("count=%d\n", dev.Count)
	fmt.Printf("mount_point=%s\n", dev.MountPoint)
	fmt.Printf("volume_size_gb=%d\n", dev.VolumeSizeGB)
	fmt.Printf("results_uri=%s\n", dev.ResultsURI)
	fmt.Printf("state_file=%s\n", statePath)
	for _, member := range members {
		ip := ""
		if instance, err := manager.Get(ctx, member.ID); err == nil {
			ip = instance.PrivateIPv4()
		}
		fmt.Printf("instance=%s lifecycle=%s health=%s az=%s private_ipv4=%s results_uri=%s%s/\n",
			member.ID, member.LifecycleState, member.HealthStatus, member.AvailabilityZone, ip, dev.ResultsURI, member.ID)
	}
	fmt.Fprintf(os.Stderr, "next:\n  etcd-infra aws dev run --name %[1]s [--instance ID] -- <command>\n  etcd-infra aws dev etcd --name %[1]s [--instance ID] [--binary ./bin/etcd]\n  aws ssm start-session --region %[2]s --target <instance-id>\n  etcd-infra aws dev scale --name %[1]s --count N\n  etcd-infra aws dev down --name %[1]s\n",
		state.Name, state.Region)
	return nil
}
