//go:build etcd_infra_custom_client

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	performanceRequestCount          = 720
	performancePairInterval          = 50 * time.Millisecond
	performancePutTimeout            = 2 * time.Second
	performanceP95Guardrail          = 50
	performancePeerRequestCount      = 256
	performancePeerBlockRequestCount = performancePeerRequestCount / 2
	performancePeerPayloadBytes      = 64 << 10
	performancePeerTrafficMaxPercent = 85

	reliabilityPutInterval          = 50 * time.Millisecond
	reliabilityPutTimeout           = 750 * time.Millisecond
	followerPauseDuration           = 5 * time.Second
	leaderElectionTimeout           = 15 * time.Second
	leaderRecoveryObservationWindow = 50 * time.Second
	leaderRediscoveryUpperBound     = 3 * time.Second
	leaderAwareObservationWindow    = 5 * time.Second
	leaderAwareProofTimeout         = 5 * time.Second
	// The immediate request and timeout-spaced cohort can still be followed by
	// one round_robin fallback pick to the paused old leader.
	leaderStaleHintBurstBudget = 2 + int(reliabilityPutTimeout/reliabilityPutInterval)
)

func TestLeaderAwarePerformanceE2E(t *testing.T) {
	fixture := localE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	clients := newBalancerTestClients(t, fixture.endpoints, 1)
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, clients.oracle, fixture.endpoints, 20*time.Second))

	prefix := fmt.Sprintf("/etcd-infra-e2e/leader-aware-performance/%d", time.Now().UnixNano())
	cleanupE2EPrefix(t, clients.oracle, prefix)
	leadership, err := waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	leader := leadership.name
	leaderPeer := memberPeer(t, fixture.members, leader)
	expectedPeers := endpointPeers(t, fixture.endpoints)
	require.NoError(t, waitForPutPeers(ctx, clients.roundRobin, clients.roundRobinRecorder, prefix+"/warmup", expectedPeers))
	_, err = waitForLeaderAwarePuts(ctx, clients.leaderAware, clients.leaderAwareRecorder, prefix+"/warmup-leader-aware", leaderPeer)
	require.NoError(t, err)
	_, _, err = waitForLeaderRefresh(ctx, clients.leaderAwareLogs, 0, memberClientURL(t, fixture.members, leader))
	require.NoError(t, err)

	statusStart := clients.leaderAwareRecorder.count(statusRPC)
	runStart := time.Now()
	roundRobinSamples := make([]performancePutSample, 0, performanceRequestCount)
	leaderAwareSamples := make([]performancePutSample, 0, performanceRequestCount)
	for index := 0; index < performanceRequestCount; index++ {
		require.NoError(t, waitPoll(ctx, performancePairInterval))
		if index%2 == 0 {
			roundRobinSamples = append(roundRobinSamples, measurePut(
				t,
				ctx,
				clients.roundRobin,
				clients.roundRobinRecorder,
				fmt.Sprintf("%s/load/round-robin/%06d", prefix, index),
			))
			leaderAwareSamples = append(leaderAwareSamples, measurePut(
				t,
				ctx,
				clients.leaderAware,
				clients.leaderAwareRecorder,
				fmt.Sprintf("%s/load/leader-aware/%06d", prefix, index),
			))
			continue
		}
		leaderAwareSamples = append(leaderAwareSamples, measurePut(
			t,
			ctx,
			clients.leaderAware,
			clients.leaderAwareRecorder,
			fmt.Sprintf("%s/load/leader-aware/%06d", prefix, index),
		))
		roundRobinSamples = append(roundRobinSamples, measurePut(
			t,
			ctx,
			clients.roundRobin,
			clients.roundRobinRecorder,
			fmt.Sprintf("%s/load/round-robin/%06d", prefix, index),
		))
	}
	require.Len(t, roundRobinSamples, performanceRequestCount)
	require.Len(t, leaderAwareSamples, performanceRequestCount)
	roundRobinPeers := performancePeerCounts(roundRobinSamples)
	leaderAwarePeers := performancePeerCounts(leaderAwareSamples)
	for peer := range expectedPeers {
		require.Equal(
			t,
			performanceRequestCount/len(expectedPeers),
			roundRobinPeers[peer],
			"round_robin distribution for %s",
			peer,
		)
	}
	require.Equal(t, performanceRequestCount, leaderAwarePeers[leaderPeer])
	roundRobinFollowerAttempts := performanceRequestCount - roundRobinPeers[leaderPeer]
	require.Positive(t, roundRobinFollowerAttempts, "round_robin did not exercise a follower first hop")

	// A kube-apiserver using this clientv3 balancer sends storage mutations
	// directly to the leader. A round-robin first hop through a follower makes
	// etcd carry the proposal to the leader before normal Raft replication.
	// Leader-aware routing cannot remove quorum replication; it removes that redundant,
	// payload-sized peer copy, reducing etcd peer NIC/serialization work even
	// when same-host latency barely changes. Any cross-zone savings remain
	// topology-dependent and are deliberately outside this local assertion.
	payload := strings.Repeat("x", performancePeerPayloadBytes)
	// The palindrome order reduces bias from gradual changes in cluster state or
	// fixed heartbeat traffic across the measurement window.
	trafficBlocks := []struct {
		name        string
		cli         *clientv3.Client
		recorder    *rpcAttemptRecorder
		leaderAware bool
	}{
		{name: "round-robin-a", cli: clients.roundRobin, recorder: clients.roundRobinRecorder},
		{name: "leader-aware-a", cli: clients.leaderAware, recorder: clients.leaderAwareRecorder, leaderAware: true},
		{name: "leader-aware-b", cli: clients.leaderAware, recorder: clients.leaderAwareRecorder, leaderAware: true},
		{name: "round-robin-b", cli: clients.roundRobin, recorder: clients.roundRobinRecorder},
	}
	var roundRobinPeerBytes, leaderAwarePeerBytes float64
	var roundRobinTrafficSamples, leaderAwareTrafficSamples []performancePutSample
	for _, block := range trafficBlocks {
		result := measurePeerTrafficBlock(
			t,
			ctx,
			fixture.endpoints,
			clients.oracle,
			block.cli,
			block.recorder,
			fmt.Sprintf("%s/peer-traffic/%s", prefix, block.name),
			payload,
		)
		if block.leaderAware {
			leaderAwarePeerBytes += result.peerSentBytes
			leaderAwareTrafficSamples = append(leaderAwareTrafficSamples, result.samples...)
			requireAllPerformancePeers(t, result.samples, leaderPeer)
		} else {
			roundRobinPeerBytes += result.peerSentBytes
			roundRobinTrafficSamples = append(roundRobinTrafficSamples, result.samples...)
			requireBalancedPerformancePeers(t, result.samples, expectedPeers)
		}
		t.Logf(
			"peer-traffic block %s: %d puts x %d bytes in %s sent %.0f peer bytes (%.0f bytes/put)",
			block.name,
			len(result.samples),
			len(payload),
			result.elapsed,
			result.peerSentBytes,
			result.peerSentBytes/float64(len(result.samples)),
		)
		blockLeadership, err := observeLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
		require.NoError(t, err)
		require.Equal(t, leadership, blockLeadership, "leadership or Raft term changed during %s", block.name)
	}
	require.Len(t, roundRobinTrafficSamples, performancePeerRequestCount)
	require.Len(t, leaderAwareTrafficSamples, performancePeerRequestCount)
	requireBalancedPerformancePeers(t, roundRobinTrafficSamples, expectedPeers)
	requireAllPerformancePeers(t, leaderAwareTrafficSamples, leaderPeer)
	roundRobinTrafficPeers := performancePeerCounts(roundRobinTrafficSamples)
	roundRobinTrafficFollowerAttempts := len(roundRobinTrafficSamples) - roundRobinTrafficPeers[leaderPeer]
	roundRobinPeerBytesPerPut := roundRobinPeerBytes / float64(len(roundRobinTrafficSamples))
	leaderAwarePeerBytesPerPut := leaderAwarePeerBytes / float64(len(leaderAwareTrafficSamples))

	// Three voters require two replication copies. Uniform round_robin adds one
	// follower-to-leader proposal copy on 2/3 of PUTs: 2 versus 8/3 payload
	// copies, an ideal leader-aware ratio of 75%. The 85% ceiling leaves room for
	// fixed Raft heartbeats and protobuf framing while requiring a material gain.
	require.LessOrEqual(
		t,
		leaderAwarePeerBytesPerPut,
		roundRobinPeerBytesPerPut*performancePeerTrafficMaxPercent/100,
		"leader-aware routing did not reduce peer-sent bytes per PUT by at least %d%%",
		100-performancePeerTrafficMaxPercent,
	)
	peerTrafficReduction := 100 * (roundRobinPeerBytesPerPut - leaderAwarePeerBytesPerPut) / roundRobinPeerBytesPerPut

	statusAttempts, statusRounds, err := waitForCompleteStatusRounds(
		ctx,
		clients.leaderAwareRecorder,
		statusStart,
		expectedPeers,
	)
	require.NoError(t, err)
	periodicStatusAttempts := len(statusAttempts)
	leadershipAfter, err := waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	require.Equal(t, leadership, leadershipAfter, "leadership or Raft term changed during the performance comparison")

	roundRobinLatencies := performanceLatencies(roundRobinSamples)
	leaderAwareLatencies := performanceLatencies(leaderAwareSamples)
	roundRobinMean := sumDurations(roundRobinLatencies) / time.Duration(len(roundRobinLatencies))
	leaderAwareMean := sumDurations(leaderAwareLatencies) / time.Duration(len(leaderAwareLatencies))
	roundRobinP95 := latencyPercentile(roundRobinLatencies, 95)
	leaderAwareP95 := latencyPercentile(leaderAwareLatencies, 95)
	require.Positive(t, roundRobinP95)
	require.Positive(t, leaderAwareP95)
	require.LessOrEqual(
		t,
		leaderAwareP95,
		roundRobinP95+roundRobinP95*performanceP95Guardrail/100,
		"leader-aware routing exceeded the %d%% local p95 non-regression guardrail",
		performanceP95Guardrail,
	)
	latencyGain := 0.0
	if roundRobinP95 > 0 {
		latencyGain = 100 * float64(roundRobinP95-leaderAwareP95) / float64(roundRobinP95)
	}
	meanLatencyGain := 100 * float64(roundRobinMean-leaderAwareMean) / float64(roundRobinMean)
	t.Logf(
		"ordinary-local performance over %s in stable Raft term %d: round_robin=%d puts mean=%s p50=%s p95=%s p99=%s; leader-aware=%d puts mean=%s p50=%s p95=%s p99=%s; mean latency delta=%.1f%%, p95 delta=%.1f%%; peer traffic=%.0f vs %.0f bytes/put (%.1f%% reduction, %d traffic-arm follower first hops avoided); latency-arm follower first hops avoided=%d; Status probes=%d across %d complete rounds (%.4f/leader-aware put)",
		time.Since(runStart),
		leadership.term,
		len(roundRobinSamples),
		roundRobinMean,
		latencyPercentile(roundRobinLatencies, 50),
		roundRobinP95,
		latencyPercentile(roundRobinLatencies, 99),
		len(leaderAwareSamples),
		leaderAwareMean,
		latencyPercentile(leaderAwareLatencies, 50),
		leaderAwareP95,
		latencyPercentile(leaderAwareLatencies, 99),
		meanLatencyGain,
		latencyGain,
		roundRobinPeerBytesPerPut,
		leaderAwarePeerBytesPerPut,
		peerTrafficReduction,
		roundRobinTrafficFollowerAttempts,
		roundRobinFollowerAttempts,
		periodicStatusAttempts,
		statusRounds,
		float64(periodicStatusAttempts)/float64(len(leaderAwareSamples)+len(leaderAwareTrafficSamples)),
	)
}

