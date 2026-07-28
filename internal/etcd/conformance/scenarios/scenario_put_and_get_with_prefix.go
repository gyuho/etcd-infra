package scenarios

import (
	"fmt"
	"reflect"
	"sort"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// RunPutAndGetWithPrefix tests the PutAndGetWithPrefix scenario.
func RunPutAndGetWithPrefix(runner Runner) {
	logutil.S().Infow("running", "scenario", PutAndGetWithPrefix.String())

	result := &Result{
		Scenario:  PutAndGetWithPrefix.String(),
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
	testKeyValues := make([]keyValue, 0)
	sortedTestKeys := make([]string, 0)
	for i := range 10 {
		kv := createKV(testKey, fmt.Sprintf("hello%02d", i), "bar")
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

	ctx, cancel := runner.NewCtx()
	gresp, err := cli.Get(ctx, testKey, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get: %v", err)

		return
	}
	logutil.S().Infow("GET success", "prefix", testKey, "revision", gresp.Header.Revision)

	// should return sorted in the lexigographical order; thus no need to sort again
	returnedKeys := make([]string, 0, len(testKeyValues))
	for _, kv := range gresp.Kvs {
		returnedKeys = append(returnedKeys, string(kv.Key))
	}
	if !reflect.DeepEqual(sortedTestKeys, returnedKeys) {
		result.Success = false
		result.Output = fmt.Sprintf("returned keys are not the same: %v != %v", sortedTestKeys, returnedKeys)

		return
	}
}
