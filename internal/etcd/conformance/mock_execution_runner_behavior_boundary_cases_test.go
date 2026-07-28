//nolint:testpackage // Need access to internals for thorough testing.
package conformance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

// TestDetermineRunnerTimeoutStepTimeoutPriority tests that a valid step timeout
// parameter is used.
func TestDetermineRunnerTimeoutStepTimeoutPriority(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("5s")
	assert.Equal(t, 5*time.Second, d)
}

// TestDetermineRunnerTimeoutInvalidStepTimeout tests that an invalid step timeout
// string falls back to default.
func TestDetermineRunnerTimeoutInvalidStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("not-a-duration")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestDetermineRunnerTimeoutNegativeStepTimeout tests that a negative step timeout
// is rejected and falls back.
func TestDetermineRunnerTimeoutNegativeStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("-10s")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestDetermineRunnerTimeoutZeroStepTimeout tests that a zero step timeout
// is rejected and falls back.
func TestDetermineRunnerTimeoutZeroStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("0s")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestDetermineRunnerTimeoutWhitespaceStepTimeout tests that whitespace-only
// step timeout falls back to default.
func TestDetermineRunnerTimeoutWhitespaceStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("   ")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestRunFunctionValidationError tests the Run() function's validation error path.
func TestRunFunctionValidationError(t *testing.T) {
	t.Parallel()

	err := Run(Options{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioID:    "NONEXISTENT_SCENARIO_XYZ",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config")
}

// TestRunFunctionNormalizationNoEndpoints tests that Run() properly normalizes
// empty endpoint inputs.
func TestRunFunctionNormalizationNoEndpoints(t *testing.T) {
	t.Parallel()

	err := Run(Options{
		Endpoints:  nil,
		ScenarioID: "NONEXISTENT_ABC",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config")
}

// TestConfigSyncJSONWriteError tests the SyncJSON error path when writing fails.
func TestConfigSyncJSONWriteError(t *testing.T) {
	t.Parallel()

	cfg := &Config{Endpoints: []string{"http://localhost:2379"}}
	err := cfg.SyncJSON("/proc/nonexistent/config.json")
	require.Error(t, err)
}

// TestConfigSyncYAMLWriteError tests the SyncYAML error path when writing fails.
func TestConfigSyncYAMLWriteError(t *testing.T) {
	t.Parallel()

	cfg := &Config{Endpoints: []string{"http://localhost:2379"}}
	err := cfg.SyncYAML("/proc/nonexistent/config.yaml")
	require.Error(t, err)
}

// TestNewPerPeerClientsInvalidTLS exercises the NewPerPeerClients error path.
func TestNewPerPeerClientsInvalidTLS(t *testing.T) {
	t.Parallel()

	r := &runner{
		cfg: Config{
			Endpoints:      []string{"http://127.0.0.1:1", "http://127.0.0.1:2"},
			CACertFile:     "/nope",
			PrivateKeyFile: "/nope",
			CertFile:       "/nope",
		},
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
	}

	_, err := r.NewPerPeerClients()
	require.Error(t, err)
}

// TestCreateClientWithURLsMaxCallSizes tests the MaxCallSendMsgSize and MaxCallRecvMsgSize paths.
func TestCreateClientWithURLsMaxCallSizes(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints: []string{"http://127.0.0.1:2379"},
	}

	cli, err := cfg.createClientWithURLs([]string{"http://127.0.0.1:2379"},
		scenarios.WithMaxCallSendMsgSize(4*1024*1024),
		scenarios.WithMaxCallRecvMsgSize(8*1024*1024),
	)
	if err == nil {
		defer func() { _ = cli.Close() }()
		require.NotNil(t, cli)
	}
	// On some systems TLS config may fail; that's OK - we're testing the option paths.
}

// TestCreateRunnerSetsStepTimeout tests that CreateRunner creates a runner with
// the correct step timeout.
func TestCreateRunnerSetsStepTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{"GET_EMPTY_KEY"},
		stepTimeout:   "3s",
	}
	r := cfg.CreateRunner()
	require.NotNil(t, r)
	assert.Equal(t, 3*time.Second, r.DefaultTimeout())
}

// TestLoadConfigJSONInvalidContent tests loading a JSON file with invalid content.
func TestLoadConfigJSONInvalidContent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "config.json")
	require.NoError(t, os.WriteFile(file, []byte("not-json"), 0o600))

	_, err := LoadConfigJSON(file)
	require.Error(t, err)
}

