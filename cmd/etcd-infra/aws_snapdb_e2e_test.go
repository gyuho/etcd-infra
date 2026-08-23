package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	awsprovider "git.tbd/etcd-infra/pkg/providers/aws"
	"git.tbd/etcd-infra/pkg/providers/compute"
	"git.tbd/etcd-infra/pkg/shell"
)

// AWS end-to-end tests for the snap.db directory-fsync fix
// (gyuho/etcd, branch test). They mirror the local container
// suite in local_snapdb_e2e_test.go — same fault sequence, same assertions —
// with two AWS-specific mechanics:
//
//   - Instances are driven over SSM RunCommand and systemd
//     (systemctl stop/start/kill on etcd-infra.service), and failpoints are
//     armed through a systemd drop-in (GOFAIL_FAILPOINTS in an override.conf)
//     before the lag phase starts, so arming cannot race the leader's
//     snapshot stream.
//   - TestSnapDBHardPowerLossAWSE2E adds the one repro no container can do:
//     an in-guest hard reboot via SysRq. The AWS documentation lists
//     reboot(b) among the SysRq commands for EC2 instances
//     (https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/troubleshoot-using-serial-console.html#SysRq);
//     with SSM shell access the same command is issued in-guest as
//     "echo b > /proc/sysrq-trigger", which reboots immediately without
//     syncing or unmounting — the guest loses its page cache exactly as in a
//     power loss. The serial console path is the interactive equivalent and
//     is not automatable over SSM. On the fixed build the snap directory was
//     fsynced before SaveDBFrom returned, so the snap.db entry is on EBS and
//     the member must boot from the snapshot on every iteration.
//
// The tests skip unless ETCD_INFRA_AWS_E2E_CLUSTER names a cluster created by
// "etcd-infra aws up" (see hack/aws-snapdb-e2e.sh). The test host must reach
// the members on TCP 2379. Clusters created with "aws up --bastion" are
// reached over SSM port-forwarding through the bastion relay, so client
// traffic originates inside the VPC and no security-group rule for the test
// host is required (the production-realistic path). Without a bastion, the
// tests dial member IPs directly: public IPs when every instance has one,
// otherwise private IPs, so run from a network with VPC access or allow the
// test host in the security group.
const (
	awsE2EClusterEnv = "ETCD_INFRA_AWS_E2E_CLUSTER"
	awsE2EFlavorEnv  = "ETCD_INFRA_AWS_E2E_FLAVOR"
	// awsE2EStateEnv points at a cluster state file directly; "aws drive"
	// sets it when it ships the state to the stress client.
	awsE2EStateEnv     = "ETCD_INFRA_AWS_E2E_STATE"
	awsE2EService      = "etcd-infra.service"
	awsE2EDataDir      = "/var/lib/etcd"
	awsE2EGofailAddr   = "http://127.0.0.1:2234"
	awsE2EReadyTimeout = 10 * time.Minute
)

type awsSnapDBE2EFixture struct {
	state     awsState
	manager   *awsprovider.Manager
	instances []compute.Instance
	endpoints []string
}

// awsE2EStatePath resolves the state file path: ETCD_INFRA_AWS_E2E_STATE
// when set (on-bastion runs), else the state directory for the cluster name.
// The replace flow writes updates back to this path; "aws drive" ships the
// file to the stress client and uploads the updated copy back to S3.
func awsE2EStatePath(t *testing.T, name string) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv(awsE2EStateEnv)); path != "" {
		return path
	}
	statePath, err := awsStatePath(name)
	require.NoError(t, err)
	return statePath
}

// awsE2EState loads the cluster state: from the path in
// ETCD_INFRA_AWS_E2E_STATE when set (on-bastion runs), else from the state
// directory for the named cluster.
func awsE2EState(t *testing.T, name string) (awsState, error) {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv(awsE2EStateEnv)); path != "" {
		return readAWSState(path)
	}
	statePath, err := awsStatePath(name)
	if err != nil {
		return awsState{}, err
	}
	return readAWSState(statePath)
}

func awsSnapDBE2EFixtureFromEnv(t *testing.T) awsSnapDBE2EFixture {
	t.Helper()
	name := awsE2EClusterName()
	if name == "" {
		t.Skipf("set %s to run the AWS snap.db durability E2E tests", awsE2EClusterEnv)
	}
	state, err := awsE2EState(t, name)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	require.NoError(t, err)
	manager := awsprovider.New(cfg)

	f := awsSnapDBE2EFixture{state: state, manager: manager}
	for _, saved := range state.Instances {
		instance, err := manager.Get(ctx, saved.ID)
		require.NoError(t, err)
		require.Equal(t, compute.InstanceStateRunning, instance.State(), "%s is not running", saved.Name)
		f.instances = append(f.instances, instance)
	}
	// Direct VPC endpoints: the suites run on the cluster's stress clients,
	// which share the members' VPC and security groups.
	f.endpoints = awsE2EMemberEndpoints(t, state)
	return f
}

func awsE2EClusterName() string {
	return strings.TrimSpace(os.Getenv(awsE2EClusterEnv))
}

// awsE2ERequireFlavor skips the test unless the cluster runs the named image
// flavor ("fix" or "control"); hack/aws-snapdb-e2e.sh sets it.
func awsE2ERequireFlavor(t *testing.T, want string) {
	t.Helper()
	if got := strings.TrimSpace(os.Getenv(awsE2EFlavorEnv)); got != want {
		t.Skipf("test requires %s=%s (got %q)", awsE2EFlavorEnv, want, got)
	}
}

