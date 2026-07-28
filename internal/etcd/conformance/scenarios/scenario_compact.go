package scenarios

import (
	"errors"
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunCompact runs the Compact scenario.
//
//nolint:gocyclo // RunCompact intentionally exercises many etcd API paths within a single scenario.
func RunCompact(runner Runner) {
	logutil.S().Infow("running", "scenario", Compact.String())

	result := &Result{
		Scenario:  Compact.String(),
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

	keysN := 10
	lastRev := int64(0)
	for range keysN {
		ctx, cancel := runner.NewCtx()
		presp, putErr := cli.Put(ctx, testKey, "bar")
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", putErr)

			return
		}

		curRev := presp.Header.GetRevision()
		if curRev <= lastRev {
			result.Success = false
			result.Output = fmt.Sprintf("revision not monotonically increasing: current %d, previous %d", curRev, lastRev)

			return
		}

		lastRev = curRev
	}

	ctx, cancel := runner.NewCtx()
	_, err = cli.Compact(ctx, lastRev)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, lastRev)
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted, got %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, lastRev+100)
	cancel()
	if !errors.Is(err, rpctypes.ErrFutureRev) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted, got %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	presp, err := cli.Put(ctx, testKey, "bar1")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev1 := presp.Header.GetRevision()

	ctx, cancel = runner.NewCtx()
	presp, err = cli.Put(ctx, testKey, leasingNewValue)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev2 := presp.Header.GetRevision()

	// NOTE: We don't check `putRev2 == putRev1+1` because in a live Kubernetes
	// cluster, concurrent etcd operations (kubelet heartbeats, controller
	// reconciliations) can advance the revision. The test verifies compaction
	// behavior, not sequential revision ordering.
	if putRev2 <= putRev1 {
		result.Success = false
		result.Output = fmt.Sprintf("revision not monotonically increasing: putRev2 %d <= putRev1 %d", putRev2, putRev1)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithRev(putRev1))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != "bar1" {
		result.Success = false
		result.Output = "expected value bar1, got " + string(gresp.Kvs[0].Value)

		return
	}
	if gresp.Kvs[0].ModRevision != putRev1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected mod revision %d, got %d", putRev1, gresp.Kvs[0].ModRevision)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey, clientv3.WithRev(putRev2))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != leasingNewValue {
		result.Success = false
		result.Output = fmt.Sprintf("expected value %s, got %s", leasingNewValue, string(gresp.Kvs[0].Value))

		return
	}
	if gresp.Kvs[0].ModRevision != putRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected mod revision %d, got %d", putRev1, gresp.Kvs[0].ModRevision)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, putRev2, clientv3.WithCompactPhysical())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to compact: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, putRev2, clientv3.WithCompactPhysical())
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted, got %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Compact(ctx, putRev2+100, clientv3.WithCompactPhysical())
	cancel()
	if !errors.Is(err, rpctypes.ErrFutureRev) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted, got %v", err)

		return
	}

	// compact was done with "putRev2", thus "putRev1" is not available
	ctx, cancel = runner.NewCtx()
	_, err = cli.Get(ctx, testKey, clientv3.WithRev(putRev1))
	cancel()
	if !errors.Is(err, rpctypes.ErrCompacted) {
		result.Success = false
		result.Output = fmt.Sprintf("expected ErrCompacted, got %v", err)

		return
	}

	// compact was done with "putRev2", thus "putRev2" is still available
	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey, clientv3.WithRev(putRev2))
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != leasingNewValue {
		result.Success = false
		result.Output = fmt.Sprintf("expected value %s, got %s", leasingNewValue, string(gresp.Kvs[0].Value))

		return
	}
	if gresp.Kvs[0].ModRevision != putRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected mod revision %d, got %d", putRev2, gresp.Kvs[0].ModRevision)

		return
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("expected 1 key, got %d", len(gresp.Kvs))

		return
	}
	if string(gresp.Kvs[0].Value) != leasingNewValue {
		result.Success = false
		result.Output = fmt.Sprintf("expected value %s, got %s", leasingNewValue, string(gresp.Kvs[0].Value))

		return
	}
	if gresp.Kvs[0].ModRevision != putRev2 {
		result.Success = false
		result.Output = fmt.Sprintf("expected mod revision %d, got %d", putRev2, gresp.Kvs[0].ModRevision)

		return
	}
}
