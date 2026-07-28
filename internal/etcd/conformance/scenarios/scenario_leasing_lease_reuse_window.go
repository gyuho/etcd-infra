package scenarios

import (
	"context"
	"fmt"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingLeaseReuseWindow exercises the kube-apiserver lease reuse window to avoid churning Lease.Grant calls
// (staging/src/k8s.io/apiserver/pkg/storage/etcd3/lease_manager.go) and confirms grants reuse cached leases until
// the attachment budget is exceeded.
func RunLeasingLeaseReuseWindow(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingLeaseReuseWindow.String())

	result := &Result{
		Scenario:  LeasingLeaseReuseWindow.String(),
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
	defer func() {
		if closeErr := cli.Close(); closeErr != nil {
			logutil.S().Warnw("failed to close client", "error", closeErr)
		}
	}()

	const (
		reuseSeconds   = int64(5)
		reusePercent   = 0.5
		maxObjectCount = int64(2)
		requestedTTL   = int64(8)
	)

	lm := newTestLeaseManager(cli, reuseSeconds, reusePercent, maxObjectCount)

	ctx, cancel := runner.NewCtx()
	leaseID1, err := lm.GetLease(ctx, requestedTTL)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("first lease grant failed: %v", err)

		return
	}
	if leaseID1 == clientv3.NoLease {
		result.Success = false
		result.Output = "first lease grant returned no lease"

		return
	}

	ctx, cancel = runner.NewCtx()
	ttlResp, err := cli.TimeToLive(ctx, leaseID1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to fetch ttl for first lease: %v", err)

		return
	}
	reuseDuration := int64(reusePercent * float64(requestedTTL))
	reuseDuration = min(reuseDuration, reuseSeconds)
	expectedGranted := requestedTTL + reuseDuration
	if ttlResp.GrantedTTL < expectedGranted {
		result.Success = false
		result.Output = fmt.Sprintf("lease granted ttl %d shorter than expected %d", ttlResp.GrantedTTL, expectedGranted)

		return
	}

	keyBase := runner.GenerateRandomKey(12)
	key1 := keyBase + "-l1"
	ctx, cancel = runner.NewCtx()
	if _, putErr := cli.Put(ctx, key1, "value1", clientv3.WithLease(leaseID1)); putErr != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key with first lease: %v", putErr)

		return
	}
	cancel()

	// Reuse the cached lease within the configured window.
	time.Sleep(500 * time.Millisecond)
	ctx, cancel = runner.NewCtx()
	leaseID2, err := lm.GetLease(ctx, requestedTTL)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("second lease acquisition failed: %v", err)

		return
	}
	if leaseID2 != leaseID1 {
		result.Success = false
		result.Output = "expected lease reuse within window but received a new lease"

		return
	}

	key2 := keyBase + "-l2"
	ctx, cancel = runner.NewCtx()
	if _, putErr := cli.Put(ctx, key2, "value2", clientv3.WithLease(leaseID2)); putErr != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key with reused lease: %v", putErr)

		return
	}
	cancel()

	ctx, cancel = runner.NewCtx()
	ttlResp, err = cli.TimeToLive(ctx, leaseID1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("TimeToLive after reuse failed: %v", err)

		return
	}
	if ttlResp.TTL <= 0 {
		result.Success = false
		result.Output = "lease unexpectedly expired during reuse window"

		return
	}

	// Exceed the max object count to trigger lease rotation.
	ctx, cancel = runner.NewCtx()
	leaseID3, err := lm.GetLease(ctx, requestedTTL)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("third lease acquisition failed: %v", err)

		return
	}
	if leaseID3 == leaseID1 {
		result.Success = false
		result.Output = "expected new lease after exceeding reuse attachment budget"

		return
	}

	key3 := keyBase + "-l3"
	ctx, cancel = runner.NewCtx()
	if _, err := cli.Put(ctx, key3, "value3", clientv3.WithLease(leaseID3)); err != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key with rotated lease: %v", err)

		return
	}
	cancel()

	// Validate stored keys report the expected lease IDs.
	if err := ensureLeaseAttachment(cli, runner, key1, leaseID1); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("first key lease validation failed: %v", err)

		return
	}
	if err := ensureLeaseAttachment(cli, runner, key2, leaseID1); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("second key lease validation failed: %v", err)

		return
	}
	if err := ensureLeaseAttachment(cli, runner, key3, leaseID3); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("third key lease validation failed: %v", err)

		return
	}

	result.Output = fmt.Sprintf("reused lease %d for two attachments before rotating to %d", leaseID1, leaseID3)
}

func ensureLeaseAttachment(cli *clientv3.Client, runner Runner, key string, want clientv3.LeaseID) error {
	ctx, cancel := runner.NewCtx()
	defer cancel()
	resp, err := cli.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to get key: %w", err)
	}
	if len(resp.Kvs) != 1 {
		return fmt.Errorf("expected 1 kv, got %d", len(resp.Kvs))
	}
	if clientv3.LeaseID(resp.Kvs[0].Lease) != want {
		return fmt.Errorf("key %s attached to lease %d, want %d", key, resp.Kvs[0].Lease, want)
	}

	return nil
}

type testLeaseManager struct {
	client *clientv3.Client

	leaseReuseDurationSeconds int64
	leaseReuseDurationPercent float64
	leaseMaxAttachedObjectCnt int64

	mu                   sync.Mutex
	prevLeaseID          clientv3.LeaseID
	prevLeaseExpireTime  time.Time
	leaseAttachedObjects int64
}

func newTestLeaseManager(client *clientv3.Client, reuseSeconds int64, reusePercent float64, maxObjectCount int64) *testLeaseManager {
	if maxObjectCount <= 0 {
		maxObjectCount = 1
	}

	return &testLeaseManager{
		client:                    client,
		leaseReuseDurationSeconds: reuseSeconds,
		leaseReuseDurationPercent: reusePercent,
		leaseMaxAttachedObjectCnt: maxObjectCount,
	}
}

func (l *testLeaseManager) GetLease(ctx context.Context, ttl int64) (clientv3.LeaseID, error) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	reuseSeconds := l.reuseDurationSecondsLocked(ttl)
	valid := now.Add(time.Duration(ttl) * time.Second).Before(l.prevLeaseExpireTime)
	sufficient := now.Add(time.Duration(ttl+reuseSeconds) * time.Second).After(l.prevLeaseExpireTime)

	l.leaseAttachedObjects++

	if valid && sufficient && l.leaseAttachedObjects <= l.leaseMaxAttachedObjectCnt && l.prevLeaseID != clientv3.NoLease {
		return l.prevLeaseID, nil
	}

	ttl += reuseSeconds
	leaseResp, err := l.client.Grant(ctx, ttl)
	if err != nil {
		return clientv3.NoLease, fmt.Errorf("failed to grant lease: %w", err)
	}
	l.prevLeaseID = leaseResp.ID
	l.prevLeaseExpireTime = now.Add(time.Duration(ttl) * time.Second)
	l.leaseAttachedObjects = 1

	return leaseResp.ID, nil
}

func (l *testLeaseManager) reuseDurationSecondsLocked(ttl int64) int64 {
	computed := int64(l.leaseReuseDurationPercent * float64(ttl))
	return min(computed, l.leaseReuseDurationSeconds)
}
