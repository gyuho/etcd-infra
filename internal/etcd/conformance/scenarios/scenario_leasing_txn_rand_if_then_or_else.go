package scenarios

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunLeasingTxnRandIfThenOrElse exercises random if/then/else leasing transactions.
//
//nolint:gocyclo // Scenario intentionally covers many transaction branches.
func RunLeasingTxnRandIfThenOrElse(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingTxnRandIfThenOrElse.String())

	result := &Result{
		Scenario:  LeasingTxnRandIfThenOrElse.String(),
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

	testPfx := runner.GenerateRandomKey(10)
	basePrefix := testPfx + "-k-"

	lKV1, closeLK1, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLK1()

	lKV2, closeLK2, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLK2()

	keyCount := 16
	dat := make([]*clientv3.PutResponse, keyCount)
	for i := range keyCount {
		k, v := fmt.Sprintf("%s%d", basePrefix, i), strconv.FormatInt(int64(i), 10)
		ctx, cancel := runner.NewCtx()
		dat[i], err = cli.Put(ctx, k, v)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", err)

			return
		}
	}

	// non-deterministically populate leasing caches
	var wg sync.WaitGroup
	errc := make(chan error, keyCount)
	getRandom := func(kv clientv3.KV) {
		defer wg.Done()
		for range keyCount / 2 {
			k := fmt.Sprintf("%s%d", basePrefix, randutil.Intn(keyCount))
			ctx, cancel := runner.NewCtx()
			_, getErr := kv.Get(ctx, k)
			cancel()
			if getErr != nil {
				errc <- fmt.Errorf("get failed: %w", getErr)
			} else {
				errc <- nil
			}
		}
	}
	wg.Add(2)
	defer wg.Wait()
	go getRandom(lKV1)
	go getRandom(lKV2)

	// random list of comparisons, all true
	cmps, useThen := createRandCmps(basePrefix, dat)
	// random list of puts/gets; unique keys
	ops := make([]clientv3.Op, 0, keyCount)
	usedIdx := make(map[int]struct{})
	for range keyCount {
		idx := randutil.Intn(keyCount)
		if _, ok := usedIdx[idx]; ok {
			continue
		}
		usedIdx[idx] = struct{}{}
		k := fmt.Sprintf("%s%d", basePrefix, idx)
		switch randutil.Intn(2) {
		case 0:
			ops = append(ops, clientv3.OpGet(k))
		case 1:
			ops = append(ops, clientv3.OpPut(k, "a"))
		}
	}
	// random lengths
	ops = ops[:randutil.Intn(len(ops))]

	// wait for all gets to populate the leasing caches before committing
	timeout := time.After(10 * time.Second)
	for i := range keyCount {
		select {
		case err = <-errc:
			if err != nil {
				return
			}
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for cache population (received %d/%d)", i, keyCount)

			return
		}
	}

	// randomly choose between then and else blocks
	var thenOps, elseOps []clientv3.Op
	if useThen {
		thenOps = ops
	} else {
		// force failure
		elseOps = ops
	}

	ctx, cancel := runner.NewCtx()
	tresp, err := lKV1.Txn(ctx).
		If(cmps...).
		Then(thenOps...).
		Else(elseOps...).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to txn: %v", err)

		return
	}
	// cmps always succeed
	if tresp.Succeeded != useThen {
		result.Success = false
		result.Output = fmt.Sprintf("expected succeeded=%v, got tresp=%+v", useThen, tresp)

		return
	}

	// get should match what was put
	checkPuts := func(kv clientv3.KV) error {
		for i := range ops {
			op := &ops[i]
			if !op.IsPut() {
				continue
			}
			ctx, cancel := runner.NewCtx()
			resp, getErr := kv.Get(ctx, string(op.KeyBytes()))
			cancel()
			if getErr != nil {
				return fmt.Errorf("get failed: %w", getErr)
			}
			if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != "a" {
				return fmt.Errorf(`get expected value="a", got %+v`, resp.Kvs)
			}
		}

		return nil
	}
	if err = checkPuts(cli); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("checkPuts failed with client: %v", err)

		return
	}
	if err = checkPuts(lKV1); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("checkPuts failed with lKV1: %v", err)

		return
	}
	if err = checkPuts(lKV2); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("checkPuts failed with lKV2: %v", err)

		return
	}
}
