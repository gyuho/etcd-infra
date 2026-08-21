//nolint:nlreturn // Keep config helpers compact.
package stress

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
	logutil "git.tbd/etcd-infra/pkg/log"
)

var (
	errNoEndpoints     = errors.New("no endpoints specified")
	errNoTestKeyPrefix = errors.New("no test key prefix specified")
	errNoScenarios     = errors.New("no scenarios specified")
	errUnknownScenario = errors.New("unknown scenario")
)

// Config defines stress testing configuration.
// Mirrors conformance Config structure exactly.
//
//nolint:tagliatelle,tagalign // Config tags use snake_case for API compatibility.
type Config struct {
	// Same fields as conformance
	Endpoints      []string `json:"endpoints"         yaml:"endpoints"`
	CACertFile     string   `json:"ca_cert_file"      yaml:"ca_cert_file"`
	PrivateKeyFile string   `json:"private_key_file"  yaml:"private_key_file"`
	CertFile       string   `json:"cert_file"         yaml:"cert_file"`
	TestKeyPrefix  string   `json:"test_key_prefix"   yaml:"test_key_prefix"`

	// Stress-specific scenarios (same pattern as ScenarioIDs in conformance)
	ScenarioIDs []string `json:"scenario_ids"        yaml:"scenario_ids"`

	// Stress-specific parameters with explicit units (cognitive load principle)
	DurationSeconds   int `json:"duration_seconds"   yaml:"duration_seconds"`
	ConcurrentWorkers int `json:"concurrent_workers" yaml:"concurrent_workers"`
	RequestsPerSecond int `json:"requests_per_second" yaml:"requests_per_second"`

	// Data sizes with explicit units
	KeySizeBytes   int `json:"key_size_bytes"   yaml:"key_size_bytes"`
	ValueSizeBytes int `json:"value_size_bytes" yaml:"value_size_bytes"`

	// Safety limits
	MaxErrorRate float64 `json:"max_error_rate" yaml:"max_error_rate"`
	MaxLatencyMs int     `json:"max_latency_ms" yaml:"max_latency_ms"`

	// Warmup and cooldown
	WarmupDurationSeconds   int `json:"warmup_duration_seconds"   yaml:"warmup_duration_seconds"`
	CooldownDurationSeconds int `json:"cooldown_duration_seconds" yaml:"cooldown_duration_seconds"`

	// stepTimeout is an unexported runtime field (not serialized).
	// Set by Run() to pass the step timeout to CreateRunner().
	stepTimeout string
}

// ValidateAndSetDefaults validates the config and applies specs.
func (cfg *Config) ValidateAndSetDefaults() error {
	if len(cfg.Endpoints) == 0 {
		return errNoEndpoints
	}
	if cfg.TestKeyPrefix == "" {
		return errNoTestKeyPrefix
	}
	if len(cfg.ScenarioIDs) == 0 {
		return errNoScenarios
	}
	// Validate scenario IDs - same pattern as conformance
	for _, id := range cfg.ScenarioIDs {
		if !scenarios.ValidStressID(id) {
			return fmt.Errorf("%w: %q", errUnknownScenario, id)
		}
	}

	// Set defaults
	if cfg.DurationSeconds <= 0 {
		cfg.DurationSeconds = 60
	}
	if cfg.ConcurrentWorkers <= 0 {
		cfg.ConcurrentWorkers = 10
	}
	if cfg.KeySizeBytes <= 0 {
		cfg.KeySizeBytes = 64
	}
	if cfg.ValueSizeBytes <= 0 {
		cfg.ValueSizeBytes = 256
	}
	if cfg.MaxErrorRate <= 0 {
		cfg.MaxErrorRate = 0.5
	}
	if cfg.MaxLatencyMs <= 0 {
		cfg.MaxLatencyMs = 30000
	}

	return nil
}

// JSON methods - exactly like conformance.
func (cfg *Config) JSON() ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}

	return data, nil
}

// SyncJSON writes the JSON config to the given file.
func (cfg *Config) SyncJSON(file string) error {
	_, statErr := os.Stat(filepath.Dir(file))
	if os.IsNotExist(statErr) {
		mkErr := os.MkdirAll(filepath.Dir(file), configDirPerm)
		if mkErr != nil {
			return fmt.Errorf("create config dir: %w", mkErr)
		}
	}
	data, err := cfg.JSON()
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	writeErr := os.WriteFile(file, data, configFilePerm)
	if writeErr != nil {
		return fmt.Errorf("write json config: %w", writeErr)
	}

	return nil
}

// LoadConfigJSON reads a JSON config from a file.
func LoadConfigJSON(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read json config: %w", err)
	}
	return ParseConfigJSON(data)
}

// ParseConfigJSON parses JSON bytes into a Config.
func ParseConfigJSON(data []byte) (*Config, error) {
	var cfg Config
	err := json.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("unmarshal json config: %w", err)
	}
	return &cfg, nil
}

// YAML methods - exactly like conformance.
func (cfg *Config) YAML() ([]byte, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}

	return data, nil
}

// SyncYAML writes the YAML config to the given file.
func (cfg *Config) SyncYAML(file string) error {
	_, statErr := os.Stat(filepath.Dir(file))
	if os.IsNotExist(statErr) {
		mkErr := os.MkdirAll(filepath.Dir(file), configDirPerm)
		if mkErr != nil {
			return fmt.Errorf("create config dir: %w", mkErr)
		}
	}
	data, err := cfg.YAML()
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	writeErr := os.WriteFile(file, data, configFilePerm)
	if writeErr != nil {
		return fmt.Errorf("write yaml config: %w", writeErr)
	}

	return nil
}

