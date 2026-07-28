package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeaseExpirationAutoDelete verifies keys attached to a lease are removed on expiration
// matching kube-apiserver lease manager behavior (staging/src/k8s.io/apiserver/pkg/storage/etcd3/lease_manager.go).
func RunLeaseExpirationAutoDelete(runner Runner) {
	logutil.S().Infow("running", "scenario", LeaseExpirationAutoDelete.String())

	result := &Result{
		Scenario:  LeaseExpirationAutoDelete.String(),
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
		_ = cli.Close()
	}()

	// Create a lease with TTL long enough to complete all Put operations
	// even in high-latency cloud/VPN environments (each Put can take 500ms-1s).
	// TTL of 15 seconds provides enough buffer for 3 puts + verification gets.
	ctx, cancel := runner.NewCtx()
	leaseResp, err := cli.Grant(ctx, 15)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant lease: %v", err)

		return
	}

	// Put multiple keys with the lease attached
	testKeys := []string{
		runner.GenerateRandomKey(10) + "/key1",
		runner.GenerateRandomKey(10) + "/key2",
		runner.GenerateRandomKey(10) + "/key3",
	}

	for _, key := range testKeys {
		ctx, cancel = runner.NewCtx()
		_, err = cli.Put(ctx, key, "value-with-lease", clientv3.WithLease(leaseResp.ID))
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put key %s with lease: %v", key, err)

			return
		}
	}

	// Verify keys exist initially
	for _, key := range testKeys {
		ctx, cancel = runner.NewCtx()
		gresp, getErr := cli.Get(ctx, key)
		cancel()
		if getErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get key %s: %v", key, getErr)

			return
		}
		if len(gresp.Kvs) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("expected key %s to exist initially", key)

			return
		}
	}

	// Wait for lease to expire (TTL + buffer for high-latency environments)
	time.Sleep(18 * time.Second)

	// Verify all keys were automatically deleted
	for _, key := range testKeys {
		ctx, cancel = runner.NewCtx()
		gresp, getErr := cli.Get(ctx, key)
		cancel()
		if getErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get key %s after lease expiry: %v", key, getErr)

			return
		}
		if len(gresp.Kvs) != 0 {
			result.Success = false
			result.Output = fmt.Sprintf("key %s should have been deleted after lease expiry, but still exists", key)

			return
		}
	}

	// Also test that a key without a lease is not affected
	keyWithoutLease := runner.GenerateRandomKey(10) + "/no-lease"
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, keyWithoutLease, "value-no-lease")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key without lease: %v", err)

		return
	}

	// Create another lease and attach to a new key
	// Use 15-second TTL for high-latency environment compatibility.
	ctx, cancel = runner.NewCtx()
	leaseResp2, err := cli.Grant(ctx, 15)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to grant second lease: %v", err)

		return
	}

	keyWithLease2 := runner.GenerateRandomKey(10) + "/lease2"
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, keyWithLease2, "value-lease2", clientv3.WithLease(leaseResp2.ID))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put key with second lease: %v", err)

		return
	}

	// Wait for second lease to expire (TTL + buffer)
	time.Sleep(18 * time.Second)

	// Verify the key without lease still exists
	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, keyWithoutLease)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get key without lease: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = "key without lease should still exist"

		return
	}

	// Verify the key with second lease was deleted
	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, keyWithLease2)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get key with second lease: %v", err)

		return
	}
	if len(gresp.Kvs) != 0 {
		result.Success = false
		result.Output = "key with second lease should have been deleted"

		return
	}

	result.Output = fmt.Sprintf("all %d keys with leases were automatically deleted after TTL expiry", len(testKeys)+1)
}