// awsE2EMemberID returns the cluster member ID for a member name.
func awsE2EMemberID(t *testing.T, ctx context.Context, cli *clientv3.Client, name string) uint64 {
	t.Helper()
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resp, err := cli.MemberList(listCtx)
	cancel()
	require.NoError(t, err)
	for _, m := range resp.Members {
		if m.Name == name {
			return m.ID
		}
	}
	t.Fatalf("member %s not found in membership", name)
	return 0
}

// awsE2EReinstallMember removes a member through the surviving quorum, wipes
// its data directory, runs an optional setup script (for example mounting a
// non-journaled filesystem), and re-adds the member with
// --initial-cluster-state=existing and no armed failpoint.
func awsE2EReinstallMember(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, cli *clientv3.Client, idx int, setupScript string) {
	t.Helper()
	target := f.instances[idx]
	targetName := f.state.Instances[idx].Name
	memberID := awsE2EMemberID(t, ctx, cli, targetName)

	awsE2EStopEtcd(t, ctx, target)
	removeCtx, removeCancel := context.WithTimeout(ctx, 15*time.Second)
	_, err := cli.MemberRemove(removeCtx, memberID)
	removeCancel()
	require.NoError(t, err)

	members := awsMembers(f.state)
	command := append([]string{"/usr/local/bin/etcd"}, etcdServerArgs(members[idx], members, f.state.Name, awsE2EDataDir)...)
	// Keep the reinstalled member consistent with the bootstrap flags; the
	// later --log-level wins over the warn default in etcdServerArgs.
	command = append(command, "--snapshot-count=10", "--snapshot-catchup-entries=10", "--log-level=info")
	for i, arg := range command {
		if arg == "new" && i > 0 && command[i-1] == "--initial-cluster-state" {
			command[i] = "existing"
		}
	}
	// The setup/teardown script runs BEFORE the wipe: the ext2 teardown must
	// umount the crash-dirty loop filesystem first — deleting files on it
	// fails with "Structure needs cleaning" — and the ext2 setup must mount
	// before the wipe so the wipe no-ops on the fresh empty filesystem
	// instead of racing the mount.
	awsE2ERun(t, ctx, target, setupScript+`
find `+awsE2EDataDir+` -mindepth 1 -delete
install -d -m 0700 `+awsE2EDataDir+`
rm -f /etc/systemd/system/etcd-infra.service.d/gofail.conf
cat > /etc/systemd/system/etcd-infra.service <<'EOF'
[Unit]
Description=etcd test cluster member
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment="`+awsE2EGofailEnv+`"
ExecStart=`+shell.JoinArgs(command)+`
Restart=on-failure
RestartSec=5s
LimitNOFILE=40000

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload`)

	addCtx, addCancel := context.WithTimeout(ctx, 15*time.Second)
	_, err = cli.MemberAdd(addCtx, []string{members[idx].PeerURL})
	addCancel()
	require.NoError(t, err)
	awsE2EStartEtcd(t, ctx, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[idx])
}

// awsE2ERun executes a shell script on an instance over SSM and requires
// exit code 0.
func awsE2ERun(t *testing.T, ctx context.Context, instance compute.Instance, script string) *compute.ExecuteResult {
	t.Helper()
	result, err := instance.RunCommandWithOptions(
		ctx,
		[]string{"bash", "-ceu", script},
		&compute.RunCommandOptions{Timeout: 2 * time.Minute},
	)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode, "script failed on %s: %s\n%s", instance.ID(), script, result.Stderr)
	return result
}

// awsE2EGofailEnv arms the member-local gofail HTTP endpoint (SSM curl to
// localhost only; never published).
const awsE2EGofailEnv = "GOFAIL_HTTP=127.0.0.1:2234"

// awsE2EGofailDropIn renders the systemd drop-in that arms a failpoint. The
// empty Environment= line first resets the list so re-arming replaces the
// previous failpoint instead of accumulating — and because the reset also
// drops the main unit's environment, GOFAIL_HTTP must be re-added here.
func awsE2EGofailDropIn(failpointTerms string) string {
	return "[Service]\nEnvironment=\n" + awsSystemdEnvironment([]string{awsE2EGofailEnv, failpointTerms})
}

// awsE2ESystemdScript wraps a systemctl operation with settle-and-retry: a
// queued or in-flight job (systemd auto-restart after a kill, or a cleanup
// from a prior run) cancels a new job with "Job for X canceled". Wait for the
// queue to settle and retry rather than failing the suite.
const awsE2ESystemdScript = `
for i in 1 2 3 4 5 6; do
  systemctl reset-failed ` + awsE2EService + ` 2>/dev/null || true
  if %s; then
    exit 0
  fi
  sleep 3
done
echo "systemd operation kept failing" >&2
exit 1
`

