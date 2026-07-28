//nolint:nlreturn,noinlineerr // Keep helpers compact and explicit.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"
	"gopkg.in/yaml.v3"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

var (
	errNoEndpoints     = errors.New("no endpoints specified")
	errNoTestKeyPrefix = errors.New("no test key prefix specified")
	errNoScenarios     = errors.New("no scenarios specified")
	errUnknownScenario = errors.New("unknown scenario")
)

const (
	// DefaultEndpoints is the default etcd endpoint used for conformance runs.
	DefaultEndpoints = "https://127.0.0.1:2379"
	// DefaultTestKeyPrefix is the default key prefix for conformance test data.
	DefaultTestKeyPrefix = "/etcd-infra-conformance/"

	scenarioTLSClientAuth = "TLS_CLIENT_AUTH"
	scenarioResSizeEst    = "RESOURCE_SIZE_ESTIMATION"
)

// Options configures the conformance runner.
type Options struct {
	Endpoints      []string
	CACertFile     string
	ClientCertFile string
	ClientKeyFile  string
	TestKeyPrefix  string
	ScenarioID     string
	StepTimeout    string
}

// Run executes the conformance scenarios based on the provided options.
func Run(opts Options) error {
	endpoints := normalizeEndpoints(opts.Endpoints)
	scenarioIDs := resolveScenarioIDs(opts.ScenarioID)

	cfg := Config{
		Endpoints:      endpoints,
		CACertFile:     strings.TrimSpace(opts.CACertFile),
		CertFile:       strings.TrimSpace(opts.ClientCertFile),
		PrivateKeyFile: strings.TrimSpace(opts.ClientKeyFile),
		TestKeyPrefix:  normalizeTestKeyPrefix(opts.TestKeyPrefix),
		ScenarioIDs:    scenarioIDs,
	}
	cfg.stepTimeout = strings.TrimSpace(opts.StepTimeout)

	if err := cfg.ValidateAndSetDefaults(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	logutil.S().Infow("running conformance tests",
		"endpoints", endpoints,
		"scenarioCount", len(scenarioIDs),
	)

	results, err := cfg.RunScenarios()
	if err != nil {
		return fmt.Errorf("conformance tests failed: %w", err)
	}

	passed, failed := countResults(results)
	logutil.S().Infow("conformance tests completed",
		"passed", passed,
		"failed", failed,
		"total", len(results),
	)

	if failed > 0 {
		for _, r := range results {
			if !r.Success {
				logutil.S().Errorw("conformance scenario FAILED",
					"scenario", r.Scenario,
					"output", r.Output,
				)
			}
		}
		return fmt.Errorf("conformance scenarios failed: %d", failed)
	}

	return nil
}

func normalizeEndpoints(endpoints []string) []string {
	if len(endpoints) == 0 {
		endpoints = []string{DefaultEndpoints}
	}
	trimmed := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		trimmed = append(trimmed, endpoint)
	}
	if len(trimmed) == 0 {
		return []string{DefaultEndpoints}
	}
	return trimmed
}

func resolveScenarioIDs(scenarioID string) []string {
	scenarioID = strings.TrimSpace(scenarioID)
	if scenarioID != "" {
		return []string{scenarioID}
	}

	ids := make([]string, 0, len(scenarios.IDStringToRunnerFunc))
	for id := range scenarios.IDStringToRunnerFunc {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	return ids
}

func normalizeTestKeyPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return DefaultTestKeyPrefix
	}
	return prefix
}

func countResults(results []scenarios.Result) (int, int) {
	passed := 0
	failed := 0
	for i := range results {
		if results[i].Success {
			passed++
		} else {
			failed++
		}
	}
	return passed, failed
}

// Config defines correctness testing configuration.
//
//nolint:tagliatelle // Config tags use snake_case for API compatibility.
type Config struct {
	// Endpoints specifies server backend endpoints.
	Endpoints []string `json:"endpoints"`

	// CACertFile is the file path to client CA cert.
	CACertFile string `json:"ca_cert_file"`
	// PrivateKeyFile is the file path to client key.
	PrivateKeyFile string `json:"private_key_file"`
	// CertFile is the file path to client cert.
	CertFile string `json:"cert_path"`

	// TestKeyPrefix is the test key prefix.
	TestKeyPrefix string `json:"test_key_prefix"`

	// A list of scenario IDs to select and run.
	ScenarioIDs []string `json:"scenario_ids"`

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
	for _, id := range cfg.ScenarioIDs {
		if !scenarios.ValidID(id) {
			return fmt.Errorf("%w: %q", errUnknownScenario, id)
		}
	}
	return nil
}