// LoadConfigYAML reads a YAML config from a file.
func LoadConfigYAML(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read yaml config: %w", err)
	}
	return ParseConfigYAML(data)
}

// ParseConfigYAML parses YAML bytes into a Config.
func ParseConfigYAML(data []byte) (*Config, error) {
	var cfg Config
	err := yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("unmarshal yaml config: %w", err)
	}
	return &cfg, nil
}

// CreateRunner - same pattern as conformance.
//
//nolint:ireturn // Factory function returns StressRunner interface for abstraction
func (cfg *Config) CreateRunner() scenarios.StressRunner {
	return &stressRunner{
		cfg:            *cfg,
		defaultTimeout: determineRunnerTimeout(cfg.stepTimeout),
		results:        make([]scenarios.Result, 0),
		metrics:        scenarios.NewMetricsCollector(),
	}
}

const (
	runnerTimeoutDefault    = 10 * time.Second
	runnerTimeoutParseError = "invalid step timeout override"
	configDirPerm           = 0o750
	configFilePerm          = 0o600
)

func determineRunnerTimeout(stepTimeout string) time.Duration {
	stepTimeout = strings.TrimSpace(stepTimeout)
	if stepTimeout != "" {
		d, err := time.ParseDuration(stepTimeout)
		switch {
		case err != nil:
			logutil.S().Warnw(runnerTimeoutParseError, "value", stepTimeout, "error", err)
		case d > 0:
			return d
		default:
			logutil.S().Warnw(runnerTimeoutParseError, "value", stepTimeout, "error", "duration must be positive")
		}
	}

	return runnerTimeoutDefault
}

// RunScenarios - same pattern as conformance.
func (cfg *Config) RunScenarios() (scenarios.StressResults, error) {
	for _, scenario := range cfg.ScenarioIDs {
		if _, ok := scenarios.StressIDStringToRunnerFunc[scenario]; !ok {
			return nil, fmt.Errorf("%w: %s", errUnknownScenario, scenario)
		}
	}

	runner := cfg.CreateRunner()
	defer func() {
		derr := runner.Cleanup()
		if derr != nil {
			logutil.S().Warnw("failed to cleanup", "error", derr)
		}
	}()

	// Default cooldown between scenarios for VM recovery
	cooldown := time.Duration(cfg.CooldownDurationSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = 10 * time.Second // Default 10s cooldown between scenarios
	}

	for idx, scenario := range cfg.ScenarioIDs {
		runnerFunc := scenarios.StressIDStringToRunnerFunc[scenario]

		// Wait for cluster to become healthy before starting scenario
		if err := waitForClusterHealth(runner, 30*time.Second); err != nil {
			logutil.S().Warnw("cluster health check failed before scenario",
				"scenario", scenario,
				"error", err,
			)
			// Continue anyway - the scenario will record failures
		}

		logutil.S().Infow("starting stress scenario",
			"scenario", scenario,
			"index", idx+1,
			"total", len(cfg.ScenarioIDs),
		)

		start := time.Now()
		beforeCount := len(runner.Results())
		runnerFunc(runner)

		results := runner.Results()
		if len(results) > beforeCount {
			last := results[len(results)-1]
			if last.Success {
				logutil.S().Infow("SCENARIO PASSED",
					"scenario", scenario,
					"index", idx+1,
					"total", len(cfg.ScenarioIDs),
					"took", time.Since(start).String(),
					"output", last.Output,
					// Request counters in the pass line: benchmark harnesses
					// need per-scenario throughput without re-deriving it.
					"requests", last.TotalRequests,
					"successful", last.SuccessfulRequests,
					"p99_ms", last.P99Latency.Milliseconds(),
					"avg_ms", last.AverageLatency.Milliseconds(),
				)
			} else {
				logutil.S().Warnw("SCENARIO FAILED",
					"scenario", scenario,
					"index", idx+1,
					"total", len(cfg.ScenarioIDs),
					"took", time.Since(start).String(),
					"output", last.Output,
					"totalRequests", last.TotalRequests,
					"failedRequests", last.FailedRequests,
				)
			}
		} else {
			logutil.S().Warnw("scenario did not record a result",
				"scenario", scenario,
				"index", idx+1,
				"total", len(cfg.ScenarioIDs),
				"took", time.Since(start).String(),
			)
		}

		// Cooldown between scenarios to let the cluster recover
		if idx < len(cfg.ScenarioIDs)-1 {
			logutil.S().Infow("cooldown between scenarios",
				"duration", cooldown.String(),
			)
			time.Sleep(cooldown)
		}
	}

	return runner.Results(), nil
}

// waitForClusterHealth waits for the etcd cluster to become healthy.
func waitForClusterHealth(runner scenarios.StressRunner, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		cli, err := runner.NewClient()
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}

		// Try a simple Get operation to verify cluster is responding
		ctx, cancel := runner.NewCtxTimeout(5 * time.Second)
		_, err = cli.Get(ctx, "__health_check__")
		cancel()
		_ = cli.Close()

		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("cluster not healthy after %v: %w", timeout, lastErr)
}
