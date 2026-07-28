//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLoadGenerator(t *testing.T) {
	t.Parallel()
	lg := NewLoadGenerator(10, 100)
	require.NotNil(t, lg)

	// Type assert to access internal fields for verification
	clg, ok := lg.(*constantLoadGenerator)
	require.True(t, ok)
	assert.Equal(t, 10, clg.durationSeconds)
	assert.Equal(t, 100, clg.requestsPerSecond)
	assert.Equal(t, 64, clg.keySizeBytes)
	assert.Equal(t, 256, clg.valueSizeBytes)
}

func TestNewLoadGeneratorWithSizes(t *testing.T) {
	t.Parallel()
	lg := NewLoadGeneratorWithSizes(5, 50, 32, 128)
	require.NotNil(t, lg)

	clg, ok := lg.(*constantLoadGenerator)
	require.True(t, ok)
	assert.Equal(t, 5, clg.durationSeconds)
	assert.Equal(t, 50, clg.requestsPerSecond)
	assert.Equal(t, 32, clg.keySizeBytes)
	assert.Equal(t, 128, clg.valueSizeBytes)
}

func TestLoadGeneratorGenerate(t *testing.T) {
	t.Parallel()
	// Create a short-lived generator
	lg := NewLoadGeneratorWithSizes(1, 10, 8, 16)

	ch := lg.Generate()
	require.NotNil(t, ch)

	// Collect some requests
	var requests []LoadRequest
	timeout := time.After(2 * time.Second)
	for {
		select {
		case req, ok := <-ch:
			if !ok {
				// Channel closed, generator finished
				goto done
			}
			requests = append(requests, req)
		case <-timeout:
			goto done
		}
	}
done:

	// We should have received some requests
	assert.NotEmpty(t, requests, "should have received at least one request")

	// Verify request structure
	if len(requests) > 0 {
		req := requests[0]
		assert.Len(t, req.Key, 8, "key should be 8 characters")
		assert.Len(t, req.Value, 16, "value should be 16 characters")
	}
}

func TestLoadGeneratorStop(t *testing.T) {
	t.Parallel()
	// Create a generator that would run for a long time
	lg := NewLoadGeneratorWithSizes(60, 10, 8, 16)

	ch := lg.Generate()
	require.NotNil(t, ch)

	// Stop immediately
	lg.Stop()

	// The channel should close shortly after Stop is called
	timeout := time.After(500 * time.Millisecond)
	channelClosed := false

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				channelClosed = true
				goto done
			}
			// Keep draining
		case <-timeout:
			goto done
		}
	}
done:

	assert.True(t, channelClosed, "channel should close after Stop")
}

func TestLoadGeneratorRateLimiting(t *testing.T) {
	t.Parallel()
	// Create a generator with a very low rate limit
	rps := 5
	lg := NewLoadGeneratorWithSizes(2, rps, 8, 16)

	ch := lg.Generate()
	require.NotNil(t, ch)

	start := time.Now()
	var count int

	// Collect requests for 1 second
	timeout := time.After(1 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				goto done
			}
			count++
		case <-timeout:
			goto done
		}
	}
done:

	elapsed := time.Since(start)

	// With 5 RPS, in 1 second we should get approximately 5-10 requests
	// (accounting for burst and timing variations)
	// We use a generous range to avoid flaky tests
	assert.LessOrEqual(t, count, rps*3, "should not exceed 3x the rate limit in one second")

	t.Logf("Received %d requests in %v (target: %d RPS)", count, elapsed, rps)
}

func TestLoadGeneratorZeroRPS(t *testing.T) {
	t.Parallel()
	// Create a generator with 0 RPS (no rate limiting)
	lg := NewLoadGeneratorWithSizes(1, 0, 8, 16)

	ch := lg.Generate()
	require.NotNil(t, ch)

	var count int
	timeout := time.After(200 * time.Millisecond)

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				goto done
			}
			count++
		case <-timeout:
			goto done
		}
	}
done:

	// Without rate limiting, we should get many requests quickly
	assert.Positive(t, count, "should receive requests without rate limiting")
	t.Logf("Received %d requests in 200ms without rate limiting", count)
}

func TestLoadGeneratorRequestStructure(t *testing.T) {
	t.Parallel()
	lg := NewLoadGeneratorWithSizes(1, 100, 10, 20)

	ch := lg.Generate()
	require.NotNil(t, ch)

	// Get one request
	select {
	case req, ok := <-ch:
		require.True(t, ok, "should receive a request")
		assert.Len(t, req.Key, 10)
		assert.Len(t, req.Value, 20)

		// Verify the content is alphanumeric lowercase
		for _, c := range req.Key {
			assert.True(t, c >= 'a' && c <= 'z', "key should contain only lowercase letters")
		}
		for _, c := range req.Value {
			assert.True(t, c >= 'a' && c <= 'z', "value should contain only lowercase letters")
		}
	case <-time.After(1 * time.Second):
		require.FailNow(t, "timeout waiting for request")
	}

	lg.Stop()
}

func TestLoadGeneratorMultipleStops(t *testing.T) {
	t.Parallel()
	lg := NewLoadGeneratorWithSizes(60, 10, 8, 16)

	ch := lg.Generate()
	require.NotNil(t, ch)

	// Stop multiple times should not panic
	lg.Stop()

	// Second stop should not panic either
	lg.Stop()
}

func TestLoadGeneratorContextExpiry(t *testing.T) {
	t.Parallel()
	// Create a generator that expires quickly
	lg := NewLoadGeneratorWithSizes(1, 1000, 8, 16)

	ch := lg.Generate()
	require.NotNil(t, ch)

	// Wait for the generator to complete naturally
	var count int
	for range ch {
		count++
	}

	assert.Positive(t, count, "should have generated some requests before expiry")
	t.Logf("Generated %d requests before natural expiry", count)
}