// awsE2EArmFailpoint arms a gofail failpoint on one member through a systemd
// drop-in and restarts the member's etcd, so the failpoint is active from
// process boot — before any peer traffic — and re-arms on every later
// restart.
func awsE2EArmFailpoint(t *testing.T, ctx context.Context, instance compute.Instance, failpointTerms string) {
	t.Helper()
	awsE2ERun(t, ctx, instance, `
mkdir -p /etc/systemd/system/etcd-infra.service.d
cat > /etc/systemd/system/etcd-infra.service.d/gofail.conf <<'EOF'
`+awsE2EGofailDropIn(failpointTerms)+`EOF
systemctl daemon-reload
`+fmt.Sprintf(awsE2ESystemdScript, "systemctl restart "+awsE2EService))
}

// awsE2EDisarmFailpointOnCleanup disarms any armed failpoint at test end,
// even on failure: the drop-in re-arms on every restart, so a suite abort
// with the cluster left up would poison later runs. Best-effort: a failed
// cleanup must not mask the test's own failure.
func awsE2EDisarmFailpointOnCleanup(t *testing.T, instance compute.Instance) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, err := instance.RunCommandWithOptions(
			ctx,
			[]string{"bash", "-ceu", "rm -f /etc/systemd/system/etcd-infra.service.d/gofail.conf; systemctl daemon-reload; systemctl restart " + awsE2EService + " || true"},
			&compute.RunCommandOptions{Timeout: time.Minute},
		)
		if err != nil {
			t.Logf("cleanup: disarm failpoint on %s: %v", instance.ID(), err)
		}
	})
}

// awsE2EDisarmFailpoint removes the failpoint drop-in and restarts etcd.
func awsE2EDisarmFailpoint(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, `
rm -f /etc/systemd/system/etcd-infra.service.d/gofail.conf
systemctl daemon-reload
`+fmt.Sprintf(awsE2ESystemdScript, "systemctl restart "+awsE2EService))
}

// awsE2EClearFailpoint deactivates a failpoint on the running process over
// the gofail HTTP endpoint without a restart.
func awsE2EClearFailpoint(t *testing.T, ctx context.Context, instance compute.Instance, name string) {
	t.Helper()
	awsE2ERun(t, ctx, instance, "curl -fsS -XDELETE "+awsE2EGofailAddr+"/"+name)
}

func awsE2EStopEtcd(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, fmt.Sprintf(awsE2ESystemdScript,
		"systemctl stop "+awsE2EService+" && while systemctl is-active --quiet "+awsE2EService+"; do sleep 1; done"))
}

func awsE2EStartEtcd(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, fmt.Sprintf(awsE2ESystemdScript, "systemctl start "+awsE2EService))
}

// awsE2EKillEtcd SIGKILLs etcd and stops the unit in the same command, so
// systemd's Restart=on-failure (RestartSec=5s) cannot restart the process
// before the test finishes mutating the data directory. The kill tolerates
// an already-dead process; the stop is what matters.
func awsE2EKillEtcd(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, "systemctl kill --signal SIGKILL "+awsE2EService+" || true; systemctl stop "+awsE2EService)
}

// awsE2EHardCrash reboots the instance immediately through the in-guest SysRq
// trigger: no sync, no unmount, page cache dropped — the EC2 equivalent of
// power loss. The SSM agent dies with the guest, so the command error is
// expected and ignored; WaitForReady then waits for the instance to boot and
// SSM to come back. The boot ID must change: without that proof, a lost
// sysrq command would let the power-loss tests pass against an instance that
// never crashed.
func awsE2EHardCrash(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, idx int) {
	t.Helper()
	instance := f.instances[idx]
	bootBefore := strings.TrimSpace(awsE2ERun(t, ctx, instance, "cat /proc/sys/kernel/random/boot_id").Stdout)

	crashCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = instance.RunCommandWithOptions(
		crashCtx,
		[]string{"bash", "-c", "echo 1 > /proc/sys/kernel/sysrq; echo b > /proc/sysrq-trigger"},
		&compute.RunCommandOptions{Timeout: 20 * time.Second},
	)
	cancel()
	_, err := f.manager.WaitForReady(ctx, instance.ID(), awsE2EReadyTimeout)
	require.NoError(t, err, "instance %s never came back after the sysrq hard crash", instance.ID())

	require.Eventually(t, func() bool {
		result, err := instance.RunCommandWithOptions(
			ctx,
			[]string{"cat", "/proc/sys/kernel/random/boot_id"},
			&compute.RunCommandOptions{Timeout: 30 * time.Second},
		)
		if err != nil || result.ExitCode != 0 {
			return false
		}
		return strings.TrimSpace(result.Stdout) != bootBefore
	}, 5*time.Minute, 5*time.Second, "instance %s never rebooted (boot ID unchanged)", instance.ID())
}

// awsE2EJournalGrep returns the journal lines matching pattern. The grep
// runs on the instance because SSM GetCommandInvocation truncates command
// output at 24KB: after a few boots the full journal exceeds the cap and the
// truncation drops the newest lines — exactly the ones the tests wait on —
// and makes absence assertions silently worthless.
func awsE2EJournalGrep(t *testing.T, ctx context.Context, instance compute.Instance, currentBootOnly bool, pattern string) string {
	t.Helper()
	args := "journalctl -u " + awsE2EService + " --no-pager"
	if currentBootOnly {
		args += " -b"
	}
	return awsE2ERun(t, ctx, instance, args+" | grep -F -- "+shell.Quote(pattern)+" || true").Stdout
}

