package scenarios

import (
	"fmt"
	"path"
	"reflect"
	"sort"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnPutMultiple tests the TxnPutMultiple scenario.
func RunTxnPutMultiple(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnPutMultiple.String())

	result := &Result{
		Scenario:  TxnPutMultiple.String(),
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

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	lastRev := gresp.Header.GetRevision()

	// ref. go.etcd.io/etcd/server/v3/server/embed.DefaultMaxTxnOps is 128
	keysN := 127
	pairs := make([]keyValue, keysN)
	for i := range pairs {
		pairs[i] = keyValue{
			k: path.Join(testKey, runner.GenerateRandomKey(12)),
			v: runner.GenerateRandomKey(20),
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].k < pairs[j].k
	})

	writes := make([]clientv3.Op, keysN)
	for i := range writes {
		k, v := pairs[i].k, pairs[i].v
		writes[i] = clientv3.OpPut(k, v)
	}
	ctx, cancel = runner.NewCtx()
	tresp, err := cli.Txn(ctx).Then(writes...).Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}
	// In a shared etcd cluster (e.g., with Kubernetes), other writes may have occurred
	// between our GET and TXN, so we only verify the TXN completed (revision increased).
	txnRev := tresp.Header.GetRevision()
	if txnRev <= lastRev {
		result.Success = false
		result.Output = fmt.Sprintf("expected txn revision > %d, got %d", lastRev, txnRev)

		return
	}

	// Build expected KVs using the actual TXN revision
	kvs := make([]*mvccpb.KeyValue, keysN)
	for i := range kvs {
		k, v := pairs[i].k, pairs[i].v
		kvs[i] = &mvccpb.KeyValue{
			Key:            []byte(k),
			Value:          []byte(v),
			CreateRevision: txnRev,
			ModRevision:    txnRev,
			Version:        1,
		}
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(
		ctx,
		testKey,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if len(gresp.Kvs) != keysN {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d keys, got %d", keysN, len(gresp.Kvs))

		return
	}
	if !reflect.DeepEqual(kvs, gresp.Kvs) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %+v, got %+v", kvs, gresp.Kvs)

		return
	}
}
