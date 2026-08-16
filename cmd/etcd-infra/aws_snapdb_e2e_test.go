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
// (gyuho/etcd@d73ad4e, fix/snapdb-dir-fsync). They mirror the local container
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
// the members on TCP 2379: the tests use public IPs when every instance has
// one, otherwise private IPs, so run from a network with VPC access or allow
// the test host in the security group.
const (
	awsE2EClusterEnv  = "ETCD_INFRA_AWS_E2E_CLUSTER"
	awsE2EFlavorEnv   = "ETCD_INFRA_AWS_E2E_FLAVOR"
	awsE2EService     = "etcd-infra.service"
	awsE2EDataDir     = "/var/lib/etcd"
	awsE2EGofailAddr  = "http://127.0.0.1:2234"
	awsE2EReadyTimout = 10 * time.Minute
)

type awsSnapDBE2EFixture struct {
	state     awsState
	manager   *awsprovider.Manager
	instances []compute.Instance
	endpoints []string
}

func awsSnapDBE2EFixtureFromEnv(t *testing.T) awsSnapDBE2EFixture {
	t.Helper()
	name := awsE2EClusterName()
	if name == "" {
		t.Skipf("set %s to run the AWS snap.db durability E2E tests", awsE2EClusterEnv)
	}
	statePath, err := awsStatePath(name)
	require.NoError(t, err)
	state, err := readAWSState(statePath)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := awsprovider.LoadDefaultConfig(ctx, state.Region)
	require.NoError(t, err)
	manager := awsprovider.New(cfg)

	f := awsSnapDBE2EFixture{state: state, manager: manager}
	usePublic := true
	for _, saved := range state.Instances {
		if saved.PublicIPv4 == "" {
			usePublic = false
		}
	}
	for _, saved := range state.Instances {
		instance, err := manager.Get(ctx, saved.ID)
		require.NoError(t, err)
		require.Equal(t, compute.InstanceStateRunning, instance.State(), "%s is not running", saved.Name)
		f.instances = append(f.instances, instance)
		ip := instance.PrivateIPv4()
		if usePublic {
			ip = instance.PublicIPv4()
		}
		require.NotEmpty(t, ip, "%s has no reachable IP", saved.Name)
		f.endpoints = append(f.endpoints, "http://"+ip+":2379")
	}
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
	command = append(command, "--snapshot-count=10", "--snapshot-catchup-entries=10")
	for i, arg := range command {
		if arg == "new" && i > 0 && command[i-1] == "--initial-cluster-state" {
			command[i] = "existing"
		}
	}
	awsE2ERun(t, ctx, target, `
find `+awsE2EDataDir+` -mindepth 1 -delete
install -d -m 0700 `+awsE2EDataDir+`
`+setupScript+`
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
systemctl restart `+awsE2EService)
}

// awsE2EDisarmFailpoint removes the failpoint drop-in and restarts etcd.
func awsE2EDisarmFailpoint(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, `
rm -f /etc/systemd/system/etcd-infra.service.d/gofail.conf
systemctl daemon-reload
systemctl restart `+awsE2EService)
}

// awsE2EClearFailpoint deactivates a failpoint on the running process over
// the gofail HTTP endpoint without a restart.
func awsE2EClearFailpoint(t *testing.T, ctx context.Context, instance compute.Instance, name string) {
	t.Helper()
	awsE2ERun(t, ctx, instance, "curl -fsS -XDELETE "+awsE2EGofailAddr+"/"+name)
}

func awsE2EStopEtcd(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, "systemctl stop "+awsE2EService)
}

func awsE2EStartEtcd(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, "systemctl start "+awsE2EService)
}

// awsE2EKillEtcd SIGKILLs etcd and stops the unit in the same command, so
// systemd's Restart=on-failure (RestartSec=5s) cannot restart the process
// before the test finishes mutating the data directory.
func awsE2EKillEtcd(t *testing.T, ctx context.Context, instance compute.Instance) {
	t.Helper()
	awsE2ERun(t, ctx, instance, "systemctl kill --signal SIGKILL "+awsE2EService+"; systemctl stop "+awsE2EService)
}

// awsE2EHardCrash reboots the instance immediately through the in-guest SysRq
// trigger: no sync, no unmount, page cache dropped — the EC2 equivalent of
// power loss. The SSM agent dies with the guest, so the command error is
// expected and ignored; WaitForReady then waits for the instance to boot and
// SSM to come back.
func awsE2EHardCrash(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, idx int) {
	t.Helper()
	instance := f.instances[idx]
	crashCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, _ = instance.RunCommandWithOptions(
		crashCtx,
		[]string{"bash", "-c", "echo 1 > /proc/sys/kernel/sysrq; echo b > /proc/sysrq-trigger"},
		&compute.RunCommandOptions{Timeout: 20 * time.Second},
	)
	cancel()
	_, err := f.manager.WaitForReady(ctx, instance.ID(), awsE2EReadyTimout)
	require.NoError(t, err, "instance %s never came back after the sysrq hard crash", instance.ID())
}

