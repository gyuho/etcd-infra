//go:build etcd_infra_custom_client

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"

	etcdclient "git.tbd/etcd-infra/internal/etcd/client"
	localprovider "git.tbd/etcd-infra/pkg/providers/local"
)

const (
	putRPC    = "/etcdserverpb.KV/Put"
	rangeRPC  = "/etcdserverpb.KV/Range"
	statusRPC = "/etcdserverpb.Maintenance/Status"

	leaderReplacementDowntime   = 30 * time.Second
	followerReplacementDowntime = 15 * time.Second
	periodicRefreshCycles       = 4
	periodicRefreshFloor        = 26 * time.Second
	periodicRefreshTimeout      = 45 * time.Second
)

func TestLeaderAwareReplacementE2E(t *testing.T) {
	cluster := os.Getenv("ETCD_INFRA_E2E_CLUSTER")
	firstPortText := os.Getenv("ETCD_INFRA_E2E_PORT")
	if cluster == "" || firstPortText == "" {
		t.Skip("set ETCD_INFRA_E2E_CLUSTER and ETCD_INFRA_E2E_PORT to run the local replacement E2E test")
	}
	firstPort, err := strconv.Atoi(firstPortText)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	members := localMembers(cluster, 3, firstPort)
	endpoints := memberClientURLs(members)
	recorder := newRPCAttemptRecorder()
	logCore, leaderLogs := observer.New(zap.DebugLevel)
	cli, err := etcdclient.New(clientv3.Config{
		Endpoints:       endpoints,
		DialTimeout:     5 * time.Second,
		MaxUnaryRetries: 1,
		Logger:          zap.New(logCore),
		DialOptions:     []grpc.DialOption{grpc.WithStatsHandler(recorder)},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, cli.Close()) }()
	require.Equal(t, "custom", etcdclient.Mode)
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, cli, endpoints, 20*time.Second))

	prefix := fmt.Sprintf("/etcd-infra-e2e/leader-aware/%d", time.Now().UnixNano())
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = cli.Delete(cleanupCtx, prefix, clientv3.WithPrefix())
	}()

	expectedPeers := endpointPeers(t, endpoints)
	require.NoError(t, waitForReadPeers(ctx, cli, recorder, prefix, expectedPeers))
	oldLeader, err := localLeaderName(ctx, cli, members)
	require.NoError(t, err)
	oldLeaderPeer := memberPeer(t, members, oldLeader)
	preRevision, err := waitForLeaderAwarePuts(ctx, cli, recorder, prefix+"/before", oldLeaderPeer)
	require.NoError(t, err)
	_, logCursor, err := waitForLeaderRefresh(ctx, leaderLogs, 0, memberClientURL(t, members, oldLeader))
	require.NoError(t, err)

	runtime, err := localRuntimeForContainer(ctx, oldLeader)
	require.NoError(t, err)
	leaderCanary := writeContainerCanary(t, ctx, runtime, oldLeader)
	leaderReplacement := startLocalReplacement(ctx, cluster, firstPortText, oldLeader, leaderReplacementDowntime)
	defer leaderReplacement.join(t)
	waitAbsentCtx, waitAbsentCancel := context.WithTimeout(ctx, 20*time.Second)
	require.NoError(t, waitForContainerAbsent(waitAbsentCtx, runtime, oldLeader, leaderReplacement))
	waitAbsentCancel()

	leaderOutageCtx, leaderOutageCancel := context.WithTimeout(ctx, 25*time.Second)
	recoveredLeader, duringRevision, recoveryRefresh, nextLogCursor, err := waitForPinnedLeaderChange(
		leaderOutageCtx,
		cli,
		recorder,
		leaderLogs,
		logCursor,
		prefix+"/leader-down",
		memberClientURL(t, members, oldLeader),
		members,
	)
	require.NoError(t, err)
	logCursor = nextLogCursor
	require.Greater(t, duringRevision, preRevision)
	leaderExists, err := inspectLocalContainer(ctx, runtime, oldLeader)
	require.NoError(t, err)
	require.False(t, leaderExists, "old leader was recreated before same-client recovery was proven")
	leaderOutageCancel()
	require.NoError(t, leaderReplacement.wait(ctx))
	assertContainerCanary(t, ctx, runtime, oldLeader, leaderCanary)
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, cli, endpoints, 20*time.Second))
	require.NoError(t, waitForReadPeers(ctx, cli, recorder, prefix, expectedPeers))

	currentLeader, err := localLeaderName(ctx, cli, members)
	require.NoError(t, err)
	require.NotEqual(t, oldLeader, currentLeader)
	currentLeaderPeer := memberPeer(t, members, currentLeader)
	postReplacementRevision, err := waitForLeaderAwarePuts(ctx, cli, recorder, prefix+"/leader-recovered", currentLeaderPeer)
	require.NoError(t, err)
	require.Greater(t, postReplacementRevision, duringRevision)
	lastRefresh := recoveryRefresh
	if currentLeader != recoveredLeader {
		lastRefresh, logCursor, err = waitForLeaderRefresh(
			ctx,
			leaderLogs,
			logCursor,
			memberClientURL(t, members, currentLeader),
		)
		require.NoError(t, err)
	}
	t.Logf("leader replacement recovered on the same client before %s rejoined: leader %s -> %s", oldLeader, oldLeader, currentLeader)

	for cycle := 1; cycle <= periodicRefreshCycles; cycle++ {
		target := memberAfter(t, members, currentLeader)
		statusStart := recorder.count(statusRPC)
		moveCtx, moveCancel := context.WithTimeout(ctx, 10*time.Second)
		err = moveLocalLeader(moveCtx, currentLeader, target, members)
		moveCancel()
		require.NoError(t, err)

		refreshCtx, refreshCancel := context.WithTimeout(ctx, periodicRefreshTimeout)
		refresh, nextCursor, refreshErr := waitForLeaderRefresh(
			refreshCtx,
			leaderLogs,
			logCursor,
			target.ClientURL,
		)
		refreshCancel()
		require.NoError(t, refreshErr)
		refreshElapsed := refresh.at.Sub(lastRefresh.at)
		require.GreaterOrEqual(t, refreshElapsed, periodicRefreshFloor)
		require.NoError(t, validateStatusRound(recorder.attemptsSince(statusRPC, statusStart), expectedPeers))

		periodicRevision, pinErr := requireLeaderAwarePuts(
			ctx,
			cli,
			recorder,
			prefix+"/periodic/"+strconv.Itoa(cycle),
			memberPeer(t, members, target.Name),
			3,
		)
		require.NoError(t, pinErr)
		require.Greater(t, periodicRevision, postReplacementRevision)
		postReplacementRevision = periodicRevision
		currentLeader = target.Name
		currentLeaderPeer = memberPeer(t, members, currentLeader)
		lastRefresh = refresh
		logCursor = nextCursor
		t.Logf("periodic refresh cycle %d routed the next write to leader %s after %s", cycle, currentLeader, refreshElapsed)
	}

	follower := followerOtherThan(t, members, currentLeader, oldLeader)
	followerRuntime, err := localRuntimeForContainer(ctx, follower.Name)
	require.NoError(t, err)
	followerCanary := writeContainerCanary(t, ctx, followerRuntime, follower.Name)
	followerReplacement := startLocalReplacement(ctx, cluster, firstPortText, follower.Name, followerReplacementDowntime)
	defer followerReplacement.join(t)
	waitFollowerCtx, waitFollowerCancel := context.WithTimeout(ctx, 20*time.Second)
	require.NoError(t, waitForContainerAbsent(waitFollowerCtx, followerRuntime, follower.Name, followerReplacement))
	waitFollowerCancel()

	followerOutageCtx, followerOutageCancel := context.WithTimeout(ctx, 6*time.Second)
	require.NoError(t, verifySurvivingLeader(followerOutageCtx, members, follower.Name, currentLeader))
	followerRevision, err := requireLeaderAwarePuts(
		followerOutageCtx,
		cli,
		recorder,
		prefix+"/follower-down",
		currentLeaderPeer,
		3,
	)
	require.NoError(t, err)
	require.Greater(t, followerRevision, postReplacementRevision)
	followerExists, err := inspectLocalContainer(ctx, followerRuntime, follower.Name)
	require.NoError(t, err)
	require.False(t, followerExists, "follower was recreated before the unaffected write window was proven")
	followerOutageCancel()
	require.NoError(t, followerReplacement.wait(ctx))
	assertContainerCanary(t, ctx, followerRuntime, follower.Name, followerCanary)
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, cli, endpoints, 20*time.Second))
	require.NoError(t, waitForReadPeers(ctx, cli, recorder, prefix, expectedPeers))

	finalLeader, err := localLeaderName(ctx, cli, members)
	require.NoError(t, err)
	finalRevision, err := waitForLeaderAwarePuts(
		ctx,
		cli,
		recorder,
		prefix+"/after",
		memberPeer(t, members, finalLeader),
	)
	require.NoError(t, err)
	require.Greater(t, finalRevision, followerRevision)

	value := fmt.Sprintf("leader=%s revision=%d", finalLeader, finalRevision)
	_, err = cli.Put(ctx, prefix+"/sentinel", value)
	require.NoError(t, err)
	response, err := cli.Get(ctx, prefix+"/sentinel")
	require.NoError(t, err)
	require.Len(t, response.Kvs, 1)
	require.Equal(t, value, string(response.Kvs[0].Value))
	t.Logf("follower replacement preserved leader-aware writes on %s; final leader %s", currentLeader, finalLeader)
}

