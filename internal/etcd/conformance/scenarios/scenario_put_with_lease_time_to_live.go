package scenarios

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutWithLeaseTimeToLive inspects TTL reporting for leased keys.
//
//nolint:gocyclo // Scenario walks through multiple TTL queries and validations.
func RunPutWithLeaseTimeToLive(runner Runner) {
	logutil.S().Infow("running", "scenario", PutWithLeaseTimeToLive.String())

	result := &Result{
		Scenario:  PutWithLeaseTimeToLive.String(),
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

	testKey := runner.GenerateRandomKey(10)

	// TTL values are set to 15 seconds to accommodate cloud/VPN environments (Tailscale/Headscale)
	// where network latency can cause shorter TTLs to expire before operations complete.
	// Hetzner cloud latency (~100-200ms RTT) requires longer TTLs than local environments.
	// This ensures keys don't expire before we can read them back after PUT operations.
	tests := []struct {
		kvs []keyValue
		ttl int64
	}{
		{
			kvs: []keyValue{
				createKV(testKey, "hello", "world"),
			},
			ttl: 0,
		},
		{
			kvs: []keyValue{
				createKV(testKey, "hello", "world"),
			},
			ttl: 15,
		},
		{
			kvs: []keyValue{
				createKV(testKey, "hello11", "world"),
				createKV(testKey, "hello12", "world"),
				createKV(testKey, "hello13", "world"),
				createKV(testKey, "hello14", "world"),
				createKV(testKey, "hello15", "world"),
				createKV(testKey, "hello16", "world"),
				createKV(testKey, "hello17", "world"),
				createKV(testKey, "hello18", "world"),
				createKV(testKey, "hello19", "world"),
				createKV(testKey, "hello20", "world"),
			},
			ttl: 15,
		},
	}

	for i, tt := range tests {
		curLease := clientv3.NoLease
		if tt.ttl > 0 {
			ctx, cancel := runner.NewCtx()
			lresp, err := cli.Grant(ctx, tt.ttl)
			cancel()
			if err != nil {
				result.Success = false
				result.Output = fmt.Sprintf("#%d: failed to grant lease: %v", i, err)

				return
			}
			curLease = lresp.ID
		}

		logutil.S().Infow("writing keys with lease",
			"leaseID", curLease,
			"leaseIDHex", fmt.Sprintf("%x", curLease),
		)

		for j, kv := range tt.kvs {
			ctx, cancel := runner.NewCtx()
			presp, err := cli.Put(ctx, kv.k, kv.v, clientv3.WithLease(curLease))
			cancel()
			if err != nil {
				result.Output = fmt.Sprintf("#%d-%d: couldn't put %q (%v)", i, j, kv.k, err)

				return
			}
			logutil.S().Debug("PUT success", "key", kv.k, "revision", presp.Header.GetRevision())

			ctx, cancel = runner.NewCtx()
			gresp, err := cli.Get(ctx, kv.k)
			cancel()
			if err != nil {
				result.Success = false
				result.Output = fmt.Sprintf("#%d-%d: couldn't get key (%v)", i, j, err)

				return
			}
			if len(gresp.Kvs) != 1 {
				result.Success = false
				result.Output = fmt.Sprintf("#%d-%d: expected 1 key, got %d", i, j, len(gresp.Kvs))

				return
			}
			if !bytes.Equal([]byte(kv.v), gresp.Kvs[0].Value) {
				result.Success = false
				result.Output = fmt.Sprintf("#%d-%d: val = %s, want %s", i, j, kv.v, gresp.Kvs[0].Value)

				return
			}
			if curLease != clientv3.LeaseID(gresp.Kvs[0].Lease) {
				result.Success = false
				result.Output = fmt.Sprintf("#%d-%d: val = %d, want %d", i, j, curLease, gresp.Kvs[0].Lease)

				return
			}

			logutil.S().Debug("GET success", "key", kv.k, "revision", gresp.Header.GetRevision())
		}

		if curLease == clientv3.NoLease {
			continue
		}

		logutil.S().Info("getting lease objects")
		ctx, cancel := runner.NewCtx()
		lresp, err := cli.Leases(ctx)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: couldn't get leases (%v)", i, err)

			return
		}

		existingLeases := make(map[clientv3.LeaseID]struct{})
		for _, l := range lresp.Leases {
			existingLeases[l.ID] = struct{}{}
		}
		if _, ok := existingLeases[curLease]; !ok {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: lease %x not found", i, curLease)

			return
		}

		logutil.S().Info("getting lease objects via time to leave api")
		ctx, cancel = runner.NewCtx()
		tresp, err := cli.TimeToLive(ctx, curLease, clientv3.WithAttachedKeys())
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: couldn't get leases (%v)", i, err)

			return
		}
		if tresp.TTL > tt.ttl {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: remaining TTL %d is greater than initial TTL %d", i, tresp.TTL, tt.ttl)

			return
		}

		keys1, keys2 := make([]string, 0), make([]string, 0)
		for _, kv := range tt.kvs {
			keys1 = append(keys1, kv.k)
		}
		for _, k := range tresp.Keys {
			keys2 = append(keys2, string(k))
		}
		sort.Strings(keys1)
		sort.Strings(keys2)

		if !reflect.DeepEqual(keys1, keys2) {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: attached keys mismatch: %v, %v", i, keys1, keys2)

			return
		}

		// Use a generous timeout multiplier (3x TTL + 5s buffer) to handle VPN latency.
		// Lease expiration over VPN can be delayed due to network round-trip times.
		if err := waitForKeysToExpire(cli, runner, tt.kvs, time.Duration(tt.ttl)*time.Second*3+5*time.Second); err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("#%d: %v", i, err)

			return
		}
	}
}

func waitForKeysToExpire(cli *clientv3.Client, runner Runner, kvs []keyValue, timeout time.Duration) error {
	if len(kvs) == 0 {
		return nil
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		allExpired := true
		for _, kv := range kvs {
			ctx, cancel := runner.NewCtx()
			gresp, err := cli.Get(ctx, kv.k)
			cancel()
			if err != nil {
				return fmt.Errorf("GET failed for key %q: %w", kv.k, err)
			}
			if len(gresp.Kvs) > 0 {
				allExpired = false

				break
			}
		}
		if allExpired {
			return nil
		}

		select {
		case <-ticker.C:
		case <-timer.C:
			return fmt.Errorf("keys still exist after waiting %s", timeout)
		}
	}
}
