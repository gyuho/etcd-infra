//nolint:all // Coverage-oriented tests intentionally use broad patterns for mock-heavy branch testing.
//nolint:testpackage // Need access to internals for thorough testing.
package scenarios

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStressResultRecordTimeEnd_Coverage(t *testing.T) {
	t.Parallel()

	start := testtime.Now()
	time.Sleep(10 * time.Millisecond)
	end := testtime.Now()

	rs := &Result{
		Scenario:      "stress-test",
		TimeStart:     start,
		TotalRequests: 100,
	}
	rs.RecordTimeEnd(end)

	assert.Equal(t, end, rs.TimeEnd)
	assert.Positive(t, rs.Took.Duration)
	assert.Positive(t, rs.RequestsPerSecond)
}

func TestStressResultRecordTimeEndZeroRequests(t *testing.T) {
	t.Parallel()

	start := testtime.Now()
	end := testtime.NewTime(start.Add(time.Second))

	rs := &Result{
		Scenario:      "no-reqs",
		TimeStart:     start,
		TotalRequests: 0,
	}
	rs.RecordTimeEnd(end)
	assert.Equal(t, float64(0), rs.RequestsPerSecond)
}

func TestStressResultsFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		results  StressResults
		expected bool
	}{
		{"empty", StressResults{}, false},
		{"all pass", StressResults{{Success: true}, {Success: true}}, false},
		{"one fail", StressResults{{Success: true}, {Success: false}}, true},
		{"all fail", StressResults{{Success: false}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.results.Failed())
		})
	}
}

func TestStressResultsTotalRequests(t *testing.T) {
	t.Parallel()

	rs := StressResults{
		{TotalRequests: 100},
		{TotalRequests: 200},
		{TotalRequests: 50},
	}
	assert.Equal(t, int64(350), rs.TotalRequests())
}

func TestStressResultsTotalRequestsEmpty(t *testing.T) {
	t.Parallel()

	rs := StressResults{}
	assert.Equal(t, int64(0), rs.TotalRequests())
}

func TestStressResultsSuccessRate(t *testing.T) {
	t.Parallel()

	rs := StressResults{
		{TotalRequests: 100, SuccessfulRequests: 90},
		{TotalRequests: 100, SuccessfulRequests: 80},
	}
	rate := rs.SuccessRate()
	assert.InDelta(t, 0.85, rate, 0.001)
}

func TestStressResultsSuccessRateZero(t *testing.T) {
	t.Parallel()

	rs := StressResults{{TotalRequests: 0}}
	assert.Equal(t, float64(0), rs.SuccessRate())
}

func TestStressResultsAverageLatency(t *testing.T) {
	t.Parallel()

	rs := StressResults{
		{AverageLatency: testtime.Duration{Duration: 10 * time.Millisecond}},
		{AverageLatency: testtime.Duration{Duration: 20 * time.Millisecond}},
	}
	avg := rs.AverageLatency()
	assert.InDelta(t, 15.0, avg, 0.01)
}

func TestStressResultsAverageLatencyEmpty(t *testing.T) {
	t.Parallel()

	rs := StressResults{}
	assert.Equal(t, float64(0), rs.AverageLatency())
}

func TestStressResultsAverageLatencyZeroDuration(t *testing.T) {
	t.Parallel()

	rs := StressResults{
		{AverageLatency: testtime.Duration{Duration: 0}},
		{AverageLatency: testtime.Duration{Duration: 0}},
	}
	assert.Equal(t, float64(0), rs.AverageLatency())
}

func TestStressResultsJSON(t *testing.T) {
	t.Parallel()

	rs := StressResults{
		{Scenario: "s1", Success: true, TotalRequests: 100},
	}
	data, err := rs.JSON()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	parsed, err := ParseStressResultsJSON(data)
	require.NoError(t, err)
	assert.Len(t, parsed, 1)
	assert.Equal(t, "s1", parsed[0].Scenario)
}

func TestStressResultsYAML(t *testing.T) {
	t.Parallel()

	rs := StressResults{
		{Scenario: "yaml1", Success: true},
	}
	data, err := rs.YAML()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	parsed, err := ParseStressResultsYAML(data)
	require.NoError(t, err)
	assert.Len(t, parsed, 1)
	assert.Equal(t, "yaml1", parsed[0].Scenario)
}

func TestStressResultsSyncJSON(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "sub", "results.json")

	rs := StressResults{{Scenario: "sync", Success: true}}
	err := rs.SyncJSON(file)
	require.NoError(t, err)

	loaded, err := LoadStressResultsJSON(file)
	require.NoError(t, err)
	assert.Len(t, loaded, 1)
}

func TestStressResultsSyncYAML(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "sub", "results.yaml")

	rs := StressResults{{Scenario: "sync", Success: true}}
	err := rs.SyncYAML(file)
	require.NoError(t, err)

	loaded, err := LoadStressResultsYAML(file)
	require.NoError(t, err)
	assert.Len(t, loaded, 1)
}

func TestStressResultsSyncJSONExistingDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.json")

	rs := StressResults{{Scenario: "existing", Success: true}}
	err := rs.SyncJSON(file)
	require.NoError(t, err)

	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestStressResultsSyncYAMLExistingDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.yaml")

	rs := StressResults{{Scenario: "existing", Success: true}}
	err := rs.SyncYAML(file)
	require.NoError(t, err)

	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestLoadStressResultsJSONNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadStressResultsJSON("/nonexistent/results.json")
	require.Error(t, err)
}

func TestLoadStressResultsYAMLNotFound(t *testing.T) {
	t.Parallel()

	_, err := LoadStressResultsYAML("/nonexistent/results.yaml")
	require.Error(t, err)
}

func TestParseStressResultsJSONInvalid(t *testing.T) {
	t.Parallel()

	_, err := ParseStressResultsJSON([]byte("not json"))
	require.Error(t, err)
}

func TestParseStressResultsYAMLInvalid(t *testing.T) {
	t.Parallel()

	_, err := ParseStressResultsYAML([]byte(":\ninvalid:\n  - [broken"))
	require.Error(t, err)
}
