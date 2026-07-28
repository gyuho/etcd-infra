package scenarios

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"git.tbd/etcd-infra/pkg/randutil"
)

// LoadRequest represents a load test request.
type LoadRequest struct {
	Key   string
	Value string
}

// LoadGenerator generates load for stress testing.
type LoadGenerator interface {
	Generate() <-chan LoadRequest
	Stop()
}

// constantLoadGenerator generates constant load.
type constantLoadGenerator struct {
	durationSeconds   int
	requestsPerSecond int
	keySizeBytes      int
	valueSizeBytes    int
	requests          chan LoadRequest
	stop              chan struct{}
	stopOnce          sync.Once
}

// NewLoadGenerator creates a new load generator.
//
//nolint:ireturn // Factory function returns LoadGenerator interface for abstraction
func NewLoadGenerator(durationSeconds, requestsPerSecond int) LoadGenerator {
	return &constantLoadGenerator{
		durationSeconds:   durationSeconds,
		requestsPerSecond: requestsPerSecond,
		keySizeBytes:      64,
		valueSizeBytes:    256,
		requests:          make(chan LoadRequest, 100),
		stop:              make(chan struct{}),
	}
}

// NewLoadGeneratorWithSizes creates a load generator with custom sizes.
//
//nolint:ireturn // Factory function returns LoadGenerator interface for abstraction
func NewLoadGeneratorWithSizes(durationSeconds, requestsPerSecond, keySizeBytes, valueSizeBytes int) LoadGenerator {
	return &constantLoadGenerator{
		durationSeconds:   durationSeconds,
		requestsPerSecond: requestsPerSecond,
		keySizeBytes:      keySizeBytes,
		valueSizeBytes:    valueSizeBytes,
		requests:          make(chan LoadRequest, 100),
		stop:              make(chan struct{}),
	}
}

// Generate starts generating load.
func (g *constantLoadGenerator) Generate() <-chan LoadRequest {
	go g.run()

	return g.requests
}

// Stop stops the load generator.
func (g *constantLoadGenerator) Stop() {
	g.stopOnce.Do(func() {
		close(g.stop)
	})
}

func (g *constantLoadGenerator) run() {
	defer close(g.requests)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(g.durationSeconds)*time.Second)
	defer cancel()

	// Create rate limiter if needed
	var limiter *rate.Limiter
	if g.requestsPerSecond > 0 {
		burst := g.requestsPerSecond / 10
		burst = max(burst, 1)
		limiter = rate.NewLimiter(rate.Limit(g.requestsPerSecond), burst)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-g.stop:
			return
		default:
			// Apply rate limiting if configured
			if limiter != nil {
				if err := limiter.Wait(ctx); err != nil {
					return
				}
			}

			// Generate request with random data
			req := LoadRequest{
				Key:   randutil.StringAlphabetsLowerCase(g.keySizeBytes),
				Value: randutil.StringAlphabetsLowerCase(g.valueSizeBytes),
			}

			select {
			case g.requests <- req:
			case <-ctx.Done():
				return
			case <-g.stop:
				return
			}
		}
	}
}
