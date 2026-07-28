//nolint:testpackage // Tests use package internals and shared resources.
package scenarios

import (
	"testing"
	"time"

	testtime "git.tbd/etcd-infra/internal/etcd/testtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResultsYAML(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, time.October, 8, 4, 0, 0, 0, time.UTC)
	end := start.Add(1500 * time.Millisecond)

	// Note: testtime.Time serializes to RFC3339 which may lose sub-second precision.
	// We truncate to seconds for comparison, but Took preserves the full duration.
	expected := Results{
		{
			Scenario:  "PUT_AND_GET",
			TimeStart: testtime.NewTime(start.Truncate(time.Second)),
			TimeEnd:   testtime.NewTime(end.Truncate(time.Second)),
			Took:      testtime.Duration{Duration: end.Sub(start)},
			Success:   true,
			Output:    "ok",
		},
	}

	data, err := expected.YAML()
	require.NoError(t, err, "YAML() returned error")

	parsed, err := ParseResultsYAML(data)
	require.NoError(t, err, "ParseResultsYAML() returned error")

	// Compare results accounting for testtime.Time timezone handling
	require.Len(t, parsed, len(expected), "result count mismatch")

	for i := range expected {
		exp := expected[i]
		got := parsed[i]

		assert.Equal(t, exp.Scenario, got.Scenario, "result[%d].Scenario mismatch", i)
		assert.Equal(t, exp.Success, got.Success, "result[%d].Success mismatch", i)
		assert.Equal(t, exp.Output, got.Output, "result[%d].Output mismatch", i)
		assert.Equal(t, exp.Took.Duration, got.Took.Duration, "result[%d].Took mismatch", i)

		// Compare times using Equal() which handles timezone differences correctly
		assert.True(t, exp.TimeStart.Time.Equal(got.TimeStart.Time), "result[%d].TimeStart mismatch: expected %v, got %v", i, exp.TimeStart, got.TimeStart)
		assert.True(t, exp.TimeEnd.Time.Equal(got.TimeEnd.Time), "result[%d].TimeEnd mismatch: expected %v, got %v", i, exp.TimeEnd, got.TimeEnd)
	}
}

// TestParseResultsYAMLWithTimezones ensures Result serialization and deserialization
// correctly handles times in different timezones.
func TestParseResultsYAMLWithTimezones(t *testing.T) {
	t.Parallel()
	// Define test timezones
	timezones := []struct {
		name string
		loc  *time.Location
	}{
		{"UTC", time.UTC},
		{"US/Pacific", mustLoadLocation(t, "America/Los_Angeles")},
		{"US/Eastern", mustLoadLocation(t, "America/New_York")},
		{"Asia/Tokyo", mustLoadLocation(t, "Asia/Tokyo")},
		{"Europe/London", mustLoadLocation(t, "Europe/London")},
	}

	for _, tz := range timezones {
		t.Run(tz.name, func(t *testing.T) {
			t.Parallel()
			// Create times in the specific timezone
			start := time.Date(2025, time.October, 8, 14, 30, 45, 0, tz.loc)
			end := start.Add(2 * time.Second)

			expected := Results{
				{
					Scenario:  "TZ_TEST",
					TimeStart: testtime.NewTime(start.Truncate(time.Second)),
					TimeEnd:   testtime.NewTime(end.Truncate(time.Second)),
					Took:      testtime.Duration{Duration: end.Sub(start)},
					Success:   true,
					Output:    "timezone test",
				},
			}

			// Serialize to YAML
			data, err := expected.YAML()
			require.NoError(t, err, "YAML() failed")

			// Deserialize from YAML
			parsed, err := ParseResultsYAML(data)
			require.NoError(t, err, "ParseResultsYAML() failed")

			require.Len(t, parsed, 1, "expected 1 result")

			// Verify times are equal (Equal handles timezone differences)
			assert.True(t, expected[0].TimeStart.Equal(&parsed[0].TimeStart),
				"TimeStart mismatch: expected %v (%s), got %v (%s)",
				expected[0].TimeStart, expected[0].TimeStart.Location(),
				parsed[0].TimeStart, parsed[0].TimeStart.Location())

			assert.True(t, expected[0].TimeEnd.Equal(&parsed[0].TimeEnd),
				"TimeEnd mismatch: expected %v (%s), got %v (%s)",
				expected[0].TimeEnd, expected[0].TimeEnd.Location(),
				parsed[0].TimeEnd, parsed[0].TimeEnd.Location())

			// Verify duration is preserved
			assert.Equal(t, expected[0].Took.Duration, parsed[0].Took.Duration, "Took mismatch")
		})
	}
}

