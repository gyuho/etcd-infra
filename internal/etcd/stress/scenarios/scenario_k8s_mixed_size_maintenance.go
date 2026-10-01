package scenarios

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	clientv3 "go.etcd.io/etcd/client/v3"

	logutil "git.tbd/etcd-infra/pkg/log"
	"git.tbd/etcd-infra/pkg/randutil"
)

// k8sObjectClass is one Kubernetes resource type in the mixed-size workload:
// its retained object count, value size, and share of the offered write rate.
type k8sObjectClass struct {
	name     string
	resource string
	objects  int     // retained objects at keyspace scale 1
	bytes    int     // value size; the pod class takes --value-bytes when set
	share    float64 // fraction of the offered write rate
	ttl      bool    // created with a TTL lease and never updated (events)
}

// k8sMixedClasses is the size/rate mix. Sources for the shapes: node leases
// are small and renewed by every kubelet; events are ~1 KB, TTL-bound and
// write-heavy; pod objects are 10–20 KB on average in real clusters and the
// object-size KEP targets 30 KB next and 100 KB eventually (upstream reports,
// not measurements); configmaps are a few KB; CRD-backed objects carry large
// schemas. The pod class is the size under test.
var k8sMixedClasses = []k8sObjectClass{
	{name: "lease", resource: "leases/kube-node-lease", objects: 500, bytes: 400, share: 0.40},
	{name: "event", resource: "events", objects: 0, bytes: 1024, share: 0.25, ttl: true},
	{name: "pod", resource: "pods", objects: 5000, bytes: 16 * 1024, share: 0.25},
	{name: "configmap", resource: "configmaps", objects: 1000, bytes: 4 * 1024, share: 0.08},
	{name: "crd", resource: "apiextensions.k8s.io/customresourcedefinitions", objects: 200, bytes: 100 * 1024, share: 0.02},
}

const (
	k8sMixedDefaultRate     = 500
	k8sMixedMaxInflight     = 1024
	k8sMixedRequestTimeout  = 10 * time.Second
	k8sMixedEventTTLSeconds = 60
	k8sMixedMaxP99Ms        = 1000
)

// ClassResult summarizes one object class of a mixed-size scenario.
type ClassResult struct {
	Class          string  `json:"class"`
	ValueBytes     int     `json:"valueBytes"`
	Offered        int64   `json:"offered"`
	Successful     int64   `json:"successful"`
	Failed         int64   `json:"failed"`
	MissedArrivals int64   `json:"missedArrivals"`
	P50LatencyMs   float64 `json:"p50LatencyMs"`
	P99LatencyMs   float64 `json:"p99LatencyMs"`
	MaxLatencyMs   float64 `json:"maxLatencyMs"`
}

// MaintenanceEvent records one compaction or defragmentation.
type MaintenanceEvent struct {
	Kind      string  `json:"kind"`
	Member    string  `json:"member,omitempty"`
	Revision  int64   `json:"revision,omitempty"`
	StartS    float64 `json:"startS"`
	DurationS float64 `json:"durationS"`
	Error     string  `json:"error,omitempty"`
}

type k8sClassState struct {
	k8sObjectClass

	keys    int
	payload string
	metrics *MetricsCollector
	offered atomic.Int64
	missed  atomic.Int64
}