type replacementFuture struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

func startLocalReplacement(ctx context.Context, cluster, firstPort, member string, downtime time.Duration) *replacementFuture {
	replacementCtx, cancel := context.WithCancel(ctx)
	replacement := &replacementFuture{done: make(chan struct{}), cancel: cancel}
	go func() {
		replacement.err = runLocalReplace(replacementCtx, []string{
			"--name", cluster,
			"--members", "3",
			"--port", firstPort,
			"--member", member,
			"--downtime", downtime.String(),
		})
		close(replacement.done)
	}()
	return replacement
}

func (replacement *replacementFuture) wait(ctx context.Context) error {
	select {
	case <-replacement.done:
		return replacement.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (replacement *replacementFuture) join(t *testing.T) {
	t.Helper()
	replacement.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	if err := replacement.wait(ctx); err != nil {
		t.Errorf("join local replacement: %v", err)
	}
}

func waitForContainerAbsent(ctx context.Context, runtime, member string, replacement *replacementFuture) error {
	for {
		exists, err := inspectLocalContainer(ctx, runtime, member)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		select {
		case <-replacement.done:
			if replacement.err != nil {
				return fmt.Errorf("replacement failed before %s was observed absent: %w", member, replacement.err)
			}
			return fmt.Errorf("replacement completed before %s was observed absent", member)
		case <-ctx.Done():
			return fmt.Errorf("observe %s absent: %w", member, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func inspectLocalContainer(ctx context.Context, runtime, member string) (bool, error) {
	output, err := exec.CommandContext(ctx, runtime, "inspect", member).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	message := strings.ToLower(string(output))
	if strings.Contains(message, "no such object") ||
		strings.Contains(message, "no such container") ||
		strings.Contains(message, "no container with name or id") {
		return false, nil
	}
	return false, fmt.Errorf("inspect local container %s: %w: %s", member, err, strings.TrimSpace(string(output)))
}

func waitForPinnedLeaderChange(
	ctx context.Context,
	cli *clientv3.Client,
	recorder *rpcAttemptRecorder,
	logs *observer.ObservedLogs,
	logCursor int,
	key, oldLeaderEndpoint string,
	members []clusterMember,
) (string, int64, leaderRefreshEvent, int, error) {
	var candidate clusterMember
	candidatePeer := ""
	var refresh leaderRefreshEvent
	consecutive := 0
	lastPeer := ""
	var lastCallErr error
	for attempt := 1; ; attempt++ {
		entries := logs.All()
		for logCursor < len(entries) {
			entry := entries[logCursor]
			logCursor++
			endpoint, _ := entry.ContextMap()["endpoint"].(string)
			if entry.Message != "refreshed etcd leader" || endpoint == "" {
				continue
			}
			consecutive = 0
			if endpoint == oldLeaderEndpoint {
				candidate = clusterMember{}
				candidatePeer = ""
				refresh = leaderRefreshEvent{}
				continue
			}
			member, ok := memberByClientURL(members, endpoint)
			if !ok {
				return "", 0, leaderRefreshEvent{}, logCursor, fmt.Errorf("leader refresh selected unknown endpoint %s", endpoint)
			}
			parsed, err := url.Parse(endpoint)
			if err != nil {
				return "", 0, leaderRefreshEvent{}, logCursor, fmt.Errorf("parse leader endpoint %s: %w", endpoint, err)
			}
			candidate = member
			candidatePeer = parsed.Host
			refresh = leaderRefreshEvent{at: entry.Time}
		}

		probe, observeErr := probePut(ctx, cli, recorder, key, strconv.Itoa(attempt))
		if observeErr != nil {
			return "", 0, leaderRefreshEvent{}, logCursor, observeErr
		}
		lastPeer = probe.peer
		lastCallErr = probe.callErr
		if candidate.Name != "" && probe.callErr == nil && probe.peer == candidatePeer {
			consecutive++
			if consecutive == 3 {
				return candidate.Name, probe.revision, refresh, logCursor, nil
			}
		} else {
			consecutive = 0
		}
		if err := waitPoll(ctx, 100*time.Millisecond); err != nil {
			return "", 0, leaderRefreshEvent{}, logCursor, fmt.Errorf(
				"observe leader-aware routing to the leader after replacing %s: %w (candidate %s, last peer %q, last call error %v)",
				oldLeaderEndpoint,
				err,
				candidate.Name,
				lastPeer,
				lastCallErr,
			)
		}
	}
}

func moveLocalLeader(ctx context.Context, currentLeader string, target clusterMember, members []clusterMember) error {
	currentURL := memberClientURLByName(members, currentLeader)
	if currentURL == "" {
		return fmt.Errorf("current leader %s is not a local member", currentLeader)
	}
	raw, err := clientv3.New(clientv3.Config{
		Endpoints:       []string{currentURL},
		DialTimeout:     5 * time.Second,
		MaxUnaryRetries: 1,
	})
	if err != nil {
		return fmt.Errorf("create direct leader client: %w", err)
	}
	defer func() { _ = raw.Close() }()

	targetStatus, err := raw.Status(ctx, target.ClientURL)
	if err != nil {
		return fmt.Errorf("status leadership target %s: %w", target.Name, err)
	}
	if targetStatus.Header == nil || targetStatus.Header.MemberId == 0 {
		return fmt.Errorf("leadership target %s has no member ID", target.Name)
	}
	targetID := targetStatus.Header.MemberId
	if _, err := raw.MoveLeader(ctx, targetID); err != nil {
		return fmt.Errorf("move leader %s -> %s: %w", currentLeader, target.Name, err)
	}
	for {
		currentStatus, currentErr := raw.Status(ctx, currentURL)
		targetStatus, targetErr := raw.Status(ctx, target.ClientURL)
		if currentErr == nil && targetErr == nil && targetStatus.Header != nil &&
			currentStatus.Leader == targetID && targetStatus.Leader == targetID && targetStatus.Header.MemberId == targetID {
			return nil
		}
		if err := waitPoll(ctx, 50*time.Millisecond); err != nil {
			return fmt.Errorf("confirm leader %s -> %s: %w", currentLeader, target.Name, err)
		}
	}
}

func verifySurvivingLeader(ctx context.Context, members []clusterMember, down, expectedLeader string) error {
	endpoints := make([]string, 0, len(members)-1)
	for _, member := range members {
		if member.Name != down {
			endpoints = append(endpoints, member.ClientURL)
		}
	}
	raw, err := clientv3.New(clientv3.Config{
		Endpoints:       endpoints,
		DialTimeout:     5 * time.Second,
		MaxUnaryRetries: 1,
	})
	if err != nil {
		return fmt.Errorf("create surviving-member client: %w", err)
	}
	defer func() { _ = raw.Close() }()

	var leaderID uint64
	for _, member := range members {
		if member.Name == down {
			continue
		}
		status, err := raw.Status(ctx, member.ClientURL)
		if err != nil {
			return fmt.Errorf("status surviving member %s: %w", member.Name, err)
		}
		if status.Header == nil || status.Leader == 0 {
			return fmt.Errorf("surviving member %s has no elected leader", member.Name)
		}
		if leaderID == 0 {
			leaderID = status.Leader
		} else if status.Leader != leaderID {
			return fmt.Errorf("surviving members disagree on leader: %x != %x", status.Leader, leaderID)
		}
		if member.Name == expectedLeader && status.Header.MemberId != status.Leader {
			return fmt.Errorf("expected leader %s no longer reports itself as leader", expectedLeader)
		}
	}
	return nil
}

type containerCanary struct {
	path  string
	value string
}

func writeContainerCanary(t *testing.T, ctx context.Context, runtime, member string) containerCanary {
	t.Helper()
	canary := containerCanary{
		path:  localprovider.DataDir + "/.etcd-infra-replacement-canary",
		value: fmt.Sprintf("%s/%d", member, time.Now().UnixNano()),
	}
	source := filepath.Join(t.TempDir(), "before")
	require.NoError(t, os.WriteFile(source, []byte(canary.value), 0o600))
	output, err := exec.CommandContext(ctx, runtime, "cp", source, member+":"+canary.path).CombinedOutput()
	require.NoErrorf(t, err, "copy canary into %s data volume: %s", member, output)
	return canary
}

func assertContainerCanary(t *testing.T, ctx context.Context, runtime, member string, canary containerCanary) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "after")
	output, err := exec.CommandContext(ctx, runtime, "cp", member+":"+canary.path, destination).CombinedOutput()
	require.NoErrorf(t, err, "copy canary from %s data volume: %s", member, output)
	value, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, canary.value, string(value))
}

func waitForReadPeers(ctx context.Context, cli *clientv3.Client, recorder *rpcAttemptRecorder, key string, expected map[string]struct{}) error {
	seen := make(map[string]struct{}, len(expected))
	for {
		peer, callErr, observeErr := probeRange(ctx, cli, recorder, key)
		if observeErr != nil {
			return observeErr
		}
		if callErr == nil {
			if _, ok := expected[peer]; !ok {
				return fmt.Errorf("read selected unexpected peer %q", peer)
			}
			seen[peer] = struct{}{}
			if len(seen) == len(expected) {
				return nil
			}
		}
		if err := waitPoll(ctx, 50*time.Millisecond); err != nil {
			return fmt.Errorf("observe all read peers: %w", err)
		}
	}
}

func waitForLeaderAwarePuts(ctx context.Context, cli *clientv3.Client, recorder *rpcAttemptRecorder, key, leaderPeer string) (int64, error) {
	consecutive := 0
	lastPeer := ""
	var lastCallErr error
	for attempt := 1; ; attempt++ {
		probe, observeErr := probePut(ctx, cli, recorder, key, strconv.Itoa(attempt))
		if observeErr != nil {
			return 0, observeErr
		}
		if probe.callErr == nil && probe.peer == leaderPeer {
			consecutive++
			if consecutive == 3 {
				return probe.revision, nil
			}
		} else {
			consecutive = 0
		}
		lastPeer = probe.peer
		lastCallErr = probe.callErr
		if err := waitPoll(ctx, 100*time.Millisecond); err != nil {
			return 0, fmt.Errorf(
				"observe leader-aware writes to %s: %w (last peer %q, last call error %v)",
				leaderPeer,
				err,
				lastPeer,
				lastCallErr,
			)
		}
	}
}

func requireLeaderAwarePuts(ctx context.Context, cli *clientv3.Client, recorder *rpcAttemptRecorder, key, leaderPeer string, count int) (int64, error) {
	var revision int64
	for attempt := 1; attempt <= count; attempt++ {
		probe, observeErr := probePut(ctx, cli, recorder, key, strconv.Itoa(attempt))
		if observeErr != nil {
			return 0, observeErr
		}
		if probe.callErr != nil {
			return 0, fmt.Errorf("put %d/%d failed: %w", attempt, count, probe.callErr)
		}
		if probe.peer != leaderPeer {
			return 0, fmt.Errorf("put %d/%d selected %s, want leader %s", attempt, count, probe.peer, leaderPeer)
		}
		revision = probe.revision
	}
	return revision, nil
}

type putProbe struct {
	revision int64
	peer     string
	callErr  error
}

func probePut(ctx context.Context, cli *clientv3.Client, recorder *rpcAttemptRecorder, key, value string) (putProbe, error) {
	putCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	label := key + "\x00" + value
	putCtx = context.WithValue(putCtx, rpcLabelKey{}, label)
	response, callErr := cli.Put(putCtx, key, value)
	cancel()
	attempts := recorder.attemptsForLabel(putRPC, label)
	if len(attempts) != 1 {
		return putProbe{}, fmt.Errorf("put used %d transport attempts, want exactly 1", len(attempts))
	}
	attempt := attempts[0]
	if attempt.transparent {
		return putProbe{}, fmt.Errorf("put used a transparent gRPC retry")
	}
	probe := putProbe{peer: attempt.peer, callErr: callErr}
	if callErr == nil {
		if attempt.err != nil {
			return putProbe{}, fmt.Errorf("put returned success but transport ended with %v", attempt.err)
		}
		if response == nil || response.Header == nil {
			return putProbe{}, fmt.Errorf("put returned no response header")
		}
		probe.revision = response.Header.Revision
	}
	return probe, nil
}

func probeRange(ctx context.Context, cli *clientv3.Client, recorder *rpcAttemptRecorder, key string) (string, error, error) {
	start := recorder.count(rangeRPC)
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, callErr := cli.Get(readCtx, key, clientv3.WithSerializable())
	cancel()
	attempt, err := recorder.oneSince(rangeRPC, start)
	if err != nil {
		return "", nil, err
	}
	if attempt.transparent {
		return "", nil, fmt.Errorf("range used a transparent gRPC retry")
	}
	if callErr == nil && attempt.err != nil {
		return "", nil, fmt.Errorf("range returned success but transport ended with %v", attempt.err)
	}
	return attempt.peer, callErr, nil
}

func waitPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type leaderRefreshEvent struct {
	at time.Time
}

func waitForLeaderRefresh(ctx context.Context, logs *observer.ObservedLogs, cursor int, endpoint string) (leaderRefreshEvent, int, error) {
	for {
		entries := logs.All()
		for cursor < len(entries) {
			entry := entries[cursor]
			cursor++
			if entry.Message == "refreshed etcd leader" && entry.ContextMap()["endpoint"] == endpoint {
				return leaderRefreshEvent{at: entry.Time}, cursor, nil
			}
		}
		if err := waitPoll(ctx, 25*time.Millisecond); err != nil {
			return leaderRefreshEvent{}, cursor, fmt.Errorf("observe leader refresh for %s: %w", endpoint, err)
		}
	}
}

func validateStatusRound(attempts []rpcAttempt, expected map[string]struct{}) error {
	counts := make(map[string]int, len(expected))
	for _, attempt := range attempts {
		if attempt.transparent {
			return fmt.Errorf("leader Status probe to %s used a transparent retry", attempt.peer)
		}
		if attempt.err != nil {
			return fmt.Errorf("leader Status probe to %s failed: %w", attempt.peer, attempt.err)
		}
		if _, ok := expected[attempt.peer]; !ok {
			return fmt.Errorf("leader Status probe selected unexpected peer %q", attempt.peer)
		}
		counts[attempt.peer]++
	}
	for peer := range expected {
		if counts[peer] != 1 {
			return fmt.Errorf("leader Status probe count for %s is %d, want 1", peer, counts[peer])
		}
	}
	return nil
}

func endpointPeers(t *testing.T, endpoints []string) map[string]struct{} {
	t.Helper()
	peers := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		peers[endpointPeer(t, endpoint)] = struct{}{}
	}
	return peers
}

func memberPeer(t *testing.T, members []clusterMember, name string) string {
	t.Helper()
	return endpointPeer(t, memberClientURL(t, members, name))
}

func memberClientURL(t *testing.T, members []clusterMember, name string) string {
	t.Helper()
	if endpoint := memberClientURLByName(members, name); endpoint != "" {
		return endpoint
	}
	t.Fatalf("member %s not found", name)
	return ""
}

func memberClientURLByName(members []clusterMember, name string) string {
	for _, member := range members {
		if member.Name == name {
			return member.ClientURL
		}
	}
	return ""
}

func memberByClientURL(members []clusterMember, endpoint string) (clusterMember, bool) {
	for _, member := range members {
		if member.ClientURL == endpoint {
			return member, true
		}
	}
	return clusterMember{}, false
}

func memberAfter(t *testing.T, members []clusterMember, name string) clusterMember {
	t.Helper()
	for i, member := range members {
		if member.Name == name {
			return members[(i+1)%len(members)]
		}
	}
	t.Fatalf("member %s not found", name)
	return clusterMember{}
}

func followerOtherThan(t *testing.T, members []clusterMember, leader, replaced string) clusterMember {
	t.Helper()
	for _, member := range members {
		if member.Name != leader && member.Name != replaced {
			return member
		}
	}
	t.Fatalf("no follower differs from leader %s and replaced member %s", leader, replaced)
	return clusterMember{}
}

func endpointPeer(t *testing.T, endpoint string) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	return parsed.Host
}

type rpcAttemptKey struct{}

type rpcLabelKey struct{}

type rpcAttempt struct {
	method      string
	label       string
	peer        string
	transparent bool
	err         error
}

type rpcAttemptRecorder struct {
	mu       sync.Mutex
	attempts map[string][]rpcAttempt
}

func newRPCAttemptRecorder() *rpcAttemptRecorder {
	return &rpcAttemptRecorder{attempts: make(map[string][]rpcAttempt)}
}

func (r *rpcAttemptRecorder) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	label, _ := ctx.Value(rpcLabelKey{}).(string)
	return context.WithValue(ctx, rpcAttemptKey{}, &rpcAttempt{method: info.FullMethodName, label: label})
}

