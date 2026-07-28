package scenarios

import (
	"fmt"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/client/v3/leasing"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunLeasingPutConcurrent tests the LeasingPutConcurrent scenario.
func RunLeasingPutConcurrent(runner Runner) {
	logutil.S().Infow("running", "scenario", LeasingPutConcurrent.String())

	result := &Result{
		Scenario:  LeasingPutConcurrent.String(),
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

	// force key into leasing key cache
	ctx, cancel := runner.NewCtx()
	_, err = lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	// concurrently put through leasing client
	numPuts := 16
	rch := make(chan clientResponse, numPuts)
	for range numPuts {
		go func() {
			putCtx, putCancel := runner.NewCtx()
			presp, putErr := lKV.Put(putCtx, testKey, "bar")
			putCancel()
			rch <- clientResponse{putResp: presp, err: putErr}
		}()
	}

	// record maximum revision from puts
	maxRev := int64(0)
	timeout := time.After(10 * time.Second)
	for i := range numPuts {
		var rv clientResponse
		select {
		case rv = <-rch:
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for puts (received %d/%d)", i, numPuts)

			return
		}
		if rv.err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", rv.err)

			return
		}
		if rv.putResp.Header.GetRevision() > maxRev {
			maxRev = rv.putResp.Header.GetRevision()
		}
	}

	// confirm Get gives most recently put revisions
	ctx, cancel = runner.NewCtx()
	getResp, err := lKV.Get(ctx, testKey)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	if mr := getResp.Kvs[0].ModRevision; mr != maxRev {
		result.Success = false
		result.Output = fmt.Sprintf("revision mismatch: %d vs %d", mr, maxRev)

		return
	}
	if ver := getResp.Kvs[0].Version; ver != int64(numPuts) {
		result.Success = false
		result.Output = fmt.Sprintf("version mismatch: %d vs %d", ver, numPuts)

		return
	}
}
