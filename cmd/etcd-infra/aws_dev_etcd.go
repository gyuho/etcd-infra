package main

// "aws dev etcd" turns dev boxes into single-member etcd nodes, one
// independent cluster per box, for experimenting with fork builds:
//
//	GOOS=linux GOARCH=amd64 make build            # in the etcd fork
//	etcd-infra aws dev up --name dev01 --dry-run=false
//	etcd-infra aws dev etcd --name dev01 --binary ~/etcd/bin/etcd
//	etcd-infra aws dev run --name dev01 -- curl -fsS localhost:2379/health
//
// Rerunning it swaps the binary, flags, or environment in place and
// restarts the member; data is kept unless --reset-data is set.

import (
	"context"
	"debug/elf"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
	"git.tbd/etcd-infra/pkg/providers/compute"
	"git.tbd/etcd-infra/pkg/shell"
)

const (
	awsDevEtcdTimeout = 10 * time.Minute
	// awsDevEtcdHealthPolls bounds the post-restart health wait (2s each).
	awsDevEtcdHealthPolls = 60
)

// awsDevEtcdBinary is a local binary shipped to the boxes via S3.
type awsDevEtcdBinary struct {
	Path   string // local path
	Name   string // installed as /usr/local/bin/<Name>
	SHA256 string
	URI    string // s3:// object the boxes download
}

func runAWSDevEtcd(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("aws dev etcd", flag.ContinueOnError)
	name := flags.String("name", defaultAWSDevName, "dev group name")
	instances := flags.String("instance", "", "comma-separated instance IDs (default: every in-service box)")
	binary := flags.String("binary", "", "local Linux etcd binary to install (for example a fork build); default: the --version release")
	etcdctl := flags.String("etcdctl", "", "local Linux etcdctl binary to install alongside --binary (optional)")
	version := flags.String("version", "", "etcd release version when --binary is unset (default: latest)")
	extraArgs := flags.String("extra-args", "", "space-separated extra arguments appended to the etcd server command")
	extraArgsFile := flags.String("extra-args-file", os.Getenv(extraArgsFileEnv), "file with extra etcd server arguments, one per line (default: $"+extraArgsFileEnv+")")
	env := flags.String("env", "", "comma-separated KEY=VALUE environment variables for the etcd systemd unit")
	resetData := flags.Bool("reset-data", false, "stop etcd and delete its data directory before starting")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *binary != "" && *version != "" {
		return errors.New("--binary and --version are mutually exclusive")
	}
	if *etcdctl != "" && *binary == "" {
		return errors.New("--etcdctl requires --binary (a release install already includes etcdctl)")
	}
	resolvedArgs, err := resolveExtraArgs(*extraArgs, *extraArgsFile)
	if err != nil {
		return err
	}
	envVars, err := parseLocalEnv(*env)
	if err != nil {
		return err
	}
	state, _, err := readAWSDevGroupState(*name)
	if err != nil {
		return err
	}

	var binaries []awsDevEtcdBinary
	for _, b := range []struct{ path, name string }{{*binary, "etcd"}, {*etcdctl, "etcdctl"}} {
		if b.path == "" {
			continue
		}
		if err := checkLinuxBinary(b.path, state.Arch); err != nil {
			return err
		}
		sum, err := fileSHA256(b.path)
		if err != nil {
			return err
		}
		binaries = append(binaries, awsDevEtcdBinary{
			Path: b.path, Name: b.name, SHA256: sum,
			// Content-addressed under the group's prefix, which the stock
			// instance role can read.
			URI: fmt.Sprintf("%sbin/%s/%s", state.Dev.ResultsURI, sum[:16], b.name),
		})
	}
	bootstrap := awsBootstrapOptions{Arch: state.Arch}
	if len(binaries) == 0 {
		v := *version
		if v == "" {
			v = "latest"
		}
		if bootstrap.Version, err = resolveVersion(ctx, v); err != nil {
			return err
		}
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

	install := awsEtcdInstallScript(bootstrap)
	for _, b := range binaries {
		fmt.Fprintf(os.Stderr, "uploading %s to %s\n", b.Path, b.URI)
		if err := awsCLI(ctx, "", "s3", "cp", "--only-show-errors", b.Path, b.URI, "--region", state.Region); err != nil {
			return fmt.Errorf("upload %s: %w", b.Name, err)
		}
	}
	if len(binaries) > 0 {
		install = awsDevEtcdS3InstallScript(binaries)
	}
	dataDir := state.Dev.MountPoint + "/etcd"

	fmt.Fprintf(os.Stderr, "starting single-member etcd on %s\n", strings.Join(targets, ", "))
	type outcome struct {
		ip     string
		result *compute.ExecuteResult
		err    error
	}
	outcomes := make([]outcome, len(targets))
	var wg sync.WaitGroup
	for i, id := range targets {
		wg.Go(func() {
			instance, err := manager.Get(ctx, id)
			if err != nil {
				outcomes[i].err = err
				return
			}
			ip := instance.PrivateIPv4()
			outcomes[i].ip = ip
			if ip == "" {
				outcomes[i].err = errors.New("no private IPv4 address")
				return
			}
			member := clusterMember{Name: id, ClientURL: "http://" + ip + ":2379", PeerURL: "http://" + ip + ":2380"}
			script := awsDevEtcdScript(member, state.Name, dataDir, install, resolvedArgs, envVars, *resetData)
			outcomes[i].result, outcomes[i].err = instance.RunCommandWithOptions(ctx,
				[]string{"bash", "-c", awsDevRunScript(script)},
				&compute.RunCommandOptions{
					Timeout: awsDevEtcdTimeout,
					// Extra args are forwarded verbatim; never let a token
					// in them turn into instance termination.
					ProviderConfig: awsprovider.CommandConfig{GuestShutdown: true},
				},
			)
		})
	}
	wg.Wait()

	var failed []string
	for i, id := range targets {
		o := outcomes[i]
		switch {
		case o.err != nil:
			fmt.Fprintf(os.Stderr, "etcd on %s: %v\n", id, o.err)
		case o.result.ExitCode != 0:
			fmt.Fprintf(os.Stderr, "etcd on %s: exit %d\n%s%s", id, o.result.ExitCode, o.result.Stdout, o.result.Stderr)
		default:
			fmt.Printf("instance=%s client_url=http://%s:2379 data_dir=%s etcd_version=%s\n",
				id, o.ip, dataDir, awsDevEtcdVersion(o.result.Stdout))
			continue
		}
		failed = append(failed, id)
	}
	fmt.Fprintf(os.Stderr, "next:\n  etcd-infra aws dev run --name %[1]s -- curl -fsS http://127.0.0.1:2379/health\n  etcd-infra aws dev run --name %[1]s -- journalctl -u etcd-infra.service --no-pager -n 100\n  etcd-infra aws dev etcd --name %[1]s --binary <rebuilt etcd>   # swap and restart\n", state.Name)
	if len(failed) > 0 {
		return fmt.Errorf("dev group %s: etcd failed on %d of %d boxes: %s", state.Name, len(failed), len(targets), strings.Join(failed, ", "))
	}
	return nil
}

// checkLinuxBinary rejects binaries the boxes cannot run, the usual
// mistake being a host (macOS) build of the fork.
func checkLinuxBinary(path, arch string) error {
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[arch]
	if want == 0 {
		return fmt.Errorf("dev group has unknown arch %q", arch)
	}
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("%s is not a Linux ELF binary (build it with GOOS=linux GOARCH=%s): %w", path, arch, err)
	}
	defer func() { _ = f.Close() }()
	if f.Machine != want {
		return fmt.Errorf("%s is built for %s, but the dev group is %s (build it with GOOS=linux GOARCH=%s)", path, f.Machine, arch, arch)
	}
	return nil
}