func TestLeaderAwareReliabilityE2E(t *testing.T) {
	// Leader-aware routing changes mutating unary RPCs only. This test therefore measures
	// mutation availability and data safety; streaming reads retain round_robin.
	fixture := localE2EFixtureFromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	clients := newBalancerTestClients(t, fixture.endpoints, 1)
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, clients.oracle, fixture.endpoints, 20*time.Second))

	prefix := fmt.Sprintf("/etcd-infra-e2e/leader-aware-reliability/%d", time.Now().UnixNano())
	cleanupE2EPrefix(t, clients.oracle, prefix)
	initialLeadership, err := waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	initialLeader := initialLeadership.name
	initialLeaderPeer := memberPeer(t, fixture.members, initialLeader)
	require.NoError(t, waitForPutPeers(
		ctx,
		clients.roundRobin,
		clients.roundRobinRecorder,
		prefix+"/warmup",
		endpointPeers(t, fixture.endpoints),
	))
	_, err = waitForLeaderAwarePuts(ctx, clients.leaderAware, clients.leaderAwareRecorder, prefix+"/warmup-leader-aware", initialLeaderPeer)
	require.NoError(t, err)
	_, logCursor, err := waitForLeaderRefresh(
		ctx,
		clients.leaderAwareLogs,
		0,
		memberClientURL(t, fixture.members, initialLeader),
	)
	require.NoError(t, err)

	follower := firstFollower(t, fixture.members, initialLeader)
	followerRuntime, err := localRuntimeForContainer(ctx, follower.Name)
	require.NoError(t, err)
	resumeFollower := pauseLocalContainer(t, ctx, followerRuntime, follower.Name)
	survivingLeadership, err := waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, follower.Name)
	require.NoError(t, err)
	require.Equal(t, initialLeadership, survivingLeadership)
	followerStart := time.Now()
	roundRobinFollowerLoad := startPutLoad(
		t,
		ctx,
		clients.roundRobin,
		clients.roundRobinRecorder,
		prefix+"/load/follower/round-robin",
		reliabilityPutInterval,
		reliabilityPutTimeout,
	)
	leaderAwareFollowerLoad := startPutLoad(
		t,
		ctx,
		clients.leaderAware,
		clients.leaderAwareRecorder,
		prefix+"/load/follower/leader-aware",
		reliabilityPutInterval,
		reliabilityPutTimeout,
	)
	require.NoError(t, waitPoll(ctx, followerPauseDuration))
	stopPutLoads(roundRobinFollowerLoad, leaderAwareFollowerLoad)
	require.NoError(t, resumeFollower(ctx))
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, clients.oracle, fixture.endpoints, 20*time.Second))
	restoredLeadership, err := waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	require.Equal(t, initialLeadership, restoredLeadership)

	roundRobinFollowerSummary := summarizePutLoad(
		roundRobinFollowerLoad.snapshot(),
		followerStart,
		roundRobinFollowerLoad.endTime(),
	)
	leaderAwareFollowerSummary := summarizePutLoad(
		leaderAwareFollowerLoad.snapshot(),
		followerStart,
		leaderAwareFollowerLoad.endTime(),
	)
	require.Zero(t, roundRobinFollowerSummary.dropped)
	require.Zero(t, leaderAwareFollowerSummary.dropped)
	require.Zero(t, leaderAwareFollowerSummary.failed, "a paused follower disrupted leader-aware writes")
	require.Positive(t, leaderAwareFollowerSummary.successful)
	require.Equal(t, leaderAwareFollowerSummary.transportAttempts, leaderAwareFollowerSummary.peerAttempts[initialLeaderPeer])
	followerPeer := memberPeer(t, fixture.members, follower.Name)
	require.Positive(t, roundRobinFollowerSummary.peerAttempts[followerPeer], "round_robin never selected the paused follower")
	require.Positive(t, roundRobinFollowerSummary.failed, "paused follower did not affect round_robin control")
	require.Positive(t, roundRobinFollowerSummary.successful, "round_robin control made no progress on live members")
	require.True(t, hasDeadlineFailureToPeer(roundRobinFollowerLoad.snapshot(), followerPeer), "round_robin did not time out on the paused follower")
	requireLeaderAwareFaultLoad(t, leaderAwareFollowerLoad.snapshot(), initialLeaderPeer)
	require.Greater(t, leaderAwareFollowerSummary.successRate(), roundRobinFollowerSummary.successRate())
	t.Logf(
		"follower gray failure: round_robin success=%d/%d (%.1f%%), failed=%d, max gap=%s; leader-aware success=%d/%d (%.1f%%), failed=%d, max gap=%s",
		roundRobinFollowerSummary.successful,
		roundRobinFollowerSummary.scheduled,
		100*roundRobinFollowerSummary.successRate(),
		roundRobinFollowerSummary.failed,
		roundRobinFollowerSummary.maxSuccessGap,
		leaderAwareFollowerSummary.successful,
		leaderAwareFollowerSummary.scheduled,
		100*leaderAwareFollowerSummary.successRate(),
		leaderAwareFollowerSummary.failed,
		leaderAwareFollowerSummary.maxSuccessGap,
	)

	leadershipAfterFollower, err := waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	require.Equal(t, initialLeadership, leadershipAfterFollower, "pausing a follower changed leadership or Raft term")
	require.NoError(t, waitForPutPeers(
		ctx,
		clients.roundRobin,
		clients.roundRobinRecorder,
		prefix+"/prewarm-leader-fault",
		endpointPeers(t, fixture.endpoints),
	))
	_, err = waitForLeaderAwarePuts(ctx, clients.leaderAware, clients.leaderAwareRecorder, prefix+"/prewarm-leader-aware-leader-fault", initialLeaderPeer)
	require.NoError(t, err)
	logCursor = len(clients.leaderAwareLogs.All())
	leaderRuntime, err := localRuntimeForContainer(ctx, initialLeader)
	require.NoError(t, err)
	resumeLeader := pauseLocalContainer(t, ctx, leaderRuntime, initialLeader)
	leaderStart := time.Now()
	roundRobinLeaderLoad := startPutLoad(
		t,
		ctx,
		clients.roundRobin,
		clients.roundRobinRecorder,
		prefix+"/load/leader/round-robin",
		reliabilityPutInterval,
		reliabilityPutTimeout,
	)
	leaderAwareLeaderLoad := startPutLoad(
		t,
		ctx,
		clients.leaderAware,
		clients.leaderAwareRecorder,
		prefix+"/load/leader/leader-aware",
		reliabilityPutInterval,
		reliabilityPutTimeout,
	)
	electionCtx, electionCancel := context.WithTimeout(ctx, leaderElectionTimeout)
	newLeadership, err := waitForLeadershipAgreement(electionCtx, clients.oracle, fixture.members, initialLeader)
	electionCancel()
	require.NoError(t, err)
	newLeader := newLeadership.name
	electionObservedAt := time.Now()
	refreshCtx, refreshCancel := context.WithTimeout(ctx, leaderRecoveryObservationWindow)
	recoveryRefresh, logCursor, refreshErr := waitForLeaderRefresh(
		refreshCtx,
		clients.leaderAwareLogs,
		logCursor,
		memberClientURL(t, fixture.members, newLeader),
	)
	refreshCancel()
	require.NoError(t, refreshErr, "leader-aware client did not rediscover the elected leader while the old leader remained paused")
	require.False(t, recoveryRefresh.at.Before(leaderStart), "leader refresh predates the injected leader fault")
	proofCtx, proofCancel := context.WithTimeout(ctx, leaderAwareProofTimeout)
	_, err = waitForLeaderAwarePuts(
		proofCtx,
		clients.leaderAware,
		clients.leaderAwareRecorder,
		prefix+"/proof/leader-paused",
		memberPeer(t, fixture.members, newLeader),
	)
	proofCancel()
	require.NoError(t, err, "leader-aware client did not activate the new-leader picker while the old leader remained paused")
	postRecoveryStart := time.Now()
	require.NoError(t, waitPoll(ctx, leaderAwareObservationWindow))
	stopPutLoads(roundRobinLeaderLoad, leaderAwareLeaderLoad)
	require.NoError(t, resumeLeader(ctx))
	require.NoError(t, waitForLocalEndpointsHealthy(ctx, clients.oracle, fixture.endpoints, 20*time.Second))
	restoredLeadership, err = waitForLeadershipAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	require.Equal(t, newLeadership, restoredLeadership)

	roundRobinLeaderSummary := summarizePutLoad(
		roundRobinLeaderLoad.snapshot(),
		leaderStart,
		roundRobinLeaderLoad.endTime(),
	)
	leaderAwareLeaderSummary := summarizePutLoad(
		leaderAwareLeaderLoad.snapshot(),
		leaderStart,
		leaderAwareLeaderLoad.endTime(),
	)
	require.Zero(t, roundRobinLeaderSummary.dropped)
	require.Zero(t, leaderAwareLeaderSummary.dropped)
	require.Positive(t, roundRobinLeaderSummary.successful)
	require.Positive(t, leaderAwareLeaderSummary.successful, "leader-aware client made no progress while the old leader was paused")
	requireFirstPutFailedToPeer(t, leaderAwareLeaderLoad.snapshot(), initialLeaderPeer)
	require.True(t, hasDeadlineFailureToPeer(roundRobinLeaderLoad.snapshot(), initialLeaderPeer), "round_robin never exercised the paused old leader")
	availabilityBudget := electionObservedAt.Sub(leaderStart) + leaderRediscoveryUpperBound + 2*reliabilityPutTimeout
	require.LessOrEqual(
		t,
		leaderAwareLeaderSummary.maxSuccessGap,
		availabilityBudget,
		"leader-aware application writes did not resume within the election and invalidation budget",
	)
	require.LessOrEqual(
		t,
		leaderAwareLeaderSummary.maxSuccessGap,
		roundRobinLeaderSummary.maxSuccessGap+reliabilityPutTimeout+reliabilityPutInterval,
		"leader-aware routing caused a longer application outage than round_robin plus one stale-hint timeout",
	)
	staleHintFailures, err := initialDeadlineFailureCohort(leaderAwareLeaderLoad.snapshot(), initialLeaderPeer)
	require.NoError(t, err)
	require.LessOrEqual(
		t,
		staleHintFailures,
		leaderStaleHintBurstBudget,
		"stale leader hint exposed more than the %d-request in-flight cohort",
		leaderStaleHintBurstBudget,
	)
	roundRobinPreRecoveryFailures := failedPutCount(putLoadSamplesInWindow(
		roundRobinLeaderLoad.snapshot(),
		leaderStart,
		postRecoveryStart,
	))
	leaderAwarePreRecoveryFailures := failedPutCount(putLoadSamplesInWindow(
		leaderAwareLeaderLoad.snapshot(),
		leaderStart,
		postRecoveryStart,
	))
	require.LessOrEqual(
		t,
		leaderAwarePreRecoveryFailures,
		roundRobinPreRecoveryFailures+leaderStaleHintBurstBudget,
		"leader-aware routing exceeded the %d-request stale-hint concurrency budget",
		leaderStaleHintBurstBudget,
	)
	refreshAfterElection := recoveryRefresh.at.Sub(electionObservedAt)
	if refreshAfterElection < 0 {
		refreshAfterElection = 0
	}
	require.LessOrEqual(t, refreshAfterElection, periodicRefreshTimeout)
	postRecoveryEnd := leaderAwareLeaderLoad.endTime()
	if roundRobinLeaderLoad.endTime().Before(postRecoveryEnd) {
		postRecoveryEnd = roundRobinLeaderLoad.endTime()
	}
	roundRobinPostRecoverySamples := putLoadSamplesInWindow(
		roundRobinLeaderLoad.snapshot(),
		postRecoveryStart,
		postRecoveryEnd,
	)
	leaderAwarePostRecoverySamples := putLoadSamplesInWindow(
		leaderAwareLeaderLoad.snapshot(),
		postRecoveryStart,
		postRecoveryEnd,
	)
	roundRobinPostRecoverySummary := summarizePutLoad(roundRobinPostRecoverySamples, postRecoveryStart, postRecoveryEnd)
	leaderAwarePostRecoverySummary := summarizePutLoad(leaderAwarePostRecoverySamples, postRecoveryStart, postRecoveryEnd)
	requireLeaderAwareFaultLoad(t, leaderAwarePostRecoverySamples, memberPeer(t, fixture.members, newLeader))
	require.Positive(t, roundRobinPostRecoverySummary.successful, "round_robin made no post-recovery progress")
	require.Positive(t, roundRobinPostRecoverySummary.failed, "round_robin did not remain exposed to the paused old leader")
	require.True(
		t,
		hasDeadlineFailureToPeer(roundRobinPostRecoverySamples, initialLeaderPeer),
		"round_robin did not exercise the paused old leader after leader-awareness recovery",
	)
	require.Greater(t, leaderAwarePostRecoverySummary.successRate(), roundRobinPostRecoverySummary.successRate())
	t.Logf(
		"leader gray failure %s -> %s resumed application writes within %s and rerouted them to %s after election: round_robin success=%d/%d (%.1f%%), failed=%d, max gap=%s; leader-aware success=%d/%d (%.1f%%), failed=%d, max gap=%s; post-recovery round_robin=%d/%d vs leader-aware=%d/%d",
		initialLeader,
		newLeader,
		availabilityBudget,
		refreshAfterElection,
		roundRobinLeaderSummary.successful,
		roundRobinLeaderSummary.scheduled,
		100*roundRobinLeaderSummary.successRate(),
		roundRobinLeaderSummary.failed,
		roundRobinLeaderSummary.maxSuccessGap,
		leaderAwareLeaderSummary.successful,
		leaderAwareLeaderSummary.scheduled,
		100*leaderAwareLeaderSummary.successRate(),
		leaderAwareLeaderSummary.failed,
		leaderAwareLeaderSummary.maxSuccessGap,
		roundRobinPostRecoverySummary.successful,
		roundRobinPostRecoverySummary.scheduled,
		leaderAwarePostRecoverySummary.successful,
		leaderAwarePostRecoverySummary.scheduled,
	)

	currentLeader, err := waitForLeaderAgreement(ctx, clients.oracle, fixture.members, "")
	require.NoError(t, err)
	require.Equal(t, newLeader, currentLeader)
	target := memberAfter(t, fixture.members, currentLeader)
	moveCtx, moveCancel := context.WithTimeout(ctx, 10*time.Second)
	err = moveLocalLeader(moveCtx, currentLeader, target, fixture.members)
	moveCancel()
	require.NoError(t, err)
	periodicCtx, periodicCancel := context.WithTimeout(ctx, periodicRefreshTimeout)
	periodicRefresh, _, err := waitForLeaderRefresh(
		periodicCtx,
		clients.leaderAwareLogs,
		logCursor,
		target.ClientURL,
	)
	periodicCancel()
	require.NoError(t, err)
	require.GreaterOrEqual(t, periodicRefresh.at.Sub(recoveryRefresh.at), periodicRefreshFloor)
	// The observed periodic refresh completed a probe round after the cluster
	// returned to health, so the recorder sits on a round boundary. Validate
	// the next full round: every probe succeeds without transparent retries.
	statusCursor := clients.leaderAwareRecorder.count(statusRPC)
	statusCtx, statusCancel := context.WithTimeout(ctx, periodicRefreshTimeout)
	_, statusRounds, statusErr := waitForCompleteStatusRounds(
		statusCtx,
		clients.leaderAwareRecorder,
		statusCursor,
		endpointPeers(t, fixture.endpoints),
	)
	statusCancel()
	require.NoError(t, statusErr)
	require.Positive(t, statusRounds)
	_, err = requireLeaderAwarePuts(
		ctx,
		clients.leaderAware,
		clients.leaderAwareRecorder,
		prefix+"/proof/periodic-refresh",
		memberPeer(t, fixture.members, target.Name),
		3,
	)
	require.NoError(t, err)

	validateAcknowledgedWrites(
		t,
		ctx,
		clients.oracle,
		prefix+"/load/",
		roundRobinFollowerLoad,
		leaderAwareFollowerLoad,
		roundRobinLeaderLoad,
		leaderAwareLeaderLoad,
	)
	t.Logf("long-lived leader-aware mutation client remained data-safe and rerouted to %s after the periodic refresh", target.Name)
}