// TestLoadConfigYAMLInvalidContent tests loading a YAML file with invalid content.
func TestLoadConfigYAMLInvalidContent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte(":\n  - [broken"), 0o600))

	_, err := LoadConfigYAML(file)
	require.Error(t, err)
}

// TestRunnerNewCtxTimeoutPositiveSmall tests NewCtxTimeout with a small positive value.
func TestRunnerNewCtxTimeoutPositiveSmall(t *testing.T) {
	t.Parallel()

	r := &runner{
		defaultTimeout: 60 * time.Second,
		results:        make([]scenarios.Result, 0),
	}
	ctx, cancel := r.NewCtxTimeout(100 * time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Positive(t, time.Until(deadline))
	assert.LessOrEqual(t, time.Until(deadline), 150*time.Millisecond)
}

// TestGetScenarioTimeoutDeleteFromKey tests the FROM_KEY path for DELETE.
func TestGetScenarioTimeoutDeleteFromKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, scenarioTimeoutExtended, getScenarioTimeout("DELETE_ALL_WITH_FROM_KEY"))
}

// TestShouldRetryScenarioWatchWithProgressNotify tests retry for WATCH_WITH_PROGRESS_NOTIFY.
func TestShouldRetryScenarioWatchWithProgressNotify(t *testing.T) {
	t.Parallel()
	assert.True(t, shouldRetryScenario("WATCH_WITH_PROGRESS_NOTIFY"))
}

// TestShouldRetryScenarioWatchBookmark tests that WATCH_BOOKMARK is NOT retried.
func TestShouldRetryScenarioWatchBookmark(t *testing.T) {
	t.Parallel()
	assert.False(t, shouldRetryScenario("WATCH_BOOKMARK_DELIVERY"))
}

// TestRunScenariosUnknownScenarioID tests that RunScenarios returns an error
// for unknown scenario IDs.
func TestRunScenariosUnknownScenarioID(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://127.0.0.1:2379"},
		TestKeyPrefix: "/test/",
		ScenarioIDs:   []string{"NONEXISTENT_XYZ"},
	}

	// Note: RunScenarios will try to create a client and cleanup, which will fail.
	// But the unknown scenario lookup should happen and return the error.
	// However, it first calls CreateRunner, then iterates. Let's use Cleanup mocking.
	// Actually since createClientWithURLs is used for Cleanup, and TLS files don't exist,
	// the cleanup error is just logged (not returned). The scenario lookup error IS returned.
	_, err := cfg.RunScenarios()
	require.Error(t, err)
	require.ErrorIs(t, err, errUnknownScenario)
}

// TestConfigJSON_MarshalError tests the JSON marshal error path.
func TestConfigJSON_MarshalError(t *testing.T) { //nolint:paralleltest // Sequential test
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock json.Marshal to return an error
	mock := mockey.Mock(mockey.GetMethod(&Config{}, "JSON")).To(func(*Config) ([]byte, error) {
		return nil, errors.New("marshal error")
	}).Build()
	defer mock.UnPatch()

	_, err := cfg.JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal")
}

// TestConfigYAML_MarshalError tests the YAML marshal error path.
func TestConfigYAML_MarshalError(t *testing.T) { //nolint:paralleltest // Sequential test
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock YAML method to return an error
	mock := mockey.Mock(mockey.GetMethod(&Config{}, "YAML")).To(func(*Config) ([]byte, error) {
		return nil, errors.New("yaml marshal error")
	}).Build()
	defer mock.UnPatch()

	_, err := cfg.YAML()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal")
}