func (r *rpcAttemptRecorder) HandleRPC(ctx context.Context, rpcStats stats.RPCStats) {
	attempt, _ := ctx.Value(rpcAttemptKey{}).(*rpcAttempt)
	if attempt == nil || !rpcStats.IsClient() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch event := rpcStats.(type) {
	case *stats.Begin:
		attempt.transparent = event.IsTransparentRetryAttempt
	case *stats.OutHeader:
		if event.RemoteAddr != nil {
			attempt.peer = event.RemoteAddr.String()
		}
	case *stats.End:
		attempt.err = event.Error
		r.attempts[attempt.method] = append(r.attempts[attempt.method], *attempt)
	}
}

func (*rpcAttemptRecorder) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (*rpcAttemptRecorder) HandleConn(context.Context, stats.ConnStats) {}

func (r *rpcAttemptRecorder) count(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.attempts[method])
}

func (r *rpcAttemptRecorder) attemptsSince(method string, index int) []rpcAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rpcAttempt(nil), r.attempts[method][index:]...)
}

func (r *rpcAttemptRecorder) attemptsForLabel(method, label string) []rpcAttempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	var matches []rpcAttempt
	for _, attempt := range r.attempts[method] {
		if attempt.label == label {
			matches = append(matches, attempt)
		}
	}
	return matches
}

func (r *rpcAttemptRecorder) oneSince(method string, index int) (rpcAttempt, error) {
	attempts := r.attemptsSince(method, index)
	if len(attempts) != 1 {
		return rpcAttempt{}, fmt.Errorf("%s used %d transport attempts, want exactly 1", method, len(attempts))
	}
	return attempts[0], nil
}