type localE2EFixture struct {
	members   []clusterMember
	endpoints []string
}

func localE2EFixtureFromEnv(t *testing.T) localE2EFixture {
	t.Helper()
	cluster := os.Getenv("ETCD_INFRA_E2E_CLUSTER")
	firstPortText := os.Getenv("ETCD_INFRA_E2E_PORT")
	if cluster == "" || firstPortText == "" {
		t.Skip("set ETCD_INFRA_E2E_CLUSTER and ETCD_INFRA_E2E_PORT to run the local balancer E2E tests")
	}
	firstPort, err := strconv.Atoi(firstPortText)
	require.NoError(t, err)
	members := localMembers(cluster, 3, firstPort)
	return localE2EFixture{
		members:   members,
		endpoints: memberClientURLs(members),
	}
}

type balancerTestClients struct {
	roundRobin          *clientv3.Client
	leaderAware         *clientv3.Client
	oracle              *clientv3.Client
	roundRobinRecorder  *rpcAttemptRecorder
	leaderAwareRecorder *rpcAttemptRecorder
	leaderAwareLogs     *observer.ObservedLogs
}

func newBalancerTestClients(t *testing.T, endpoints []string, maxUnaryRetries uint) balancerTestClients {
	t.Helper()
	roundRobinRecorder := newRPCAttemptRecorder()
	leaderAwareRecorder := newRPCAttemptRecorder()
	logCore, leaderAwareLogs := observer.New(zap.DebugLevel)
	roundRobin, err := clientv3.New(clientv3.Config{
		Endpoints:       endpoints,
		DialTimeout:     5 * time.Second,
		MaxUnaryRetries: maxUnaryRetries,
		Logger:          zap.NewNop(),
		DialOptions:     []grpc.DialOption{grpc.WithStatsHandler(roundRobinRecorder)},
	}.WithBalancer(clientv3.DefaultBalancerName))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, roundRobin.Close()) })
	leaderAware, err := clientv3.New(clientv3.Config{
		Endpoints:       endpoints,
		DialTimeout:     5 * time.Second,
		MaxUnaryRetries: maxUnaryRetries,
		Logger:          zap.New(logCore),
		DialOptions:     []grpc.DialOption{grpc.WithStatsHandler(leaderAwareRecorder)},
	}.WithBalancer(clientv3.LeaderAwareBalancerName))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, leaderAware.Close()) })
	oracle, err := clientv3.New(clientv3.Config{
		Endpoints:       endpoints,
		DialTimeout:     5 * time.Second,
		MaxUnaryRetries: maxUnaryRetries,
		Logger:          zap.NewNop(),
	}.WithBalancer(clientv3.DefaultBalancerName))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, oracle.Close()) })
	return balancerTestClients{
		roundRobin:          roundRobin,
		leaderAware:         leaderAware,
		oracle:              oracle,
		roundRobinRecorder:  roundRobinRecorder,
		leaderAwareRecorder: leaderAwareRecorder,
		leaderAwareLogs:     leaderAwareLogs,
	}
}

