package scenarios

import (
	"fmt"
	"strings"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunPutLinearizability verifies linearizable writes across endpoints.
//
//nolint:gocyclo // Scenario emulates client failover and repeated assertions.
func RunPutLinearizability(runner Runner) {
	logutil.S().Infow("running", "scenario", PutLinearizability.String())

	result := &Result{
		Scenario:  PutLinearizability.String(),
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

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, "foo")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to put: %v", err)

		return
	}
	startRev := gresp.Header.Revision

	keysN := 500
	testKey := runner.GenerateRandomKey(10)

	ch := make(chan clientResponse, keysN)
	for range keysN {
		go func() {
			putCtx, putCancel := runner.NewCtx()
			presp, putErr := cli.Put(putCtx, testKey, "bar")
			putCancel()
			ch <- clientResponse{key: testKey, putResp: presp, err: putErr}
		}()
	}
	putResponses := make([]clientResponse, 0, keysN)
	timeout := time.After(10 * time.Second)
	for i := range keysN {
		select {
		case resp := <-ch:
			putResponses = append(putResponses, resp)
		case <-timeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for put responses (received %d/%d)", i, keysN)

			return
		}
	}

	putRevs := make(map[int64]struct{})
	putErrs := make([]string, 0)
	for _, resp := range putResponses {
		if resp.err != nil {
			putErrs = append(putErrs, resp.err.Error())

			continue
		}

		rev := resp.putResp.Header.GetRevision()
		_, ok := putRevs[rev]
		if ok {
			result.Success = false
			result.Output = fmt.Sprintf("found duplicate revision %d for key %q", rev, resp.key)

			return
		}

		putRevs[rev] = struct{}{}
	}

	ctx, cancel = runner.NewCtx()
	gresp, err = cli.Get(ctx, "foo")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get end revision (%v)", err)

		return
	}
	lastRev := gresp.Header.Revision

	// In a live Kubernetes cluster, background controllers may also write to etcd,
	// causing additional revision bumps. We only require that all our writes were
	// recorded (lastRev >= startRev + keysN). The uniqueness of revisions is already
	// verified above via the putRevs map.
	if lastRev < startRev+int64(keysN) {
		result.Success = false
		result.Output = fmt.Sprintf("last revision %d is less than expected minimum (start revision %d + keysN %d = %d), errors: %s", lastRev, startRev, keysN, startRev+int64(keysN), strings.Join(putErrs, ","))

		return
	}

	// Concurrent reads at the actual revisions we wrote to (from putRevs).
	// In a live cluster, revisions may not be contiguous due to background writes,
	// so we read at the exact revisions we recorded from our PUT responses.
	actualPutRevs := make([]int64, 0, len(putRevs))
	for rev := range putRevs {
		actualPutRevs = append(actualPutRevs, rev)
	}

	ch = make(chan clientResponse, len(actualPutRevs))
	for _, rev := range actualPutRevs {
		go func(getRev int64) {
			ctx, cancel := runner.NewCtx()
			gresp, err := cli.Get(ctx, testKey, clientv3.WithRev(getRev))
			cancel()
			ch <- clientResponse{key: testKey, getRevRequested: getRev, getResp: gresp, err: err}
		}(rev)
	}
	getResponses := make([]clientResponse, 0, len(actualPutRevs))
	getTimeout := time.After(10 * time.Second)
	for i := range actualPutRevs {
		select {
		case resp := <-ch:
			getResponses = append(getResponses, resp)
		case <-getTimeout:
			result.Success = false
			result.Output = fmt.Sprintf("timed out waiting for get responses (received %d/%d)", i, len(actualPutRevs))

			return
		}
	}
	for _, resp := range getResponses {
		if resp.err != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to get at revision %d: %v", resp.getRevRequested, resp.err)

			return
		}

		if len(resp.getResp.Kvs) != 1 {
			result.Success = false
			result.Output = fmt.Sprintf("expected 1 key-value pair at revision %d, got %d", resp.getRevRequested, len(resp.getResp.Kvs))

			return
		}

		// Verify the key has the expected value at this revision
		if string(resp.getResp.Kvs[0].Value) != leasingValueBar {
			result.Success = false
			result.Output = fmt.Sprintf("unexpected value at revision %d: got %q, want %q", resp.getRevRequested, resp.getResp.Kvs[0].Value, leasingValueBar)

			return
		}
	}
}
