//nolint:all // Coverage-oriented tests for uncovered branches.
package scenarios

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingClientRunner is a Runner that always returns error from NewClient/NewPerPeerClients.
// Running all scenarios through it covers the "failed to create client" error branch in every RUN_* function.
type failingClientRunner struct {
	results []Result
}

func (r *failingClientRunner) RecordResult(rs Result)        { r.results = append(r.results, rs) }
func (r *failingClientRunner) Results() Results              { return r.results }
func (r *failingClientRunner) Cleanup() error                { return nil }
func (r *failingClientRunner) DefaultTimeout() time.Duration { return time.Second }
func (r *failingClientRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Second)
}

func (r *failingClientRunner) NewCtxTimeout(time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Second)
}
func (r *failingClientRunner) GenerateRandomKey(int) string { return "test-key" }
func (r *failingClientRunner) NewClient(...OpOption) (*clientv3.Client, error) {
	return nil, errors.New("mock client error")
}

func (r *failingClientRunner) NewPerPeerClients(...OpOption) ([]*clientv3.Client, error) {
	return nil, errors.New("mock peer client error")
}

// brokenEndpointRunner returns a client connected to a dead endpoint (no etcd).
// All operations fail with connection errors, exercising error-handling branches
// beyond the initial NewClient call.
type brokenEndpointRunner struct {
	results []Result
}

func (r *brokenEndpointRunner) RecordResult(rs Result)        { r.results = append(r.results, rs) }
func (r *brokenEndpointRunner) Results() Results              { return r.results }
func (r *brokenEndpointRunner) Cleanup() error                { return nil }
func (r *brokenEndpointRunner) DefaultTimeout() time.Duration { return 100 * time.Millisecond }
func (r *brokenEndpointRunner) GenerateRandomKey(int) string  { return "broken-key" }

func (r *brokenEndpointRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 100*time.Millisecond)
}

func (r *brokenEndpointRunner) NewCtxTimeout(time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Millisecond)
}

func (r *brokenEndpointRunner) NewClient(opts ...OpOption) (*clientv3.Client, error) {
	return clientv3.New(clientv3.Config{
		Endpoints:   []string{"127.0.0.1:1"},
		DialTimeout: 10 * time.Millisecond,
	})
}

func (r *brokenEndpointRunner) NewPerPeerClients(opts ...OpOption) ([]*clientv3.Client, error) {
	cli, err := r.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return []*clientv3.Client{cli}, nil
}

// TestAllScenariosNewClientError runs every scenario through a runner whose NewClient always fails.
// This covers the "failed to create client" error branch in all registered scenario functions.
func TestAllScenariosNewClientError(t *testing.T) {
	t.Parallel()

	runner := &failingClientRunner{}

	for name, fn := range IDStringToRunnerFunc {
		t.Run(name, func(t *testing.T) {
			fn(runner)
		})
	}

	// All results should have Success=false with the "failed to create" message
	for _, r := range runner.Results() {
		assert.False(t, r.Success, "scenario %s should fail with mock client error", r.Scenario)
		assert.True(t,
			strings.Contains(r.Output, "failed to create client") ||
				strings.Contains(r.Output, "failed to create aggregate client") ||
				strings.Contains(r.Output, "failed to create per-peer clients") ||
				strings.Contains(r.Output, "failed to create TLS client"),
			"scenario %s output should mention client creation failure, got: %s", r.Scenario, r.Output)
	}
}