func waitForAWSJournal(t *testing.T, ctx context.Context, instance compute.Instance, substr string, currentBootOnly bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.TrimSpace(awsE2EJournalGrep(t, ctx, instance, currentBootOnly, substr)) != ""
	}, 150*time.Second, 2*time.Second, "%s never logged %q", instance.ID(), substr)
}

// awsE2ETimestamp returns the instance's local time in journalctl --since
// format. Captured on the instance itself so clock skew with the test host
// cannot shift the window.
func awsE2ETimestamp(t *testing.T, ctx context.Context, instance compute.Instance) string {
	t.Helper()
	return strings.TrimSpace(awsE2ERun(t, ctx, instance, "date '+%Y-%m-%d %H:%M:%S'").Stdout)
}

// awsE2EJournalGrepSince matches journal lines newer than a timestamp
// captured before the triggering action. Members are reused across tests and
// across service restarts within one machine boot, so neither the all-boots
// journal nor "-b" can separate a fresh snapshot from an earlier one; only a
// time window can.
func awsE2EJournalGrepSince(t *testing.T, ctx context.Context, instance compute.Instance, since, pattern string) string {
	t.Helper()
	args := "journalctl -u " + awsE2EService + " --no-pager --since " + shell.Quote(since)
	return awsE2ERun(t, ctx, instance, args+" | grep -F -- "+shell.Quote(pattern)+" || true").Stdout
}

func waitForAWSJournalSince(t *testing.T, ctx context.Context, instance compute.Instance, substr, since string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.TrimSpace(awsE2EJournalGrepSince(t, ctx, instance, since, substr)) != ""
	}, 150*time.Second, 2*time.Second, "%s never logged %q after %s", instance.ID(), substr, since)
}

func awsE2ESnapDBFiles(t *testing.T, ctx context.Context, instance compute.Instance) []string {
	t.Helper()
	out := awsE2ERun(t, ctx, instance, "ls "+awsE2EDataDir+"/member/snap/*.snap.db 2>/dev/null || true").Stdout
	return strings.Fields(out)
}

func waitForAWSSnapDBFile(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(awsE2ESnapDBFiles(t, ctx, instance)) > 0
	}, 120*time.Second, 2*time.Second, "%s never received a snapshot db", instance.ID())
}

// awsE2EDriveSnapshotToMember makes the member lag behind the leader's
// compacted log and restarts its etcd, so the leader must stream it a
// snapshot: stop the etcd service, advance the cluster past snapshot-count
// plus snapshot-catchup entries, compact, then start the service again.
//
// The leader snapshots on its own apply loop, so a single round can lose the
// race: the member reconnects before the leader's snapshotter compacts the
// raft log past the member's position and catches up from plain entries
// (observed on EC2: a member 26 entries behind caught up from the log). Each
// round pushes the leader another snapshot-count plus catchup-entries ahead
// of the still-stopped member, so a bounded retry converges
// deterministically.
func awsE2EDriveSnapshotToMember(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, cli *clientv3.Client, idx int) {
	t.Helper()
	awsE2EDriveSnapshot(t, ctx, f, cli, idx, nil, "")
}

// awsE2EDriveSnapshot drives a snapshot to the member with bounded retry.
// probe confirms the arrival: nil means "a snap.db file exists" (the save
// completed or is paused mid-apply). DirSyncError instead probes for the
// injected-failure journal line, because there the save fails by design and
// no durable snap.db may exist.
func awsE2EDriveSnapshot(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, cli *clientv3.Client, idx int, probe func() bool, probeDesc string) {
	t.Helper()
	target := f.instances[idx]

	for round := 1; ; round++ {
		// The round's receive marker must survive any receive speed: the
		// *.snap.db filename exists only for the stream's duration, which is
		// sub-millisecond on a fast in-VPC path, and "applying snapshot" only
		// logs after SaveDBFrom — after the pause failpoints. The handler's
		// "receiving database snapshot" logs at stream start and persists
		// through every pause, so probe the journal for it, scoped to this
		// round's start.
		roundSince := awsE2ETimestamp(t, ctx, target)
		if probe == nil {
			probe = func() bool {
				return strings.TrimSpace(awsE2EJournalGrepSince(t, ctx, target, roundSince, "receiving database snapshot")) != ""
			}
			probeDesc = "a snapshot receive (journal)"
		}
		t.Logf("round %d: stopping %s at %s", round, target.ID(), time.Now().UTC().Format("15:04:05.000"))
		awsE2EStopEtcd(t, ctx, target)
		// Prove the stop: the member's endpoint must refuse connections before
		// the drive's writes land, or the member receives them live and no
		// snapshot is ever needed.
		statusCtx, statusCancel := context.WithTimeout(ctx, 3*time.Second)
		_, statusErr := cli.Status(statusCtx, f.endpoints[idx])
		statusCancel()
		if statusErr == nil {
			t.Logf("round %d: %s still answers after stop; waiting for the stop to settle", round, target.ID())
			require.Eventually(t, func() bool {
				c, cancel2 := context.WithTimeout(ctx, 3*time.Second)
				_, err := cli.Status(c, f.endpoints[idx])
				cancel2()
				return err != nil
			}, 30*time.Second, time.Second, "%s never stopped answering", target.ID())
		}
		// Drop interrupted-save artifacts before the freshness check, every
		// round: a completed apply renames snap.db to .snap, so a *.snap.db
		// present while etcd is stopped is by definition stale, and the file
		// probe must only ever match a snapshot from this round.
		awsE2ERun(t, ctx, target, "rm -f "+awsE2EDataDir+"/member/snap/*.snap.db")
		var revision int64
		for i := 0; i < snapDBSnapshotKeys; i++ {
			putCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			resp, err := cli.Put(putCtx, fmt.Sprintf("/etcd-infra-e2e/snapdb/%s/%06d", f.state.Instances[idx].Name, i), "payload")
			cancel()
			require.NoError(t, err)
			revision = resp.Header.Revision
		}
		compactCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := cli.Compact(compactCtx, revision)
		cancel()
		require.NoError(t, err)

		// The leader compacts its raft log only when its own snapshotter runs
		// (every snapshot-count applies, asynchronously). Restarting the member
		// before that compaction lands lets it catch up from plain log entries
		// and no snapshot streams. The SSM command latencies masked this on
		// slower paths; on the fast in-VPC path the slack must be explicit.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		awsE2EStartEtcd(t, ctx, target)

		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if probe() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("context done while waiting for %s on %s: %v", probeDesc, target.ID(), ctx.Err())
			case <-time.After(2 * time.Second):
			}
		}
		journalTail := awsE2ERun(t, ctx, target,
			"journalctl -u "+awsE2EService+" --no-pager | tail -20 | cut -c1-220")
		t.Logf("no snapshot streamed in round %d; member journal tail:\n%s", round, journalTail.Stdout)
		// The leader's side decides log-vs-snapshot: dump every peer's
		// unfiltered tail (snapshot and compaction greps missed a failure mode
		// once already).
		for _, other := range f.instances {
			if other.ID() == target.ID() {
				continue
			}
			out := awsE2ERun(t, ctx, other,
				"journalctl -u "+awsE2EService+" --no-pager | tail -25 | cut -c1-220")
			t.Logf("peer %s journal tail:\n%s", other.ID(), out.Stdout)
		}
		if round == 3 {
			t.Fatalf("%s: no %s after %d rounds", target.ID(), probeDesc, round)
		}
	}
}