func awsE2EJournal(t *testing.T, ctx context.Context, instance compute.Instance, currentBootOnly bool) string {
	t.Helper()
	args := "journalctl -u " + awsE2EService + " --no-pager"
	if currentBootOnly {
		args += " -b"
	}
	return awsE2ERun(t, ctx, instance, args).Stdout
}

func waitForAWSJournal(t *testing.T, ctx context.Context, instance compute.Instance, substr string, currentBootOnly bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Contains(awsE2EJournal(t, ctx, instance, currentBootOnly), substr)
	}, 150*time.Second, 2*time.Second, "%s never logged %q", instance.ID(), substr)
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
func awsE2EDriveSnapshotToMember(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, cli *clientv3.Client, idx int) {
	t.Helper()
	awsE2EStopEtcd(t, ctx, f.instances[idx])

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

	awsE2EStartEtcd(t, ctx, f.instances[idx])
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

	t.Log("arm the crash window from process boot (snapDBRenameBeforeDirSync=sleep(30s))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=snapDBRenameBeforeDirSync=sleep("30s")`)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename has happened (the member is inside the crash window)")
	waitForAWSSnapDBFile(t, ctx, target)

	t.Log("SIGKILL etcd inside the window, between the snap.db rename and the directory fsync")
	awsE2EKillEtcd(t, ctx, target)

	t.Log("start etcd; with no WAL snapshot record it must boot cleanly and receive the snapshot again")
	awsE2EStartEtcd(t, ctx, target)
	waitForAWSJournal(t, ctx, target, "saved database snapshot to disk", false)
	assertAWSKVHashEqual(t, ctx, f, cli)
	require.NotContains(t, awsE2EJournal(t, ctx, target, false), "failed to find database snapshot file")
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
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("the injected fsync failure must surface loudly")
	// The injected failpoint returns before the fsyncSnapDir Warn log line,
	// so the signal is the rafthttp handler's error carrying the injected
	// fsync failure message.
	waitForAWSJournal(t, ctx, target, "failed to save incoming database snapshot", false)
	waitForAWSJournal(t, ctx, target, "injected snap dir fsync failure", false)

	t.Log("clear the failure on the running process; the leader resends and the member catches up")
	// The resend for the same snapshot id takes the idempotent path in
	// SaveDBFrom (the interrupted attempt left the renamed snap.db behind),
	// which fsyncs the directory but skips the "saved database snapshot to
	// disk" log line, so the recovery signal is the rafthttp handler's line.
	awsE2EClearFailpoint(t, ctx, target, "snapDBDirSyncError")
	waitForAWSJournal(t, ctx, target, "received and saved database snapshot", false)
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
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and applySnapshot is paused before consuming it")
	waitForAWSJournal(t, ctx, target, "applying snapshot", false)
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
	awsE2EStartEtcd(t, ctx, target)
	// systemd (Restart=on-failure) restarts etcd into the same panic, so the
	// member crash-loops and never serves: the blast radius is this one
	// member's availability, never cluster data.
	waitForAWSJournal(t, ctx, target, "failed to find database snapshot file", false)
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
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and the WAL snapshot record is durable")
	waitForAWSJournal(t, ctx, target, "applying snapshot", false)
	waitForAWSSnapDBFile(t, ctx, target)
	time.Sleep(2 * time.Second)

	t.Log("hard-crash the instance: in-guest sysrq reboot(b), no sync, page cache dropped")
	awsE2EHardCrash(t, ctx, f, targetIdx)

	t.Log("after reboot the member must boot from the durable snap.db and rejoin")
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])
	assertAWSKVHashEqual(t, ctx, f, cli)
	bootJournal := awsE2EJournal(t, ctx, target, true)
	require.NotContains(t, bootJournal, "failed to find database snapshot file")
	require.Contains(t, bootJournal, "Recovering from snapshot")
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
	assertAWSKVHashEqual(t, ctx, f, cli)

	t.Log("arm apply to pause before consuming the snap.db (applyBeforeOpenSnapshot=sleep(60s))")
	awsE2EArmFailpoint(t, ctx, target, `GOFAIL_FAILPOINTS=applyBeforeOpenSnapshot=sleep("60s")`)
	awsE2EWaitMemberHealthy(t, ctx, cli, f.endpoints[targetIdx])

	t.Log("make the member lag and restart its etcd so the leader must stream a snapshot")
	awsE2EDriveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and the WAL snapshot record is durable")
	waitForAWSJournal(t, ctx, target, "applying snapshot", false)
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
		bootJournal := awsE2EJournal(t, ctx, target, true)
		require.NotContains(t, bootJournal, "failed to find database snapshot file")
		require.Contains(t, bootJournal, "Recovering from snapshot")
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