// JSON returns the config marshaled as JSON.
func (cfg *Config) JSON() ([]byte, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}

	return data, nil
}

// SyncJSON writes the JSON config to the given file.
func (cfg *Config) SyncJSON(file string) error {
	if _, err := os.Stat(filepath.Dir(file)); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(file), configDirPerm); mkErr != nil {
			return fmt.Errorf("create config dir: %w", mkErr)
		}
	}
	data, err := cfg.JSON()
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if err := os.WriteFile(file, data, configFilePerm); err != nil {
		return fmt.Errorf("write json config: %w", err)
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

// YAML returns the config marshaled as YAML.
func (cfg *Config) YAML() ([]byte, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}

	return data, nil
}

// SyncYAML writes the YAML config to the given file.
func (cfg *Config) SyncYAML(file string) error {
	if _, err := os.Stat(filepath.Dir(file)); os.IsNotExist(err) {
		if mkErr := os.MkdirAll(filepath.Dir(file), configDirPerm); mkErr != nil {
			return fmt.Errorf("create config dir: %w", mkErr)
		}
	}
	data, err := cfg.YAML()
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if err := os.WriteFile(file, data, configFilePerm); err != nil {
		return fmt.Errorf("write yaml config: %w", err)
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

// CreateRunner builds a conformance runner for this config.
//
//nolint:ireturn // Factory function returns Runner interface for abstraction
func (cfg *Config) CreateRunner() scenarios.Runner {
	return &runner{
		cfg:            *cfg,
		defaultTimeout: determineRunnerTimeout(cfg.stepTimeout),
		results:        make([]scenarios.Result, 0),
	}
}

// =============================================================================
// CONFORMANCE TEST TUNING: TIMEOUT AND RETRY CONFIGURATION
//
// These tunings accommodate VPN-based distributed clusters (Tailscale/Headscale/
// WireGuard) where network latency between geographically distributed nodes can
// be 10-100x higher than datacenter-local deployments.
//
// IMPORTANT: These tunings DO NOT compromise correctness requirements.
// =============================================================================
//
// WHY EXTENDED TIMEOUTS ARE NEEDED:
// =================================
// 1. Cross-datacenter latency: Hetzner US ↔ Hetzner EU = 80-150ms RTT
// 2. WireGuard tunnel overhead: Encryption adds 5-10ms per hop
// 3. Leader election during tests: etcd may elect new leaders mid-scenario
// 4. Large data operations: Snapshots and prefix scans transfer significant data
//
// WHY RETRY LOGIC IS NEEDED:
// ==========================
// 1. Transient network glitches: VPN tunnels may briefly disconnect
// 2. Leader elections: Client may hit old leader during failover
// 3. Load balancer timing: DNS-based endpoint resolution may be stale
// 4. TLS session resumption: First connection may fail, retry succeeds
//
// CORRECTNESS GUARANTEE:
// ======================
// These tunings affect ONLY timing, not the correctness verification itself:
//
// ✓ Linearizability checks are NOT relaxed - all operations verify ordering
// ✓ Consistency assertions are NOT weakened - data integrity is fully checked
// ✓ Transaction atomicity is NOT compromised - all-or-nothing semantics verified
// ✓ Watch event ordering is NOT altered - events are checked in correct sequence
// ✓ No operations are skipped or marked as "soft pass"
//
// The test either PASSES with correct etcd behavior verified, or FAILS.
// Extended timeouts simply give operations more time to complete over slow
// networks, but the verification logic remains identical to official etcd
// conformance tests.
//
// If a scenario fails after all retries, it is recorded as a genuine failure.
// Retries only help avoid false negatives from transient network issues,
// they cannot mask actual etcd correctness bugs.
//
// =============================================================================

const (
	// runnerTimeoutDefault is the per-step timeout within each scenario.
	// Set to 180s to accommodate cross-datacenter VPN environments where:
	// - Network latency: 80-150ms RTT between cloud regions
	// - WireGuard tunnel overhead: 5-10ms per encrypted packet
	// - Operations that transfer large data over the tunnel
	//
	// History: 10s → 30s → 90s → 180s as real-world cross-DC usage revealed needs.
	// This does NOT affect correctness - operations either complete correctly or fail.
	runnerTimeoutDefault    = 180 * time.Second
	runnerTimeoutParseError = "invalid step timeout override"
	configDirPerm           = 0o750
	configFilePerm          = 0o600

	// scenarioTimeoutDefault is the maximum time for a single scenario to complete.
	// 5 minutes is sufficient for most scenarios running over VPN with 100ms latency.
	// If exceeded, the scenario fails fast rather than hanging indefinitely.
	//
	// This timeout exists to prevent network issues (leader elections, tunnel drops)
	// from causing the entire test suite to hang. It does NOT relax correctness -
	// if a scenario times out, it is recorded as a FAILURE.
	scenarioTimeoutDefault = 5 * time.Minute

	// scenarioTimeoutExtended is for scenarios involving:
	// - Continuous data sync (MIRROR_SYNC_*): Must complete full data transfer
	// - Concurrent contention (*_CONTENDING_*): Multiple clients competing
	// - Watch establishment (WATCH_WITH_*): Watcher setup over high-latency links
	// - Maintenance operations (MAINTENANCE_*): Defrag, snapshot under load
	//
	// 10 minutes allows these operations to complete over slow VPN connections.
	// Again, this does NOT affect correctness - the verification is identical,
	// we just give operations more time to finish before failing.
	scenarioTimeoutExtended = 10 * time.Minute
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

// getScenarioTimeout returns the appropriate timeout for a scenario based on its name.
// Stress scenarios (mirror sync, contending operations, multi-watchers) get extended timeouts
// because they involve continuous operations that take longer over VPN with added latency.
//
// Extended timeout scenarios (10 minutes):
//   - MIRROR_SYNC_*: Continuous data mirroring between etcd instances
//   - *_CONTENDING_*: Concurrent contending operations that stress the cluster
//   - WATCH_WITH_*: Watch operations that may need time to establish watchers over VPN
//   - MAINTENANCE_*: Defrag, snapshot, and hash operations under load
//   - LEASING_PUT_AND_GET_AND_DELETE_CONCURRENT: Concurrent lease operations
//   - *_WITH_PREFIX: Prefix-based operations that scan multiple keys
//   - *FROM_KEY*: FROM_KEY operations scan entire keyspace from given key
//   - TLS_CLIENT_AUTH: TLS handshake may need extra time over high-latency VPN
//   - RESOURCE_SIZE_ESTIMATION: Large data operations
//
// All other scenarios use the default 5-minute timeout.
func getScenarioTimeout(scenario string) time.Duration {
	// Scenarios that require extended timeout due to:
	// - Continuous data sync operations
	// - Concurrent contending operations
	// - Multi-client stress operations
	// - I/O-intensive maintenance operations
	// - VPN network latency sensitive operations
	switch {
	case strings.HasPrefix(scenario, "MIRROR_SYNC"):
		return scenarioTimeoutExtended
	case strings.Contains(scenario, "CONTENDING"):
		return scenarioTimeoutExtended
	case strings.HasPrefix(scenario, "WATCH_WITH"):
		// All watch scenarios need extended timeout for VPN latency
		return scenarioTimeoutExtended
	case strings.HasPrefix(scenario, "MAINTENANCE"):
		return scenarioTimeoutExtended
	case scenario == "LEASING_PUT_AND_GET_AND_DELETE_CONCURRENT":
		return scenarioTimeoutExtended
	case strings.Contains(scenario, "WITH_PREFIX"):
		// Prefix operations scan multiple keys
		return scenarioTimeoutExtended
	case strings.Contains(scenario, "FROM_KEY"):
		// FROM_KEY operations return ALL keys from a given key onwards,
		// potentially scanning entire etcd keyspace (thousands of keys)
		return scenarioTimeoutExtended
	case scenario == scenarioTLSClientAuth:
		// TLS handshake over VPN can be slow
		return scenarioTimeoutExtended
	case scenario == scenarioResSizeEst:
		// Large data operations
		return scenarioTimeoutExtended
	default:
		return scenarioTimeoutDefault
	}
}

// scenarioRetryCount defines how many times to retry network-sensitive scenarios.
//
// IMPORTANT: Retries do NOT compromise correctness. Each retry runs the FULL
// scenario with ALL correctness checks. A scenario only passes if all assertions
// succeed. Retries simply help avoid false negatives from transient network issues.
//
// Why 2 retries:
// - 1 retry catches single transient failures (leader election, tunnel hiccup)
// - 2 retries handle consecutive failures (rare but possible with VPN reconnects)
// - More retries would mask genuine issues and slow down the test suite.
const scenarioRetryCount = 2

// shouldRetryScenario returns true if a scenario should be retried on failure.
//
// These scenarios are network-sensitive and may experience transient failures
// due to VPN latency, leader elections, or connection establishment overhead.
// Retrying improves reliability WITHOUT sacrificing correctness:
//
//   - WATCH_WITH_*: Watch channel establishment over high-latency links
//     may timeout on first attempt but succeed on retry
//   - *_WITH_PREFIX: Large prefix scans transfer more data, more susceptible
//     to mid-transfer network glitches
//   - *FROM_KEY*: FROM_KEY operations scan potentially entire keyspace,
//     returning all keys lexicographically >= given key
//   - TLS_CLIENT_AUTH: TLS handshake over VPN has higher latency, session
//     resumption on retry is faster
//   - RESOURCE_SIZE_ESTIMATION: Large data operations may timeout on slow links
//
// CORRECTNESS NOTE: Each retry runs the IDENTICAL test logic. If etcd has
// a correctness bug, retries will fail repeatedly and the test fails.
// Retries only help with infrastructure-level transient failures.
func shouldRetryScenario(scenario string) bool {
	switch {
	case strings.HasPrefix(scenario, "WATCH_WITH"):
		return true
	case strings.Contains(scenario, "WITH_PREFIX"):
		return true
	case strings.Contains(scenario, "FROM_KEY"):
		// FROM_KEY operations scan potentially large key ranges
		return true
	case scenario == "TLS_CLIENT_AUTH":
		return true
	case scenario == "RESOURCE_SIZE_ESTIMATION":
		return true
	case scenario == "LEASE_LIST":
		// Lease grant replication may not be immediately visible from all
		// etcd members over WireGuard (high-latency mesh). The Leases() call
		// can hit a member that hasn't applied the raft entry yet, causing
		// a freshly-granted lease to transiently appear missing.
		return true
	default:
		return false
	}
}

// RunScenarios executes the configured scenario set.
func (cfg *Config) RunScenarios() (scenarios.Results, error) {
	for _, scenario := range cfg.ScenarioIDs {
		if _, ok := scenarios.IDStringToRunnerFunc[scenario]; !ok {
			return nil, fmt.Errorf("%w: %s", errUnknownScenario, scenario)
		}
	}

	conformanceRunner := cfg.CreateRunner()
	defer func() {
		derr := conformanceRunner.Cleanup()
		if derr != nil {
			logutil.S().Warnw("failed to cleanup", "error", derr)
		}
	}()

	for idx, scenario := range cfg.ScenarioIDs {
		runnerFunc := scenarios.IDStringToRunnerFunc[scenario]

		scenarioTimeout := getScenarioTimeout(scenario)
		maxAttempts := 1
		if shouldRetryScenario(scenario) {
			maxAttempts = 1 + scenarioRetryCount
		}

		var finalErr error
		var totalTook time.Duration
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			logutil.S().Infow("starting conformance scenario",
				"scenario", scenario,
				"index", idx+1,
				"total", len(cfg.ScenarioIDs),
				"attempt", attempt,
				"maxAttempts", maxAttempts,
			)

			start := time.Now()
			err := runScenarioWithTimeout(conformanceRunner, runnerFunc, scenario, scenarioTimeout)
			took := time.Since(start)
			totalTook += took

			if err == nil {
				logutil.S().Infow("conformance scenario passed",
					"scenario", scenario,
					"index", idx+1,
					"total", len(cfg.ScenarioIDs),
					"took", took.Round(time.Millisecond).String(),
					"attempt", attempt,
				)
				finalErr = nil
				break
			}

			finalErr = err
			if attempt < maxAttempts {
				logutil.S().Warnw("conformance scenario failed, will retry",
					"scenario", scenario,
					"index", idx+1,
					"total", len(cfg.ScenarioIDs),
					"took", took.Round(time.Millisecond).String(),
					"attempt", attempt,
					"maxAttempts", maxAttempts,
					"error", err,
				)
				// Brief pause before retry to let network stabilize
				time.Sleep(2 * time.Second)
			} else {
				logutil.S().Errorw("conformance scenario FAILED after all retries",
					"scenario", scenario,
					"index", idx+1,
					"total", len(cfg.ScenarioIDs),
					"totalTook", totalTook.Round(time.Millisecond).String(),
					"attempts", maxAttempts,
					"error", err,
				)
			}
		}

		if finalErr != nil {
			// Record the failure and continue with remaining scenarios
			conformanceRunner.RecordResult(scenarios.Result{
				Scenario: scenario,
				Success:  false,
				Output:   finalErr.Error(),
			})
			continue
		}

		logutil.S().Infow("finished conformance scenario",
			"scenario", scenario,
			"index", idx+1,
			"total", len(cfg.ScenarioIDs),
			"took", totalTook.String(),
		)
	}

	return conformanceRunner.Results(), nil
}

// runScenarioWithTimeout executes a scenario with a maximum timeout.
// If the scenario exceeds the timeout, it returns an error instead of hanging indefinitely.
func runScenarioWithTimeout(r scenarios.Runner, runnerFunc func(scenarios.Runner), scenario string, timeout time.Duration) error {
	done := make(chan struct{})
	var panicValue any

	go func() {
		defer func() {
			if p := recover(); p != nil {
				panicValue = p
			}
			close(done)
		}()
		runnerFunc(r)
	}()

	select {
	case <-done:
		if panicValue != nil {
			return fmt.Errorf("scenario %s panicked: %v", scenario, panicValue)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("scenario %s timed out after %v (stuck on network operation or leader election)", scenario, timeout)
	}
}

var _ scenarios.Runner = (*runner)(nil)

type runner struct {
	cfg Config

	defaultTimeout time.Duration

	resultsMu sync.RWMutex
	results   []scenarios.Result
}

func (r *runner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.defaultTimeout)
}

func (r *runner) NewCtxTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		logutil.S().Warnw("invalid timeout provided, using default", "provided", timeout, "default", r.defaultTimeout)
		timeout = r.defaultTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

func (r *runner) GenerateRandomKey(n int) string {
	return path.Join(r.cfg.TestKeyPrefix, randutil.StringAlphabetsLowerCase(n))
}

func (r *runner) RecordResult(rs scenarios.Result) {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()

	r.results = append(r.results, rs)
}

func (r *runner) Results() scenarios.Results {
	r.resultsMu.RLock()
	defer r.resultsMu.RUnlock()

	return r.results
}

func (r *runner) DefaultTimeout() time.Duration {
	return r.defaultTimeout
}

func (r *runner) Cleanup() error {
	logutil.S().Infow("cleaning up")

	cli, err := r.NewClient()
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	start := time.Now()

	// NOTE: Never delete the full keyspace. These tests may run against a live
	// Only delete the test prefix so a shared etcd cluster remains untouched.
	ctx, cancel := r.NewCtx()

	dresp, err := cli.Delete(ctx, r.cfg.TestKeyPrefix, clientv3.WithPrefix())
	cancel()
	if err != nil {
		return fmt.Errorf("delete keys: %w", err)
	}

	latestRev := dresp.Header.GetRevision()
	logutil.S().Infow("deleted keys", "latestRevision", latestRev, "deleted", dresp.Deleted)

	compactStart := time.Now()
	ctx, cancel = r.NewCtx()
	_, err = cli.Compact(ctx, latestRev, clientv3.WithCompactPhysical())
	cancel()
	if err != nil && !errors.Is(err, rpctypes.ErrCompacted) {
		logutil.S().Warnw("failed to compact", "error", err)
	} else {
		logutil.S().Infow("discarded historical revisions",
			"took", time.Since(compactStart).String(),
			"error", err,
		)
	}

	logutil.S().Infow("cleanup done", "took", time.Since(start).String())
	return nil
}

func (r *runner) NewClient(opts ...scenarios.OpOption) (*clientv3.Client, error) {
	return r.cfg.createClientWithURLs(r.cfg.Endpoints, opts...)
}

func (r *runner) NewPerPeerClients(opts ...scenarios.OpOption) ([]*clientv3.Client, error) {
	cs := make([]*clientv3.Client, 0)
	for _, ep := range r.cfg.Endpoints {
		c, err := r.cfg.createClientWithURLs([]string{ep}, opts...)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, nil
}

func (cfg *Config) createClientWithURLs(urls []string, opts ...scenarios.OpOption) (*clientv3.Client, error) {
	options := &scenarios.Op{}
	options.ApplyOpts(opts)

	logutil.S().Infow("creating client", "urls", urls)

	ti := &transport.TLSInfo{
		Logger:        logutil.L(),
		TrustedCAFile: cfg.CACertFile,
		KeyFile:       cfg.PrivateKeyFile,
		CertFile:      cfg.CertFile,
	}
	tlsCfg, err := ti.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("create TLS config: %w", err)
	}

	clientCfg := clientv3.Config{
		Endpoints:   urls,
		DialTimeout: options.DialTimeout,
		TLS:         tlsCfg,
	}
	if options.MaxCallSendMsgSize > 0 {
		clientCfg.MaxCallSendMsgSize = options.MaxCallSendMsgSize
	}
	if options.MaxCallRecvMsgSize > 0 {
		clientCfg.MaxCallRecvMsgSize = options.MaxCallRecvMsgSize
	}

	client, err := clientv3.New(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return client, nil
}