// RunK8sMixedSizeMaintenance drives a Kubernetes-shaped mix of small and large
// objects open-loop while the apiserver-style maintenance runs.
//
// WHAT: node-lease renewals, TTL-bound events, pod status updates (the size
// under test, --value-bytes), configmap updates, and large CRD-backed objects
// are written at fixed shares of --rps, over a preloaded keyspace, with
// informer watches on every collection. Every --compact-interval seconds the
// scenario physically compacts to the revision observed one interval earlier
// (the kube-apiserver compactor's target), and with --defrag-after-compact it then
// defragments each member, followers first and leader last.
//
// WHY: storage engines differ most when large and small objects share one
// keyspace under maintenance: a small-object path stalls behind large-value
// copies, and a defrag that blocks writes shows up as missed arrivals for
// every class. Closed-loop clients hide such stalls (they simply send less),
// so arrivals are scheduled open-loop: latency is measured from the scheduled
// time, and an arrival with no free in-flight slot is counted as missed.
//
// HOW: the scenario fails on any failed request, any missed arrival, any
// maintenance error, a watch error, or a per-class p99 above 1 s. Per-class
// results and the maintenance log are recorded in the result.
func RunK8sMixedSizeMaintenance(runner StressRunner) {
	logutil.S().Infow("running", "scenario", K8sMixedSizeMaintenance.String())

	result := &Result{
		Scenario:  K8sMixedSizeMaintenance.String(),
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

	cfg := runner.GetConfig()
	metrics := runner.GetMetricsCollector()
	metrics.Reset()

	root := runner.GenerateRandomKey(keySize(cfg, 8)) + "/registry"
	classes := newK8sClassStates(cfg)

	// Preload the retained keyspace before measuring, as a running cluster
	// already holds its objects.
	preloadStart := time.Now()
	if err := preloadK8sClasses(runner, cli, root, classes); err != nil {
		result.Success = false
		result.Output = fmt.Sprintf("preload failed: %v", err)

		return
	}
	logutil.S().Infow("preloaded keyspace", "took", time.Since(preloadStart).String())

	var eventsReceived, watchErrors atomic.Int64
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	for _, c := range classes {
		ch := clientv3.NewWatcher(cli).Watch(watchCtx, root+"/"+c.resource+"/", clientv3.WithPrefix())
		go func() {
			for resp := range ch {
				if err := resp.Err(); err != nil {
					watchErrors.Add(1)
					continue
				}
				eventsReceived.Add(int64(len(resp.Events)))
			}
		}()
	}

	rate := cfg.RequestsPerSecond
	if rate <= 0 {
		rate = k8sMixedDefaultRate
	}
	start := time.Now()
	stopAt := start.Add(scenarioDuration(cfg))

	var (
		revMu sync.Mutex
		revs  []revisionSample
		maint []MaintenanceEvent
	)
	maintCtx, maintCancel := context.WithCancel(context.Background())
	maintDone := make(chan struct{})
	go func() {
		defer close(maintDone)
		maint = runK8sMaintenance(maintCtx, cli, cfg, start, func(cutoff time.Time) int64 {
			revMu.Lock()
			defer revMu.Unlock()
			return compactTarget(revs, cutoff)
		})
	}()

	eventLeases := newTTLLeasePool(cli, k8sMixedEventTTLSeconds)
	sem := make(chan struct{}, k8sMixedMaxInflight)
	var inflight sync.WaitGroup
	var schedulers sync.WaitGroup
	for _, c := range classes {
		classRate := float64(rate) * c.share
		if classRate <= 0 {
			continue
		}
		schedulers.Go(func() {
			interval := time.Duration(float64(time.Second) / classRate)
			for i := 0; ; i++ {
				due := start.Add(time.Duration(i) * interval)
				if !due.Before(stopAt) {
					return
				}
				if d := time.Until(due); d > 0 {
					time.Sleep(d)
				}
				c.offered.Add(1)
				select {
				case sem <- struct{}{}:
				default:
					c.missed.Add(1)
					continue
				}
				inflight.Go(func() {
					defer func() { <-sem }()
					rev, err := writeK8sObject(cli, eventLeases, root, c, i, due)
					latencyMs := float64(time.Since(due)) / float64(time.Millisecond)
					if err != nil {
						c.metrics.RecordFailure(latencyMs, err.Error())
						metrics.RecordFailure(latencyMs, err.Error())
						return
					}
					c.metrics.RecordSuccess(latencyMs)
					metrics.RecordSuccess(latencyMs)
					metrics.RecordBytesWritten(int64(c.bytes))
					revMu.Lock()
					revs = append(revs, revisionSample{at: time.Now(), rev: rev})
					revMu.Unlock()
				})
			}
		})
	}
	schedulers.Wait()
	inflight.Wait()
	maintCancel()
	<-maintDone
	watchCancel()
	eventLeases.revokeAll(runner)

	var missed int64
	for _, c := range classes {
		s := c.metrics.GetStatistics()
		missed += c.missed.Load()
		result.Classes = append(result.Classes, ClassResult{
			Class: c.name, ValueBytes: c.bytes, Offered: c.offered.Load(),
			Successful: s.SuccessCount, Failed: s.FailureCount, MissedArrivals: c.missed.Load(),
			P50LatencyMs: s.P50LatencyMs, P99LatencyMs: s.P99LatencyMs, MaxLatencyMs: s.MaxLatencyMs,
		})
	}
	result.MissedArrivals = missed
	if len(maint) > 0 {
		result.Maintenance = maint
	}

	stats := finalizeScenario(result, metrics, nil, 1.0, 0)
	result.Success, result.Output = judgeK8sMixed(result, stats, watchErrors.Load(), eventsReceived.Load())

	logutil.S().Infow("scenario completed",
		"scenario", K8sMixedSizeMaintenance.String(),
		"missed", missed,
		"maintenance", len(maint),
		"events", eventsReceived.Load(),
		"watch_errors", watchErrors.Load(),
	)
}

func newK8sClassStates(cfg StressConfig) []*k8sClassState {
	scale := cfg.KeyspaceScale
	if scale <= 0 {
		scale = 1
	}
	states := make([]*k8sClassState, 0, len(k8sMixedClasses))
	for _, c := range k8sMixedClasses {
		if c.name == "pod" {
			c.bytes = valueSize(cfg, c.bytes)
		}
		s := &k8sClassState{k8sObjectClass: c, metrics: NewMetricsCollector()}
		s.keys = max(1, int(float64(c.objects)*scale))
		// One random payload per class; each write takes a rotated window of
		// it so consecutive values differ without per-request generation.
		s.payload = randutil.StringAlphabetsLowerCase(c.bytes + 4096)
		states = append(states, s)
	}

	return states
}

func (c *k8sClassState) key(root string, i int) string {
	return fmt.Sprintf("%s/%s/ns-%02d/%s-%07d", root, c.resource, i%16, c.name, i%c.keys)
}

func (c *k8sClassState) value(i int) string {
	o := (i * 61) % 4096
	return c.payload[o : o+c.bytes]
}

func preloadK8sClasses(runner StressRunner, cli *clientv3.Client, root string, classes []*k8sClassState) error {
	type item struct {
		c *k8sClassState
		i int
	}
	work := make(chan item, 1024)
	go func() {
		defer close(work)
		for _, c := range classes {
			if c.ttl {
				continue
			}
			for i := range c.keys {
				work <- item{c, i}
			}
		}
	}()
	var firstErr error
	var once sync.Once
	errs := runWorkers(32, func(int, chan<- error) {
		for it := range work {
			var err error
			for range 5 {
				ctx, cancel := runner.NewCtxTimeout(30 * time.Second)
				_, err = cli.Put(ctx, it.c.key(root, it.i), it.c.value(it.i))
				cancel()
				if err == nil {
					break
				}
				time.Sleep(time.Second)
			}
			if err != nil {
				once.Do(func() { firstErr = err })
			}
		}
	})
	if len(errs) > 0 {
		return errs[0]
	}

	return firstErr
}

func writeK8sObject(cli *clientv3.Client, leases *ttlLeasePool, root string, c *k8sClassState, i int, due time.Time) (int64, error) {
	ctx, cancel := context.WithDeadline(context.Background(), due.Add(k8sMixedRequestTimeout))
	defer cancel()
	if c.ttl {
		lease, err := leases.current(ctx)
		if err != nil {
			return 0, err
		}
		key := fmt.Sprintf("%s/%s/ns-%02d/event-%09d", root, c.resource, i%16, i)
		resp, err := cli.Put(ctx, key, c.value(i), clientv3.WithLease(lease))
		if err != nil {
			return 0, err
		}

		return resp.Header.Revision, nil
	}
	resp, err := cli.Put(ctx, c.key(root, randutil.Intn(c.keys)), c.value(i))
	if err != nil {
		return 0, err
	}

	return resp.Header.Revision, nil
}

// ttlLeasePool hands out one lease per 10 s window, as the kube-apiserver
// lease manager reuses a lease for objects with similar TTLs.
type ttlLeasePool struct {
	cli    *clientv3.Client
	ttl    int64
	mu     sync.Mutex
	id     clientv3.LeaseID
	since  time.Time
	issued []clientv3.LeaseID
}

func newTTLLeasePool(cli *clientv3.Client, ttlSeconds int64) *ttlLeasePool {
	return &ttlLeasePool{cli: cli, ttl: ttlSeconds}
}

func (p *ttlLeasePool) current(ctx context.Context) (clientv3.LeaseID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.id != 0 && time.Since(p.since) < 10*time.Second {
		return p.id, nil
	}
	resp, err := p.cli.Grant(ctx, p.ttl)
	if err != nil {
		return 0, err
	}
	p.id, p.since = resp.ID, time.Now()
	p.issued = append(p.issued, resp.ID)

	return p.id, nil
}

func (p *ttlLeasePool) revokeAll(runner StressRunner) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range p.issued {
		ctx, cancel := runner.NewCtxTimeout(10 * time.Second)
		_, _ = p.cli.Revoke(ctx, id)
		cancel()
	}
}

