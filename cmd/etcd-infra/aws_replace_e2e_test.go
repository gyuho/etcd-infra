package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

// AWS machine-replacement E2E tests: the AWS counterpart of the local
// replacement tests. They skip unless ETCD_INFRA_AWS_E2E_CLUSTER names a
// cluster created by "etcd-infra aws up --replaceable" (see
// hack/aws-replace-e2e.sh). Client traffic reaches the members through the
// fixture endpoints (bastion tunnels when the cluster has a bastion); the
// tunnels terminate at the bastion and relay by private IP, so replacing a
// member's machine does not disturb them.

// replaceOutcome carries one finished replacement back to the test.
type replaceOutcome struct {
	instance compute.Instance
	err      error
}

// awsReplaceRequireCluster loads the fixture and skips unless every member
// has a dedicated data volume (created with --replaceable).
func awsReplaceRequireCluster(t *testing.T) awsSnapDBE2EFixture {
	t.Helper()
	f := awsSnapDBE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i, instance := range f.instances {
		volumeID, err := f.manager.DataVolumeID(ctx, instance.ID())
		require.NoError(t, err)
		if volumeID == "" {
			t.Skipf("member %s has no data volume; recreate the cluster with --replaceable", f.state.Instances[i].Name)
		}
	}
	return f
}

// awsReplaceMemberIndexByName resolves a member name to its index in the
// fixture's instance/state slices.
func awsReplaceMemberIndexByName(t *testing.T, f awsSnapDBE2EFixture, name string) int {
	t.Helper()
	for i, instance := range f.state.Instances {
		if instance.Name == name {
			return i
		}
	}
	t.Fatalf("member %q not found in cluster %s", name, f.state.Name)
	return -1
}

// awsReplaceLeaderIndex returns the index of the member currently holding
// leadership, derived from per-member Status plus a MemberList mapping.
func awsReplaceLeaderIndex(t *testing.T, ctx context.Context, f awsSnapDBE2EFixture, cli *clientv3.Client) int {
	t.Helper()
	// Two consistent reads a second apart: a single transitional view could
	// misidentify the leader and replace the wrong machine.
	readLeader := func() uint64 {
		for _, endpoint := range f.endpoints {
			statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			status, err := cli.Status(statusCtx, endpoint)
			cancel()
			if err != nil {
				continue
			}
			return status.Leader
		}
		return 0
	}
	leaderID := readLeader()
	require.NotZero(t, leaderID, "cluster has no leader")
	time.Sleep(time.Second)
	require.Equal(t, leaderID, readLeader(), "leader view unstable")
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	listResp, err := cli.MemberList(listCtx)
	cancel()
	require.NoError(t, err)
	for _, m := range listResp.Members {
		if m.ID == leaderID {
			return awsReplaceMemberIndexByName(t, f, m.Name)
		}
	}
	t.Fatalf("leader id %d not found in membership", leaderID)
	return -1
}

// awsReplaceWaitHealthy waits until every fixture endpoint answers Status.
func awsReplaceWaitHealthy(t *testing.T, ctx context.Context, cli *clientv3.Client, f awsSnapDBE2EFixture) {
	t.Helper()
	for _, endpoint := range f.endpoints {
		awsE2EWaitMemberHealthy(t, ctx, cli, endpoint)
	}
}

// awsReplaceCanary writes and later re-reads a canary key.
func awsReplaceCanaryPut(t *testing.T, ctx context.Context, cli *clientv3.Client, key string) {
	t.Helper()
	putCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, err := cli.Put(putCtx, key, "survived")
	cancel()
	require.NoError(t, err)
}

func awsReplaceCanaryGet(t *testing.T, ctx context.Context, cli *clientv3.Client, key string) {
	t.Helper()
	getCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resp, err := cli.Get(getCtx, key)
	cancel()
	require.NoError(t, err)
	require.Len(t, resp.Kvs, 1, "canary key %s lost across replacement", key)
	require.Equal(t, "survived", string(resp.Kvs[0].Value))
}

