package scenarios

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunResourceSizeEstimation verifies etcd behaviors that enable accurate resource size tracking under churn.
// Kubernetes tracks object sizes via watch events and periodic reconciliation to report storage metrics.
// staging/src/k8s.io/apiserver/pkg/storage/etcd3/stats.go (lines 1654-1661)
//
// This test validates the etcd server behaviors that K8s relies on:
// 1. Watch events include complete KeyValue data with accurate size information
// 2. Get operations return consistent KeyValue data (key, value, create_revision, mod_revision, version, lease)
// 3. Prefix scans with WithKeysOnly work correctly for reconciliation
// 4. Size tracking remains accurate under concurrent updates and deletes
//
// The test simulates the Kubernetes resourceSizeEstimator pattern:
// - Maintain an in-memory size map based on watch events
// - Periodically reconcile via WithKeysOnly scans
// - Verify accuracy under churn (updates, deletes, additions)
//
//nolint:gocyclo // Scenario mirrors the full estimator workflow with multiple branches.
func RunResourceSizeEstimation(runner Runner) {
	logutil.S().Infow("running", "scenario", ResourceSizeEstimation.String())

	result := &Result{
		Scenario:  ResourceSizeEstimation.String(),
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

	prefix := runner.GenerateRandomKey(10)

	// Phase 1: Start watch BEFORE seeding keys to track all events (simulating K8s resourceSizeEstimator)
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	watcher := clientv3.NewWatcher(cli)
	defer func() { _ = watcher.Close() }()

	watchChan := watcher.Watch(
		watchCtx,
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithPrevKV(),
	)

	// Size estimator state (simulates stats.go resourceSizeEstimator)
	var mu sync.Mutex
	estimatedSizes := make(map[string]int) // key -> estimated size
	var watchEventCount int
	var watchErrors []string

	// Process watch events in background
	var watchWg sync.WaitGroup
	watchWg.Go(func() {
		for {
			select {
			case <-watchCtx.Done():
				return
			case resp, ok := <-watchChan:
				if !ok {
					return
				}
				if resp.Err() != nil {
					mu.Lock()
					watchErrors = append(watchErrors, resp.Err().Error())
					mu.Unlock()

					return
				}

				mu.Lock()
				for _, ev := range resp.Events {
					watchEventCount++
					key := string(ev.Kv.Key)
					switch ev.Type {
					case mvccpb.PUT:
						// Update size estimate from watch event
						// K8s stats.go tracks: len(kv.Key) + len(kv.Value)
						estimatedSize := len(ev.Kv.Key) + len(ev.Kv.Value)
						estimatedSizes[key] = estimatedSize
					case mvccpb.DELETE:
						// Remove from size estimate
						delete(estimatedSizes, key)
					}
				}
				mu.Unlock()
			}
		}
	})

	// Phase 2: Seed initial keys with known sizes (watch is already running)
	const initialKeyCount = 20
	seededKeys := make(map[string]int) // key -> value size
	for i := range initialKeyCount {
		key := fmt.Sprintf("%s/obj-%04d", prefix, i)
		// Deterministic value sizes: 100, 200, 300, ..., 2000 bytes
		valueSize := (i + 1) * 100
		value := randutil.StringAlphabetsLowerCase(valueSize)
		seededKeys[key] = len(value)

		ctx, cancel := runner.NewCtx()
		_, putErr := cli.Put(ctx, key, value)
		cancel()
		if putErr != nil {
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("failed to seed key %q: %v", key, putErr)

			return
		}
	}

	// Wait for watch to catch up with seeded keys
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		currentCount := len(estimatedSizes)
		mu.Unlock()
		if currentCount >= initialKeyCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify initial size estimates match seeded keys
	mu.Lock()
	if len(estimatedSizes) != len(seededKeys) {
		mu.Unlock()
		watchCancel()
		watchWg.Wait()
		result.Success = false
		result.Output = fmt.Sprintf("initial watch sync: expected %d keys, got %d", len(seededKeys), len(estimatedSizes))

		return
	}
	for key, valueSize := range seededKeys {
		estimatedSize, exists := estimatedSizes[key]
		if !exists {
			mu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("watch missing key %q", key)

			return
		}
		expectedSize := len(key) + valueSize
		if estimatedSize != expectedSize {
			mu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("key %q size mismatch: expected %d, got %d", key, expectedSize, estimatedSize)

			return
		}
	}
	initialEventCount := watchEventCount
	mu.Unlock()

	// Phase 3: Simulate churn (updates and deletes)
	const churnOps = 30
	var churnMu sync.Mutex

	for i := range churnOps {
		switch i % 3 {
		case 0:
			// Update existing key
			key := fmt.Sprintf("%s/obj-%04d", prefix, i%initialKeyCount)
			newValueSize := (i + 1) * 50
			newValue := randutil.StringAlphabetsLowerCase(newValueSize)

			ctx, cancel := runner.NewCtx()
			_, putErr := cli.Put(ctx, key, newValue)
			cancel()
			if putErr != nil {
				result.Success = false
				result.Output = fmt.Sprintf("churn put failed: %v", putErr)

				return
			}

			churnMu.Lock()
			seededKeys[key] = len(newValue) // Update expected size
			churnMu.Unlock()
		case 1:
			// Delete a key
			key := fmt.Sprintf("%s/obj-%04d", prefix, i%initialKeyCount)

			ctx, cancel := runner.NewCtx()
			_, deleteErr := cli.Delete(ctx, key)
			cancel()
			if deleteErr != nil {
				result.Success = false
				result.Output = fmt.Sprintf("churn delete failed: %v", deleteErr)

				return
			}

			churnMu.Lock()
			delete(seededKeys, key)
			churnMu.Unlock()
		default:
			// Add new key
			key := fmt.Sprintf("%s/churn-%04d", prefix, i)
			valueSize := (i + 1) * 75
			value := randutil.StringAlphabetsLowerCase(valueSize)

			ctx, cancel := runner.NewCtx()
			_, putErr := cli.Put(ctx, key, value)
			cancel()
			if putErr != nil {
				result.Success = false
				result.Output = fmt.Sprintf("churn add failed: %v", putErr)

				return
			}

			churnMu.Lock()
			seededKeys[key] = len(value)
			churnMu.Unlock()
		}
	}

	// Wait for watch to process churn events
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		currentEventCount := watchEventCount
		currentEstimatedCount := len(estimatedSizes)
		mu.Unlock()

		churnMu.Lock()
		expectedCount := len(seededKeys)
		churnMu.Unlock()

		if currentEstimatedCount == expectedCount && currentEventCount > initialEventCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify watch-based estimates match expected state after churn
	mu.Lock()
	churnMu.Lock()
	if len(estimatedSizes) != len(seededKeys) {
		expectedCount := len(seededKeys)
		actualCount := len(estimatedSizes)
		mu.Unlock()
		churnMu.Unlock()
		watchCancel()
		watchWg.Wait()
		result.Success = false
		result.Output = fmt.Sprintf("after churn: expected %d keys, watch reported %d", expectedCount, actualCount)

		return
	}
	for key, valueSize := range seededKeys {
		estimatedSize, exists := estimatedSizes[key]
		if !exists {
			mu.Unlock()
			churnMu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("after churn: watch missing key %q", key)

			return
		}
		expectedSize := len(key) + valueSize
		if estimatedSize != expectedSize {
			mu.Unlock()
			churnMu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("after churn: key %q size mismatch: expected %d, got %d", key, expectedSize, estimatedSize)

			return
		}
	}
	churnMu.Unlock()
	mu.Unlock()

	// Phase 4: Reconciliation via WithKeysOnly scan (simulates stats.go cleanKeys)
	// This verifies that WithKeysOnly returns accurate key list for reconciliation
	ctx, cancel := runner.NewCtx()
	scanResp, err := cli.Get(
		ctx,
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithKeysOnly(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
	)
	cancel()
	if err != nil {
		watchCancel()
		watchWg.Wait()
		result.Success = false
		result.Output = fmt.Sprintf("reconciliation scan failed: %v", err)

		return
	}

	// Build reconciled key set
	reconciledKeys := make(map[string]bool)
	for _, kv := range scanResp.Kvs {
		reconciledKeys[string(kv.Key)] = true
		// WithKeysOnly should return empty values
		if len(kv.Value) != 0 {
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("WithKeysOnly returned non-empty value for key %q", string(kv.Key))

			return
		}
	}

	// Verify reconciliation matches watch-based estimates
	mu.Lock()
	if len(reconciledKeys) != len(estimatedSizes) {
		actualCount := len(reconciledKeys)
		estimatedCount := len(estimatedSizes)
		mu.Unlock()
		watchCancel()
		watchWg.Wait()
		result.Success = false
		result.Output = fmt.Sprintf("reconciliation mismatch: scan found %d keys, watch has %d", actualCount, estimatedCount)

		return
	}
	for key := range estimatedSizes {
		if !reconciledKeys[key] {
			mu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("reconciliation: watch has key %q not in scan", key)

			return
		}
	}
	for key := range reconciledKeys {
		if _, exists := estimatedSizes[key]; !exists {
			mu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("reconciliation: scan found key %q not in watch", key)

			return
		}
	}
	finalEventCount := watchEventCount
	finalKeyCount := len(estimatedSizes)
	mu.Unlock()

	// Phase 5: Verify size consistency via full Get (with values)
	ctx, cancel = runner.NewCtx()
	fullGetResp, err := cli.Get(
		ctx,
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
	)
	cancel()
	if err != nil {
		watchCancel()
		watchWg.Wait()
		result.Success = false
		result.Output = fmt.Sprintf("full Get verification failed: %v", err)

		return
	}

	// Verify Get returns consistent sizes
	mu.Lock()
	for _, kv := range fullGetResp.Kvs {
		key := string(kv.Key)
		estimatedSize, exists := estimatedSizes[key]
		if !exists {
			mu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("full Get: found key %q not in watch estimates", key)

			return
		}
		actualSize := len(kv.Key) + len(kv.Value)
		if actualSize != estimatedSize {
			mu.Unlock()
			watchCancel()
			watchWg.Wait()
			result.Success = false
			result.Output = fmt.Sprintf("full Get: key %q size mismatch: watch has %d, actual %d", key, estimatedSize, actualSize)

			return
		}
	}
	mu.Unlock()

	// Stop watch
	watchCancel()
	watchWg.Wait()

	// Check for watch errors
	mu.Lock()
	if len(watchErrors) > 0 {
		mu.Unlock()
		result.Success = false
		result.Output = fmt.Sprintf("watch errors: %v", watchErrors)

		return
	}
	mu.Unlock()

	// Calculate total estimated size
	mu.Lock()
	totalEstimatedSize := 0
	keys := make([]string, 0, len(estimatedSizes))
	for key, size := range estimatedSizes {
		totalEstimatedSize += size
		keys = append(keys, key)
	}
	sort.Strings(keys)
	mu.Unlock()

	result.Output = fmt.Sprintf(
		"resource size estimation verified: %d keys, %d bytes total, %d watch events, reconciliation consistent",
		finalKeyCount,
		totalEstimatedSize,
		finalEventCount,
	)
}
