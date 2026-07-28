package scenarios

import (
	"fmt"
	"path"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/namespace"
	"google.golang.org/protobuf/proto"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchAndPutWithNamespace tests the WatchAndPutWithNamespace scenario.
func RunWatchAndPutWithNamespace(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchAndPutWithNamespace.String())

	result := &Result{
		Scenario:  WatchAndPutWithNamespace.String(),
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
	nsKV := namespace.NewKV(cli.KV, testKey+"/")
	nsWatcher := namespace.NewWatcher(cli.Watcher, testKey+"/")

	ctx, cancel := runner.NewCtx()
	presp, err := nsKV.Put(ctx, "abc", "bar")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	putRev := presp.Header.GetRevision()

	wkv1 := &mvccpb.KeyValue{
		Key:            []byte("abc"),
		Value:          []byte("bar"),
		CreateRevision: putRev,
		ModRevision:    putRev,
		Version:        1,
	}

	// whole test shouldn't take more than 15 seconds
	ctx, cancel = runner.NewCtxTimeout(15 * time.Second)
	defer cancel()

	wch1 := nsWatcher.Watch(ctx, "abc", clientv3.WithRev(putRev))
	if wr := <-wch1; len(wr.Events) != 1 || !proto.Equal(wr.Events[0].Kv, wkv1) {
		result.Success = false
		result.Output = fmt.Sprintf("watch response mismatch, expected %+v, got %+v", wkv1, wr)

		return
	}

	wkv2 := &mvccpb.KeyValue{
		Key:            []byte(path.Join(testKey, "abc")),
		Value:          []byte("bar"),
		CreateRevision: putRev,
		ModRevision:    putRev,
		Version:        1,
	}
	wch2 := cli.Watch(ctx, path.Join(testKey, "abc"), clientv3.WithRev(putRev))
	if wr := <-wch2; len(wr.Events) != 1 || !proto.Equal(wr.Events[0].Kv, wkv2) {
		result.Success = false
		result.Output = fmt.Sprintf("watch response mismatch, expected %+v, got %+v", wkv2, wr)

		return
	}

	cerr := nsWatcher.Close()
	if cerr != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to close watcher: %v", cerr)

		return
	}
}
