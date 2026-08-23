package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
	"git.tbd/etcd-infra/pkg/providers/compute"
	"git.tbd/etcd-infra/pkg/shell"
)

const (
	defaultAWSInstanceType = "t3a.medium"
	awsReadyTimeout        = 10 * time.Minute
)

type awsUpOptions struct {
	Name               string
	Region             string
	VPCID              string
	SubnetID           string
	SecurityGroupIDs   []string
	AMI                string
	InstanceType       string
	IAMInstanceProfile string
	Arch               string
	Version            string
	BinaryURL          string
	BinarySHA256       string
	ExtraArgs          string
	Env                string
	Members            int
	DryRun             bool
	StressClients      int
	BastionType        string
	Replaceable        bool
}

type awsState struct {
	Name      string             `json:"name"`
	Region    string             `json:"region"`
	Version   string             `json:"version"`
	Instances []awsInstanceState `json:"instances"`
	// StressClients are the in-VPC driver instances: suites execute there
	// (binary via S3, results back via S3), so etcd never needs a public
	// ingress rule and no port-forwarding tunnels exist. They share the
	// members' security groups, so client traffic is covered by the
	// member-to-member rules. More than one spreads across the VPC's
	// subnets (availability zones).
	StressClients []awsInstanceState `json:"stressClients,omitempty"`
	// The fields below let "aws replace" reproduce a member exactly.
	Arch         string   `json:"arch,omitempty"`
	ExtraArgs    []string `json:"extraArgs,omitempty"`
	Env          []string `json:"env,omitempty"`
	BinaryURL    string   `json:"binaryURL,omitempty"`
	BinarySHA256 string   `json:"binarySHA256,omitempty"`
	Replaceable  bool     `json:"replaceable,omitempty"`
}

type awsInstanceState struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	PrivateIPv4 string `json:"privateIPv4"`
	PublicIPv4  string `json:"publicIPv4,omitempty"`
	SubnetID    string `json:"subnetID,omitempty"`
	// DataVolumeID is the member's dedicated data volume, recorded at
	// creation: teardown deletes exactly these IDs, never a tag sweep.
	DataVolumeID string `json:"dataVolumeID,omitempty"`
}

func runAWS(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: etcd-infra aws <up|down|status|drive|replace>")
	}

	switch args[0] {
	case "up":
		return runAWSUp(ctx, args[1:])
	case "down":
		return runAWSDown(ctx, args[1:])
	case "status":
		return runAWSStatus(ctx, args[1:])
	case "drive":
		return runAWSDrive(ctx, args[1:])
	case "replace":
		return runAWSReplace(ctx, args[1:])
	default:
		return fmt.Errorf("unknown aws command %q", args[0])
	}
}