func awsE2EWaitMemberHealthy(t *testing.T, ctx context.Context, cli *clientv3.Client, endpoint string) {
	t.Helper()
	require.Eventually(t, func() bool {
		statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := cli.Status(statusCtx, endpoint)
		cancel()
		return err == nil
	}, 120*time.Second, 2*time.Second, "%s never became healthy", endpoint)
}

func assertAWSKVHashEqual(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, cli *clientv3.Client) {
	t.Helper()
	require.Eventually(t, func() bool {
		var reference *clientv3.HashKVResponse
		for _, endpoint := range f.endpoints {
			hashCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			resp, err := cli.HashKV(hashCtx, endpoint, 0)
			cancel()
			if err != nil {
				return false
			}
			if reference == nil {
				reference = resp
				continue
			}
			if resp.Hash != reference.Hash || resp.Header.Revision != reference.Header.Revision {
				return false
			}
		}
		return reference != nil
	}, 150*time.Second, 2*time.Second, "cluster members never converged on an equal KV hash")
}

func newAWSSnapDBE2EClient(t *testing.T, endpoints []string) *clientv3.Client {
	t.Helper()
	cli, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })
	return cli
}

// TestSnapDBReceiveCrashWindowAWSE2E is the AWS counterpart of
// TestSnapDBReceiveCrashWindowLocalE2E: a SIGKILL between the snap.db rename
// and SaveDBFrom's return leaves no durable WAL record, so the member boots
// and the leader resends. Requires the fix image (the
// snapDBRenameBeforeDirSync failpoint exists only there).
func TestSnapDBReceiveCrashWindowAWSE2E(t *testing.T) {
	awsE2ERequireFlavor(t, "fix")
	f := awsSnapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.instances) - 1
	target := f.instances[targetIdx]
	// journald persists across suite runs on the root volume; scope the
	// absence assertion to this test so a previous run's panic cannot fail it.
	sinceTestStart := awsE2ETimestamp(t, ctx, target)

	t.Log("arm the crash window from process boot (snapDBRenameBeforeDirSync=sleep(30s))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=snapDBRenameBeforeDirSync=sleep("30s")`)
	awsE2EDisarmFailpointOnCleanup(t, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	// The crash window is the rename-to-fsync pause. On a fast in-VPC path
	// the receive stream is sub-millisecond, so the *.snap.db filename (gone
	// after the rename) cannot be polled reliably. Detect the window from the
	// journal instead: "applying snapshot" is logged before the pause and
	// persists, while SaveDBFrom's "saved database snapshot to disk" only
	// appears after the pause — and the failpoint pauses between them.
	// The crash window is the rename-to-fsync pause. "receiving database
	// snapshot" logs at stream start and persists through the pause, while
	// SaveDBFrom's "saved database snapshot to disk" only appears after it —
	// the failpoint pauses between them. (The older "applying snapshot"
	// marker logs after the pause and so cannot see the window.)
	t.Log("wait until the snap.db rename has happened (the member is inside the crash window)")
	waitForAWSJournalSince(t, ctx, target, "receiving database snapshot", sinceTestStart)
	require.Eventually(t, func() bool {
		return strings.TrimSpace(awsE2EJournalGrepSince(t, ctx, target, sinceTestStart, "saved database snapshot to disk")) == ""
	}, 30*time.Second, time.Second, "the save completed before the crash could land in the window")

	t.Log("SIGKILL etcd inside the window, between the snap.db rename and the directory fsync")
	awsE2EKillEtcd(t, ctx, target)

	t.Log("start etcd; with no WAL snapshot record it must boot cleanly and receive the snapshot again")
	sinceStart := awsE2ETimestamp(t, ctx, target)
	awsE2EStartEtcd(t, ctx, target)
	// The resend reuses the same snapshot id (no entries commit between the
	// kill and the resend), so SaveDBFrom takes its idempotent path, which
	// skips the "saved database snapshot to disk" line. The rafthttp handler
	// line is emitted on both save paths, and the killed first attempt never
	// reached it (SaveDBFrom never returned), so it is unambiguous.
	waitForAWSJournalSince(t, ctx, target, "received and saved database snapshot", sinceStart)
	assertAWSKVHashEqual(t, ctx, f, cli)
	require.Empty(t, awsE2EJournalGrepSince(t, ctx, target, sinceTestStart, "failed to find database snapshot file"))
	awsE2EDisarmFailpoint(t, ctx, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])
	t.Logf("%s survived a SIGKILL inside the SaveDBFrom crash window and caught up via resend", f.state.Instances[targetIdx].Name)
}