// TestScenariosBrokenEndpoint runs non-watch scenarios through a runner connected to a dead endpoint.
// Watch scenarios are excluded because they hang on gRPC stream retries.
// This exercises error-handling branches beyond the initial NewClient call.
func TestScenariosBrokenEndpoint(t *testing.T) {
	t.Parallel()

	// Scenarios that are safe to run with a broken endpoint (non-watch, non-blocking).
	// Watch scenarios, mirror scenarios, concurrency scenarios, and long-running lease scenarios
	// are excluded because they hang on gRPC stream retries or timeouts.
	fastScenarios := []struct {
		name string
		fn   func(Runner)
	}{
		// KV basics
		{"PUT_EMPTY_KEY_SHOULD_ERROR", RunPutEmptyKeyShouldError},
		{"PUT_LARGE_SHOULD_ERROR", RunPutLargeShouldError},
		{"PUT_AND_GET_WITH_LATEST_REVISION", RunPutAndGetWithLatestRevision},
		{"PUT_AND_GET_WITH_OLD_REVISION", RunPutAndGetWithOldRevision},
		{"PUT_AND_GET_WITH_PREFIX", RunPutAndGetWithPrefix},
		{"PUT_AND_GET_WITH_FROM_KEY", RunPutAndGetWithFromKey},
		{"PUT_AND_GET_WITH_SORT", RunPutAndGetWithSort},
		{"PUT_AND_GET_WITH_OP", RunPutAndGetWithOp},
		{"PUT_AND_DELETE", RunPutAndDelete},
		{"PUT_AND_DELETE_WITH_PREFIX", RunPutAndDeleteWithPrefix},
		{"PUT_AND_DELETE_AND_GET_WITH_OLD_REVISION", RunPutAndDeleteAndGetWithOldRevision},
		{"PUT_WITH_PREV_KV", RunPutWithPrevKv},

		// Lease scenarios (short-TTL ones that don't block)
		{"PUT_WITH_LEASE_NOT_FOUND", RunPutWithLeaseNotFound},
		{"PUT_WITH_LEASE_AND_REVOKE", RunPutWithLeaseAndRevoke},
		{"PUT_WITH_IGNORE_VALUE", RunPutWithIgnoreValue},
		{"PUT_WITH_IGNORE_LEASE", RunPutWithIgnoreLease},
		{"LEASE_TOO_LARGE", RunLeaseTooLarge},
		{"PUT_WITH_LEASE_ATTACH", RunPutWithLeaseAttach},
		{"LEASE_LIST", RunLeaseList},
		{"LEASE_GRANT_TTL_BOUNDS", RunLeaseGrantTTLBounds},

		// Get scenarios
		{"GET_EMPTY_KEY", RunGetEmptyKey},
		{"GET_WITH_PREFIX", RunGetWithPrefix},
		{"GET_WITH_FROM_KEY", RunGetWithFromKey},
		{"GET_WITH_RANGE", RunGetWithRange},
		{"GET_WITH_LIMIT_AND_COUNT", RunGetWithLimitAndCount},
		{"GET_WITH_REVISION_FILTERS", RunGetWithRevisionFilters},
		{"GET_WITH_REQUIRE_LEADER", RunGetWithRequireLeader},
		{"GET_WITH_KEYS_ONLY", RunGetWithKeysOnly},
		{"GET_WITH_CONTINUE_TOKEN", RunGetWithContinueToken},

		// Delete scenarios
		{"DELETE_EMPTY_KEY", RunDeleteEmptyKey},
		{"DELETE_ALL_WITH_PREFIX", RunDeleteAllWithPrefix},
		{"DELETE_ALL_WITH_FROM_KEY", RunDeleteAllWithFromKey},
		{"DELETE_WITH_RANGE", RunDeleteWithRange},
		{"DELETE_WITH_PRECONDITION", RunDeleteWithPrecondition},
		{"DELETE_WITH_PREV_KV", RunDeleteWithPrevKv},

		// Compact scenarios
		{"COMPACT", RunCompact},
		{"COMPACT_REVISION_RETENTION", RunCompactRevisionRetention},

		// Transaction scenarios
		{"TXN_PUT_ONE", RunTxnPutOne},
		{"TXN_PUT_MULTIPLE", RunTxnPutMultiple},
		{"TXN_KEY_EXISTS", RunTxnKeyExists},
		{"TXN_KEY_MISSING", RunTxnKeyMissing},
		{"TXN_COMPARE_RANGE", RunTxnCompareRange},
		{"TXN_COMPARE_MODREVISION", RunTxnCompareModrevision},
		{"TXN_COMPARE_CREATEREVISION", RunTxnCompareCreaterevision},
		{"TXN_COMPARE_VERSION", RunTxnCompareVersion},
		{"TXN_COMPARE_VALUE", RunTxnCompareValue},
		{"TXN_COMPARE_LEASE", RunTxnCompareLease},
		{"TXN_NESTED", RunTxnNested},
		{"TXN_ERROR_DUPLICATE_KEY", RunTxnErrorDuplicateKey},
		{"TXN_ERROR_TOO_MANY_OPS", RunTxnErrorTooManyOps},
		{"TXN_MULTI_OP_ATOMICITY", RunTxnMultiOpAtomicity},

		// Error scenarios
		{"ERROR_COMPACTED", RunErrorCompacted},
		{"ERROR_FUTURE_REV", RunErrorFutureRev},

		// Revision scenarios
		{"HEADER_REVISION_MONOTONIC", RunHeaderRevisionMonotonic},
		{"MOD_REVISION_CONSISTENCY", RunModRevisionConsistency},

		// Maintenance scenarios
		{"MAINTENANCE_STATUS", RunMaintenanceStatus},
		{"MAINTENANCE_SNAPSHOT", RunMaintenanceSnapshot},
		{"MAINTENANCE_DEFRAGMENT", RunMaintenanceDefragment},
		{"MAINTENANCE_HASH_KV", RunMaintenanceHashKv},

		// Cluster scenarios (read-only membership query only)
		{"CLUSTER_MEMBER_LIST", RunClusterMemberList},

		// TLS and resource scenarios
		{"TLS_CLIENT_AUTH", RunTLSClientAuth},
		{"RESOURCE_SIZE_ESTIMATION", RunResourceSizeEstimation},

		// Leasing scenarios (non-blocking)
		{"LEASING_PUT_AND_GET", RunLeasingPutAndGet},
		{"LEASING_PUT_AND_GET_WITH_PREFIX", RunLeasingPutAndGetWithPrefix},
		{"LEASING_PUT_AND_GET_INVALIDATE_NEW", RunLeasingPutAndGetInvalidateNew},
		{"LEASING_PUT_AND_GET_INVALIDATE_EXISTING", RunLeasingPutAndGetInvalidateExisting},
		{"LEASING_PUT_AND_GET_WITH_PREV_KV", RunLeasingPutAndGetWithPrevKv},
		{"LEASING_PUT_AND_GET_WITH_REV", RunLeasingPutAndGetWithRev},
		{"LEASING_PUT_AND_GET_WITH_OPTS", RunLeasingPutAndGetWithOpts},
		{"LEASING_PUT_CONCURRENT", RunLeasingPutConcurrent},
		{"LEASING_PUT_AND_GET_OVERWRITE_RESPONSE", RunLeasingPutAndGetOverwriteResponse},
		{"LEASING_PUT_AND_DELETE_WITH_PREFIX", RunLeasingPutAndDeleteWithPrefix},
		{"LEASING_PUT_AND_DELETE_WITH_FROM_KEY", RunLeasingPutAndDeleteWithFromKey},
		{"LEASING_DELETE_OWNER", RunLeasingDeleteOwner},
		{"LEASING_DELETE_NON_OWNER", RunLeasingDeleteNonOwner},
		{"LEASING_TXN_OWNER_GET", RunLeasingTxnOwnerGet},
		{"LEASING_TXN_OWNER_GET_WITH_PREFIX", RunLeasingTxnOwnerGetWithPrefix},
		{"LEASING_TXN_OWNER_DELETE", RunLeasingTxnOwnerDelete},
		{"LEASING_TXN_OWNER_DELETE_WITH_PREFIX", RunLeasingTxnOwnerDeleteWithPrefix},
		{"LEASING_DO", RunLeasingDo},
		{"LEASING_LEASE_REUSE_WINDOW", RunLeasingLeaseReuseWindow},

		// PutAndGetWithNamespace
		{"PUT_AND_GET_WITH_NAMESPACE", RunPutAndGetWithNamespace},

		// PutLinearizability
		{"PUT_LINEARIZABILITY", RunPutLinearizability},
	}

	// This suite is coverage-oriented; we only need a representative subset to
	// exercise broken-endpoint error handling without blowing up overall unit time.
	const maxBrokenEndpointScenarios = 24
	if len(fastScenarios) > maxBrokenEndpointScenarios {
		fastScenarios = fastScenarios[:maxBrokenEndpointScenarios]
	}

	for _, s := range fastScenarios {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			runner := &brokenEndpointRunner{}
			s.fn(runner)
			require.Len(t, runner.results, 1)
			// With a broken endpoint, all scenarios should report failure
			assert.False(t, runner.results[0].Success,
				"scenario %s should fail with broken endpoint", s.name)
		})
	}
}