func cleanupE2EPrefix(t *testing.T, cli *clientv3.Client, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = cli.Delete(ctx, prefix, clientv3.WithPrefix())
	})
}

type performancePutSample struct {
	latency time.Duration
	peer    string
}

func measurePut(
	t *testing.T,
	ctx context.Context,
	cli *clientv3.Client,
	recorder *rpcAttemptRecorder,
	key string,
) performancePutSample {
	t.Helper()
	return measurePutValue(t, ctx, cli, recorder, key, key)
}

func measurePutValue(
	t *testing.T,
	ctx context.Context,
	cli *clientv3.Client,
	recorder *rpcAttemptRecorder,
	key string,
	value string,
) performancePutSample {
	t.Helper()
	attemptStart := recorder.count(putRPC)
	callCtx, cancel := context.WithTimeout(ctx, performancePutTimeout)
	startedAt := time.Now()
	response, callErr := cli.Put(callCtx, key, value)
	latency := time.Since(startedAt)
	cancel()
	require.NoError(t, callErr, "put %s", key)
	require.NotNil(t, response)
	require.NotNil(t, response.Header)
	attempt, err := recorder.oneSince(putRPC, attemptStart)
	require.NoError(t, err, "put %s", key)
	require.False(t, attempt.transparent, "put %s used a transparent retry", key)
	require.NoError(t, attempt.err, "transport for put %s", key)
	require.NotEmpty(t, attempt.peer, "put %s recorded no peer", key)
	return performancePutSample{latency: latency, peer: attempt.peer}
}