// TestSnapDBDirSyncErrorAWSE2E is the AWS counterpart of
// TestSnapDBDirSyncErrorLocalE2E: an injected snap-directory fsync failure
// must surface in the member's logs, and the member must recover once the
// failure is cleared and the leader resends. Requires the fix image.
func TestSnapDBDirSyncErrorAWSE2E(t *testing.T) {
	awsE2ERequireFlavor(t, "fix")
	f := awsSnapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.instances) - 1
	target := f.instances[targetIdx]

	t.Log("arm the fsync failure from process boot (snapDBDirSyncError=return(...))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=snapDBDirSyncError=return("injected snap dir fsync failure")`)
	awsE2EDisarmFailpointOnCleanup(t, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	sinceDrive := awsE2ETimestamp(t, ctx, target)
	awsE2EDriveSnapshot(t, ctx, f, cli, targetIdx, func() bool {
		// The injected failure makes every save fail, so no durable snap.db
		// may exist; the snapshot's arrival is the handler's error line.
		return strings.TrimSpace(awsE2EJournalGrepSince(t, ctx, target, sinceDrive, "failed to save incoming database snapshot")) != ""
	}, "failed-save signal")

	t.Log("the injected fsync failure must surface loudly")
	// The injected failpoint returns before the fsyncSnapDir Warn log line,
	// so the signal is the rafthttp handler's error carrying the injected
	// fsync failure message.
	waitForAWSJournalSince(t, ctx, target, "failed to save incoming database snapshot", sinceDrive)
	waitForAWSJournalSince(t, ctx, target, "injected snap dir fsync failure", sinceDrive)

	t.Log("clear the failure on the running process; the leader resends and the member catches up")
	// The interrupted attempt left the renamed snap.db behind, and the resend
	// for the same snapshot id would take SaveDBFrom's idempotent path, which
	// bypasses the snapDBDirSyncError failpoint — a silent no-op clear would
	// still "recover". Remove the orphan first so the resend must take the
	// rename path through the failpoint: if the clear did nothing, the save
	// fails again and the wait times out loudly.
	awsE2ERun(t, ctx, target, "rm -f "+awsE2EDataDir+"/member/snap/*.snap.db")
	sinceClear := awsE2ETimestamp(t, ctx, target)
	awsE2EClearFailpoint(t, ctx, target, "snapDBDirSyncError")
	// Restart so the member reconnects fresh: the failed save broke its
	// receive stream, and the leader's snapshot resend cadence is not
	// prompt or guaranteed. With the failpoint cleared the member then
	// converges — by resend-snapshot or by log entries — and any save that
	// does run after the clear must succeed: a silent no-op clear would
	// print the failure line again and fail the absence check loudly.
	awsE2EStopEtcd(t, ctx, target)
	awsE2EStartEtcd(t, ctx, target)
	assertAWSKVHashEqual(t, ctx, f, cli)
	require.Empty(t, awsE2EJournalGrepSince(t, ctx, target, sinceClear, "injected snap dir fsync failure"),
		"the snap dir fsync failpoint fired again after it was cleared")
	assertAWSKVHashEqual(t, ctx, f, cli)
	awsE2EDisarmFailpoint(t, ctx, target)
	t.Logf("%s reported the snap dir fsync failure loudly and recovered via resend", f.state.Instances[targetIdx].Name)
}

// TestSnapDBDirentLostAWSE2E is the AWS counterpart of
// TestSnapDBDirentLostLocalE2E: it builds the post-crash state the fix makes
// unreachable (durable WAL snapshot record, snap.db directory entry deleted
// from the data disk — what a machine crash leaves behind on unfixed code),
// requires the loud bootstrap panic, then repairs the member through the
// surviving quorum. Runs on the fixed and control images alike.
func TestSnapDBDirentLostAWSE2E(t *testing.T) {
	f := awsSnapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.instances) - 1
	target := f.instances[targetIdx]
	targetName := f.state.Instances[targetIdx].Name

	t.Log("arm apply to pause before consuming the snap.db (applyBeforeOpenSnapshot=sleep(60s))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=applyBeforeOpenSnapshot=sleep("60s")`)
	awsE2EDisarmFailpointOnCleanup(t, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	sinceDrive := awsE2ETimestamp(t, ctx, target)
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and applySnapshot is paused before consuming it")
	waitForAWSJournalSince(t, ctx, target, "applying snapshot", sinceDrive)
	waitForAWSSnapDBFile(t, ctx, target)
	// The failpoint sleep dominates the millisecond gap between the "applying
	// snapshot" log line and the WAL snapshot record sync; two seconds in, the
	// member is past notifyc, so the WAL snapshot record is durable.
	time.Sleep(2 * time.Second)

	t.Log("SIGKILL etcd, then fabricate the machine crash: the kernel never persisted the rename's directory entry")
	// SIGKILL leaves the page cache (and the dirent) intact. Deleting snap.db
	// from the data disk reproduces the exact on-disk result of a hard crash
	// dropping the un-fsynced directory entry on unfixed code.
	awsE2EKillEtcd(t, ctx, target)
	awsE2ERun(t, ctx, target, "rm -f "+awsE2EDataDir+"/member/snap/*.snap.db")

	t.Log("start etcd; bootstrap finds the WAL snapshot record, cannot find snap.db, and must fail loudly")
	sinceStart := awsE2ETimestamp(t, ctx, target)
	awsE2EStartEtcd(t, ctx, target)
	// systemd (Restart=on-failure) restarts etcd into the same panic, so the
	// member crash-loops and never serves: the blast radius is this one
	// member's availability, never cluster data.
	waitForAWSJournalSince(t, ctx, target, "failed to find database snapshot file", sinceStart)
	t.Logf("%s failed loudly on the lost snap.db directory entry, as in field reports #11949/#14497/#14569", targetName)

	t.Log("remediate as documented: wipe the member's data dir and re-add it to the cluster")
	// Repair the membership through the surviving quorum: remove the dead
	// member, wipe its data dir, re-add it, and start it with
	// --initial-cluster-state=existing and no armed failpoint.
	awsE2EReinstallMember(t, ctx, f, cli, targetIdx, "")

	t.Log("the wiped and re-added member must rejoin and converge")
	assertAWSKVHashEqual(t, ctx, f, cli)
	t.Logf("%s was wiped and re-added; cluster healthy again", targetName)
}

// TestSnapDBHardPowerLossAWSE2E runs the full receive-crash-recover chain
// under a real machine crash: the member receives a snapshot, the WAL
// snapshot record is durable, apply is paused before consuming the snap.db,
// and the instance is hard-rebooted through the in-guest SysRq trigger
// (echo b > /proc/sysrq-trigger — reboot without sync or unmount, the same
// SysRq reboot(b) command the AWS EC2 documentation lists for the serial
// console, issued over SSM because the guest shell is reachable and the
// serial console is not automatable). The guest loses its page cache, so
// only fsynced data survives on EBS.
//
// On the fixed build the snap directory was fsynced before SaveDBFrom
// returned, so the snap.db directory entry is durable and the member must
// boot from the snapshot and rejoin — every time. On the unfixed control
// build the entry's survival depends on ext4 journaling timing (the WAL fsync
// right after usually commits the rename's journal transaction as a side
// effect), which is why the control run is diagnostic only: the fix converts
// a timing accident into a guarantee.
//
// Requires the fix image.
func TestSnapDBHardPowerLossAWSE2E(t *testing.T) {
	awsE2ERequireFlavor(t, "fix")
	f := awsSnapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.instances) - 1
	target := f.instances[targetIdx]

	t.Log("arm apply to pause before consuming the snap.db (applyBeforeOpenSnapshot=sleep(60s))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=applyBeforeOpenSnapshot=sleep("60s")`)
	awsE2EDisarmFailpointOnCleanup(t, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	sinceDrive := awsE2ETimestamp(t, ctx, target)
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and the WAL snapshot record is durable")
	waitForAWSJournalSince(t, ctx, target, "applying snapshot", sinceDrive)
	waitForAWSSnapDBFile(t, ctx, target)
	time.Sleep(2 * time.Second)

	t.Log("hard-crash the instance: in-guest sysrq reboot(b), no sync, page cache dropped")
	awsE2EHardCrash(t, ctx, f, targetIdx)

	t.Log("after reboot the member must boot from the durable snap.db and rejoin")
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])
	assertAWSKVHashEqual(t, ctx, f, cli)
	require.Empty(t, awsE2EJournalGrep(t, ctx, target, true, "failed to find database snapshot file"))
	require.NotEmpty(t, awsE2EJournalGrep(t, ctx, target, true, "Recovering from snapshot"))
	awsE2EDisarmFailpoint(t, ctx, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])
	t.Logf("%s booted from the fsynced snap.db after a real machine crash", f.state.Instances[targetIdx].Name)
}

