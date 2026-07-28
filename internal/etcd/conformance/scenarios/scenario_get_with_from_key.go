package scenarios

import (
	"fmt"
	"path"
	"sort"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithFromKey tests the GetWithFromKey scenario.
func RunGetWithFromKey(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithFromKey.String())

	result := &Result{
		Scenario:  GetWithFromKey.String(),
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

	sortedTestKeys := make([]string, 0)
	kvs := []*mvccpb.KeyValue{}
	for i := range 10 {
		k := path.Join(testKey, fmt.Sprintf("%02d", i))
		sortedTestKeys = append(sortedTestKeys, k)

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
	sort.Strings(sortedTestKeys)

	// Use runner's default timeout for WithFromKey() queries which can return large datasets
	// in high-latency cloud/VPN environments (e.g., cross-DC WireGuard networks).
	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithFromKey())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}

	if len(gresp.Kvs) < len(kvs) {
		result.Success = false
		result.Output = fmt.Sprintf("expected len(resp.Kvs) >= len(kvs), got len(resp.Kvs)=%d and len(kvs)=%d", len(gresp.Kvs), len(kvs))

		return
	}

	// returned keys should be
	// (1) sorted in the lexigographical order
	// (2) equal or super set of the test keys

	// Check if returned keys are sorted in lexicographical order
	for i := 1; i < len(gresp.Kvs); i++ {
		if string(gresp.Kvs[i-1].Key) >= string(gresp.Kvs[i].Key) {
			result.Success = false
			result.Output = fmt.Sprintf("keys are not sorted: %s >= %s", gresp.Kvs[i-1].Key, gresp.Kvs[i].Key)

			return
		}
	}

	// Check if returned keys are a superset of test keys
	respKeys := make(map[string]string)
	for _, kv := range gresp.Kvs {
		respKeys[string(kv.Key)] = string(kv.Value)
	}
	for _, testKey := range sortedTestKeys {
		v, ok := respKeys[testKey]
		if !ok {
			result.Success = false
			result.Output = "response keys do not contain all test keys: missing " + testKey

			return
		}
		if v != "" {
			result.Success = false
			result.Output = fmt.Sprintf("response value is not the same: %s != ''", v)

			return
		}
	}
}