type peerTrafficBlockResult struct {
	peerSentBytes float64
	samples       []performancePutSample
	elapsed       time.Duration
}

func measurePeerTrafficBlock(
	t *testing.T,
	ctx context.Context,
	endpoints []string,
	oracle *clientv3.Client,
	cli *clientv3.Client,
	recorder *rpcAttemptRecorder,
	prefix string,
	payload string,
) peerTrafficBlockResult {
	t.Helper()
	require.NoError(t, waitForAppliedIndexAgreement(ctx, oracle, endpoints))
	before, err := scrapePeerSentBytes(ctx, endpoints)
	require.NoError(t, err)
	startedAt := time.Now()
	samples := make([]performancePutSample, 0, performancePeerBlockRequestCount)
	for index := 0; index < performancePeerBlockRequestCount; index++ {
		samples = append(samples, measurePutValue(
			t,
			ctx,
			cli,
			recorder,
			fmt.Sprintf("%s/%06d", prefix, index),
			payload,
		))
	}
	require.NoError(t, waitForAppliedIndexAgreement(ctx, oracle, endpoints))
	elapsed := time.Since(startedAt)
	after, err := scrapePeerSentBytes(ctx, endpoints)
	require.NoError(t, err)
	delta, err := peerSentBytesDelta(before, after)
	require.NoError(t, err)
	require.Positive(t, delta, "peer-sent counter did not increase during %s", prefix)
	return peerTrafficBlockResult{peerSentBytes: delta, samples: samples, elapsed: elapsed}
}

