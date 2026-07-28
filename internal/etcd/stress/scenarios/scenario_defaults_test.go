//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScenarioHelpers(t *testing.T) {
	t.Parallel()
	cfg := StressConfig{}
	require.Equal(t, 60*time.Second, scenarioDuration(cfg))
	require.Equal(t, 1, workerCount(cfg))
	require.Equal(t, 10, valueSize(cfg, 10))
	require.Equal(t, 12, keySize(cfg, 12))
	require.Equal(t, 7, intMax(3, 7))

	cfg = StressConfig{DurationSeconds: 5, ConcurrentWorkers: 3, ValueSizeBytes: 128, KeySizeBytes: 64}
	require.Equal(t, 5*time.Second, scenarioDuration(cfg))
	require.Equal(t, 3, workerCount(cfg))
	require.Equal(t, 128, valueSize(cfg, 10))
	require.Equal(t, 64, keySize(cfg, 12))
}
