package main

// "aws dev" manages ephemeral single-instance Ubuntu boxes for manual and
// agent-driven testing of private branches. A box is an empty instance (no
// etcd) with:
//
//   - a dedicated gp3 EBS data volume, formatted and mounted at --mount-point
//     (default /mnt/data), remounted across reboots via fstab;
//   - the AWS CLI and a verified write path to s3://<bucket>/etcd-infra/dev/<name>/
//     for results (probed during "up");
//   - SSM command access: "aws dev run" executes commands as root over SSM
//     RunCommand, and "aws ssm start-session" opens an interactive shell.
//
// Workflow (every command is keyed by --name; state lives in
// ~/.etcd-infra/aws/<name>.json, the same store "aws up" uses). stdout of
// up/status is only key=value lines; progress and hints go to stderr.
//
//	etcd-infra aws dev up --name dev01 --vpc vpc-... --instance-profile etcd-infra-ssm \
//	    --bucket etcd-infra-e2e-... --dry-run=false
//	etcd-infra aws dev run --name dev01 -- uname -a
//	etcd-infra aws dev run --name dev01 --script ./test.sh
//	etcd-infra aws dev status --name dev01
//	etcd-infra aws dev down --name dev01
//
// Cleanup contract: the data volume is created with DeleteOnTermination, so
// EC2 deletes it with the instance whenever the instance is terminated ("dev
// down", "aws down", or the console). An in-guest poweroff only stops the
// box (EC2's default shutdown behavior): instance and volumes remain, and
// billed, until "dev down". "aws dev down" terminates the recorded
// instance, waits until EC2 reports it terminated, and removes the state
// file. It is idempotent: an instance already purged from the API counts as
// gone only when the caller's AWS account matches the one recorded at "up"
// (wrong-account credentials see the same NotFound). A failed "up" saves
// state right after launch so "down" can always finish the job. Results in
// S3 are kept.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
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
	awsDevSetupTimeout        = 15 * time.Minute
	awsDevTerminateTimeout    = 10 * time.Minute
	awsDevRole                = "dev"
	// awsDevEnvFile is sourced by every "aws dev run" command and by login
	// shells (start-session), so both see the same box environment.
	awsDevEnvFile = "/etc/profile.d/etcd-infra-dev.sh"
	// ssmOutputLimit is the SSM GetCommandInvocation stdout/stderr cap.
	ssmOutputLimit = 24000
)

// awsDevState records what "aws dev up" created beyond the shared instance
// fields; it marks an awsState as a dev box.
type awsDevState struct {
	// AccountID is the AWS account that created the box; teardown trusts an
	// instance NotFound only from the same account.
	AccountID    string `json:"accountID"`
	AMI          string `json:"ami"`
	InstanceType string `json:"instanceType"`
	MountPoint   string `json:"mountPoint"`
	VolumeSizeGB int    `json:"volumeSizeGB"`
	Bucket       string `json:"bucket"`
	// ResultsURI is the S3 prefix the box can write, exported on the box as
	// $ETCD_INFRA_DEV_RESULTS.
	ResultsURI string `json:"resultsURI"`
}

type awsDevUpOptions struct {
	Name               string
	Region             string
	VPCID              string
	SubnetID           string
	SecurityGroupIDs   []string
	AMI                string
	UbuntuRelease      string
	Arch               string
	InstanceType       string
	IAMInstanceProfile string
	VolumeSizeGB       int
	MountPoint         string
	Bucket             string
	DryRun             bool
}

func runAWSDev(ctx context.Context, args []string) error {
	const usage = "usage: etcd-infra aws dev <up|run|status|down>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "up":
		return runAWSDevUp(ctx, args[1:])
	case "run":
		return runAWSDevRun(ctx, args[1:])
	case "status":
		return runAWSDevStatus(ctx, args[1:])
	case "down":
		return runAWSDevDown(ctx, args[1:])
	default:
		return fmt.Errorf("unknown aws dev command %q; %s", args[0], usage)
	}
}

