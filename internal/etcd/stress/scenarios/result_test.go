//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"path/filepath"
	"testing"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStressResultRecordTimeEnd(t *testing.T) {
	t.Parallel()
	start := testtime.NewTime(time.Unix(100, 0))
	sr := &Result{
		Scenario:      "BURST_WRITES",
		TimeStart:     start,
		TotalRequests: 100,
	}

	sr.RecordTimeEnd(testtime.NewTime(start.Add(2 * time.Second)))

	assert.Equal(t, testtime.Duration{Duration: 2 * time.Second}, sr.Took)
	assert.InDelta(t, 50.0, sr.RequestsPerSecond, 0.01)
	assert.False(t, sr.TimeEnd.IsZero())
}

func TestStressResultsAggregations(t *testing.T) {
	t.Parallel()
	now := testtime.NewTime(time.Unix(200, 0))
	results := StressResults{
		{
			Scenario:           "A",
			TimeStart:          now,
			TimeEnd:            now,
			Success:            true,
			TotalRequests:      80,
			SuccessfulRequests: 75,
			AverageLatency:     testtime.Duration{Duration: 10 * time.Millisecond},
			P99Latency:         testtime.Duration{Duration: 20 * time.Millisecond},
		},
		{
			Scenario:           "B",
			TimeStart:          now,
			TimeEnd:            now,
			Success:            false,
			TotalRequests:      20,
			SuccessfulRequests: 10,
			AverageLatency:     testtime.Duration{Duration: 30 * time.Millisecond},
			P99Latency:         testtime.Duration{Duration: 45 * time.Millisecond},
		},
	}

	assert.True(t, results.Failed())
	assert.Equal(t, int64(100), results.TotalRequests())
	assert.InDelta(t, 0.85, results.SuccessRate(), 0.0001)
	assert.InDelta(t, 20.0, results.AverageLatency(), 0.0001)
}

func TestStressResultsJSONAndYAML(t *testing.T) {
	t.Parallel()
	now := testtime.NewTime(time.Unix(300, 0))
	results := StressResults{
		{
			Scenario:           "SUSTAINED_LOAD",
			TimeStart:          now,
			TimeEnd:            now,
			Success:            true,
			Output:             "ok",
			TotalRequests:      42,
			SuccessfulRequests: 40,
			AverageLatency:     testtime.Duration{Duration: 12300 * time.Microsecond},
		},
	}

	data, err := results.JSON()
	require.NoError(t, err)

	roundTrip, err := ParseStressResultsJSON(data)
	require.NoError(t, err)
	assert.Equal(t, results, roundTrip)

	yd, err := results.YAML()
	require.NoError(t, err)

	yamlRoundTrip, err := ParseStressResultsYAML(yd)
	require.NoError(t, err)
	assert.Equal(t, results, yamlRoundTrip)

	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "stress.json")
	yamlPath := filepath.Join(tmpDir, "stress.yaml")

	require.NoError(t, results.SyncJSON(jsonPath))
	loadedJSON, err := LoadStressResultsJSON(jsonPath)
	require.NoError(t, err)
	assert.Equal(t, results, loadedJSON)

	require.NoError(t, results.SyncYAML(yamlPath))
	loadedYAML, err := LoadStressResultsYAML(yamlPath)
	require.NoError(t, err)
	assert.Equal(t, results, loadedYAML)
}
