package scenarios

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunLeaderElectionContention staging/src/k8s.io/client-go/tools/leaderelection Kubernetes controllers use leader election to ensure only one replica is active. This test stresses: multiple replicas contending for leader lease with renewal.
func RunLeaderElectionContention(runner StressRunner) {
	logutil.S().Infow("running", "scenario", LeaderElectionContention.String())

	result := &Result{
		Scenario:  LeaderElectionContention.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	cfg := runner.GetConfig()
	metrics := runner.GetMetricsCollector()
	metrics.Reset()

	// Simulate leader election for 3 components (controller-manager, scheduler, custom-controller)
	componentCount := 3
	replicasPerComponent := 5 // 5 replicas competing for each leader lock

	prefix := runner.GenerateRandomKey(keySize(cfg, 8))
	duration := scenarioDuration(cfg)
	deadline := time.Now().Add(duration)

	const leaderLeaseDuration = 15 * time.Second
	const renewalInterval = 10 * time.Second

	var totalElections, totalRenewals, electionConflicts, renewalFailures atomic.Int64

	var wg sync.WaitGroup

	// Start election workers for each component
	for comp := range componentCount {
		lockKey := fmt.Sprintf("%s/election-%d", prefix, comp)

		for replica := range replicasPerComponent {
			wg.Add(1)
			go func(compID, replicaID int, lockKey string) {
				defer wg.Done()

				identity := fmt.Sprintf("comp-%d-replica-%d-%s", compID, replicaID, randutil.StringAlphabetsLowerCase(8))
				var isLeader bool
				var leaseID clientv3.LeaseID

				for time.Now().Before(deadline) {
					//nolint:nestif // nested branches model leader election attempts and handoff
					if !isLeader {
						// Try to acquire leadership
						ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
						start := time.Now()

						// Grant lease for leader lock
						leaseResp, err := cli.Grant(ctx, int64(leaderLeaseDuration.Seconds()))
						cancel()

						if err != nil {
							metrics.RecordFailure(float64(time.Since(start).Milliseconds()), err.Error())
							time.Sleep(time.Duration(randutil.Intn(3000)+1000) * time.Millisecond)

							continue
						}

						leaseID = leaseResp.ID

						// Try to acquire lock with transaction
						ctx, cancel = runner.NewCtxTimeout(5 * time.Second)
						startTxn := time.Now()

						txnResp, err := cli.Txn(ctx).
							If(clientv3.Compare(clientv3.CreateRevision(lockKey), "=", 0)).
							Then(clientv3.OpPut(lockKey, identity, clientv3.WithLease(leaseID))).
							Else(clientv3.OpGet(lockKey)).
							Commit()
						cancel()

						latencyMs := float64(time.Since(startTxn).Milliseconds())

						if err != nil {
							metrics.RecordFailure(latencyMs, err.Error())
							// Revoke unused lease
							ctx, cancel = runner.NewCtxTimeout(3 * time.Second)
							_, _ = cli.Revoke(ctx, leaseID)
							cancel()
							time.Sleep(time.Duration(randutil.Intn(2000)+500) * time.Millisecond)

							continue
						}

						if txnResp.Succeeded {
							// Won leadership!
							isLeader = true
							totalElections.Add(1)
							metrics.RecordSuccess(latencyMs)
							logutil.S().Debugw("acquired leadership", "scenario", LeaderElectionContention.String(),
								"identity", identity, "lock", lockKey)
						} else {
							// Lost election
							electionConflicts.Add(1)
							metrics.RecordSuccess(latencyMs) // Conflict is expected
							// Revoke lease
							ctx, cancel = runner.NewCtxTimeout(3 * time.Second)
							_, _ = cli.Revoke(ctx, leaseID)
							cancel()
							time.Sleep(time.Duration(randutil.Intn(5000)+2000) * time.Millisecond)
						}
					} else {
						// Renew leadership (keepalive)
						time.Sleep(renewalInterval)

						if time.Now().After(deadline) {
							break
						}

						ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
						start := time.Now()

						// Check if still leader and refresh
						getResp, err := cli.Get(ctx, lockKey)
						cancel()

						if err != nil {
							renewalFailures.Add(1)
							isLeader = false

							continue
						}

						if len(getResp.Kvs) == 0 || string(getResp.Kvs[0].Value) != identity {
							// Lost leadership (lease expired or someone else took over)
							logutil.S().Debugw("lost leadership", "scenario", LeaderElectionContention.String(),
								"identity", identity)
							isLeader = false

							continue
						}

						// Still leader, renew lease
						ctx, cancel = runner.NewCtxTimeout(3 * time.Second)
						_, err = cli.KeepAliveOnce(ctx, leaseID)
						cancel()

						latencyMs := float64(time.Since(start).Milliseconds())

						if err != nil {
							renewalFailures.Add(1)
							metrics.RecordFailure(latencyMs, err.Error())
							isLeader = false
						} else {
							totalRenewals.Add(1)
							metrics.RecordSuccess(latencyMs)
						}
					}
				}

				// Clean up if still leader
				if isLeader && leaseID != 0 {
					ctx, cancel := runner.NewCtxTimeout(3 * time.Second)
					_, _ = cli.Revoke(ctx, leaseID)
					cancel()
				}
			}(comp, replica, lockKey)
		}
	}

	wg.Wait()

	// Leader election involves lease grant + create-if-absent transaction, each
	// crossing the WireGuard tunnel; renewal also adds periodic RTTs.
	stats := finalizeScenario(result, metrics, nil, 0.80, 5000)
	if !result.Success {
		return
	}

	if totalElections.Load() == 0 {
		result.Success = false
		result.Output = "no successful elections"

		return
	}

	result.Output = fmt.Sprintf(
		"leader election: %d components, %d replicas each; %d elections, %d renewals, %d conflicts; success %.2f%%, p99 %.0fms",
		componentCount,
		replicasPerComponent,
		totalElections.Load(),
		totalRenewals.Load(),
		electionConflicts.Load(),
		stats.SuccessRate()*100,
		stats.P99LatencyMs,
	)
}
