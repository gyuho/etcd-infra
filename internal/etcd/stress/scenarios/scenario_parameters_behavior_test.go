//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestScenarioDurationDefault(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{DurationSeconds: 0}
	assert.Equal(t, 60*time.Second, scenarioDuration(cfg))
}

func TestScenarioDurationNegative(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{DurationSeconds: -5}
	assert.Equal(t, 60*time.Second, scenarioDuration(cfg))
}

func TestScenarioDurationCustom(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{DurationSeconds: 30}
	assert.Equal(t, 30*time.Second, scenarioDuration(cfg))
}

func TestWorkerCountDefault(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{ConcurrentWorkers: 0}
	assert.Equal(t, 1, workerCount(cfg))
}

func TestWorkerCountNegative(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{ConcurrentWorkers: -1}
	assert.Equal(t, 1, workerCount(cfg))
}

func TestWorkerCountCustom(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{ConcurrentWorkers: 5}
	assert.Equal(t, 5, workerCount(cfg))
}

func TestValueSizeDefault(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{ValueSizeBytes: 0}
	assert.Equal(t, 256, valueSize(cfg, 256))
}

func TestValueSizeNegative(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{ValueSizeBytes: -1}
	assert.Equal(t, 256, valueSize(cfg, 256))
}

func TestValueSizeCustom(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{ValueSizeBytes: 512}
	assert.Equal(t, 512, valueSize(cfg, 256))
}

func TestKeySizeDefault(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{KeySizeBytes: 0}
	assert.Equal(t, 64, keySize(cfg, 64))
}

func TestKeySizeNegative(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{KeySizeBytes: -1}
	assert.Equal(t, 64, keySize(cfg, 64))
}

func TestKeySizeCustom(t *testing.T) {
	t.Parallel()

	cfg := StressConfig{KeySizeBytes: 128}
	assert.Equal(t, 128, keySize(cfg, 64))
}

func TestIntMax(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 5, intMax(3, 5))
	assert.Equal(t, 5, intMax(5, 3))
	assert.Equal(t, 3, intMax(3, 3))
}