func waitForAppliedIndexAgreement(ctx context.Context, cli *clientv3.Client, endpoints []string) error {
	for {
		var appliedIndex uint64
		agree := true
		for _, endpoint := range endpoints {
			statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			response, err := cli.Status(statusCtx, endpoint)
			cancel()
			if err != nil {
				return fmt.Errorf("status %s: %w", endpoint, err)
			}
			if response == nil || response.RaftAppliedIndex == 0 {
				return fmt.Errorf("status %s returned no applied index", endpoint)
			}
			if appliedIndex == 0 {
				appliedIndex = response.RaftAppliedIndex
			} else if response.RaftAppliedIndex != appliedIndex {
				agree = false
			}
		}
		if agree {
			return nil
		}
		if err := waitPoll(ctx, 10*time.Millisecond); err != nil {
			return fmt.Errorf("wait for all members to apply the measured PUTs: %w", err)
		}
	}
}

// scrapePeerSentBytes sums the peer-sent counter per endpoint, using the
// shared scraper from metrics.go. etcd_network_peer_sent_bytes_total counts
// serialized Raft messages rather than TCP/TLS framing, which makes it the
// causal server-side measure of the redundant proposal copy; summing received
// bytes too would double-count it.
func scrapePeerSentBytes(ctx context.Context, endpoints []string) (map[string]float64, error) {
	values := make(map[string]float64, len(endpoints))
	httpClient := &http.Client{Timeout: 5 * time.Second}
	for _, endpoint := range endpoints {
		value, err := scrapePeerMetric(ctx, httpClient, endpoint, peerSentBytesMetric)
		if err != nil {
			return nil, fmt.Errorf("scrape peer-sent bytes from %s: %w", endpoint, err)
		}
		values[endpoint] = value
	}
	return values, nil
}

func peerSentBytesDelta(before, after map[string]float64) (float64, error) {
	var total float64
	for endpoint, beforeValue := range before {
		afterValue, ok := after[endpoint]
		if !ok {
			return 0, fmt.Errorf("peer-sent snapshot omitted %s", endpoint)
		}
		if afterValue < beforeValue {
			return 0, fmt.Errorf("peer-sent counter for %s reset from %.0f to %.0f", endpoint, beforeValue, afterValue)
		}
		total += afterValue - beforeValue
	}
	return total, nil
}

func requireAllPerformancePeers(t *testing.T, samples []performancePutSample, expectedPeer string) {
	t.Helper()
	for _, sample := range samples {
		require.Equal(t, expectedPeer, sample.peer)
	}
}

func requireBalancedPerformancePeers(t *testing.T, samples []performancePutSample, expected map[string]struct{}) {
	t.Helper()
	counts := performancePeerCounts(samples)
	require.Len(t, counts, len(expected), "round_robin selected unexpected peers: %v", counts)
	minimum, maximum := len(samples), 0
	for peer := range expected {
		count := counts[peer]
		require.Positive(t, count, "round_robin did not select %s", peer)
		if count < minimum {
			minimum = count
		}
		if count > maximum {
			maximum = count
		}
	}
	require.LessOrEqual(t, maximum-minimum, 1, "round_robin peer distribution was not balanced: %v", counts)
}

func performancePeerCounts(samples []performancePutSample) map[string]int {
	counts := make(map[string]int)
	for _, sample := range samples {
		counts[sample.peer]++
	}
	return counts
}