// awsE2EExt2Setup replaces the member's data directory with a loop-mounted
// ext2 filesystem backed by a file on the root EBS volume. ext2 has no
// journal, so a WAL fsync after SaveDBFrom returns cannot commit the snap.db
// rename's metadata as a side effect — the directory entry survives only in
// the page cache until writeback. A hard crash in that window loses the
// entry deterministically on unfixed code. On the fixed build the directory
// fsync flushes through the loop device to EBS, so the entry is durable.
// The test is self-checking: if the loop device ever stopped forwarding
// flushes, the fixed build would fail too.
//
// The fstab entry matters: the mount must be restored at boot before the
// etcd unit starts (its default dependencies order it after local-fs.target),
// otherwise the member would come up on an empty root-volume directory and
// rejoin cleanly instead of replaying the crashed state.
const awsE2EExt2Setup = `
command -v mkfs.ext2 >/dev/null || yum install -y e2fsprogs || apt-get install -y e2fsprogs
dd if=/dev/zero of=/var/lib/etcd-data.img bs=1M count=512 status=none
mkfs.ext2 -F /var/lib/etcd-data.img >/dev/null
sync
echo '/var/lib/etcd-data.img /var/lib/etcd ext2 loop,noatime 0 0' >> /etc/fstab
mount /var/lib/etcd
`

