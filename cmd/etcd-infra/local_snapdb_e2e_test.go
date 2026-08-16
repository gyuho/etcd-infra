package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"git.tbd/etcd-infra/pkg/providers/compute"
	localprovider "git.tbd/etcd-infra/pkg/providers/local"
)

// Snapshot durability E2E tests for the snap.db directory-fsync fix
// (gyuho/etcd@d73ad4e, fix/snapdb-dir-fsync).
//
// Before the fix, SaveDBFrom renamed the received snapshot db into place
// without fsyncing the snap directory. A hard machine crash could then lose
// the snap.db directory entry while the WAL snapshot record survived, and the
// next boot panicked in RecoverSnapshotBackend ("failed to find database
// snapshot file"). A process kill cannot reproduce the loss because the page
// cache survives it. These container tests pin what etcd itself controls:
//
//   - T1 (TestSnapDBReceiveCrashWindowLocalE2E): a SIGKILL between the
//     snap.db rename and SaveDBFrom's return leaves no durable WAL record,
//     so the member boots and the leader resends.
//   - T2 (TestSnapDBDirSyncErrorLocalE2E): an injected snap-directory fsync
//     failure is reported loudly, and the member recovers when the leader
//     resends.
//   - T3 (TestSnapDBDirentLostLocalE2E): the test fabricates the post-crash
//     state the fix prevents — a durable WAL snapshot record with the snap.db
//     directory entry lost — by deleting snap.db from the member's data
//     volume. Bootstrap must fail loudly, and the documented remediation
//     (wipe and re-add the member) must restore the cluster. The test passes
//     identically on the fixed and unfixed images, because no local
//     environment can drop the page cache on demand.
//
// The tests are driven by hack/snapdb-e2e.sh and skip unless
// ETCD_INFRA_E2E_CLUSTER, ETCD_INFRA_E2E_PORT, ETCD_INFRA_E2E_GOFAIL_PORT,
// and ETCD_INFRA_E2E_IMAGE are set.
const (
	snapDBGofailContainerPort = 2234
	snapDBGofailEnv           = "GOFAIL_HTTP=0.0.0.0:2234"
	snapDBHelperImage         = "docker.io/library/busybox:latest"
	snapDBSnapshotKeys        = 25 // > snapshot-count(10) + snapshot-catchup-entries(10)
)

type snapDBE2EFixture struct {
	cluster         string
	image           string
	runtime         string
	members         []clusterMember
	endpoints       []string
	gofailFirstPort int
}

func snapDBE2EFixtureFromEnv(t *testing.T) snapDBE2EFixture {
	t.Helper()
	cluster := os.Getenv("ETCD_INFRA_E2E_CLUSTER")
	firstPortText := os.Getenv("ETCD_INFRA_E2E_PORT")
	gofailPortText := os.Getenv("ETCD_INFRA_E2E_GOFAIL_PORT")
	image := os.Getenv("ETCD_INFRA_E2E_IMAGE")
	if cluster == "" || firstPortText == "" || gofailPortText == "" || image == "" {
		t.Skip("set ETCD_INFRA_E2E_CLUSTER, ETCD_INFRA_E2E_PORT, ETCD_INFRA_E2E_GOFAIL_PORT, and ETCD_INFRA_E2E_IMAGE to run the snap.db durability E2E tests")
	}
	firstPort, err := strconv.Atoi(firstPortText)
	require.NoError(t, err)
	gofailFirstPort, err := strconv.Atoi(gofailPortText)
	require.NoError(t, err)
	probeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runtime, err := localContainerRuntime(probeCtx)
	require.NoError(t, err)
	members := localMembers(cluster, 3, firstPort)
	return snapDBE2EFixture{
		cluster:         cluster,
		image:           image,
		runtime:         runtime,
		members:         members,
		endpoints:       memberClientURLs(members),
		gofailFirstPort: gofailFirstPort,
	}
}

func newSnapDBE2EClient(t *testing.T, endpoints []string) *clientv3.Client {
	t.Helper()
	cli, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cli.Close()) })
	return cli
}