type revisionSample struct {
	at  time.Time
	rev int64
}

// compactTarget returns the highest revision acknowledged at or before cutoff,
// or 0 when none was: the apiserver compacts to the revision it saw one
// interval ago, never to one newer than that.
func compactTarget(samples []revisionSample, cutoff time.Time) int64 {
	var target int64
	for _, s := range samples {
		if !s.at.After(cutoff) && s.rev > target {
			target = s.rev
		}
	}

	return target
}

func runK8sMaintenance(ctx context.Context, cli *clientv3.Client, cfg StressConfig, start time.Time, target func(time.Time) int64) []MaintenanceEvent {
	if cfg.CompactIntervalSeconds <= 0 {
		return nil
	}
	interval := time.Duration(cfg.CompactIntervalSeconds) * time.Second
	var events []MaintenanceEvent
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return events
		case <-ticker.C:
		}
		rev := target(time.Now().Add(-interval))
		if rev == 0 {
			continue
		}
		t0 := time.Now()
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		// Physical, so the following defrag reclaims the compacted revisions.
		_, err := cli.Compact(cctx, rev, clientv3.WithCompactPhysical())
		cancel()
		events = append(events, maintenanceEvent("compaction", "", rev, start, t0, err))
		if !cfg.DefragAfterCompact {
			continue
		}
		for _, ep := range defragOrder(cli) {
			t1 := time.Now()
			dctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			_, err := cli.Defragment(dctx, ep)
			cancel()
			events = append(events, maintenanceEvent("defrag", ep, 0, start, t1, err))
		}
	}
}