// TestSyncJSONMkdirAllError tests error path when MkdirAll fails.
func TestSyncJSONMkdirAllError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	readOnly := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnly, 0o555))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
	file := filepath.Join(readOnly, "sub", "results.json")

	rs := Results{{Scenario: "test"}}
	err := rs.SyncJSON(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create directory")
}

// TestSyncYAMLMkdirAllError tests error path when MkdirAll fails.
func TestSyncYAMLMkdirAllError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	readOnly := filepath.Join(tmpDir, "readonly")
	require.NoError(t, os.Mkdir(readOnly, 0o555))
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o755) })
	file := filepath.Join(readOnly, "sub", "results.yaml")

	rs := Results{{Scenario: "test"}}
	err := rs.SyncYAML(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create directory")
}

// TestSyncJSONWriteFileError tests error path when WriteFile fails.
func TestSyncJSONWriteFileError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.json")
	require.NoError(t, os.Mkdir(file, 0o755))

	rs := Results{{Scenario: "test"}}
	err := rs.SyncJSON(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write JSON file")
}

// TestSyncYAMLWriteFileError tests error path when WriteFile fails.
func TestSyncYAMLWriteFileError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.yaml")
	require.NoError(t, os.Mkdir(file, 0o755))

	rs := Results{{Scenario: "test"}}
	err := rs.SyncYAML(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write YAML file")
}

// TestIDStringOutOfRangeNegative covers the fallback branch in the stringer.
func TestIDStringOutOfRangeNegative(t *testing.T) {
	t.Parallel()

	s := ID(-1).String()
	assert.Contains(t, s, "ID(-1)")

	s2 := ID(9999).String()
	assert.Contains(t, s2, "ID(9999)")
}