// awsE2EExt2Teardown unmounts the ext2 filesystem, drops the fstab entry, and
// removes the backing file, returning the member to the root volume.
const awsE2EExt2Teardown = `
umount /var/lib/etcd
sed -i '\#etcd-data.img#d' /etc/fstab
rm -f /var/lib/etcd-data.img
`

// awsE2EHardPowerLossNoJournal drives the receive-crash-recover chain with
// the member's data directory on a non-journaled (ext2) filesystem and then
// hard-crashes the instance. expectPanic selects the image-specific outcome:
// the control build must panic with the field-report signature (the bug,
// reproduced under real power loss), and the fixed build must boot from the
// snap.db and rejoin (the fix, proven where nothing masks it).
func awsE2EHardPowerLossNoJournal(t *testing.T, expectPanic bool) {
	f := awsSnapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.instances) - 1
	target := f.instances[targetIdx]
	targetName := f.state.Instances[targetIdx].Name

	t.Log("reinstall the member with its data dir on a non-journaled (ext2) filesystem")
	awsE2EReinstallMember(t, ctx, f, cli, targetIdx, awsE2EExt2Setup)
	// A mid-test failure would leave the crash-dirty loop mount and its fstab
	// entry, remounting a stale non-journaled filesystem at every later boot.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, err := target.RunCommandWithOptions(
			ctx,
			[]string{"bash", "-ceu", "systemctl stop " + awsE2EService + " || true; umount /var/lib/etcd 2>/dev/null || true; sed -i '\\#etcd-data.img#d' /etc/fstab; rm -f /var/lib/etcd-data.img"},
			&compute.RunCommandOptions{Timeout: time.Minute},
		)
		if err != nil {
			t.Logf("cleanup: ext2 teardown on %s: %v", target.ID(), err)
		}
	})
	assertAWSKVHashEqual(t, ctx, f, cli)

	t.Log("arm apply to pause before consuming the snap.db (applyBeforeOpenSnapshot=sleep(60s))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=applyBeforeOpenSnapshot=sleep("60s")`)
	awsE2EDisarmFailpointOnCleanup(t, target)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	sinceDrive := awsE2ETimestamp(t, ctx, target)
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and the WAL snapshot record is durable")
	waitForAWSJournalSince(t, ctx, target, "applying snapshot", sinceDrive)
	waitForAWSSnapDBFile(t, ctx, target)
	time.Sleep(2 * time.Second)

	t.Log("hard-crash the instance: in-guest sysrq reboot(b), no sync, page cache dropped")
	awsE2EHardCrash(t, ctx, f, targetIdx)

	if expectPanic {
		t.Log("control build: with no directory fsync and no journal to mask it, the dirent is lost and bootstrap must panic")
		waitForAWSJournal(t, ctx, target, "failed to find database snapshot file", true)
		t.Logf("%s panicked on the lost snap.db directory entry after a real machine crash: the bug, live on EC2", targetName)
	} else {
		t.Log("fixed build: the directory fsync put the entry on EBS, so the member must boot from the snap.db")
		awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])
		assertAWSKVHashEqual(t, ctx, f, cli)
		require.Empty(t, awsE2EJournalGrep(t, ctx, target, true, "failed to find database snapshot file"))
		require.NotEmpty(t, awsE2EJournalGrep(t, ctx, target, true, "Recovering from snapshot"))
		t.Logf("%s booted from the fsynced snap.db on a non-journaled filesystem after a real machine crash", targetName)
	}

	t.Log("restore the member to the root volume")
	awsE2EReinstallMember(t, ctx, f, cli, targetIdx, awsE2EExt2Teardown)
	assertAWSKVHashEqual(t, ctx, f, cli)
}

// TestSnapDBHardPowerLossNoJournalFixAWSE2E runs the no-journal power-loss
// test on the fixed build: the member must boot from the snap.db.
func TestSnapDBHardPowerLossNoJournalFixAWSE2E(t *testing.T) {
	awsE2ERequireFlavor(t, "fix")
	awsE2EHardPowerLossNoJournal(t, false)
}

// TestSnapDBHardPowerLossNoJournalControlAWSE2E runs the no-journal
// power-loss test on the unfixed control build: the member must panic with
// the field-report signature, reproducing the bug under real power loss. It
// then restores the member, leaving the cluster healthy.
func TestSnapDBHardPowerLossNoJournalControlAWSE2E(t *testing.T) {
	awsE2ERequireFlavor(t, "control")
	awsE2EHardPowerLossNoJournal(t, true)
}
