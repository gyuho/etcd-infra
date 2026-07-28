package scenarios

import (
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"sort"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

var errWatcherClosed = errors.New("watcher unexpectedly closed")

// RunWatchWithMultipleWatchers verifies watch delivery across multiple watchers on same key.
//
//nolint:gocyclo // Scenario coordinates several watcher lifecycles.
func RunWatchWithMultipleWatchers(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithMultipleWatchers.String())

	result := &Result{
		Scenario:  WatchWithMultipleWatchers.String(),
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

	keyUpdates := 10
	keys := []string{
		path.Join(testKey, "foo"),
		path.Join(testKey, "bar"),
		path.Join(testKey, "baz"),
	}

	errc, readyc := make(chan error), make(chan struct{})

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	// key watchers
	for _, k := range keys {
		go func(key string) {
			wch := cli.Watch(cctx, key)
			readyc <- struct{}{}
			for i := range keyUpdates {
				wr, open := <-wch
				if !open {
					errc <- fmt.Errorf("watch channel for %q closed", key)

					return
				}
				if wr.Err() != nil {
					errc <- fmt.Errorf("watch channel for %q returned error: %w", key, wr.Err())

					return
				}

				v := fmt.Sprintf("%s-%d", key, i)
				gotv := string(wr.Events[0].Kv.Value)
				if gotv != v {
					errc <- fmt.Errorf("watch channel for %q returned wrong value: %q, expected %q", key, gotv, v)

					return
				}
			}
			errc <- nil
		}(k)
	}

	// prefix watchers
	go func() {
		wchPfx := cli.Watch(cctx, path.Join(testKey, "b"), clientv3.WithPrefix())
		readyc <- struct{}{}

		evs := []*clientv3.Event{}
		for range keyUpdates * 2 {
			wr, open := <-wchPfx
			if !open {
				errc <- errWatcherClosed

				return
			}
			evs = append(evs, wr.Events...)
		}

		// check response
		expected := []string{}
		bkeys := []string{
			path.Join(testKey, "bar"),
			path.Join(testKey, "baz"),
		}
		for _, k := range bkeys {
			for i := range keyUpdates {
				expected = append(expected, fmt.Sprintf("%s-%d", k, i))
			}
		}
		got := make([]string, 0, len(evs))
		for _, ev := range evs {
			got = append(got, string(ev.Kv.Value))
		}
		sort.Strings(got)
		if !reflect.DeepEqual(expected, got) {
			errc <- fmt.Errorf("got %v, expected %v", got, expected)

			return
		}

		// ensure no extra data
		select {
		case wr, open := <-wchPfx:
			if !open {
				errc <- errWatcherClosed

				return
			}
			errc <- fmt.Errorf("unexpected event %+v", wr)

			return

		case <-time.After(time.Second):
		}

		errc <- nil
	}()

	// wait for watchers
	for range len(keys) + 1 {
		<-readyc
	}

	// create events to watchers
	for i := range keyUpdates {
		for _, k := range keys {
			v := fmt.Sprintf("%s-%d", k, i)
			ctx, cancel := runner.NewCtx()
			_, err := cli.Put(ctx, k, v)
			cancel()
			if err != nil {
				result.Success = false
				result.Output = fmt.Sprintf("failed to put %q: %v", k, err)

				return
			}
		}
	}

	// wait for watcher shutdown
	for range len(keys) + 1 {
		if err := <-errc; err != nil {
			return
		}
	}
}
