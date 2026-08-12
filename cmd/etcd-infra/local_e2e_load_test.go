//go:build etcd_infra_custom_client

package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const putLoadMaxInFlight = 64

type putLoadSample struct {
	key         string
	value       string
	scheduledAt time.Time
	completedAt time.Time
	latency     time.Duration
	issued      bool
	callErr     error
	attempts    []rpcAttempt
}

type putLoad struct {
	ctx      context.Context
	cli      *clientv3.Client
	recorder *rpcAttemptRecorder
	prefix   string
	interval time.Duration
	timeout  time.Duration
	stopc    chan struct{}
	donec    chan struct{}
	inFlight chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu      sync.Mutex
	next    int
	samples []putLoadSample
	endedAt time.Time
}

func startPutLoad(
	t *testing.T,
	ctx context.Context,
	cli *clientv3.Client,
	recorder *rpcAttemptRecorder,
	prefix string,
	interval, timeout time.Duration,
) *putLoad {
	t.Helper()
	load := &putLoad{
		ctx:      ctx,
		cli:      cli,
		recorder: recorder,
		prefix:   prefix,
		interval: interval,
		timeout:  timeout,
		stopc:    make(chan struct{}),
		donec:    make(chan struct{}),
		inFlight: make(chan struct{}, putLoadMaxInFlight),
	}
	go load.run()
	t.Cleanup(load.stop)
	return load
}

func (load *putLoad) run() {
	defer func() {
		load.wg.Wait()
		load.mu.Lock()
		load.endedAt = time.Now()
		load.mu.Unlock()
		close(load.donec)
	}()
	load.schedule(time.Now())
	ticker := time.NewTicker(load.interval)
	defer ticker.Stop()
	for {
		select {
		case scheduledAt := <-ticker.C:
			load.schedule(scheduledAt)
		case <-load.stopc:
			return
		case <-load.ctx.Done():
			return
		}
	}
}

func (load *putLoad) schedule(scheduledAt time.Time) {
	load.next++
	key := fmt.Sprintf("%s/%06d", load.prefix, load.next)
	value := fmt.Sprintf("value-%06d", load.next)
	select {
	case load.inFlight <- struct{}{}:
		load.wg.Add(1)
		go load.put(key, value, scheduledAt)
	default:
		load.append(putLoadSample{
			key:         key,
			value:       value,
			scheduledAt: scheduledAt,
			completedAt: time.Now(),
			callErr:     fmt.Errorf("put load reached %d in-flight requests", putLoadMaxInFlight),
		})
	}
}

func (load *putLoad) put(key, value string, scheduledAt time.Time) {
	defer load.wg.Done()
	defer func() { <-load.inFlight }()
	startedAt := time.Now()
	callCtx, cancel := context.WithTimeout(load.ctx, load.timeout)
	callCtx = context.WithValue(callCtx, rpcLabelKey{}, key)
	_, callErr := load.cli.Put(callCtx, key, value)
	cancel()
	completedAt := time.Now()
	load.append(putLoadSample{
		key:         key,
		value:       value,
		scheduledAt: scheduledAt,
		completedAt: completedAt,
		latency:     completedAt.Sub(startedAt),
		issued:      true,
		callErr:     callErr,
		attempts:    load.recorder.attemptsForLabel(putRPC, key),
	})
}

func (load *putLoad) append(sample putLoadSample) {
	load.mu.Lock()
	load.samples = append(load.samples, sample)
	load.mu.Unlock()
}

func (load *putLoad) snapshot() []putLoadSample {
	load.mu.Lock()
	defer load.mu.Unlock()
	return append([]putLoadSample(nil), load.samples...)
}

func (load *putLoad) endTime() time.Time {
	load.mu.Lock()
	defer load.mu.Unlock()
	return load.endedAt
}

func (load *putLoad) stopScheduling() {
	load.stopOnce.Do(func() { close(load.stopc) })
}

func (load *putLoad) wait() {
	<-load.donec
}

func (load *putLoad) stop() {
	load.stopScheduling()
	load.wait()
}

func stopPutLoads(loads ...*putLoad) {
	for _, load := range loads {
		load.stopScheduling()
	}
	for _, load := range loads {
		load.wait()
	}
}

type putLoadSummary struct {
	scheduled          int
	successful         int
	failed             int
	dropped            int
	transportAttempts  int
	transparentRetries int
	peerAttempts       map[string]int
	latencies          []time.Duration
	maxSuccessGap      time.Duration
}

func summarizePutLoad(samples []putLoadSample, start, end time.Time) putLoadSummary {
	summary := putLoadSummary{peerAttempts: make(map[string]int)}
	var successes []time.Time
	for _, sample := range samples {
		if sample.scheduledAt.Before(start) || sample.scheduledAt.After(end) {
			continue
		}
		summary.scheduled++
		if !sample.issued {
			summary.dropped++
			continue
		}
		for _, attempt := range sample.attempts {
			summary.transportAttempts++
			summary.peerAttempts[attempt.peer]++
			if attempt.transparent {
				summary.transparentRetries++
			}
		}
		if sample.callErr != nil {
			summary.failed++
			continue
		}
		summary.successful++
		summary.latencies = append(summary.latencies, sample.latency)
		successes = append(successes, sample.completedAt)
	}
	sort.Slice(successes, func(i, j int) bool { return successes[i].Before(successes[j]) })
	if len(successes) == 0 {
		summary.maxSuccessGap = end.Sub(start)
		return summary
	}
	previous := start
	for _, completedAt := range successes {
		if gap := completedAt.Sub(previous); gap > summary.maxSuccessGap {
			summary.maxSuccessGap = gap
		}
		previous = completedAt
	}
	if gap := end.Sub(previous); gap > summary.maxSuccessGap {
		summary.maxSuccessGap = gap
	}
	return summary
}

func (summary putLoadSummary) successRate() float64 {
	if summary.scheduled == 0 {
		return 0
	}
	return float64(summary.successful) / float64(summary.scheduled)
}

func latencyPercentile(latencies []time.Duration, percentile int) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	values := append([]time.Duration(nil), latencies...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (len(values)*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	return values[index-1]
}
