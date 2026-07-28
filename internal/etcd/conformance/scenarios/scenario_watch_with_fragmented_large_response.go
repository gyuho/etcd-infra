package scenarios

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithFragmentedLargeResponse validates watch handling of fragmented large responses.
//
//nolint:gocyclo // Scenario steps through multiple watch pipelines.
func RunWatchWithFragmentedLargeResponse(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithFragmentedLargeResponse.String())

	result := &Result{
		Scenario:  WatchWithFragmentedLargeResponse.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	// set 2 MB limit; response over 2 MB should fail without fragmentation
	recvLimit := int(2 * 1024 * 1024)

	cli, err := runner.NewClient(WithMaxCallRecvMsgSize(recvLimit))
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create client: %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	testKey := runner.GenerateRandomKey(10)

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	getRev := gresp.Header.GetRevision()

	// Write just enough data to exceed the 2 MB limit so fragmentation is still
	// required, but avoid the original ~12 MB payload to keep the test quick.
	keysN, keySize := 6, 600*1024
	keys := make([]string, keysN)
	for i := range keys {
		keys[i] = path.Join(testKey, runner.GenerateRandomKey(20))
	}
	sort.Strings(keys)

	errc := make(chan error)
	for i := range keysN {
		go func(idx int) {
			ctx, cancel := runner.NewCtx()
			_, putErr := cli.Put(ctx, keys[idx], strings.Repeat("x", keySize))
			cancel()
			if putErr != nil {
				errc <- fmt.Errorf("#%d: PUT failed: %w", idx, putErr)

				return
			}
			errc <- nil
		}(i)
	}
	// Large PUT operations with 600KB values over VPN need more time than typical operations.
	// Use 3x the default timeout to accommodate VPN encryption overhead and latency.
	putTimeout := 3 * runner.DefaultTimeout()
	timeout := time.After(putTimeout)
	for i := range keysN {
		select {
		case err = <-errc:
			if err != nil {
				return
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for puts to complete (received %d/%d)", i, keysN)

			return
		}
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	wch := cli.Watch(
		cctx,
		testKey,
		clientv3.WithRev(getRev),
		clientv3.WithPrefix(),
		clientv3.WithFragment(),
	)
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	expectedSet := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		expectedSet[k] = struct{}{}
	}

	receivedCounts := make(map[string]int, len(keys))
	uniqueSeen := 0

	missingKeys := func() []string {
		missing := make([]string, 0, len(keys)-uniqueSeen)
		for _, k := range keys {
			if receivedCounts[k] == 0 {
				missing = append(missing, k)
			}
		}

		return missing
	}

	// Large fragmented responses over VPN need significantly more time than regular
	// scenarios. Use 3x the default timeout (9 minutes) to accommodate:
	// - 600KB values * 6 keys = 3.6MB of data fragmented into many small chunks
	// - Each fragment incurs VPN tunnel overhead (WireGuard/Tailscale encryption)
	// - Cross-datacenter latency compounds with each fragment
	fragmentedTimeout := 3 * runner.DefaultTimeout()
	deadline := time.Now().Add(fragmentedTimeout)

	drainTimer := func(t *time.Timer) {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
	}

	for uniqueSeen < len(expectedSet) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for watch events; missing keys: %q", missingKeys())

			return
		}

		timer := time.NewTimer(remaining)
		select {
		case wr, open := <-wch:
			if !open {
				result.Success = false
				result.Output = fmt.Sprintf("watch channel closed early; missing keys: %q", missingKeys())
				drainTimer(timer)

				return
			}
			if wr.Canceled {
				result.Success = false
				result.Output = "watch was unexpectedly canceled"
				drainTimer(timer)

				return
			}
			if err := wr.Err(); err != nil {
				result.Success = false
				result.Output = fmt.Sprintf("watch received error: %v", err)
				drainTimer(timer)

				return
			}

			if len(wr.Events) == 0 {
				// Fragmented responses can stream empty batches (e.g., progress notifications).
				drainTimer(timer)

				continue
			}

			for _, ev := range wr.Events {
				key := string(ev.Kv.Key)
				if _, ok := expectedSet[key]; !ok {
					result.Success = false
					result.Output = fmt.Sprintf("received unexpected key: %q", key)
					drainTimer(timer)

					return
				}
				if receivedCounts[key] == 0 {
					uniqueSeen++
				}
				receivedCounts[key]++
				if receivedCounts[key] > 1 {
					result.Success = false
					result.Output = fmt.Sprintf("received duplicate key: %q", key)
					drainTimer(timer)

					return
				}
			}
			drainTimer(timer)

		case <-timer.C:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for watch events; missing keys: %q", missingKeys())

			return
		}
	}

	ccancel()
}
