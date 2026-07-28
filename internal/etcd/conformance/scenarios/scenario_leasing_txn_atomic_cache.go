package scenarios

import (
	"fmt"
	"path"
	"strconv"
	"sync"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingTxnAtomicCache tests the LeasingTxnAtomicCache scenario.
func RunLeasingTxnAtomicCache(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingTxnAtomicCache.String())

	result := &Result{
		Scenario:  LeasingTxnAtomicCache.String(),
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

	lKV, closeLKV, err := leasing.NewKV(cli, testPfx)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create leasing KV: %v", err)

		return
	}
	defer closeLKV()

	testKey := runner.GenerateRandomKey(10)

	puts, gets := make([]clientv3.Op, 16), make([]clientv3.Op, 16)
	for i := range puts {
		k := path.Join(testKey, strconv.Itoa(i))
		puts[i], gets[i] = clientv3.OpPut(k, k), clientv3.OpGet(k)
	}

	ctx, cancel := runner.NewCtx()
	_, err = cli.Txn(ctx).Then(puts...).Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to txn: %v", err)

		return
	}
	for i := range gets {
		ctx, cancel = runner.NewCtx()
		_, err = lKV.Do(ctx, gets[i])
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get: %v", err)

			return
		}
	}

	numPuts, numGets := 16, 16

	var (
		wgPuts sync.WaitGroup
		wgGets sync.WaitGroup
	)
	wgPuts.Add(numPuts)
	wgGets.Add(numGets)

	f := func(errc chan error) {
		defer wgPuts.Done()
		for range 10 {
			ctx, cancel := runner.NewCtx()
			_, commitErr := lKV.Txn(ctx).Then(puts...).Commit()
			cancel()
			if commitErr != nil {
				errc <- commitErr

				return
			}
		}
		errc <- nil
	}

	donec := make(chan struct{}, numPuts)
	g := func(errc chan error) {
		defer wgGets.Done()
		for {
			select {
			case <-donec:
				errc <- nil

				return
			default:
			}

			ctx, cancel := runner.NewCtx()
			tresp, txnErr := lKV.Txn(ctx).Then(gets...).Commit()
			cancel()
			if txnErr != nil {
				errc <- txnErr

				return
			}
			revs := make([]int64, len(gets))
			for i, resp := range tresp.Responses {
				rr := resp.GetResponseRange()
				revs[i] = rr.Kvs[0].ModRevision
			}
			for i := 1; i < len(revs); i++ {
				if revs[i] != revs[i-1] {
					errc <- fmt.Errorf("expected matching revisions, got %+v", revs)

					return
				}
			}
		}
	}

	errc := make(chan error, numGets+numPuts)
	for range numGets {
		go g(errc)
	}
	for range numPuts {
		go f(errc)
	}

	wgPuts.Wait()
	close(donec)
	wgGets.Wait()

	for range numGets + numPuts {
		err = <-errc
		if err != nil {
			return
		}
	}
}
