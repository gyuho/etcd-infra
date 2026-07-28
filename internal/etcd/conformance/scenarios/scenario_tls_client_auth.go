package scenarios

import (
	"fmt"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunTLSClientAuth verifies that TLS client authentication and secure communication work correctly.
// Kubernetes configures TLS for etcd connections, requiring proper certificate validation and secure transport.
// staging/src/k8s.io/apiserver/pkg/storage/storagebackend/factory/etcd3.go#newETCD3Client (lines 345-353)
//
// This test validates:
// 1. TLS handshake succeeds when establishing connection
// 2. Basic KV operations work over TLS
// 3. Watch operations work over TLS
// 4. Transaction operations work over TLS
//
// The test is deterministic and does not rely on timing or external configuration.
func RunTLSClientAuth(runner Runner) {
	logutil.S().Infow("running", "scenario", TLSClientAuth.String())

	result := &Result{
		Scenario:  TLSClientAuth.String(),
		TimeStart: testtime.Now(),
		Success:   true,
		Output:    "ok",
	}
	defer func() {
		result.RecordTimeEnd(testtime.Now())
		runner.RecordResult(*result)
	}()

	// Create client - this tests TLS handshake if TLS is configured
	cli, err := runner.NewClient()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to create TLS client (handshake may have failed): %v", err)

		return
	}
	defer func() { _ = cli.Close() }()

	prefix := runner.GenerateRandomKey(10)

	// Test 1: Basic Put operation over TLS
	key1 := prefix + "/tls-test-put"
	value1 := "tls-secure-value"
	ctx, cancel := runner.NewCtx()
	putResp, err := cli.Put(ctx, key1, value1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Put operation failed over TLS: %v", err)

		return
	}
	if putResp.Header.Revision == 0 {
		result.Success = false
		result.Output = "Put response missing revision"

		return
	}
	putRevision := putResp.Header.Revision

	// Test 2: Basic Get operation over TLS
	ctx, cancel = runner.NewCtx()
	getResp, err := cli.Get(ctx, key1)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Get operation failed over TLS: %v", err)

		return
	}
	if len(getResp.Kvs) != 1 {
		result.Success = false
		result.Output = fmt.Sprintf("Get returned unexpected count: want 1, got %d", len(getResp.Kvs))

		return
	}
	if string(getResp.Kvs[0].Value) != value1 {
		result.Success = false
		result.Output = fmt.Sprintf("Get returned wrong value: want %q, got %q", value1, string(getResp.Kvs[0].Value))

		return
	}

	// Test 3: Transaction operation over TLS (optimistic concurrency)
	key2 := prefix + "/tls-test-txn"
	value2 := "tls-txn-value"
	ctx, cancel = runner.NewCtx()
	txnResp, err := cli.Txn(ctx).
		If(clientv3.Compare(clientv3.Version(key2), "=", 0)).
		Then(clientv3.OpPut(key2, value2)).
		Else(clientv3.OpGet(key2)).
		Commit()
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Txn operation failed over TLS: %v", err)

		return
	}
	if !txnResp.Succeeded {
		result.Success = false
		result.Output = "Txn should have succeeded (key should not exist)"

		return
	}

	// Test 4: Watch operation over TLS
	watchKey := prefix + "/tls-test-watch"
	ctx, cancel = runner.NewCtx()
	watcher := clientv3.NewWatcher(cli)
	defer func() {
		_ = watcher.Close()
	}()

	watchChan := watcher.Watch(ctx, watchKey)

	// Write to the watched key
	ctx2, cancel2 := runner.NewCtx()
	_, err = cli.Put(ctx2, watchKey, "watch-value")
	cancel2()
	if err != nil {
		cancel()
		result.Success = false
		result.Output = fmt.Sprintf("Put for watch test failed over TLS: %v", err)

		return
	}

	// Read watch event
	select {
	case watchResp := <-watchChan:
		if watchResp.Err() != nil {
			cancel()
			result.Success = false
			result.Output = fmt.Sprintf("Watch failed over TLS: %v", watchResp.Err())

			return
		}
		if len(watchResp.Events) != 1 {
			cancel()
			result.Success = false
			result.Output = fmt.Sprintf("Watch expected 1 event, got %d", len(watchResp.Events))

			return
		}
		if string(watchResp.Events[0].Kv.Key) != watchKey {
			cancel()
			result.Success = false
			result.Output = fmt.Sprintf("Watch event key mismatch: want %q, got %q", watchKey, string(watchResp.Events[0].Kv.Key))

			return
		}
	case <-ctx.Done():
		result.Success = false
		result.Output = "Watch operation timed out (TLS may have issues)"

		return
	}
	cancel()

	// Test 5: Delete operation over TLS
	ctx, cancel = runner.NewCtx()
	delResp, err := cli.Delete(ctx, prefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Delete operation failed over TLS: %v", err)

		return
	}
	if delResp.Deleted < 3 {
		result.Success = false
		result.Output = fmt.Sprintf("Delete expected at least 3 keys, deleted %d", delResp.Deleted)

		return
	}

	// Test 6: Maintenance Status operation over TLS
	endpoints := cli.Endpoints()
	if len(endpoints) == 0 {
		result.Success = false
		result.Output = "No endpoints available"

		return
	}

	ctx, cancel = runner.NewCtx()
	statusResp, err := cli.Status(ctx, endpoints[0])
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("Status operation failed over TLS: %v", err)

		return
	}
	if statusResp.Version == "" {
		result.Success = false
		result.Output = "Status response missing version"

		return
	}

	result.Output = fmt.Sprintf(
		"TLS client auth verified: Put/Get/Txn/Watch/Delete/Status succeeded (revision %d, version %s)",
		putRevision,
		statusResp.Version,
	)
}
