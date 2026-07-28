package scenarios

import (
	"context"
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunWatchWithFilter tests the WatchWithFilter scenario.
func RunWatchWithFilter(runner Runner) {
	logutil.S().Infow("running", "scenario", WatchWithFilter.String())

	result := &Result{
		Scenario:  WatchWithFilter.String(),
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

	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()

	testKey := runner.GenerateRandomKey(10)

	wcNoPut := cli.Watch(cctx, testKey, clientv3.WithFilterPut())
	wcNoDel := cli.Watch(cctx, testKey, clientv3.WithFilterDelete())

	ctx, cancel := runner.NewCtx()
	_, err = cli.Put(ctx, testKey, "abc")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}

	ctx, cancel = runner.NewCtx()
	_, err = cli.Delete(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to delete: %v", err)

		return
	}

	npResp := <-wcNoPut
	if len(npResp.Events) != 1 || npResp.Events[0].Type != clientv3.EventTypeDelete {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected event on filtered put (%+v)", npResp)

		return
	}

	ndResp := <-wcNoDel
	if len(ndResp.Events) != 1 || ndResp.Events[0].Type != clientv3.EventTypePut {
		result.Success = false
		result.Output = fmt.Sprintf("unexpected event on filtered delete (%+v)", ndResp)

		return
	}

	select {
	case resp := <-wcNoPut:
		result.Success = false
		result.Output = fmt.Sprintf("unexpected event on unfiltered put (%+v)", resp)

		return

	case resp := <-wcNoDel:
		result.Success = false
		result.Output = fmt.Sprintf("unexpected event on unfiltered delete (%+v)", resp)

		return

	case <-time.After(100 * time.Millisecond):
	}
}