// TestAWSReplaceLeaderHandoffAWSE2E replaces the leader's machine: the
// cluster must elect a new leader while the old leader is down, the
// replacement must come back with the same identity (name, private IP) and
// the member's data, and the cluster must converge afterwards.
func TestAWSReplaceLeaderHandoffAWSE2E(t *testing.T) {
	f := awsReplaceRequireCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	awsReplaceWaitHealthy(t, ctx, cli, f)

	canary := fmt.Sprintf("/etcd-infra-e2e/replace/%d", time.Now().UnixNano())
	awsReplaceCanaryPut(t, ctx, cli, canary)

	leaderIdx := awsReplaceLeaderIndex(t, ctx, f, cli)
	leaderName := f.state.Instances[leaderIdx].Name
	leaderMachineID := f.state.Instances[leaderIdx].ID
	leaderIP := f.state.Instances[leaderIdx].PrivateIPv4
	leaderEtcdID := awsE2EMemberID(t, ctx, cli, leaderName)
	t.Logf("replacing leader %s (%s)", leaderName, leaderMachineID)

	statePath, err := awsStatePath(f.state.Name)
	require.NoError(t, err)

	outcome := make(chan replaceOutcome, 1)
	go func() {
		instance, err := awsReplaceMember(ctx, statePath, f.state, leaderIdx, f.manager)
		outcome <- replaceOutcome{instance: instance, err: err}
	}()
	// Join the replacement on every exit path: a failed test must not leave a
	// replacement running into the next test on the shared cluster.
	joined := false
	defer func() {
		if !joined {
			o := <-outcome
			if o.err != nil {
				t.Logf("replacement finished with error after test failure: %v", o.err)
			}
		}
	}()

	// While the leader's machine is down, the surviving quorum must elect a
	// new leader. The old member disappears from the membership's reachability
	// but keeps its seat.
	require.Eventually(t, func() bool {
		for i, endpoint := range f.endpoints {
			if i == leaderIdx {
				continue
			}
			statusCtx, statusCancel := context.WithTimeout(ctx, 5*time.Second)
			status, statusErr := cli.Status(statusCtx, endpoint)
			statusCancel()
			if statusErr == nil && status.Leader != 0 && status.Leader != leaderEtcdID {
				return true
			}
		}
		return false
	}, 3*time.Minute, 3*time.Second, "no new leader elected while leader %s was being replaced", leaderName)
	t.Log("new leader elected during replacement")

	o := <-outcome
	joined = true
	require.NoError(t, o.err, "replacement orchestration failed")

	// The replacement keeps the member's identity. Read the state back from
	// disk instead of relying on the fixture's slice: awsReplaceMember
	// persists the replacement there.
	require.NotEqual(t, leaderMachineID, o.instance.ID(), "machine ID must change")
	require.Equal(t, compute.InstanceStateRunning, o.instance.State())
	require.Equal(t, leaderIP, o.instance.PrivateIPv4(), "private IP must be preserved")
	stateAfter, err := readAWSState(statePath)
	require.NoError(t, err)
	require.Equal(t, leaderName, stateAfter.Instances[leaderIdx].Name)
	require.Equal(t, o.instance.ID(), stateAfter.Instances[leaderIdx].ID)

	awsReplaceWaitHealthy(t, ctx, cli, f)
	awsReplaceCanaryGet(t, ctx, cli, canary)
	assertAWSKVHashEqual(t, ctx, f, cli)
}

// TestAWSReplaceFollowerAWSE2E replaces a follower's machine: the cluster
// must keep serving with its leader unchanged throughout, and the
// replacement must rejoin with its data.
func TestAWSReplaceFollowerAWSE2E(t *testing.T) {
	f := awsReplaceRequireCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	cli := newAWSSnapDBE2EClient(t, f.endpoints)
	awsReplaceWaitHealthy(t, ctx, cli, f)

	canary := fmt.Sprintf("/etcd-infra-e2e/replace/%d", time.Now().UnixNano())
	awsReplaceCanaryPut(t, ctx, cli, canary)

	leaderIdx := awsReplaceLeaderIndex(t, ctx, f, cli)
	followerIdx := -1
	for i := range f.instances {
		if i != leaderIdx {
			followerIdx = i
			break
		}
	}
	if followerIdx == -1 {
		t.Skip("single-member cluster has no follower to replace")
	}
	followerName := f.state.Instances[followerIdx].Name
	leaderMachineID := f.state.Instances[leaderIdx].ID
	leaderEtcdID := awsE2EMemberID(t, ctx, cli, f.state.Instances[leaderIdx].Name)
	t.Logf("replacing follower %s; leader is %s", followerName, f.state.Instances[leaderIdx].Name)

	statePath, err := awsStatePath(f.state.Name)
	require.NoError(t, err)

	outcome := make(chan replaceOutcome, 1)
	go func() {
		instance, err := awsReplaceMember(ctx, statePath, f.state, followerIdx, f.manager)
		outcome <- replaceOutcome{instance: instance, err: err}
	}()
	joined := false
	defer func() {
		if !joined {
			o := <-outcome
			if o.err != nil {
				t.Logf("replacement finished with error after test failure: %v", o.err)
			}
		}
	}()

	// Prove the follower is actually down before measuring service; a write
	// that lands before the termination proves nothing about the outage.
	require.Eventually(t, func() bool {
		statusCtx, statusCancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := cli.Status(statusCtx, f.endpoints[followerIdx])
		statusCancel()
		return err != nil
	}, 3*time.Minute, 2*time.Second, "follower %s never went down", followerName)

	// The cluster must keep serving writes with the same leader for the whole
	// replacement, not just once at the start.
	var o replaceOutcome
	for !joined {
		select {
		case o = <-outcome:
			joined = true
		default:
		}
		if joined {
			break
		}
		putCtx, putCancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := cli.Put(putCtx, canary+"/during", "serving")
		putCancel()
		require.NoError(t, err, "cluster stopped serving writes during follower replacement")
		statusCtx, statusCancel := context.WithTimeout(ctx, 5*time.Second)
		status, err := cli.Status(statusCtx, f.endpoints[leaderIdx])
		statusCancel()
		require.NoError(t, err, "leader unreachable during follower replacement")
		require.Equal(t, leaderEtcdID, status.Leader, "leadership changed during follower replacement")
	}
	require.NoError(t, o.err, "replacement orchestration failed")
	require.Equal(t, leaderMachineID, f.state.Instances[leaderIdx].ID, "leader machine must be untouched")

	awsReplaceWaitHealthy(t, ctx, cli, f)
	awsReplaceCanaryGet(t, ctx, cli, canary)
	assertAWSKVHashEqual(t, ctx, f, cli)
}