func performanceLatencies(samples []performancePutSample) []time.Duration {
	latencies := make([]time.Duration, 0, len(samples))
	for _, sample := range samples {
		latencies = append(latencies, sample.latency)
	}
	return latencies
}

func sumDurations(values []time.Duration) time.Duration {
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return total
}

func waitForPutPeers(
	ctx context.Context,
	cli *clientv3.Client,
	recorder *rpcAttemptRecorder,
	key string,
	expected map[string]struct{},
) error {
	seen := make(map[string]struct{}, len(expected))
	for attempt := 1; ; attempt++ {
		probe, err := probePut(ctx, cli, recorder, key, strconv.Itoa(attempt))
		if err != nil {
			return err
		}
		if probe.callErr == nil {
			if _, ok := expected[probe.peer]; !ok {
				return fmt.Errorf("put selected unexpected peer %q", probe.peer)
			}
			seen[probe.peer] = struct{}{}
			if len(seen) == len(expected) {
				return nil
			}
		}
		if err := waitPoll(ctx, 50*time.Millisecond); err != nil {
			return fmt.Errorf("observe Put on every peer: %w", err)
		}
	}
}

func firstFollower(t *testing.T, members []clusterMember, leader string) clusterMember {
	t.Helper()
	for _, member := range members {
		if member.Name != leader {
			return member
		}
	}
	t.Fatalf("cluster has no follower for leader %s", leader)
	return clusterMember{}
}

func pauseLocalContainer(t *testing.T, ctx context.Context, runtime, member string) func(context.Context) error {
	t.Helper()
	resume := func(resumeCtx context.Context) error {
		paused, err := localContainerPaused(resumeCtx, runtime, member)
		if err != nil {
			return err
		}
		if !paused {
			return nil
		}
		if err := runLocalContainerAction(resumeCtx, runtime, "unpause", member); err != nil {
			return err
		}
		return waitForLocalContainerPaused(resumeCtx, runtime, member, false)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := resume(cleanupCtx); err != nil {
			t.Errorf("unpause local container %s during cleanup: %v", member, err)
		}
	})
	if err := runLocalContainerAction(ctx, runtime, "pause", member); err != nil {
		t.Fatalf("pause local container %s: %v", member, err)
	}
	require.NoError(t, waitForLocalContainerPaused(ctx, runtime, member, true))
	return resume
}

func runLocalContainerAction(ctx context.Context, runtime, action, member string) error {
	output, err := exec.CommandContext(ctx, runtime, action, member).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", runtime, action, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func localContainerPaused(ctx context.Context, runtime, member string) (bool, error) {
	output, err := exec.CommandContext(ctx, runtime, "inspect", "--format", "{{.State.Paused}}", member).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("%s inspect paused state: %w: %s", runtime, err, strings.TrimSpace(string(output)))
	}
	switch strings.TrimSpace(string(output)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s inspect returned unknown paused state %q", runtime, strings.TrimSpace(string(output)))
	}
}

func waitForLocalContainerPaused(ctx context.Context, runtime, member string, expected bool) error {
	for {
		paused, err := localContainerPaused(ctx, runtime, member)
		if err != nil {
			return err
		}
		if paused == expected {
			return nil
		}
		if err := waitPoll(ctx, 25*time.Millisecond); err != nil {
			return fmt.Errorf("wait for %s paused=%t: %w", member, expected, err)
		}
	}
}

func waitForLeaderAgreement(
	ctx context.Context,
	cli *clientv3.Client,
	members []clusterMember,
	excluded string,
) (string, error) {
	leadership, err := waitForLeadershipAgreement(ctx, cli, members, excluded)
	return leadership.name, err
}

type observedLeadership struct {
	name string
	term uint64
}

func waitForLeadershipAgreement(
	ctx context.Context,
	cli *clientv3.Client,
	members []clusterMember,
	excluded string,
) (observedLeadership, error) {
	var lastErr error
	for {
		leadership, err := observeLeadershipAgreement(ctx, cli, members, excluded)
		if err == nil {
			return leadership, nil
		}
		lastErr = err
		if err := waitPoll(ctx, 50*time.Millisecond); err != nil {
			return observedLeadership{}, fmt.Errorf("wait for leader agreement excluding %q: %w (last error %v)", excluded, err, lastErr)
		}
	}
}

func observeLeadershipAgreement(
	ctx context.Context,
	cli *clientv3.Client,
	members []clusterMember,
	excluded string,
) (observedLeadership, error) {
	var leaderID uint64
	var raftTerm uint64
	leaderName := ""
	responses := 0
	for _, member := range members {
		if member.Name == excluded {
			continue
		}
		statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		response, err := cli.Status(statusCtx, member.ClientURL)
		cancel()
		if err != nil {
			return observedLeadership{}, fmt.Errorf("status %s: %w", member.Name, err)
		}
		if response == nil || response.Header == nil || response.Header.MemberId == 0 || response.Leader == 0 || response.RaftTerm == 0 {
			return observedLeadership{}, fmt.Errorf("status %s returned incomplete identity", member.Name)
		}
		responses++
		if leaderID == 0 {
			leaderID = response.Leader
		} else if response.Leader != leaderID {
			return observedLeadership{}, fmt.Errorf("members disagree on leader: %x and %x", leaderID, response.Leader)
		}
		if raftTerm == 0 {
			raftTerm = response.RaftTerm
		} else if response.RaftTerm != raftTerm {
			return observedLeadership{}, fmt.Errorf("members disagree on Raft term: %d and %d", raftTerm, response.RaftTerm)
		}
		if response.Header.MemberId == response.Leader {
			leaderName = member.Name
		}
	}
	wantResponses := len(members)
	if excluded != "" {
		wantResponses--
	}
	if responses != wantResponses || leaderName == "" {
		return observedLeadership{}, fmt.Errorf("leader did not self-confirm across %d/%d responses", responses, wantResponses)
	}
	return observedLeadership{name: leaderName, term: raftTerm}, nil
}

func waitForCompleteStatusRounds(
	ctx context.Context,
	recorder *rpcAttemptRecorder,
	start int,
	expected map[string]struct{},
) ([]rpcAttempt, int, error) {
	for {
		attempts := recorder.attemptsSince(statusRPC, start)
		rounds, complete, err := completeStatusRounds(attempts, expected)
		if err != nil {
			return nil, 0, err
		}
		if complete {
			return attempts, rounds, nil
		}
		if err := waitPoll(ctx, 25*time.Millisecond); err != nil {
			return nil, 0, fmt.Errorf("wait for a complete periodic Status round: %w", err)
		}
	}
}

