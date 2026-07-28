package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunLeasingTxnOwnerGet tests the LeasingTxnOwnerGet scenario.
func RunLeasingTxnOwnerGet(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingTxnOwnerGet.String())

	result := &Result{
		Scenario:  LeasingTxnOwnerGet.String(),
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

	keyCount := randutil.Intn(10) + 1
	ops := make([]clientv3.Op, 0, keyCount)
	presps := make([]*clientv3.PutResponse, keyCount)
	keys := make([]string, keyCount)
	for i := range presps {
		// Use prefixed keys to ensure test isolation in live clusters
		k := fmt.Sprintf("%s/k-%d", testPfx, i)
		keys[i] = k
		ctx, cancel := runner.NewCtx()
		presp, putErr := cli.Put(ctx, k, k+k)
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", putErr)

			return
		}
		presps[i] = presp

		ctx, cancel = runner.NewCtx()
		_, err = lKV.Get(ctx, k)
		cancel()
		if err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get: %v", err)

			return
		}
		ops = append(ops, clientv3.OpGet(k))
	}
	ops = ops[:randutil.Intn(len(ops))]

	var thenOps, elseOps []clientv3.Op
	cmps, useThen := createRandCmps(testPfx+"/k-", presps)
	if useThen {
		thenOps = ops
		elseOps = []clientv3.Op{clientv3.OpPut(testPfx+"/k", "1")}
	} else {
		thenOps = []clientv3.Op{clientv3.OpPut(testPfx+"/k", "1")}
		elseOps = ops
	}

	ctx, cancel := runner.NewCtx()
	tresp, err := lKV.Txn(ctx).
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
	if tresp.Succeeded != useThen {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch, expected %v, got %v", useThen, tresp.Succeeded)

		return
	}
	if len(tresp.Responses) != len(ops) {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch, expected %d responses, got %d", len(ops), len(tresp.Responses))

		return
	}
	wrev := presps[len(presps)-1].Header.GetRevision()
	if tresp.Header.GetRevision() < wrev {
		result.Success = false
		result.Output = fmt.Sprintf("cache mismatch, expected revision >= %d, got %d", wrev, tresp.Header.GetRevision())

		return
	}

	for i := range ops {
		k := keys[i]
		rr := tresp.Responses[i].GetResponseRange()
		if rr == nil {
			result.Success = false
			result.Output = fmt.Sprintf("cache mismatch, expected response for %q", k)

			return
		}
		if string(rr.Kvs[0].Key) != k || string(rr.Kvs[0].Value) != k+k {
			result.Success = false
			result.Output = fmt.Sprintf("cache mismatch for %q: %+v", k, rr)

			return
		}
	}
}