// mustLoadLocation loads a timezone location or skips the test if not available.
func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %s not available: %v", name, err)
	}

	return loc
}

func TestResultRecordTimeEnd(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, time.October, 8, 12, 0, 0, 0, time.UTC)
	rs := &Result{
		Scenario:  "TEST_SCENARIO",
		TimeStart: testtime.NewTime(start),
	}

	end := start.Add(5 * time.Second)
	rs.RecordTimeEnd(testtime.NewTime(end))

	assert.True(t, rs.TimeEnd.Time.Equal(end))
	assert.Equal(t, 5*time.Second, rs.Took.Duration)
}

func TestResultsFailed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		results  Results
		expected bool
	}{
		{
			name:     "empty results",
			results:  Results{},
			expected: false,
		},
		{
			name: "all success",
			results: Results{
				{Scenario: "A", Success: true},
				{Scenario: "B", Success: true},
			},
			expected: false,
		},
		{
			name: "one failure",
			results: Results{
				{Scenario: "A", Success: true},
				{Scenario: "B", Success: false},
			},
			expected: true,
		},
		{
			name: "all failures",
			results: Results{
				{Scenario: "A", Success: false},
				{Scenario: "B", Success: false},
			},
			expected: true,
		},
		{
			name: "first failure",
			results: Results{
				{Scenario: "A", Success: false},
				{Scenario: "B", Success: true},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.results.Failed())
		})
	}
}

func TestResultsJSONRoundTrip(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, time.October, 8, 4, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)

	expected := Results{
		{
			Scenario:  "TEST_JSON",
			TimeStart: testtime.NewTime(start),
			TimeEnd:   testtime.NewTime(end),
			Took:      testtime.Duration{Duration: 2 * time.Second},
			Success:   true,
			Output:    "json test output",
		},
	}

	data, err := expected.JSON()
	require.NoError(t, err)

	parsed, err := ParseResultsJSON(data)
	require.NoError(t, err)

	require.Len(t, parsed, 1)
	assert.Equal(t, expected[0].Scenario, parsed[0].Scenario)
	assert.Equal(t, expected[0].Success, parsed[0].Success)
	assert.Equal(t, expected[0].Output, parsed[0].Output)
	assert.Equal(t, expected[0].Took.Duration, parsed[0].Took.Duration)
}

func TestResultsSyncJSONAndLoad(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, time.October, 8, 4, 0, 0, 0, time.UTC)
	results := Results{
		{
			Scenario:  "SYNC_TEST",
			TimeStart: testtime.NewTime(start),
			Success:   true,
		},
	}

	// Test with nested directory
	path := t.TempDir() + "/nested/results.json"
	err := results.SyncJSON(path)
	require.NoError(t, err)

	loaded, err := LoadResultsJSON(path)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, results[0].Scenario, loaded[0].Scenario)
}

func TestResultsSyncYAMLAndLoad(t *testing.T) {
	t.Parallel()
	start := time.Date(2025, time.October, 8, 4, 0, 0, 0, time.UTC)
	results := Results{
		{
			Scenario:  "SYNC_YAML_TEST",
			TimeStart: testtime.NewTime(start),
			Success:   false,
			Output:    "failed test",
		},
	}

	// Test with nested directory
	path := t.TempDir() + "/nested/results.yaml"
	err := results.SyncYAML(path)
	require.NoError(t, err)

	loaded, err := LoadResultsYAML(path)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, results[0].Scenario, loaded[0].Scenario)
	assert.Equal(t, results[0].Output, loaded[0].Output)
}

func TestLoadResultsJSONMissingFile(t *testing.T) {
	t.Parallel()
	_, err := LoadResultsJSON("/nonexistent/path/results.json")
	require.Error(t, err)
}

func TestLoadResultsYAMLMissingFile(t *testing.T) {
	t.Parallel()
	_, err := LoadResultsYAML("/nonexistent/path/results.yaml")
	require.Error(t, err)
}

func TestParseResultsJSONInvalid(t *testing.T) {
	t.Parallel()
	_, err := ParseResultsJSON([]byte("not valid json"))
	require.Error(t, err)
}

func TestParseResultsYAMLInvalid(t *testing.T) {
	t.Parallel()
	_, err := ParseResultsYAML([]byte(":::invalid yaml"))
	require.Error(t, err)
}

func TestResultsEmptySlice(t *testing.T) {
	t.Parallel()
	results := Results{}

	// JSON should work with empty slice
	data, err := results.JSON()
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))

	// YAML should work with empty slice
	yamlData, err := results.YAML()
	require.NoError(t, err)
	assert.Contains(t, string(yamlData), "[]")
}