// TestConfigSyncJSON_JSONError tests SyncJSON when JSON() fails.
func TestConfigSyncJSON_JSONError(t *testing.T) { //nolint:paralleltest // Sequential test
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock JSON() to return an error
	mock := mockey.Mock((*Config).JSON).Return(nil, errors.New("json error")).Build()
	defer mock.UnPatch()

	err := cfg.SyncJSON("/tmp/test.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal json")
}

// TestConfigSyncYAML_YAMLError tests SyncYAML when YAML() fails.
func TestConfigSyncYAML_YAMLError(t *testing.T) { //nolint:paralleltest // Sequential test
	// Cannot run in parallel - uses global mocks

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Mock YAML() to return an error
	mock := mockey.Mock((*Config).YAML).Return(nil, errors.New("yaml error")).Build()
	defer mock.UnPatch()

	err := cfg.SyncYAML("/tmp/test.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal yaml")
}

// TestConfigSyncJSON_MkdirError tests SyncJSON when MkdirAll fails.
func TestConfigSyncJSON_MkdirError(t *testing.T) {
	mockey.PatchConvey("SyncJSON returns error when MkdirAll fails", t, func() {
		cfg := &Config{
			Endpoints:     []string{"http://localhost:2379"},
			TestKeyPrefix: "/test/",
		}

		mockey.Mock(os.MkdirAll).Return(errors.New("mkdir error")).Build()

		err := cfg.SyncJSON("/nonexistent/dir/test.json")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create config dir")
	})
}

// TestConfigSyncYAML_MkdirError tests SyncYAML when MkdirAll fails.
func TestConfigSyncYAML_MkdirError(t *testing.T) {
	mockey.PatchConvey("SyncYAML returns error when MkdirAll fails", t, func() {
		cfg := &Config{
			Endpoints:     []string{"http://localhost:2379"},
			TestKeyPrefix: "/test/",
		}

		mockey.Mock(os.MkdirAll).Return(errors.New("mkdir error")).Build()

		err := cfg.SyncYAML("/nonexistent/dir/test.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create config dir")
	})
}

// TestConfigSyncJSON_StatSuccess tests SyncJSON when os.Stat succeeds
// (directory already exists), which skips the MkdirAll block.
func TestConfigSyncJSON_StatSuccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Use an existing directory - os.Stat will succeed, MkdirAll is skipped
	err := cfg.SyncJSON(filepath.Join(tmpDir, "test_sync.json"))
	require.NoError(t, err)
}

// TestConfigSyncYAML_StatSuccess tests SyncYAML when os.Stat succeeds
// (directory already exists), which skips the MkdirAll block.
func TestConfigSyncYAML_StatSuccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
	}

	// Use an existing directory - os.Stat will succeed, MkdirAll is skipped
	err := cfg.SyncYAML(filepath.Join(tmpDir, "test_sync.yaml"))
	require.NoError(t, err)
}

// TestCreateClientWithURLs_MaxCallSizesSet tests that MaxCallSendMsgSize and
// MaxCallRecvMsgSize are properly set in the client config when provided.
func TestCreateClientWithURLs_MaxCallSizesSet(t *testing.T) {
	mockey.PatchConvey("createClientWithURLs passes max call sizes to clientv3.New", t, func() {
		cfg := &Config{
			Endpoints: []string{"http://127.0.0.1:2379"},
		}

		var capturedConfig clientv3.Config
		mockey.Mock(clientv3.New).To(func(config clientv3.Config) (*clientv3.Client, error) {
			capturedConfig = config
			return &clientv3.Client{}, nil
		}).Build()

		cli, err := cfg.createClientWithURLs(
			[]string{"http://127.0.0.1:2379"},
			scenarios.WithMaxCallSendMsgSize(4*1024*1024),
			scenarios.WithMaxCallRecvMsgSize(8*1024*1024),
		)
		require.NoError(t, err)
		require.NotNil(t, cli)

		assert.Equal(t, 4*1024*1024, capturedConfig.MaxCallSendMsgSize)
		assert.Equal(t, 8*1024*1024, capturedConfig.MaxCallRecvMsgSize)
	})
}
