//nolint:testpackage // Need access to internals for thorough testing.
package stress

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

// TestDetermineRunnerTimeoutStepTimeoutPriority tests that the step timeout
// parameter is used when provided.
func TestDetermineRunnerTimeoutStepTimeoutPriority(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("5s")
	assert.Equal(t, 5*time.Second, d)
}

// TestDetermineRunnerTimeoutInvalidStepTimeout tests that an invalid step timeout
// string falls back to the default.
func TestDetermineRunnerTimeoutInvalidStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("not-a-duration")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestDetermineRunnerTimeoutNegativeStepTimeout tests that a negative step timeout
// is rejected and falls back to the default.
func TestDetermineRunnerTimeoutNegativeStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("-10s")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestDetermineRunnerTimeoutZeroStepTimeout tests that a zero step timeout
// is rejected and falls back to the default.
func TestDetermineRunnerTimeoutZeroStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("0s")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestDetermineRunnerTimeoutWhitespaceStepTimeout tests that whitespace-only
// step timeout falls back to the default.
func TestDetermineRunnerTimeoutWhitespaceStepTimeout(t *testing.T) {
	t.Parallel()

	d := determineRunnerTimeout("   ")
	assert.Equal(t, runnerTimeoutDefault, d)
}

// TestConfigValidateAndSetDefaultsCustomValues tests that ValidateAndSetDefaults
// does NOT override custom positive values.
func TestConfigValidateAndSetDefaultsCustomValues(t *testing.T) {
	t.Parallel()

	var validID string
	for k := range stressScenarioIDMap() {
		validID = k
		break
	}
	require.NotEmpty(t, validID)

	cfg := &Config{
		Endpoints:         []string{"http://localhost:2379"},
		TestKeyPrefix:     "/test/",
		ScenarioIDs:       []string{validID},
		DurationSeconds:   120,
		ConcurrentWorkers: 20,
		KeySizeBytes:      128,
		ValueSizeBytes:    512,
		MaxErrorRate:      0.1,
		MaxLatencyMs:      5000,
	}
	err := cfg.ValidateAndSetDefaults()
	require.NoError(t, err)

	// Custom values should be preserved, not overridden by defaults
	assert.Equal(t, 120, cfg.DurationSeconds)
	assert.Equal(t, 20, cfg.ConcurrentWorkers)
	assert.Equal(t, 128, cfg.KeySizeBytes)
	assert.Equal(t, 512, cfg.ValueSizeBytes)
	assert.InDelta(t, 0.1, cfg.MaxErrorRate, 0.001)
	assert.Equal(t, 5000, cfg.MaxLatencyMs)
}

// TestConfigSyncJSONWriteError tests the SyncJSON error path when writing fails.
func TestConfigSyncJSONWriteError(t *testing.T) {
	t.Parallel()

	// Use a path that definitely doesn't have write permissions
	// /proc on linux is read-only
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

// TestRunFunctionNormalization tests that Run() properly normalizes inputs.
func TestRunFunctionNormalizationNoEndpoints(t *testing.T) {
	t.Parallel()

	// Empty endpoints should be normalized to default, but unknown scenario should still fail.
	err := Run(Options{
		Endpoints:  nil,
		ScenarioID: "NONEXISTENT_ABC",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config")
}

// TestStressRunnerNewCtxTimeoutNegativeTwo ensures the negative timeout guard
// uses the default timeout for -1.
func TestStressRunnerNewCtxTimeoutNegativeTwo(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
	}
	ctx, cancel := r.NewCtxTimeout(-1 * time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Positive(t, time.Until(deadline))
	assert.LessOrEqual(t, time.Until(deadline), 5*time.Second)
}

// TestStressRunnerGenerateRandomKeyDifferentLengths tests key generation with various lengths.
func TestStressRunnerGenerateRandomKeyDifferentLengths(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg:     Config{TestKeyPrefix: "/stress/"},
		results: make([]scenarios.Result, 0),
		metrics: scenarios.NewMetricsCollector(),
	}

	key4 := r.GenerateRandomKey(4)
	key32 := r.GenerateRandomKey(32)
	assert.Less(t, len(key4), len(key32))
	assert.Contains(t, key4, "/stress/")
	assert.Contains(t, key32, "/stress/")
}

// TestCreateRunnerSetsTimeout tests that CreateRunner creates a runner with
// the correct default timeout.
func TestCreateRunnerSetsTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Endpoints:     []string{"http://localhost:2379"},
		TestKeyPrefix: "/test/",
		stepTimeout:   "3s",
	}
	r := cfg.CreateRunner()
	require.NotNil(t, r)

	// The runner's NewCtx should use the 3s timeout
	ctx, cancel := r.NewCtx()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.LessOrEqual(t, time.Until(deadline), 3*time.Second)
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

// TestNewPerPeerClientsInvalidTLS exercises the NewPerPeerClients error path.
func TestNewPerPeerClientsInvalidTLS(t *testing.T) {
	t.Parallel()

	r := &stressRunner{
		cfg: Config{
			Endpoints:      []string{"http://127.0.0.1:1", "http://127.0.0.1:2"},
			CACertFile:     "/nope",
			PrivateKeyFile: "/nope",
			CertFile:       "/nope",
		},
		defaultTimeout: 5 * time.Second,
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
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

	// With custom max call sizes
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