// The tests arm failpoints through the GOFAIL_FAILPOINTS container
// environment variable (hack/snapdb-e2e.sh passes it to local up --env).
// Arming at process boot — before any peer traffic — cannot race the
// leader's snapshot stream, which can arrive within milliseconds of boot,
// and the failpoint re-arms automatically when the container restarts.
//
// gofailVerifyActive confirms on the gofail HTTP endpoint that the env-armed
// failpoint is live, so a misconfigured cluster fails fast instead of timing
// out deep in a test.
func gofailVerifyActive(t *testing.T, ctx context.Context, f snapDBE2EFixture, memberIdx int, name, terms string) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/%s", f.gofailFirstPort+memberIdx, name)
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		return strings.Contains(string(body), terms)
	}, 30*time.Second, 100*time.Millisecond, "gofail %s is not active on %s", name, f.members[memberIdx].Name)
}

func gofailClear(t *testing.T, ctx context.Context, f snapDBE2EFixture, memberIdx int, name string) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/%s", f.gofailFirstPort+memberIdx, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	require.Contains(t, []int{http.StatusNoContent, http.StatusOK}, resp.StatusCode, "deactivate gofail %s on %s", name, f.members[memberIdx].Name)
}

func snapDBRuntime(t *testing.T, ctx context.Context, f snapDBE2EFixture, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, f.runtime, args...).CombinedOutput()
	require.NoError(t, err, "%s %s: %s", f.runtime, strings.Join(args, " "), strings.TrimSpace(string(output)))
	return string(output)
}

func snapDBContainerRunning(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) bool {
	t.Helper()
	output, err := exec.CommandContext(ctx, f.runtime, "inspect", "--format", "{{.State.Running}}", member).CombinedOutput()
	require.NoError(t, err, "inspect %s: %s", member, strings.TrimSpace(string(output)))
	return strings.TrimSpace(string(output)) == "true"
}

func snapDBStopMember(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) {
	t.Helper()
	snapDBRuntime(t, ctx, f, "stop", member)
	require.Eventually(t, func() bool { return !snapDBContainerRunning(t, ctx, f, member) },
		30*time.Second, 100*time.Millisecond, "%s never stopped", member)
}

// snapDBStartMember starts a stopped member. It deliberately does not wait
// for a running state: a member that panics on boot (the T3 crash repro)
// exits faster than any poll interval can observe.
func snapDBStartMember(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) {
	t.Helper()
	snapDBRuntime(t, ctx, f, "start", member)
}

func snapDBKillMember(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) {
	t.Helper()
	snapDBRuntime(t, ctx, f, "kill", "--signal", "SIGKILL", member)
	require.Eventually(t, func() bool { return !snapDBContainerRunning(t, ctx, f, member) },
		30*time.Second, 100*time.Millisecond, "%s never died", member)
}

// snapDBVolumeExec runs a shell command with the member's named data volume
// mounted at /data.
func snapDBVolumeExec(t *testing.T, ctx context.Context, f snapDBE2EFixture, member, shellCommand string) string {
	t.Helper()
	return snapDBRuntime(t, ctx, f,
		"run", "--rm",
		"--volume", member+"-data:/data",
		snapDBHelperImage, "sh", "-c", shellCommand,
	)
}

// snapDBFiles lists the committed snap.db files on the member's data volume
// (SaveDBFrom renames a tmp file to %016x.snap.db, so the glob only matches
// completed renames).
func snapDBFiles(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) []string {
	t.Helper()
	out := snapDBVolumeExec(t, ctx, f, member, "ls /data/member/snap/*.snap.db 2>/dev/null || true")
	return strings.Fields(out)
}

// snapDBRemoveSnapDBs deletes the committed snap.db files from the member's
// data volume. This is exactly what a hard machine crash does when the snap
// directory was never fsynced: the kernel drops the un-fsynced directory
// entry, and after reboot the file is gone. A container kill cannot
// reproduce the loss because the page cache survives it.
func snapDBRemoveSnapDBs(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) {
	t.Helper()
	snapDBVolumeExec(t, ctx, f, member, "rm -f /data/member/snap/*.snap.db")
	require.Empty(t, snapDBFiles(t, ctx, f, member), "snap.db files survived removal on %s", member)
}

func snapDBMemberLogs(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, f.runtime, "logs", member).CombinedOutput()
	require.NoError(t, err, "logs %s: %s", member, strings.TrimSpace(string(output)))
	return string(output)
}

func waitForSnapDBLog(t *testing.T, ctx context.Context, f snapDBE2EFixture, member, substr string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Contains(snapDBMemberLogs(t, ctx, f, member), substr)
	}, 90*time.Second, 250*time.Millisecond, "%s never logged %q", member, substr)
}

