package scenarios

import (
	"fmt"
	"sort"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunPutAndGetWithFromKey tests the PutAndGetWithFromKey scenario.
func RunPutAndGetWithFromKey(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithFromKey.String())

	result := &Result{
		Scenario:  PutAndGetWithFromKey.String(),
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
	testVal := randutil.StringAlphabetsLowerCase(10)

	testKeyValues := make([]keyValue, 0)
	sortedTestKeys := make([]string, 0)

	for i := range 10 {
		kv := createKV(testKey, fmt.Sprintf("hello%02d", i), testVal)
		testKeyValues = append(testKeyValues, kv)
		sortedTestKeys = append(sortedTestKeys, kv.k)
	}

	// shuffle to write in a random order
	randutil.Shuffle(len(testKeyValues), func(i, j int) {
		testKeyValues[i], testKeyValues[j] = testKeyValues[j], testKeyValues[i]
	})
	sort.Strings(sortedTestKeys)

	for _, kv := range testKeyValues {
		ctx, cancel := runner.NewCtx()
		_, putErr := cli.Put(ctx, kv.k, kv.v)
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to put: %v", putErr)

			return
		}
	}

	// Use extended timeout for WithFromKey() queries which return ALL keys from the given key
	// onwards in lexicographical order. In production etcd with Kubernetes data (/registry/...),
	// this can return thousands of keys. Cross-DC WireGuard networks amplify this latency.
	// Use 3x the default timeout (3 * 90s = 270s = 4.5 minutes) for WithFromKey operations.
	fromKeyTimeout := max(3*runner.DefaultTimeout(),
		// minimum 3 minutes for WithFromKey
		180*time.Second)
	ctx, cancel := runner.NewCtxTimeout(fromKeyTimeout)
	gresp, err := cli.Get(ctx, testKey, clientv3.WithFromKey())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	logutil.S().Infow("GET success", "prefix", testKey, "revision", gresp.Header.Revision)

	// if other tests wrote the key with the prefix of higher lexicographical order,
	// more keys should have been returned
	if len(gresp.Kvs) < len(testKeyValues) {
		result.Success = false
		result.Output = fmt.Sprintf("expected equal or more keys: test keys/values %+v, got %+v", testKeyValues, gresp.Kvs)

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
		if v != testVal {
			result.Success = false
			result.Output = fmt.Sprintf("response value is not the same: %s != %s", v, testVal)

			return
		}
	}
}