// runAWSDevUp creates the box: launch, record state, wait for SSM, mount the
// data volume, install the AWS CLI, and probe the S3 results prefix. State is
// saved right after launch so any later failure is cleanable by "dev down".
func runAWSDevUp(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev up", flag.ContinueOnError)
	opts := awsDevUpOptions{}
	var securityGroups string
	flags.StringVar(&opts.Name, "name", defaultAWSDevName, "dev box name (state key and etcd-infra.cluster tag)")
	flags.StringVar(&opts.Region, "region", os.Getenv("AWS_REGION"), "AWS region (defaults to AWS configuration)")
	flags.StringVar(&opts.VPCID, "vpc", "", "existing VPC ID (required)")
	flags.StringVar(&opts.SubnetID, "subnet", "", "existing subnet ID (default: first subnet in the VPC); needs outbound internet or NAT")
	flags.StringVar(&securityGroups, "security-groups", "", "comma-separated security group IDs (default: the VPC default group); no inbound rules are needed")
	flags.StringVar(&opts.AMI, "ami", "", "AMI ID override (default: latest Canonical Ubuntu Server for --ubuntu-release/--arch)")
	flags.StringVar(&opts.UbuntuRelease, "ubuntu-release", awsprovider.DefaultUbuntuRelease, "Ubuntu Server release when --ami is unset (22.04, 24.04, 26.04)")
	flags.StringVar(&opts.Arch, "arch", "amd64", "CPU architecture (amd64 or arm64); must match the instance type")
	flags.StringVar(&opts.InstanceType, "instance-type", "", "EC2 instance type (default: t3a.medium for amd64, t4g.medium for arm64)")
	flags.StringVar(&opts.IAMInstanceProfile, "instance-profile", "", "IAM instance profile with SSM core and S3 write on --bucket (required)")
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

	if amiParameter != "" {
		ami, resolveErr := manager.ResolveSSMParameter(ctx, amiParameter)
		if resolveErr != nil {
			return fmt.Errorf("resolve Ubuntu AMI (grant ssm:GetParameters on the public parameter, or pass --ami): %w", resolveErr)
		}
		opts.AMI = ami
	}

	instance, err := manager.Create(ctx, compute.NewCreateRequest(
		compute.WithName(opts.Name),
		compute.WithRegion(opts.Region),
		compute.WithVPCID(opts.VPCID),
		compute.WithSubnetID(opts.SubnetID),
		compute.WithSecurityGroupIDs(opts.SecurityGroupIDs),
		compute.WithImage(opts.AMI),
		compute.WithSize(opts.InstanceType),
		compute.WithUserData(awsDevUserData),
		compute.WithTags(map[string]string{"etcd-infra.cluster": opts.Name, "etcd-infra.role": awsDevRole}),
		compute.WithProviderConfig(awsprovider.CreateConfig{
			IAMInstanceProfile:            opts.IAMInstanceProfile,
			DataVolumeSizeGB:              opts.VolumeSizeGB,
			DataVolumeDeleteOnTermination: true,
		}),
	))
	if err != nil {
		return fmt.Errorf("create dev box %s: %w", opts.Name, err)
	}
	state := awsState{
		Name:      opts.Name,
		Region:    opts.Region,
		Arch:      opts.Arch,
		Instances: []awsInstanceState{{Name: opts.Name, ID: instance.ID()}},
		Dev: &awsDevState{
			AccountID:    accountID,
			AMI:          opts.AMI,
			InstanceType: opts.InstanceType,
			MountPoint:   opts.MountPoint,
			VolumeSizeGB: opts.VolumeSizeGB,
			Bucket:       opts.Bucket,
			ResultsURI:   awsDevResultsURI(opts.Bucket, opts.Name),
		},
	}
	if err := writeAWSState(statePath, state); err != nil {
		if delErr := terminateInstanceRetryingNotFound(ctx, manager, instance.ID()); delErr != nil {
			return fmt.Errorf("save AWS state: %w (compensating delete of unrecorded instance %s also failed: %v — terminate it manually; its data volume is deleted with it)", err, instance.ID(), delErr)
		}
		return fmt.Errorf("save AWS state: %w (instance %s and its data volume were terminated)", err, instance.ID())
	}
	fmt.Fprintf(os.Stderr, "launched %s (%s, %s, %s); waiting for SSM\n", instance.ID(), opts.InstanceType, opts.AMI, opts.Region)

	ready, err := manager.WaitForReady(ctx, instance.ID(), awsReadyTimeout)
	if err != nil {
		return awsDevSetupError(statePath, state, fmt.Errorf("wait for SSM on %s: %w", instance.ID(), err))
	}
	box := &state.Instances[0]
	box.PrivateIPv4 = ready.PrivateIPv4()
	box.PublicIPv4 = ready.PublicIPv4()
	volumeID, err := manager.DataVolumeID(ctx, instance.ID())
	if err != nil {
		return awsDevSetupError(statePath, state, fmt.Errorf("read data volume: %w", err))
	}
	if volumeID == "" {
		return awsDevSetupError(statePath, state, fmt.Errorf("instance %s has no data volume attached", instance.ID()))
	}
	box.DataVolumeID = volumeID
	if err := writeAWSState(statePath, state); err != nil {
		return awsDevSetupError(statePath, state, fmt.Errorf("save resolved AWS state: %w", err))
	}

	fmt.Fprintf(os.Stderr, "configuring %s: mount %s, AWS CLI, S3 probe\n", instance.ID(), opts.MountPoint)
	result, err := ready.RunCommandWithOptions(ctx,
		[]string{"bash", "-ceu", awsDevSetupScript(state)},
		&compute.RunCommandOptions{Timeout: awsDevSetupTimeout},
	)
	if err != nil {
		return awsDevSetupError(statePath, state, fmt.Errorf("configure dev box: %w", err))
	}
	if result.ExitCode != 0 {
		return awsDevSetupError(statePath, state, fmt.Errorf("configure dev box: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr+"\n"+result.Stdout)))
	}

	fmt.Fprintf(os.Stderr, "dev box %s is ready\n", opts.Name)
	printAWSDevSummary(statePath, state)
	return nil
}

// runAWSDevRun executes one command or local script on the box as root over
// SSM, from $ETCD_INFRA_DEV_DIR with the box environment sourced. Output is
// printed after completion; the exit status is propagated as an error.
func runAWSDevRun(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev run", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev box name")
	scriptPath := flags.String("script", "", "local bash script to run instead of trailing arguments")
	timeout := flags.Duration("timeout", defaultAWSDevRunTimeout, "remote execution timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if (*scriptPath == "") == (len(command) == 0) {
		return errors.New("usage: etcd-infra aws dev run --name NAME [--timeout D] (--script FILE | -- COMMAND [ARGS...])")
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

	state, _, err := readAWSDevState(*name)
	if err != nil {
		return err
	}
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	instance, err := awsprovider.New(cfg).Get(ctx, state.Instances[0].ID)
	if err != nil {
		return fmt.Errorf("get dev box %s (%s): %w", state.Name, state.Instances[0].ID, err)
	}
	result, err := instance.RunCommandWithOptions(ctx,
		[]string{"bash", "-c", awsDevRunScript(userScript)},
		&compute.RunCommandOptions{
			Timeout: *timeout,
			// Forwarded scripts may legitimately mention or run reboot and
			// friends; never turn them into instance termination.
			ProviderConfig: awsprovider.CommandConfig{GuestShutdown: true},
		},
	)
	if err != nil {
		return fmt.Errorf("run on dev box %s: %w", state.Name, err)
	}
	fmt.Fprint(os.Stdout, result.Stdout)
	fmt.Fprint(os.Stderr, result.Stderr)
	if len(result.Stdout) >= ssmOutputLimit || len(result.Stderr) >= ssmOutputLimit {
		fmt.Fprintf(os.Stderr, "warning: SSM caps output at %d characters; write large output under $ETCD_INFRA_DEV_DIR and upload it to $ETCD_INFRA_DEV_RESULTS\n", ssmOutputLimit)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("dev box %s: remote command exited %d", state.Name, result.ExitCode)
	}
	return nil
}

func runAWSDevStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev status", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev box name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	state, statePath, err := readAWSDevState(*name)
	if err != nil {
		return err
	}
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	instance, err := awsprovider.New(cfg).Get(ctx, state.Instances[0].ID)
	if err != nil {
		return fmt.Errorf("get dev box %s (%s): %w", state.Name, state.Instances[0].ID, err)
	}
	fmt.Printf("instance_state=%s\n", instance.State())
	printAWSDevSummary(statePath, state)
	return nil
}

// runAWSDevDown terminates the box and waits for EC2 to confirm, which also
// deletes the data volume (DeleteOnTermination). It refuses non-dev state so
// a mistyped name can never tear down an etcd cluster.
func runAWSDevDown(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev down", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev box name")
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
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	manager := awsprovider.New(cfg)
	box := state.Instances[0]
	keep := fmt.Sprintf("state kept at %s; rerun to retry", statePath)
	// Retries NotFound for a minute first: a "down" seconds after an
	// interrupted "up" can see the post-launch eventual-consistency NotFound.
	err = terminateInstanceRetryingNotFound(ctx, manager, box.ID)
	switch {
	case errors.Is(err, awsprovider.ErrInstanceNotFound):
		// Purged long after termination, or invisible to these credentials:
		// only the recorded account can tell the two apart.
		account, accountErr := manager.AccountID(ctx)
		if accountErr != nil {
			return fmt.Errorf("dev box %s (%s) not found and the AWS account is unknown: %w; %s", state.Name, box.ID, accountErr, keep)
		}
		if err := awsDevConfirmPurged(account, state.Dev.AccountID); err != nil {
			return fmt.Errorf("dev box %s (%s): %w; %s", state.Name, box.ID, err, keep)
		}
	case err != nil:
		return fmt.Errorf("terminate dev box %s (%s): %w; %s", state.Name, box.ID, err, keep)
	default:
		if err := manager.WaitForTerminated(ctx, box.ID, awsDevTerminateTimeout); err != nil {
			return fmt.Errorf("wait for dev box %s (%s) to terminate: %w; %s", state.Name, box.ID, err, keep)
		}
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove AWS state: %w", err)
	}
	volume := box.DataVolumeID
	if volume == "" {
		volume = "data volume"
	}
	fmt.Printf("terminated dev box %s (%s); %s deleted with the instance; results kept at %s\n", state.Name, box.ID, volume, state.Dev.ResultsURI)
	return nil
}