func completeStatusRounds(attempts []rpcAttempt, expected map[string]struct{}) (int, bool, error) {
	if len(expected) == 0 {
		return 0, false, fmt.Errorf("no expected Status peers")
	}
	width := len(expected)
	rounds := len(attempts) / width
	for round := 0; round < rounds; round++ {
		start := round * width
		if err := validateStatusRound(attempts[start:start+width], expected); err != nil {
			return 0, false, fmt.Errorf("periodic Status round %d: %w", round+1, err)
		}
	}
	partial := attempts[rounds*width:]
	seen := make(map[string]struct{}, len(partial))
	for _, attempt := range partial {
		if attempt.transparent {
			return 0, false, fmt.Errorf("leader Status probe to %s used a transparent retry", attempt.peer)
		}
		if attempt.err != nil {
			return 0, false, fmt.Errorf("leader Status probe to %s failed: %w", attempt.peer, attempt.err)
		}
		if _, ok := expected[attempt.peer]; !ok {
			return 0, false, fmt.Errorf("leader Status probe selected unexpected peer %q", attempt.peer)
		}
		if _, ok := seen[attempt.peer]; ok {
			return 0, false, fmt.Errorf("periodic Status partial round probed %s more than once", attempt.peer)
		}
		seen[attempt.peer] = struct{}{}
	}
	return rounds, rounds > 0 && len(partial) == 0, nil
}

func hasDeadlineFailureToPeer(samples []putLoadSample, peer string) bool {
	for _, sample := range samples {
		if !isDeadlineExceeded(sample.callErr) {
			continue
		}
		for _, attempt := range sample.attempts {
			if attempt.peer == peer {
				return true
			}
		}
	}
	return false
}

func putLoadSamplesInWindow(samples []putLoadSample, start, end time.Time) []putLoadSample {
	filtered := make([]putLoadSample, 0, len(samples))
	for _, sample := range samples {
		if sample.scheduledAt.Before(start) || sample.scheduledAt.After(end) {
			continue
		}
		filtered = append(filtered, sample)
	}
	return filtered
}

func failedPutCount(samples []putLoadSample) int {
	failed := 0
	for _, sample := range samples {
		if sample.issued && sample.callErr != nil {
			failed++
		}
	}
	return failed
}

func initialDeadlineFailureCohort(samples []putLoadSample, peer string) (int, error) {
	ordered := append([]putLoadSample(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].scheduledAt.Before(ordered[j].scheduledAt) })
	cohort := 0
	for _, sample := range ordered {
		if !sample.issued {
			continue
		}
		if len(sample.attempts) != 1 {
			return 0, fmt.Errorf("initial stale-hint Put %s used %d transport attempts", sample.key, len(sample.attempts))
		}
		attempt := sample.attempts[0]
		if attempt.peer != peer {
			if cohort == 0 {
				return 0, fmt.Errorf("first leader-fault Put selected %s, want stale leader %s", attempt.peer, peer)
			}
			return cohort, nil
		}
		if attempt.transparent {
			return 0, fmt.Errorf("stale-hint Put %s used a transparent retry", sample.key)
		}
		if !isDeadlineExceeded(sample.callErr) || !isDeadlineExceeded(attempt.err) {
			return 0, fmt.Errorf("stale-hint Put %s did not end at its deadline: call=%v transport=%v", sample.key, sample.callErr, attempt.err)
		}
		cohort++
	}
	if cohort == 0 {
		return 0, fmt.Errorf("leader-fault load issued no Put to stale leader %s", peer)
	}
	return cohort, nil
}

func requireLeaderAwareFaultLoad(t *testing.T, samples []putLoadSample, leaderPeer string) {
	t.Helper()
	for _, sample := range samples {
		require.True(t, sample.issued, "leader-aware load dropped %s", sample.key)
		require.NoError(t, sample.callErr, "leader-aware put %s", sample.key)
		require.Len(t, sample.attempts, 1, "leader-aware put %s", sample.key)
		require.False(t, sample.attempts[0].transparent, "leader-aware put %s used a transparent retry", sample.key)
		require.NoError(t, sample.attempts[0].err, "leader-aware transport for %s", sample.key)
		require.Equal(t, leaderPeer, sample.attempts[0].peer, "leader-aware put %s", sample.key)
	}
}

func requireFirstPutFailedToPeer(t *testing.T, samples []putLoadSample, peer string) {
	t.Helper()
	ordered := append([]putLoadSample(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].scheduledAt.Before(ordered[j].scheduledAt) })
	for _, sample := range ordered {
		if !sample.issued {
			continue
		}
		require.True(t, isDeadlineExceeded(sample.callErr), "first post-pause Put did not time out: %v", sample.callErr)
		require.Len(t, sample.attempts, 1, "first post-pause Put transport attempts")
		require.Equal(t, peer, sample.attempts[0].peer, "first post-pause Put did not exercise the stale leader hint")
		return
	}
	t.Fatal("leader-fault load issued no Put")
}

func isDeadlineExceeded(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded
}

func validateAcknowledgedWrites(
	t *testing.T,
	ctx context.Context,
	cli *clientv3.Client,
	prefix string,
	loads ...*putLoad,
) {
	t.Helper()
	attempted := make(map[string]string)
	acknowledged := make(map[string]string)
	for _, load := range loads {
		for _, sample := range load.snapshot() {
			if !sample.issued {
				continue
			}
			attempted[sample.key] = sample.value
			if sample.callErr == nil {
				acknowledged[sample.key] = sample.value
			}
		}
	}
	response, err := cli.Get(ctx, prefix, clientv3.WithPrefix())
	require.NoError(t, err)
	observed := make(map[string]string, len(response.Kvs))
	for _, kv := range response.Kvs {
		key := string(kv.Key)
		value := string(kv.Value)
		expected, ok := attempted[key]
		require.True(t, ok, "observed unattempted key %s", key)
		require.Equal(t, expected, value, "value mismatch for %s", key)
		observed[key] = value
	}
	for key, value := range acknowledged {
		require.Equal(t, value, observed[key], "acknowledged write %s was lost", key)
	}
}