// driveSnapshotToMember makes the member lag behind the leader's compacted
// log and restarts it, so the leader must stream it a snapshot: stop the
// member, advance the cluster past snapshot-count plus snapshot-catchup
// entries, compact, then start the member again. A container pause does not
// work here: the VM kernel keeps buffering peer TCP traffic for the frozen
// process, and on resume the member can drain the backlog and catch up from
// raft entries, so the leader never sends a snapshot.
func driveSnapshotToMember(t *testing.T, ctx context.Context, f snapDBE2EFixture, cli *clientv3.Client, memberIdx int) {
	t.Helper()
	member := f.members[memberIdx]
	snapDBStopMember(t, ctx, f, member.Name)

	var revision int64
	for i := 0; i < snapDBSnapshotKeys; i++ {
		putCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := cli.Put(putCtx, fmt.Sprintf("/etcd-infra-e2e/snapdb/%s/%06d", member.Name, i), "payload")
		cancel()
		require.NoError(t, err)
		revision = resp.Header.Revision
	}
	compactCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := cli.Compact(compactCtx, revision)
	cancel()
	require.NoError(t, err)

	snapDBStartMember(t, ctx, f, member.Name)
}

func waitForSnapDBFile(t *testing.T, ctx context.Context, f snapDBE2EFixture, member string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(snapDBFiles(t, ctx, f, member)) > 0
	}, 90*time.Second, 200*time.Millisecond, "%s never received a snapshot db", member)
}

// assertSnapDBKVHashEqual waits until every member answers HashKV with the
// same revision and hash, proving the cluster converged on identical data.
func assertSnapDBKVHashEqual(t *testing.T, ctx context.Context, f snapDBE2EFixture, cli *clientv3.Client) {
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
	}, 90*time.Second, 500*time.Millisecond, "cluster members never converged on an equal KV hash")
}

// TestSnapDBReceiveCrashWindowLocalE2E kills a member inside the crash
// window — after the snap.db rename but before SaveDBFrom returns — and
// verifies the member still boots and rejoins. It pins the ordering that
// makes the window safe: the WAL snapshot record is written only after
// SaveDBFrom returns, so a crash inside SaveDBFrom cannot leave a durable
// WAL record pointing at an unconfirmed snap.db.
//
// The snapDBRenameBeforeDirSync failpoint exists only in the fixed build, so
// this test requires the fix image.
func TestSnapDBReceiveCrashWindowLocalE2E(t *testing.T) {
	f := snapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.members) - 1
	target := f.members[targetIdx]

	t.Log("confirm the crash window is armed from process boot (snapDBRenameBeforeDirSync=sleep(30s))")
	gofailVerifyActive(t, ctx, f, targetIdx, "snapDBRenameBeforeDirSync", `sleep("30s")`)

	t.Log("make the member lag and restart it so the leader must stream a snapshot")
	driveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename has happened (the member is inside the crash window)")
	waitForSnapDBFile(t, ctx, f, target.Name)

	t.Log("SIGKILL the member inside the window, between the snap.db rename and the directory fsync")
	snapDBKillMember(t, ctx, f, target.Name)

	t.Log("restart the member; with no WAL snapshot record it must boot cleanly and receive the snapshot again")
	snapDBStartMember(t, ctx, f, target.Name)
	waitForSnapDBLog(t, ctx, f, target.Name, "saved database snapshot to disk")
	assertSnapDBKVHashEqual(t, ctx, f, cli)
	require.NotContains(t, snapDBMemberLogs(t, ctx, f, target.Name), "failed to find database snapshot file")
	t.Logf("%s survived a SIGKILL inside the SaveDBFrom crash window and caught up via resend", target.Name)
}

