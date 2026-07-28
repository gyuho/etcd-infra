package scenarios

import (
	"fmt"
	"path"
	"reflect"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithPrefix tests the GetWithPrefix scenario.
func RunGetWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithPrefix.String())

	result := &Result{
		Scenario:  GetWithPrefix.String(),
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

	kvs := []*mvccpb.KeyValue{}
	for i := range 10 {
		k := path.Join(testKey, fmt.Sprintf("%02d", i))

		ctx, cancel := runner.NewCtx()
		presp, putErr := cli.Put(ctx, k, "")
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", putErr)

			return
		}
		kvs = append(kvs, &mvccpb.KeyValue{
			Key:            []byte(k),
			Value:          nil,
			CreateRevision: presp.Header.GetRevision(),
			ModRevision:    presp.Header.GetRevision(),
			Version:        1,
		})
	}

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(gresp.Kvs) != len(kvs) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %d keys, got %d", len(kvs), len(gresp.Kvs))

		return
	}

	if !reflect.DeepEqual(kvs, gresp.Kvs) {
		result.Success = false
		result.Output = fmt.Sprintf("expected %+v, got %+v", kvs, gresp.Kvs)

		return
	}
}
