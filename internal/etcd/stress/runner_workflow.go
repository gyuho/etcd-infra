//nolint:nlreturn // Keep runner helpers compact.
package stress

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"

	etcdclient "git.tbd/etcd-infra/internal/etcd/client"
	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

const (
	// DefaultEndpoints is the default etcd endpoint used for stress runs.
	DefaultEndpoints = "https://127.0.0.1:2379"
	// DefaultTestKeyPrefix is the default key prefix for stress test data.
	DefaultTestKeyPrefix = "/etcd-infra-stress/"
)

// Options configures the stress runner.
type Options struct {
	Endpoints      []string
	CACertFile     string
	ClientCertFile string
	ClientKeyFile  string
	TestKeyPrefix  string
	ScenarioID     string
	StepTimeout    string
	Duration       int
	Workers        int
	RequestsPerSec int
}

// Run executes the stress scenarios based on the provided options.
func Run(opts Options) error {
	endpoints := normalizeEndpoints(opts.Endpoints)
	scenarioIDs := resolveScenarioIDs(opts.ScenarioID)

	cfg := Config{
		Endpoints:         endpoints,
		CACertFile:        strings.TrimSpace(opts.CACertFile),
		CertFile:          strings.TrimSpace(opts.ClientCertFile),
		PrivateKeyFile:    strings.TrimSpace(opts.ClientKeyFile),
		TestKeyPrefix:     normalizeTestKeyPrefix(opts.TestKeyPrefix),
		ScenarioIDs:       scenarioIDs,
		DurationSeconds:   opts.Duration,
		ConcurrentWorkers: opts.Workers,
		RequestsPerSecond: opts.RequestsPerSec,
	}
	cfg.stepTimeout = strings.TrimSpace(opts.StepTimeout)

	if err := cfg.ValidateAndSetDefaults(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	logutil.S().Infow("running stress tests",
		"endpoints", endpoints,
		"scenarioCount", len(scenarioIDs),
	)

	results, err := cfg.RunScenarios()
	if err != nil {
		return fmt.Errorf("stress tests failed: %w", err)
	}

	passed, failed := countResults(results)
	logutil.S().Infow("stress tests completed",
		"passed", passed,
		"failed", failed,
		"total", len(results),
	)

	if failed > 0 {
		return fmt.Errorf("stress scenarios failed: %d", failed)
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

	ids := make([]string, 0, len(scenarios.StressIDStringToRunnerFunc))
	for id := range scenarios.StressIDStringToRunnerFunc {
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

var _ scenarios.StressRunner = (*stressRunner)(nil)

// stressRunner implements StressRunner interface.
// Mirrors conformance runner structure exactly.
type stressRunner struct {
	cfg Config

	defaultTimeout time.Duration

	resultsMu sync.RWMutex
	results   []scenarios.Result

	metrics *scenarios.MetricsCollector
	loadGen scenarios.LoadGenerator
}

// NewCtx - same as conformance.
func (r *stressRunner) NewCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.defaultTimeout)
}

// NewCtxTimeout - same as conformance.
func (r *stressRunner) NewCtxTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		logutil.S().Warnw("invalid timeout provided, using default", "provided", timeout, "default", r.defaultTimeout)
		timeout = r.defaultTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// GenerateRandomKey - same as conformance.
func (r *stressRunner) GenerateRandomKey(n int) string {
	return path.Join(r.cfg.TestKeyPrefix, randutil.StringAlphabetsLowerCase(n))
}

// RecordResult - same as conformance.
func (r *stressRunner) RecordResult(rs scenarios.Result) {
	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()

	r.results = append(r.results, rs)
}

// Results - same as conformance.
func (r *stressRunner) Results() scenarios.StressResults {
	r.resultsMu.RLock()
	defer r.resultsMu.RUnlock()

	return r.results
}

// Cleanup - same as conformance.
func (r *stressRunner) Cleanup() error {
	logutil.S().Infow("cleaning up")

	cli, err := r.NewClient()
	if err != nil {
		return fmt.Errorf("create client for cleanup: %w", err)
	}
	defer func() { _ = cli.Close() }()

	start := time.Now()

	// NOTE: Never delete the full keyspace. These tests may run against a live
	// Only delete the test prefix so a shared etcd cluster remains untouched.
	ctx, cancel := r.NewCtx()

	var dresp *clientv3.DeleteResponse
	dresp, err = cli.Delete(ctx, r.cfg.TestKeyPrefix, clientv3.WithPrefix())
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

// NewClient - same as conformance.
func (r *stressRunner) NewClient(opts ...scenarios.StressOpOption) (*clientv3.Client, error) {
	return r.cfg.createClientWithURLs(r.cfg.Endpoints, opts...)
}

// NewPerPeerClients - same as conformance.
func (r *stressRunner) NewPerPeerClients(opts ...scenarios.StressOpOption) ([]*clientv3.Client, error) {
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

// GetMetricsCollector returns the metrics collector.
func (r *stressRunner) GetMetricsCollector() *scenarios.MetricsCollector {
	return r.metrics
}

// GetLoadGenerator returns the load generator for the current config.
//
//nolint:ireturn // Returns LoadGenerator interface for abstraction
func (r *stressRunner) GetLoadGenerator() scenarios.LoadGenerator {
	if r.loadGen == nil {
		r.loadGen = scenarios.NewLoadGenerator(r.cfg.DurationSeconds, r.cfg.RequestsPerSecond)
	}
	return r.loadGen
}

func (r *stressRunner) GetConfig() scenarios.StressConfig {
	return scenarios.StressConfig{
		DurationSeconds:   r.cfg.DurationSeconds,
		ConcurrentWorkers: r.cfg.ConcurrentWorkers,
		RequestsPerSecond: r.cfg.RequestsPerSecond,
		KeySizeBytes:      r.cfg.KeySizeBytes,
		ValueSizeBytes:    r.cfg.ValueSizeBytes,
	}
}

// createClientWithURLs - same as conformance.
func (cfg *Config) createClientWithURLs(urls []string, opts ...scenarios.StressOpOption) (*clientv3.Client, error) {
	options := &scenarios.StressOp{}
	options.ApplyOpts(opts)

	logutil.S().Infow("creating client", "urls", urls, "client", etcdclient.Mode)

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

	client, err := etcdclient.New(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return client, nil
}