func runAWSUp(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws up", flag.ContinueOnError)
	opts := awsUpOptions{}
	var securityGroups string
	flags.StringVar(&opts.Name, "name", defaultClusterName, "cluster name")
	flags.StringVar(&opts.Region, "region", os.Getenv("AWS_REGION"), "AWS region (defaults to AWS configuration)")
	flags.StringVar(&opts.VPCID, "vpc", "", "existing VPC ID")
	flags.StringVar(&opts.SubnetID, "subnet", "", "existing subnet ID (optional)")
	flags.StringVar(&securityGroups, "security-groups", "", "comma-separated existing security group IDs")
	flags.StringVar(&opts.AMI, "ami", "", "Linux AMI ID with systemd, curl, tar, sha256sum, and SSM agent")
	flags.StringVar(&opts.InstanceType, "instance-type", defaultAWSInstanceType, "EC2 instance type")
	flags.StringVar(&opts.IAMInstanceProfile, "instance-profile", "", "IAM instance profile name or ARN with SSM permissions")
	flags.StringVar(&opts.Arch, "arch", "amd64", "etcd release architecture (amd64 or arm64)")
	flags.StringVar(&opts.Version, "version", "latest", "etcd release version")
	flags.StringVar(&opts.BinaryURL, "binary-url", "", "download a custom etcd binary from this URL instead of the release tarball (requires --binary-sha256)")
	flags.StringVar(&opts.BinarySHA256, "binary-sha256", "", "SHA-256 checksum of the --binary-url download")
	flags.StringVar(&opts.ExtraArgs, "extra-args", "", "space-separated extra arguments appended to the etcd server command")
	flags.StringVar(&opts.Env, "env", "", "comma-separated KEY=VALUE environment variables for the etcd systemd unit")
	flags.IntVar(&opts.Members, "members", 3, "cluster member count (1 or 3)")
	flags.IntVar(&opts.StressClients, "stress-clients", 1, "in-VPC stress client (driver) instances; suites run there — no tunnels, no public etcd ingress; >1 spreads across the VPC's subnets")
	flags.BoolVar(&opts.Replaceable, "replaceable", false, "give each member a dedicated data volume that survives termination, enabling 'aws replace'")
	flags.StringVar(&opts.BastionType, "bastion-instance-type", "", "stress client EC2 instance type (default derived from --arch: t3a.medium or t4g.medium)")
	flags.BoolVar(&opts.DryRun, "dry-run", true, "show the plan without creating EC2 instances")
	if err := flags.Parse(args); err != nil {
		return err
	}
	opts.SecurityGroupIDs = splitCSV(securityGroups)
	if err := validateAWSUpOptions(opts); err != nil {
		return err
	}

	if opts.BinaryURL == "" {
		resolvedVersion, err := resolveVersion(ctx, opts.Version)
		if err != nil {
			return err
		}
		opts.Version = resolvedVersion
	}
	if opts.DryRun {
		printAWSPlan(opts)
		return nil
	}

	statePath, err := awsStatePath(opts.Name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(statePath); err == nil {
		return fmt.Errorf("AWS cluster state already exists at %s", statePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check AWS cluster state: %w", err)
	}

	cfg, err := awsprovider.LoadDefaultConfig(ctx, opts.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		return errors.New("AWS region is required via --region or AWS configuration")
	}
	opts.Region = cfg.Region
	if opts.StressClients > 0 && opts.BastionType == "" {
		opts.BastionType = defaultBastionInstanceType(opts.Arch)
	}
	manager := awsprovider.New(cfg)
	state := awsState{
		Name:         opts.Name,
		Region:       opts.Region,
		Version:      opts.Version,
		Arch:         opts.Arch,
		ExtraArgs:    strings.Fields(opts.ExtraArgs),
		Env:          splitCSV(opts.Env),
		BinaryURL:    opts.BinaryURL,
		BinarySHA256: opts.BinarySHA256,
		Replaceable:  opts.Replaceable,
	}

	for i := range opts.Members {
		memberName := fmt.Sprintf("%s-%d", opts.Name, i+1)
		instance, createErr := manager.Create(ctx, compute.NewCreateRequest(
			compute.WithName(memberName),
			compute.WithRegion(opts.Region),
			compute.WithVPCID(opts.VPCID),
			compute.WithSubnetID(opts.SubnetID),
			compute.WithSecurityGroupIDs(opts.SecurityGroupIDs),
			compute.WithImage(opts.AMI),
			compute.WithSize(opts.InstanceType),
			compute.WithTags(map[string]string{"etcd-infra.cluster": opts.Name}),
			compute.WithProviderConfig(awsprovider.CreateConfig{
				IAMInstanceProfile: opts.IAMInstanceProfile,
				DataVolumeSizeGB:   dataVolumeSizeGB(opts.Replaceable),
			}),
		))
		if createErr != nil {
			return awsSetupError(statePath, state, fmt.Errorf("create %s: %w", memberName, createErr))
		}
		state.Instances = append(state.Instances, awsInstanceState{
			Name:        memberName,
			ID:          instance.ID(),
			PrivateIPv4: instance.PrivateIPv4(),
			PublicIPv4:  instance.PublicIPv4(),
		})
		if err := writeAWSState(statePath, state); err != nil {
			if _, delErr := manager.Delete(ctx, compute.NewDeleteRequest(instance.ID())); delErr != nil {
				return fmt.Errorf("save AWS cluster state: %w (compensating delete of unrecorded instance %s also failed: %v — terminate it manually)", err, instance.ID(), delErr)
			}
			return fmt.Errorf("save AWS cluster state: %w", err)
		}
	}

	for i := range state.Instances {
		instance, waitErr := manager.WaitForReady(ctx, state.Instances[i].ID, awsReadyTimeout)
		if waitErr != nil {
			return awsSetupError(statePath, state, fmt.Errorf("wait for %s: %w", state.Instances[i].Name, waitErr))
		}
		state.Instances[i].PrivateIPv4 = instance.PrivateIPv4()
		state.Instances[i].PublicIPv4 = instance.PublicIPv4()
		if state.Instances[i].PrivateIPv4 == "" {
			return awsSetupError(statePath, state, fmt.Errorf("%s has no private IPv4 address", state.Instances[i].Name))
		}
		// Record the data volume ID so teardown deletes exactly the volumes
		// this tool created — by ID, never by tag sweep.
		if opts.Replaceable {
			volumeID, volErr := manager.DataVolumeID(ctx, state.Instances[i].ID)
			if volErr != nil {
				return awsSetupError(statePath, state, fmt.Errorf("read %s data volume: %w", state.Instances[i].Name, volErr))
			}
			state.Instances[i].DataVolumeID = volumeID
		}
	}
	if err := writeAWSState(statePath, state); err != nil {
		return awsSetupError(statePath, state, fmt.Errorf("save resolved AWS cluster state: %w", err))
	}

	if opts.StressClients > 0 {
		// Stress clients execute the suites in-VPC: no etcd bootstrap, only
		// the SSM agent (required of the AMI already) and the AWS CLI. They
		// share the members' security groups, so client traffic is covered by
		// the member-to-member rules. More than one spreads across the VPC's
		// subnets so the load is balanced across availability zones.
		infos, subnetsErr := manager.SubnetInfosInVPC(ctx, opts.VPCID)
		if subnetsErr != nil {
			return awsSetupError(statePath, state, fmt.Errorf("list subnets for stress client placement: %w", subnetsErr))
		}
		if opts.SubnetID == "" {
			// The suites are multi-AZ tests: refuse regions whose VPC spans
			// fewer than three availability zones.
			azs := map[string]bool{}
			for _, info := range infos {
				azs[info.AZ] = true
			}
			if len(azs) < 3 {
				return fmt.Errorf("VPC %s spans only %d availability zones; the AWS suites require at least 3 (pick a region with 3+ AZs)", opts.VPCID, len(azs))
			}
		}
		subnets := make([]string, 0, len(infos))
		for _, info := range infos {
			subnets = append(subnets, info.ID)
		}
		if opts.SubnetID != "" {
			subnets = []string{opts.SubnetID}
		}
		if len(subnets) == 0 {
			return awsSetupError(statePath, state, fmt.Errorf("VPC %s has no subnets for stress clients", opts.VPCID))
		}
		for i := range opts.StressClients {
			clientName := fmt.Sprintf("%s-stress-client-%d", opts.Name, i+1)
			subnet := subnets[i%len(subnets)]
			instance, createErr := manager.Create(ctx, compute.NewCreateRequest(
				compute.WithName(clientName),
				compute.WithRegion(opts.Region),
				compute.WithVPCID(opts.VPCID),
				compute.WithSubnetID(subnet),
				compute.WithSecurityGroupIDs(opts.SecurityGroupIDs),
				compute.WithImage(opts.AMI),
				compute.WithSize(opts.BastionType),
				compute.WithTags(map[string]string{"etcd-infra.cluster": opts.Name, "etcd-infra.role": "stress-client"}),
				compute.WithProviderConfig(awsprovider.CreateConfig{
					IAMInstanceProfile: opts.IAMInstanceProfile,
				}),
			))
			if createErr != nil {
				return awsSetupError(statePath, state, fmt.Errorf("create %s: %w", clientName, createErr))
			}
			state.StressClients = append(state.StressClients, awsInstanceState{Name: clientName, ID: instance.ID(), SubnetID: subnet})
			if err := writeAWSState(statePath, state); err != nil {
				if _, delErr := manager.Delete(ctx, compute.NewDeleteRequest(instance.ID())); delErr != nil {
					return fmt.Errorf("save AWS cluster state: %w (compensating delete of unrecorded stress client %s also failed: %v — terminate it manually)", err, instance.ID(), delErr)
				}
				return fmt.Errorf("save AWS cluster state: %w", err)
			}
			ready, waitErr := manager.WaitForReady(ctx, instance.ID(), awsReadyTimeout)
			if waitErr != nil {
				return awsSetupError(statePath, state, fmt.Errorf("wait for %s: %w", clientName, waitErr))
			}
			state.StressClients[i].PrivateIPv4 = ready.PrivateIPv4()
			state.StressClients[i].PublicIPv4 = ready.PublicIPv4()
			if state.StressClients[i].PrivateIPv4 == "" {
				return awsSetupError(statePath, state, fmt.Errorf("%s has no private IPv4 address", clientName))
			}
			if err := writeAWSState(statePath, state); err != nil {
				return awsSetupError(statePath, state, fmt.Errorf("save stress client state: %w", err))
			}
		}
	}

	members := awsMembers(state)
	bootstrap := awsBootstrapOptions{
		Version:         opts.Version,
		Arch:            opts.Arch,
		BinaryURL:       opts.BinaryURL,
		BinarySHA256:    opts.BinarySHA256,
		ExtraArgs:       strings.Fields(opts.ExtraArgs),
		Env:             splitCSV(opts.Env),
		DataVolumeSetup: opts.Replaceable,
	}
	for i, member := range members {
		instance, getErr := manager.Get(ctx, state.Instances[i].ID)
		if getErr != nil {
			return awsSetupError(statePath, state, fmt.Errorf("get %s: %w", member.Name, getErr))
		}
		result, commandErr := instance.RunCommandWithOptions(
			ctx,
			[]string{"bash", "-ceu", awsBootstrapScript(member, members, opts.Name, bootstrap)},
			&compute.RunCommandOptions{Timeout: 10 * time.Minute},
		)
		if commandErr != nil {
			return awsSetupError(statePath, state, fmt.Errorf("configure %s: %w", member.Name, commandErr))
		}
		if result.ExitCode != 0 {
			return awsSetupError(
				statePath,
				state,
				fmt.Errorf("configure %s: exit %d: %s", member.Name, result.ExitCode, strings.TrimSpace(result.Stderr)),
			)
		}
	}

	first, err := manager.Get(ctx, state.Instances[0].ID)
	if err != nil {
		return awsSetupError(statePath, state, fmt.Errorf("get health-check instance: %w", err))
	}
	healthScript := awsHealthScript(memberClientURLs(members))
	if opts.BinaryURL != "" {
		// A custom binary does not ship etcdctl; probe the HTTP health endpoint.
		healthScript = awsHealthCurlScript(memberClientURLs(members))
	}
	result, err := first.RunCommandWithOptions(
		ctx,
		[]string{"bash", "-ceu", healthScript},
		&compute.RunCommandOptions{Timeout: 2 * time.Minute},
	)
	if err != nil {
		return awsSetupError(statePath, state, fmt.Errorf("check AWS etcd health: %w", err))
	}
	if result.ExitCode != 0 {
		return awsSetupError(statePath, state, fmt.Errorf("check AWS etcd health: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr)))
	}

	fmt.Printf("AWS etcd %s cluster is healthy; state: %s\n", awsVersionLabel(opts), statePath)
	printAWSEndpoints(state)
	return nil
}

func runAWSDown(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws down", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	statePath, err := awsStatePath(*name)
	if err != nil {
		return err
	}
	state, err := readAWSState(statePath)
	if err != nil {
		// Teardown is idempotent: nothing on disk means nothing to remove.
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("no AWS cluster state for %s; nothing to do\n", *name)
			return nil
		}
		return err
	}
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	manager := awsprovider.New(cfg)

	remaining := make([]awsInstanceState, 0, len(state.Instances))
	var deleteErrors []error
	remainingClients := state.StressClients[:0]
	for _, client := range state.StressClients {
		if _, err := manager.Delete(ctx, compute.NewDeleteRequest(client.ID)); err != nil {
			remainingClients = append(remainingClients, client)
			deleteErrors = append(deleteErrors, fmt.Errorf("delete %s (%s): %w", client.Name, client.ID, err))
		}
	}
	state.StressClients = remainingClients
	for _, instance := range state.Instances {
		if _, err := manager.Delete(ctx, compute.NewDeleteRequest(instance.ID)); err != nil {
			remaining = append(remaining, instance)
			deleteErrors = append(deleteErrors, fmt.Errorf("delete %s (%s): %w", instance.Name, instance.ID, err))
		}
	}
	if len(remaining) > 0 || len(state.StressClients) > 0 {
		state.Instances = remaining
		if err := writeAWSState(statePath, state); err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("save remaining AWS state: %w", err))
		}
		return errors.Join(deleteErrors...)
	}
	if state.Replaceable {
		// Replaceable clusters leave their data volumes behind on purpose
		// (DeleteOnTermination=false). Volumes detach only once the
		// instances finish terminating, so wait for that first — otherwise
		// the delete races the detach and leaks the volume. Only the volume
		// IDs recorded at creation are deleted.
		for _, instance := range state.Instances {
			if err := manager.WaitForTerminated(ctx, instance.ID, 5*time.Minute); err != nil {
				return fmt.Errorf("wait for %s to terminate before volume cleanup: %w", instance.Name, err)
			}
		}
		for _, instance := range state.Instances {
			if instance.DataVolumeID == "" {
				continue
			}
			if err := manager.DeleteDataVolume(ctx, instance.DataVolumeID); err != nil {
				return fmt.Errorf("delete data volume %s: %w", instance.DataVolumeID, err)
			}
		}
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove AWS state: %w", err)
	}
	fmt.Printf("terminated AWS etcd cluster %s\n", *name)
	return nil
}

func runAWSStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws status", flag.ContinueOnError)
	name := flags.String("name", defaultClusterName, "cluster name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateClusterName(*name); err != nil {
		return err
	}
	statePath, err := awsStatePath(*name)
	if err != nil {
		return err
	}
	state, err := readAWSState(statePath)
	if err != nil {
		return err
	}
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	manager := awsprovider.New(cfg)
	for _, saved := range state.Instances {
		instance, err := manager.Get(ctx, saved.ID)
		if err != nil {
			return fmt.Errorf("get %s (%s): %w", saved.Name, saved.ID, err)
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", saved.Name, saved.ID, instance.State(), instance.PrivateIPv4())
	}
	for _, client := range state.StressClients {
		instance, err := manager.Get(ctx, client.ID)
		if err != nil {
			return fmt.Errorf("get %s (%s): %w", client.Name, client.ID, err)
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", client.Name, client.ID, instance.State(), instance.PrivateIPv4())
	}
	return nil
}

func validateAWSUpOptions(opts awsUpOptions) error {
	if err := validateClusterName(opts.Name); err != nil {
		return err
	}
	if err := validateMemberCount(opts.Members); err != nil {
		return err
	}
	required := []struct {
		name  string
		value string
	}{
		{name: "vpc", value: opts.VPCID},
		{name: "ami", value: opts.AMI},
		{name: "instance type", value: opts.InstanceType},
		{name: "instance profile", value: opts.IAMInstanceProfile},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if opts.Arch != "amd64" && opts.Arch != "arm64" {
		return fmt.Errorf("arch must be amd64 or arm64, got %q", opts.Arch)
	}
	if opts.BastionType != "" && opts.StressClients == 0 {
		return errors.New("--bastion-instance-type requires --stress-clients > 0")
	}
	if opts.StressClients < 0 {
		return errors.New("--stress-clients must be >= 0")
	}
	if opts.Replaceable && opts.BinaryURL != "" {
		return errors.New("--replaceable requires a release version; custom-binary clusters cannot be replaced (the presigned URL expires)")
	}
	if (opts.BinaryURL == "") != (opts.BinarySHA256 == "") {
		return errors.New("--binary-url and --binary-sha256 must be set together")
	}
	if opts.BinaryURL != "" && !strings.HasPrefix(opts.BinaryURL, "https://") {
		return fmt.Errorf("--binary-url must be an https URL, got %q", opts.BinaryURL)
	}
	for _, entry := range splitCSV(opts.Env) {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			return fmt.Errorf("invalid --env entry %q, expected KEY=VALUE", entry)
		}
	}
	return nil
}

// defaultBastionInstanceType sizes the stress clients for their actual
// load: they execute the test suites in-VPC (the k8x pattern), so the medium
// tier matches the members. The AMI architecture must match --arch.
// awsDataVolumeSizeGB is the per-member data volume size for replaceable
// clusters; the durability tests write well under 1 GiB.
const awsDataVolumeSizeGB = 8

// dataVolumeSizeGB returns the data volume size when the cluster is
// replaceable, or 0 for no data volume.
func dataVolumeSizeGB(replaceable bool) int {
	if replaceable {
		return awsDataVolumeSizeGB
	}
	return 0
}

func defaultBastionInstanceType(arch string) string {
	if arch == "arm64" {
		return "t4g.medium"
	}
	return "t3a.medium"
}

func awsVersionLabel(opts awsUpOptions) string {
	if opts.BinaryURL != "" {
		return "custom-binary"
	}
	return releaseTag(opts.Version)
}

func printAWSPlan(opts awsUpOptions) {
	fmt.Printf("AWS dry run: %d etcd %s instances in VPC %s", opts.Members, opts.InstanceType, opts.VPCID)
	if opts.SubnetID != "" {
		fmt.Printf(", subnet %s", opts.SubnetID)
	}
	fmt.Printf(", etcd %s/%s\n", awsVersionLabel(opts), opts.Arch)
	if opts.StressClients > 0 {
		bastionType := opts.BastionType
		if bastionType == "" {
			bastionType = defaultBastionInstanceType(opts.Arch)
		}
		fmt.Printf("plus %d %s stress client(s) running suites in-VPC (no inbound rules needed)\n", opts.StressClients, bastionType)
	}
	if opts.Replaceable {
		fmt.Printf("plus 1 dedicated %d GiB data volume per member (survives termination for 'aws replace')\n", awsDataVolumeSizeGB)
	}
	fmt.Println("requires security-group ingress between members on TCP 2379 and 2380")
	fmt.Println("rerun with --dry-run=false to create the cluster")
}

func awsMembers(state awsState) []clusterMember {
	members := make([]clusterMember, 0, len(state.Instances))
	for _, instance := range state.Instances {
		members = append(members, clusterMember{
			Name:      instance.Name,
			ClientURL: "http://" + instance.PrivateIPv4 + ":2379",
			PeerURL:   "http://" + instance.PrivateIPv4 + ":2380",
		})
	}
	return members
}

type awsBootstrapOptions struct {
	Version         string   // etcd release version (ignored when BinaryURL is set)
	Arch            string   // etcd release architecture
	BinaryURL       string   // custom etcd binary URL (replaces the release download)
	BinarySHA256    string   // SHA-256 of the custom binary
	ExtraArgs       []string // extra etcd server arguments
	Env             []string // KEY=VALUE environment for the systemd unit
	DataVolumeSetup bool     // find, format-if-empty, and mount the dedicated data volume at /var/lib/etcd
}

func awsBootstrapScript(member clusterMember, members []clusterMember, token string, opts awsBootstrapOptions) string {
	execStart := shell.JoinArgs(append(
		append([]string{"/usr/local/bin/etcd"}, etcdServerArgs(member, members, token, "/var/lib/etcd")...),
		opts.ExtraArgs...,
	))

	var install string
	if opts.BinaryURL != "" {
		// Custom binary (for example a gofail-enabled fork build): download and
		// verify it like the release artifacts. Only the etcd binary is
		// installed; etcdctl/etcdutl are not part of a custom build.
		install = fmt.Sprintf(`binary=%s
binary_sha256=%s
curl -fsSL "$binary" -o "$tmp/etcd"
echo "$binary_sha256  $tmp/etcd" > "$tmp/checksum"
(cd "$tmp" && sha256sum -c checksum)
install -m 0755 "$tmp/etcd" /usr/local/bin/etcd`,
			shell.Quote(opts.BinaryURL), shell.Quote(opts.BinarySHA256))
	} else {
		tag := releaseTag(opts.Version)
		archive := fmt.Sprintf("etcd-%s-linux-%s.tar.gz", tag, opts.Arch)
		releaseURL := "https://github.com/etcd-io/etcd/releases/download/" + tag
		install = fmt.Sprintf(`archive=%s
release_url=%s
curl -fsSL "$release_url/$archive" -o "$tmp/$archive"
curl -fsSL "$release_url/SHA256SUMS" -o "$tmp/SHA256SUMS"
grep -E "[[:space:]]+$archive$" "$tmp/SHA256SUMS" > "$tmp/checksum"
(cd "$tmp" && sha256sum -c checksum)
tar -xzf "$tmp/$archive" -C "$tmp"
install -m 0755 "$tmp/${archive%%.tar.gz}/etcd" /usr/local/bin/etcd
install -m 0755 "$tmp/${archive%%.tar.gz}/etcdctl" /usr/local/bin/etcdctl
install -m 0755 "$tmp/${archive%%.tar.gz}/etcdutl" /usr/local/bin/etcdutl`,
			shell.Quote(archive), shell.Quote(releaseURL))
	}

	var volumeSetup string
	if opts.DataVolumeSetup {
		volumeSetup = awsDataVolumeSetupScript + "\n"
	}

	return fmt.Sprintf(`set -euo pipefail
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
%s%s
install -d -m 0700 /var/lib/etcd
cat > /etc/systemd/system/etcd-infra.service <<'EOF'
[Unit]
Description=etcd test cluster member
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
%sExecStart=%s
Restart=on-failure
RestartSec=5s
LimitNOFILE=40000

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now etcd-infra.service
`, volumeSetup, install, awsSystemdEnvironment(opts.Env), execStart)
}

// awsDataVolumeSetupScript mounts the dedicated EBS data volume at
// /var/lib/etcd. The volume is the non-root NVMe disk. It is formatted on
// first use only; a replacement instance finds the existing filesystem and
// the member's data. The fstab entry remounts it across reboots (the
// hard-crash tests reboot members in-guest).
const awsDataVolumeSetupScript = `root_disk="$(findmnt -n -o SOURCE / | sed 's/p[0-9]*$//')"
data_dev=""
for _ in $(seq 1 60); do
    for d in /dev/nvme[0-9]n1; do
        [ -b "$d" ] || continue
        [ "$d" = "$root_disk" ] && continue
        data_dev="$d"
        break
    done
    [ -n "$data_dev" ] && break
    sleep 1
done
[ -n "$data_dev" ] || { echo "data volume device not found" >&2; exit 1; }
if ! blkid "$data_dev" >/dev/null 2>&1; then
    mkfs.ext4 -q -L etcd-data "$data_dev"
fi
uuid="$(blkid -s UUID -o value "$data_dev")"
install -d -m 0700 /var/lib/etcd
grep -q "UUID=$uuid" /etc/fstab || echo "UUID=$uuid /var/lib/etcd ext4 defaults,nofail 0 2" >> /etc/fstab
mountpoint -q /var/lib/etcd || mount /var/lib/etcd`

// awsSystemdEnvironment renders systemd Environment= lines, one per KEY=VALUE
// entry. Values are double-quoted because failpoint terms contain spaces and
// quotes (for example GOFAIL_FAILPOINTS=fp=return("msg")).
func awsSystemdEnvironment(env []string) string {
	var b strings.Builder
	for _, entry := range env {
		escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(entry)
		fmt.Fprintf(&b, "Environment=\"%s\"\n", escaped)
	}
	return b.String()
}

func awsHealthScript(endpoints []string) string {
	return fmt.Sprintf(
		`for attempt in $(seq 1 60); do ETCDCTL_API=3 /usr/local/bin/etcdctl --endpoints=%s endpoint health --cluster && exit 0; sleep 2; done; exit 1`,
		shell.Quote(strings.Join(endpoints, ",")),
	)
}

// awsHealthCurlScript probes the HTTP health endpoint of every member. It is
// used with custom binaries, which do not install etcdctl.
func awsHealthCurlScript(endpoints []string) string {
	var b strings.Builder
	b.WriteString("for attempt in $(seq 1 60); do ok=1\n")
	for _, endpoint := range endpoints {
		fmt.Fprintf(&b, "curl -fsS %s/health | grep -q '\"health\":\"true\"' || ok=0\n", shell.Quote(endpoint))
	}
	b.WriteString("[[ $ok == 1 ]] && exit 0; sleep 2; done; exit 1")
	return b.String()
}

func awsSetupError(statePath string, state awsState, err error) error {
	if len(state.Instances) == 0 {
		return err
	}
	return fmt.Errorf("%w; created instance state is saved at %s; run 'etcd-infra aws down --name %s' to clean up", err, statePath, state.Name)
}

func awsStatePath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".etcd-infra", "aws", name+".json"), nil
}

