package scenarios

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithTooLargeResponse ensures watches fail gracefully on oversized responses.
//
//nolint:gocyclo // Scenario walks through multiple watch/error paths.
func RunWatchWithTooLargeResponse(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithTooLargeResponse.String())

	result := &Result{
		Scenario:  WatchWithTooLargeResponse.String(),
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
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	getRev := gresp.Header.GetRevision()

	// write >10 MB data
	keys, keySize := 12, 1024*1024
	errc := make(chan error)
	for i := range keys {
		go func(idx int) {
			ctx, cancel := runner.NewCtx()
			_, putErr := cli.Put(ctx, path.Join(testKey, fmt.Sprintf("%03d", idx)), strings.Repeat("0", keySize))
			cancel()
			if putErr != nil {
				errc <- fmt.Errorf("#%d: PUT failed: %w", idx, putErr)

				return
			}
			errc <- nil
		}(i)
	}
	// Use generous timeout for cloud/VPN environments where large PUT operations
	// (12 x 1MB) can have significant latency over high-latency networks; the
	// slow-path multiplier extends it for SSM port-forwarding, where 1 MB puts
	// through the tunnel take ~15s each under concurrency (observed: 8/12 in
	// 120s).
	timeout := time.After(testtime.ScaleDuration(120 * time.Second))
	for i := range keys {
		select {
		case err = <-errc:
			if err != nil {
				return
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for puts to complete (received %d/%d)", i, keys)

			return
		}
	}

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	wch := cli.Watch(cctx, testKey, clientv3.WithRev(getRev), clientv3.WithPrefix())
	if wch == nil {
		result.Success = false
		result.Output = watchCreateFailedMsg

		return
	}

	select {
	case wr, open := <-wch:
		if !open {
			result.Success = false
			result.Output = watchChannelClosedMsg

			return
		}
		if !wr.Canceled {
			result.Success = false
			result.Output = "watch channel is not canceled for writing >10 MB data"

			return
		}
		if len(wr.Events) > 0 {
			result.Success = false
			result.Output = "watch channel received events"

			return
		}

		err := wr.Err()
		exp := "code = ResourceExhausted desc = grpc: received message larger than max ("
		if !strings.Contains(err.Error(), exp) {
			result.Success = false
			result.Output = fmt.Sprintf("expected error containing %q, got %q", exp, err)

			return
		}

	case <-time.After(3 * time.Second):
		result.Success = false
		result.Output = "watch channel did not receive any events"

		return
	}

	select {
	case wr, open := <-wch:
		if open {
			result.Success = false
			result.Output = "watch channel is not closed after watch error"

			return
		}
		if wr.Canceled {
			result.Success = false
			result.Output = "watch channel is canceled after watch error"

			return
		}
		if len(wr.Events) > 0 {
			result.Success = false
			result.Output = "watch channel received events after watch error, expected none"

			return
		}

	case <-time.After(3 * time.Second):
		result.Success = false
		result.Output = "watch channel did not receive more events after watch error"

		return
	}
}