// awsDevEtcdS3InstallScript downloads each binary from S3 with the box's
// instance role, verifies its checksum, and installs it. It expects $tmp.
func awsDevEtcdS3InstallScript(binaries []awsDevEtcdBinary) string {
	var b strings.Builder
	for _, bin := range binaries {
		fmt.Fprintf(&b, `aws s3 cp --only-show-errors %[1]s "$tmp/%[2]s"
echo %[3]s"  $tmp/%[2]s" | sha256sum -c -
install -m 0755 "$tmp/%[2]s" /usr/local/bin/%[2]s
`, shell.Quote(bin.URI), bin.Name, bin.SHA256)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// awsDevEtcdScript installs etcd, (re)writes the unit for a single-member
// cluster with its data under dataDir, restarts it, and waits for /health.
// On success stdout is "etcd --version"; on failure stderr carries the unit
// status and journal tail.
func awsDevEtcdScript(member clusterMember, token, dataDir, install string, extraArgs, env []string, resetData bool) string {
	execStart := shell.JoinArgs(append(
		append([]string{"/usr/local/bin/etcd"}, etcdServerArgs(member, []clusterMember{member}, token, dataDir)...),
		extraArgs...,
	))
	var reset string
	if resetData {
		reset = fmt.Sprintf("systemctl stop etcd-infra.service\nrm -rf -- %s\n", shell.Quote(dataDir))
	}
	return fmt.Sprintf(`set -euo pipefail
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
%[1]s
%[2]s%[3]sinstall -d -m 0700 %[4]s
systemctl enable etcd-infra.service
systemctl restart etcd-infra.service
for _ in $(seq 1 %[5]d); do
    if curl -fsS --max-time 2 http://127.0.0.1:2379/health >/dev/null 2>&1; then
        /usr/local/bin/etcd --version
        exit 0
    fi
    sleep 2
done
echo "etcd is not healthy after restart; unit status and journal follow" >&2
systemctl --no-pager status etcd-infra.service >&2 || true
journalctl -u etcd-infra.service --no-pager -n 40 >&2 || true
exit 1
`, install, awsEtcdUnitScript(env, execStart), reset, shell.Quote(dataDir), awsDevEtcdHealthPolls)
}

// awsDevEtcdVersion extracts the server version from "etcd --version".
func awsDevEtcdVersion(stdout string) string {
	for line := range strings.Lines(stdout) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "etcd Version: "); ok {
			return v
		}
	}
	return "unknown"
}