// TestSnapDBDirSyncErrorLocalE2E injects a failure into the snap-directory
// fsync and verifies that SaveDBFrom reports it instead of silently
// acknowledging an undurable snapshot db, and that the member recovers once
// the failure is removed and the leader resends.
//
// The snapDBDirSyncError failpoint exists only in the fixed build, so this
// test requires the fix image.
func TestSnapDBDirSyncErrorLocalE2E(t *testing.T) {
	f := snapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.members) - 1
	target := f.members[targetIdx]

	t.Log("confirm the fsync failure is armed from process boot (snapDBDirSyncError=return(...))")
	gofailVerifyActive(t, ctx, f, targetIdx, "snapDBDirSyncError", `return("injected snap dir fsync failure")`)

	t.Log("make the member lag and restart it so the leader must stream a snapshot")
	driveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("the leader streams a snapshot, and the injected fsync failure must surface loudly")
	// The injected failpoint returns before the fsyncSnapDir Warn log line,
	// so the signal is the rafthttp handler's error carrying the injected
	// fsync failure message.
	waitForSnapDBLog(t, ctx, f, target.Name, "failed to save incoming database snapshot")
	waitForSnapDBLog(t, ctx, f, target.Name, "injected snap dir fsync failure")

	t.Log("remove the failure for the running process; the leader resends the snapshot and the member catches up")
	// The resend for the same snapshot id takes the idempotent path in
	// SaveDBFrom (the interrupted attempt left the renamed snap.db behind),
	// which fsyncs the directory but skips the "saved database snapshot to
	// disk" log line, so the recovery signal is the rafthttp handler's line.
	gofailClear(t, ctx, f, targetIdx, "snapDBDirSyncError")
	waitForSnapDBLog(t, ctx, f, target.Name, "received and saved database snapshot")
	assertSnapDBKVHashEqual(t, ctx, f, cli)
	t.Logf("%s reported the snap dir fsync failure loudly and recovered via resend", target.Name)
}