func writeAWSState(path string, state awsState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal AWS state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create AWS state directory: %w", err)
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write AWS state: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("replace AWS state: %w", err)
	}
	return nil
}

func readAWSState(path string) (awsState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return awsState{}, fmt.Errorf("read AWS state %s: %w", path, err)
	}
	var state awsState
	if err := json.Unmarshal(data, &state); err != nil {
		return awsState{}, fmt.Errorf("parse AWS state %s: %w", path, err)
	}
	// A state with no members is valid when a stress client remains: a
	// failed "aws down" can delete every member yet fail on a stress client,
	// and the next "aws down" must still be able to read the state to finish
	// the job.
	if state.Name == "" || state.Region == "" || (len(state.Instances) == 0 && len(state.StressClients) == 0) {
		return awsState{}, fmt.Errorf("invalid AWS state %s", path)
	}
	return state, nil
}

func printAWSEndpoints(state awsState) {
	privateEndpoints := make([]string, 0, len(state.Instances))
	publicEndpoints := make([]string, 0, len(state.Instances))
	for _, instance := range state.Instances {
		privateEndpoints = append(privateEndpoints, "http://"+instance.PrivateIPv4+":2379")
		if instance.PublicIPv4 != "" {
			publicEndpoints = append(publicEndpoints, "http://"+instance.PublicIPv4+":2379")
		}
	}
	fmt.Printf("VPC endpoints: %s\n", strings.Join(privateEndpoints, ","))
	if len(state.StressClients) > 0 {
		fmt.Printf("stress clients: suites run in-VPC on %s; no inbound security-group rule for the test host is required\n",
			stressClientNames(state))
	} else if len(publicEndpoints) == len(state.Instances) {
		fmt.Printf("public endpoints (requires security-group ingress): %s\n", strings.Join(publicEndpoints, ","))
	}
}

// stressClientNames lists the stress client instance names for display.
func stressClientNames(state awsState) string {
	names := make([]string, 0, len(state.StressClients))
	for _, client := range state.StressClients {
		names = append(names, fmt.Sprintf("%s (%s)", client.Name, client.ID))
	}
	return strings.Join(names, ", ")
}
