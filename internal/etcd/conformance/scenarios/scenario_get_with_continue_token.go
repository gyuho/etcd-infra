package scenarios

import (
	"encoding/base64"
	"fmt"
	"path"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
)

// RunGetWithContinueToken tests Kubernetes-style pagination with continue tokens.
// Kubernetes uses etcd's limit/more mechanism combined with revision-based continue tokens
// to implement consistent pagination across API list requests.
// staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go "GetList" with continue
//
//nolint:gocyclo // Pagination scenario exercises multiple branches to mirror API server behavior.
func RunGetWithContinueToken(runner Runner) {
	logutil.S().Infow("running", "scenario", GetWithContinueToken.String())

	result := &Result{
		Scenario:  GetWithContinueToken.String(),
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

	// Create a prefix for our test keys
	prefix := runner.GenerateRandomKey(10)

	// Insert 15 keys to paginate through
	const totalKeys = 15
	keys := make([]string, totalKeys)
	for i := range totalKeys {
		keys[i] = path.Join(prefix, fmt.Sprintf("key-%03d", i))
	}

	for i, key := range keys {
		ctx, cancel := runner.NewCtx()
		_, putErr := cli.Put(ctx, key, fmt.Sprintf("value-%d", i))
		cancel()
		if putErr != nil {
			result.Success = false
			result.Output = fmt.Sprintf("failed to seed key %q: %v", key, putErr)

			return
		}
	}

	// Page 1: Get first 5 keys with limit
	const pageSize = 5
	ctx, cancel := runner.NewCtx()
	page1Resp, err := cli.Get(
		ctx,
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithLimit(pageSize),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithKeysOnly(),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get page 1: %v", err)

		return
	}

	// Validate page 1 response
	if len(page1Resp.Kvs) != pageSize {
		result.Success = false
		result.Output = fmt.Sprintf("page 1 expected %d keys, got %d", pageSize, len(page1Resp.Kvs))

		return
	}
	if !page1Resp.More {
		result.Success = false
		result.Output = "page 1 should have More=true"

		return
	}
	if page1Resp.Count != totalKeys {
		result.Success = false
		result.Output = fmt.Sprintf("page 1 count mismatch: want %d, got %d", totalKeys, page1Resp.Count)

		return
	}
	page1Revision := page1Resp.Header.GetRevision()

	// Verify keys on page 1
	for i := range pageSize {
		expectedKey := keys[i]
		actualKey := string(page1Resp.Kvs[i].Key)
		if actualKey != expectedKey {
			result.Success = false
			result.Output = fmt.Sprintf("page 1 key[%d] mismatch: want %q, got %q", i, expectedKey, actualKey)

			return
		}
	}

	// Kubernetes-style continue token: encode the last key + revision
	// In Kubernetes, the continue token contains: revision + start key
	// For simplicity, we encode "revision/lastKey" in base64
	lastKeyPage1 := page1Resp.Kvs[len(page1Resp.Kvs)-1].Key
	continueToken := encodeContinueToken(page1Revision, lastKeyPage1)

	// Simulate a write between page 1 and page 2
	// This tests that pagination uses the snapshot from the continue token revision
	newKey := path.Join(prefix, "key-new-000")
	ctx, cancel = runner.NewCtx()
	_, err = cli.Put(ctx, newKey, "new-value")
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to write between pages: %v", err)

		return
	}

	// Page 2: Decode continue token and fetch next page at the same revision
	continueRevision, continueStartKey, err := decodeContinueToken(continueToken)
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to decode continue token: %v", err)

		return
	}

	if continueRevision != page1Revision {
		result.Success = false
		result.Output = fmt.Sprintf("continue revision mismatch: want %d, got %d", page1Revision, continueRevision)

		return
	}

	// Start from the next key after the last one from page 1
	// In etcd, we use the last key + null byte to get the next key
	nextStart := append([]byte(nil), continueStartKey...)
	nextStart = append(nextStart, 0x00)

	ctx, cancel = runner.NewCtx()
	page2Resp, err := cli.Get(
		ctx,
		string(nextStart),
		clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
		clientv3.WithLimit(pageSize),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithKeysOnly(),
		clientv3.WithRev(continueRevision), // Use revision from continue token
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get page 2: %v", err)

		return
	}

	// Validate page 2 response
	if len(page2Resp.Kvs) != pageSize {
		result.Success = false
		result.Output = fmt.Sprintf("page 2 expected %d keys, got %d", pageSize, len(page2Resp.Kvs))

		return
	}
	if !page2Resp.More {
		result.Success = false
		result.Output = "page 2 should have More=true"

		return
	}
	// Note: page2Resp.Header.GetRevision() returns the current cluster revision,
	// not the revision we're reading from. This is correct etcd behavior.
	// We read from continueRevision via WithRev(), but the response header shows current rev.

	// Verify keys on page 2 (should be keys[5:10])
	for i := range pageSize {
		expectedKey := keys[pageSize+i]
		actualKey := string(page2Resp.Kvs[i].Key)
		if actualKey != expectedKey {
			result.Success = false
			result.Output = fmt.Sprintf("page 2 key[%d] mismatch: want %q, got %q", i, expectedKey, actualKey)

			return
		}
	}

	// Verify that the new key we inserted is NOT in page 2
	// This confirms we're reading from the snapshot
	for _, kv := range page2Resp.Kvs {
		if string(kv.Key) == newKey {
			result.Success = false
			result.Output = "page 2 incorrectly included new key written between pages"

			return
		}
	}

	// Page 3: Get remaining keys
	lastKeyPage2 := page2Resp.Kvs[len(page2Resp.Kvs)-1].Key
	nextStart = append([]byte(nil), lastKeyPage2...)
	nextStart = append(nextStart, 0x00)

	ctx, cancel = runner.NewCtx()
	page3Resp, err := cli.Get(
		ctx,
		string(nextStart),
		clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
		clientv3.WithLimit(pageSize),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		clientv3.WithKeysOnly(),
		clientv3.WithRev(continueRevision),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get page 3: %v", err)

		return
	}

	// Validate page 3 response (last page)
	expectedPage3Count := totalKeys - (2 * pageSize)
	if len(page3Resp.Kvs) != expectedPage3Count {
		result.Success = false
		result.Output = fmt.Sprintf("page 3 expected %d keys, got %d", expectedPage3Count, len(page3Resp.Kvs))

		return
	}
	if page3Resp.More {
		result.Success = false
		result.Output = "page 3 should have More=false (last page)"

		return
	}

	// Verify keys on page 3 (should be keys[10:15])
	for i := range expectedPage3Count {
		expectedKey := keys[2*pageSize+i]
		actualKey := string(page3Resp.Kvs[i].Key)
		if actualKey != expectedKey {
			result.Success = false
			result.Output = fmt.Sprintf("page 3 key[%d] mismatch: want %q, got %q", i, expectedKey, actualKey)

			return
		}
	}

	// Test edge case: empty continuation (no more keys)
	lastKeyPage3 := page3Resp.Kvs[len(page3Resp.Kvs)-1].Key
	nextStart = append([]byte(nil), lastKeyPage3...)
	nextStart = append(nextStart, 0x00)

	ctx, cancel = runner.NewCtx()
	emptyResp, err := cli.Get(
		ctx,
		string(nextStart),
		clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
		clientv3.WithLimit(pageSize),
		clientv3.WithRev(continueRevision),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get empty page: %v", err)

		return
	}
	if len(emptyResp.Kvs) != 0 {
		result.Success = false
		result.Output = fmt.Sprintf("expected empty page, got %d keys", len(emptyResp.Kvs))

		return
	}
	if emptyResp.More {
		result.Success = false
		result.Output = "empty page should have More=false"

		return
	}

	// Test edge case: paginate with count only on first page
	ctx, cancel = runner.NewCtx()
	countResp, err := cli.Get(
		ctx,
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithLimit(pageSize),
		clientv3.WithCountOnly(),
		clientv3.WithRev(continueRevision),
	)
	cancel()
	if err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("failed to get count-only page: %v", err)

		return
	}
	if countResp.Count != totalKeys {
		result.Success = false
		result.Output = fmt.Sprintf("count-only page count mismatch: want %d, got %d", totalKeys, countResp.Count)

		return
	}
	if len(countResp.Kvs) != 0 {
		result.Success = false
		result.Output = "count-only page should not return keys"

		return
	}

	totalPaginated := len(page1Resp.Kvs) + len(page2Resp.Kvs) + len(page3Resp.Kvs)
	result.Output = fmt.Sprintf(
		"successfully paginated %d keys in 3 pages (%d+%d+%d) using continue tokens at revision %d",
		totalPaginated,
		len(page1Resp.Kvs),
		len(page2Resp.Kvs),
		len(page3Resp.Kvs),
		continueRevision,
	)
}

// encodeContinueToken encodes revision and last key into a base64 continue token
// Format: "revision/base64(lastKey)".
func encodeContinueToken(revision int64, lastKey []byte) string {
	token := fmt.Sprintf("%d/%s", revision, base64.StdEncoding.EncodeToString(lastKey))

	return base64.StdEncoding.EncodeToString([]byte(token))
}

// decodeContinueToken decodes a continue token into revision and start key.
func decodeContinueToken(token string) (int64, []byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid continue token encoding: %w", err)
	}

	var revision int64
	var encodedKey string
	_, err = fmt.Sscanf(string(decoded), "%d/%s", &revision, &encodedKey)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid continue token format: %w", err)
	}

	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid continue token key encoding: %w", err)
	}

	return revision, key, nil
}
