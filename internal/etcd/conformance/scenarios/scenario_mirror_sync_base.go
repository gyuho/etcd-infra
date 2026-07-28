package scenarios

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"sort"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/mirror"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunMirrorSyncBase tests the MirrorSyncBase scenario.
func RunMirrorSyncBase(runner Runner) {
	logutil.S().Infow("running", "scenario", MirrorSyncBase.String())

	result := &Result{
		Scenario:  MirrorSyncBase.String(),
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

	keyN := 2000
	keys := make([]string, keyN)
	for i := range keys {
		keys[i] = path.Join(testKey, fmt.Sprintf("test%05d", i))
	}

	kch := make(chan string, 50)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for k := range kch {
				ctx, cancel := runner.NewCtx()
				_, err := cli.Put(ctx, k, "bar")
				cancel()
				if err != nil {
					result.Success = false
					result.Output = fmt.Sprintf("failed to put: %v", err)

					return
				}
			}
		})
	}
	for _, k := range keys {
		kch <- k
	}
	close(kch)
	wg.Wait()

	syncer := mirror.NewSyncer(cli, testKey, 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rch, errc := syncer.SyncBase(ctx)
	receivedKeys := make([]string, 0, keyN)

	// Read from rch with timeout. Use runner's default timeout for cross-DC environments.
	timeout := time.After(runner.DefaultTimeout())
readLoop:
	for {
		select {
		case rv, open := <-rch:
			if !open {
				break readLoop
			}
			for _, kv := range rv.Kvs {
				receivedKeys = append(receivedKeys, string(kv.Key))
			}
			if !rv.More {
				break readLoop
			}
		case <-timeout:
			result.Success = false
			result.Output = syncResponseTimeoutMsg

			return
		}
	}

	// Read from errc with timeout
	errTimeout := time.After(5 * time.Second)
errLoop:
	for {
		select {
		case err, open := <-errc:
			if !open {
				break errLoop
			}
			if err != nil {
				result.Success = false
				result.Output = fmt.Sprintf("failed to sync: %v", err)

				return
			}
		case <-errTimeout:
			result.Success = false
			result.Output = syncErrorChannelTimeoutMsg

			return
		}
	}
	received := len(receivedKeys)
	if received != keyN {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d keys, got %d", keyN, received)

		return
	}
	sort.Strings(receivedKeys)

	if !reflect.DeepEqual(keys, receivedKeys) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %v, got %v", keys, receivedKeys)

		return
	}
}