func maintenanceEvent(kind, member string, rev int64, start, t0 time.Time, err error) MaintenanceEvent {
	e := MaintenanceEvent{
		Kind: kind, Member: member, Revision: rev,
		StartS: t0.Sub(start).Seconds(), DurationS: time.Since(t0).Seconds(),
	}
	if err != nil {
		e.Error = err.Error()
	}

	return e
}

// defragOrder lists the client's endpoints with the leader last, as operators
// defragment followers first.
func defragOrder(cli *clientv3.Client) []string {
	eps := slices.Clone(cli.Endpoints())
	leader := ""
	for _, ep := range eps {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := cli.Status(ctx, ep)
		cancel()
		if err == nil && resp.Header.MemberId == resp.Leader {
			leader = ep
			break
		}
	}
	if leader == "" {
		return eps
	}

	return append(slices.DeleteFunc(eps, func(e string) bool { return e == leader }), leader)
}

func judgeK8sMixed(result *Result, stats Statistics, watchErrs, events int64) (bool, string) {
	var bad []string
	if stats.FailureCount > 0 {
		bad = append(bad, fmt.Sprintf("%d failed requests", stats.FailureCount))
	}
	if result.MissedArrivals > 0 {
		bad = append(bad, fmt.Sprintf("%d missed arrivals", result.MissedArrivals))
	}
	for _, m := range result.Maintenance {
		if m.Error != "" {
			bad = append(bad, fmt.Sprintf("%s %s failed: %s", m.Kind, m.Member, m.Error))
		}
	}
	if watchErrs > 0 {
		bad = append(bad, fmt.Sprintf("%d watch errors", watchErrs))
	}
	if events == 0 {
		bad = append(bad, "informers received no events")
	}
	budget := k8sMixedMaxP99Ms * p99LatencyMultiplier()
	parts := make([]string, 0, len(result.Classes))
	for _, c := range result.Classes {
		parts = append(parts, fmt.Sprintf("%s(%dB) p99 %.1fms", c.Class, c.ValueBytes, c.P99LatencyMs))
		if c.P99LatencyMs > budget {
			bad = append(bad, fmt.Sprintf("%s p99 %.0fms > %.0fms", c.Class, c.P99LatencyMs, budget))
		}
	}
	summary := fmt.Sprintf("%s; maintenance ops %d", strings.Join(parts, ", "), len(result.Maintenance))
	if len(bad) > 0 {
		return false, strings.Join(bad, "; ") + " — " + summary
	}

	return true, summary
}