// awsDevConfirmPurged decides whether an instance EC2 reports as NotFound is
// really gone. Wrong-account credentials get the same NotFound for a live
// instance, so only the account that created the box may conclude "purged".
func awsDevConfirmPurged(currentAccount, recordedAccount string) error {
	if recordedAccount == "" || currentAccount != recordedAccount {
		return fmt.Errorf("instance not found in account %s, but it was created in account %q; use those credentials", currentAccount, recordedAccount)
	}
	return nil
}

// terminateInstanceRetryingNotFound terminates an instance, retrying
// NotFound for about a minute: right after RunInstances EC2 can briefly
// answer NotFound for a new ID (eventual consistency), so a single NotFound
// is never trusted. A NotFound that persists is returned to the caller.
func terminateInstanceRetryingNotFound(ctx context.Context, manager *awsprovider.Manager, id string) error {
	var err error
	for range 12 {
		if _, err = manager.Delete(ctx, compute.NewDeleteRequest(id)); !errors.Is(err, awsprovider.ErrInstanceNotFound) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	return err
}

// readAWSDevState loads the named state and requires it to be a dev box.
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
		return awsState{}, "", fmt.Errorf("%s is an etcd cluster, not a dev box; use the 'aws' cluster commands", name)
	}
	if len(state.Instances) != 1 {
		return awsState{}, "", fmt.Errorf("invalid dev box state %s: want 1 instance, got %d", statePath, len(state.Instances))
	}
	return state, statePath, nil
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
	for _, field := range []struct{ flag, value string }{
		{"--vpc", opts.VPCID},
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

// awsDevResultsURI is the box's writable results prefix. It sits under
// etcd-infra/ so the stock instance-role policy (S3 put on
// etcd-infra-e2e-*/etcd-infra/*) covers it.
func awsDevResultsURI(bucket, name string) string {
	return fmt.Sprintf("s3://%s/etcd-infra/dev/%s/", bucket, name)
}

// awsDevUserData runs once at first boot, before SSM is reachable: not every
// Ubuntu AMI ships the SSM agent, and without it the box is unreachable.
// Installing an already-present snap is a no-op.
const awsDevUserData = `#!/bin/bash
if ! pgrep -f amazon-ssm-agent >/dev/null 2>&1; then
    snap wait system seed.loaded
    for _ in $(seq 1 30); do
        snap install amazon-ssm-agent --classic && break
        sleep 5
    done
fi
`

// awsDevSetupScript configures a fresh box over SSM (as root): mount the
// data volume, write the box environment file, install the AWS CLI, and
// prove the instance role can write the results prefix.
func awsDevSetupScript(state awsState) string {
	dev := state.Dev
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString(awsDataVolumeSetupScript(dev.MountPoint) + "\n")
	fmt.Fprintf(&b, `cat > %s <<'EOF'
export ETCD_INFRA_DEV_NAME=%s
export ETCD_INFRA_DEV_DIR=%s
export ETCD_INFRA_DEV_RESULTS=%s
export AWS_DEFAULT_REGION=%s
export AWS_REGION=%s
case ":$PATH:" in *:/snap/bin:*) ;; *) export PATH="$PATH:/snap/bin" ;; esac
EOF
chmod 0644 %s
. %s
`,
		awsDevEnvFile,
		shell.Quote(state.Name), shell.Quote(dev.MountPoint), shell.Quote(dev.ResultsURI),
		shell.Quote(state.Region), shell.Quote(state.Region),
		awsDevEnvFile, awsDevEnvFile)
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
	b.WriteString(`echo "ready $(date -u)" | aws s3 cp - "${ETCD_INFRA_DEV_RESULTS}.dev-ready"
findmnt "$ETCD_INFRA_DEV_DIR"
`)
	return b.String()
}

// awsDevRunScript wraps a user command or script with the box environment.
// The user script runs in the same shell, so "exit N" propagates; a missing
// environment (setup never finished) fails fast instead of running in $HOME.
func awsDevRunScript(userScript string) string {
	return fmt.Sprintf(`if ! . %[1]s; then echo "etcd-infra: %[1]s missing; 'aws dev up' did not finish (recreate with 'aws dev down' and 'aws dev up')" >&2; exit 97; fi
cd "$ETCD_INFRA_DEV_DIR" || exit 97
%[2]s
`, awsDevEnvFile, userScript)
}

func printAWSDevPlan(opts awsDevUpOptions, amiParameter string) {
	ami := opts.AMI
	if ami == "" {
		ami = "latest Ubuntu " + opts.UbuntuRelease + " (" + amiParameter + ")"
	}
	fmt.Printf("AWS dev box dry run: %s\n", opts.Name)
	fmt.Printf("  instance: 1 x %s (%s), AMI %s, VPC %s\n", opts.InstanceType, opts.Arch, ami, opts.VPCID)
	fmt.Printf("  data volume: %d GiB gp3 mounted at %s (deleted with the instance)\n", opts.VolumeSizeGB, opts.MountPoint)
	fmt.Printf("  results: %s\n", awsDevResultsURI(opts.Bucket, opts.Name))
	fmt.Println("rerun with --dry-run=false to create the box")
}

// printAWSDevSummary prints stable key=value lines on stdout for scripts and
// agents, and the suggested next commands on stderr.
func printAWSDevSummary(statePath string, state awsState) {
	box := state.Instances[0]
	fmt.Printf("name=%s\n", state.Name)
	fmt.Printf("region=%s\n", state.Region)
	fmt.Printf("account_id=%s\n", state.Dev.AccountID)
	fmt.Printf("instance_id=%s\n", box.ID)
	fmt.Printf("instance_type=%s\n", state.Dev.InstanceType)
	fmt.Printf("ami=%s\n", state.Dev.AMI)
	fmt.Printf("private_ipv4=%s\n", box.PrivateIPv4)
	fmt.Printf("data_volume_id=%s\n", box.DataVolumeID)
	fmt.Printf("mount_point=%s\n", state.Dev.MountPoint)
	fmt.Printf("results_uri=%s\n", state.Dev.ResultsURI)
	fmt.Printf("state_file=%s\n", statePath)
	fmt.Fprintf(os.Stderr, "next:\n  etcd-infra aws dev run --name %s -- <command>\n  aws ssm start-session --region %s --target %s\n  etcd-infra aws dev down --name %s\n",
		state.Name, state.Region, box.ID, state.Name)
}