// TestSnapDBDirentLostLocalE2E confirms the bug's blast radius end to end.
// It builds the exact post-crash state that the snap-directory fsync fix
// makes unreachable:
//
//  1. A real etcd binary receives a snapshot from the leader over the
//     container network, and SaveDBFrom renames snap.db into place.
//  2. The WAL snapshot record becomes durable: the applyBeforeOpenSnapshot
//     failpoint pauses apply after notifyc.
//  3. The test deletes the snap.db directory entry from the member's named
//     data volume. This is byte-for-byte the state a hard machine crash
//     (power loss, kernel panic) leaves behind when no directory fsync
//     followed the rename: the kernel drops the un-fsynced entry from the
//     page cache. A container kill cannot produce this state — the page
//     cache survives even SIGKILL — so the test deletes the entry on the
//     volume instead of simulating the crash with signals.
//  4. On restart, the real bootstrap path (wal.ValidSnapshotEntries ->
//     RecoverSnapshotBackend) finds the WAL record but not snap.db, and the
//     member panics with the field-report signature "failed to find database
//     snapshot file" (etcd-io/etcd issues #11949, #14497, and #14569 were all
//     closed without a root cause). The failure is loud, not silent, and the
//     member stays down. No data is lost; the member's availability is.
//  5. The documented remediation — MemberRemove, wipe the data volume,
//     MemberAdd, recreate the member with --initial-cluster-state=existing —
//     restores the member, and the cluster converges.
//
// The test passes identically on the fixed and unfixed images: once the
// dirent is lost, no fsync can bring it back. It documents the failure the
// fix prevents and validates the remediation path end to end. The fix-side
// evidence is the companion tests: TestSnapDBReceiveCrashWindowLocalE2E pins
// the ordering (no durable WAL record before SaveDBFrom returns), and
// TestSnapDBDirSyncErrorLocalE2E pins the error propagation (a fsync failure
// is reported and the leader resends).
func TestSnapDBDirentLostLocalE2E(t *testing.T) {
	f := snapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cli := newSnapDBE2EClient(t, f.endpoints)
	targetIdx := len(f.members) - 1
	target := f.members[targetIdx]

	t.Log("confirm apply is armed to pause before consuming the snap.db (applyBeforeOpenSnapshot=sleep(60s))")
	gofailVerifyActive(t, ctx, f, targetIdx, "applyBeforeOpenSnapshot", `sleep("60s")`)

	t.Log("record the target member ID for the later membership repair")
	listCtx, listCancel := context.WithTimeout(ctx, 5*time.Second)
	listResp, err := cli.MemberList(listCtx)
	listCancel()
	require.NoError(t, err)
	var targetMemberID uint64
	for _, m := range listResp.Members {
		if m.Name == target.Name {
			targetMemberID = m.ID
		}
	}
	require.NotZero(t, targetMemberID, "member %s not found in membership", target.Name)

	t.Log("make the member lag and restart it so the leader must stream a snapshot")
	driveSnapshotToMember(t, ctx, f, cli, targetIdx)

	t.Log("wait until the snap.db rename is done and applySnapshot is paused before consuming it")
	waitForSnapDBLog(t, ctx, f, target.Name, "applying snapshot")
	waitForSnapDBFile(t, ctx, f, target.Name)
	// The failpoint sleep dominates the millisecond gap between the "applying
	// snapshot" log line and the WAL snapshot record sync; two seconds in, the
	// member is past notifyc, so the WAL snapshot record is durable.
	time.Sleep(2 * time.Second)

	t.Log("SIGKILL the member, then fabricate the machine crash: the kernel never persisted the rename's directory entry")
	// SIGKILL leaves the page cache (and the dirent) intact. Deleting snap.db
	// from the named volume reproduces the exact on-disk result of a hard
	// crash dropping the un-fsynced directory entry on unfixed code.
	snapDBKillMember(t, ctx, f, target.Name)
	snapDBRemoveSnapDBs(t, ctx, f, target.Name)

	t.Log("restart the member; bootstrap finds the WAL snapshot record, cannot find snap.db, and must fail loudly")
	snapDBStartMember(t, ctx, f, target.Name)
	// The container must exit (no restart policy) with the panic signature
	// from the field reports, and it must stay down: the blast radius is this
	// one member's availability, never cluster data.
	require.Eventually(t, func() bool {
		if snapDBContainerRunning(t, ctx, f, target.Name) {
			return false
		}
		logs := snapDBMemberLogs(t, ctx, f, target.Name)
		return strings.Contains(logs, "failed to find database snapshot file") ||
			strings.Contains(logs, "failed to recover v3 backend from snapshot")
	}, 90*time.Second, 250*time.Millisecond, "%s never panicked on the lost snap.db", target.Name)
	t.Logf("%s failed loudly on the lost snap.db directory entry, as in field reports #11949/#14497/#14569", target.Name)

	t.Log("remediate as documented: wipe the member's data dir and re-add it to the cluster")
	// Repair the membership through the surviving quorum: remove the dead
	// member, wipe its volume, re-add it, and recreate the container with
	// --initial-cluster-state=existing on the same name, ports, and gofail
	// endpoint.
	removeCtx, removeCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = cli.MemberRemove(removeCtx, targetMemberID)
	removeCancel()
	require.NoError(t, err)
	snapDBRuntime(t, ctx, f, "rm", "--force", target.Name)
	snapDBRuntime(t, ctx, f, "volume", "rm", "--force", target.Name+"-data")
	addCtx, addCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = cli.MemberAdd(addCtx, []string{target.PeerURL})
	addCancel()
	require.NoError(t, err)

	command := append([]string{"/usr/local/bin/etcd"}, etcdServerArgs(target, f.members, f.cluster, localprovider.DataDir)...)
	for i, arg := range command {
		if arg == "new" && i > 0 && command[i-1] == "--initial-cluster-state" {
			command[i] = "existing"
		}
	}
	manager := localprovider.New(f.runtime, f.cluster, 0)
	_, err = manager.Create(ctx, compute.NewCreateRequest(
		compute.WithName(target.Name),
		compute.WithImage(f.image),
		compute.WithPortMappings([]compute.PortMapping{{
			ContainerPort: 2379,
			HostPort:      mustAtoi(t, strings.TrimPrefix(target.ClientURL, "http://127.0.0.1:")),
			HostIP:        "127.0.0.1",
		}}),
		compute.WithProviderConfig(localprovider.CreateConfig{
			Command: command,
			Env:     []string{snapDBGofailEnv},
			AuxPortMapping: &compute.PortMapping{
				ContainerPort: snapDBGofailContainerPort,
				HostPort:      f.gofailFirstPort + targetIdx,
				HostIP:        "127.0.0.1",
			},
		}),
	))
	require.NoError(t, err)

	t.Log("the wiped and re-added member must rejoin and converge")
	assertSnapDBKVHashEqual(t, ctx, f, cli)
	t.Logf("%s was wiped and re-added; cluster healthy again", target.Name)
}

func mustAtoi(t *testing.T, text string) int {
	t.Helper()
	value, err := strconv.Atoi(text)
	require.NoError(t, err)
	return value
}
