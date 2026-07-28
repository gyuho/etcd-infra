package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/clientv3util"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTxnKeyMissing tests the TxnKeyMissing scenario.
func RunTxnKeyMissing(runner Runner) {
	logutil.S().Infow("running", "scenario", TxnKeyMissing.String())

	result := &Result{
		Scenario:  TxnKeyMissing.String(),
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
	tresp, err := cli.Txn(ctx).
		If(clientv3util.KeyMissing(testKey)).
		Then(clientv3.OpPut(testKey, "bar")).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to execute txn: %v", err)

		return
	}
	txnRev := tresp.Header.GetRevision()

	kvs := &mvccpb.KeyValue{
		Key:            []byte(testKey),
		Value:          []byte("bar"),
		CreateRevision: txnRev,
		ModRevision:    txnRev,
		Version:        1,
	}

	ctx, cancel = runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey)
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
	if !proto.Equal(kvs, gresp.Kvs[0]) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %+v, got %+v", kvs, gresp.Kvs[0])

		return
	}
}
