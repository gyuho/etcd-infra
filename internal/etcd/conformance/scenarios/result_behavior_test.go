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

func TestResultRecordTimeEndDuration(t *testing.T) {
	t.Parallel()

	start := testtime.Now()
	time.Sleep(10 * time.Millisecond)
	end := testtime.Now()

	rs := &Result{
		Scenario:  "test-dur",
		TimeStart: start,
		Success:   true,
	}
	rs.RecordTimeEnd(end)

	assert.Equal(t, end, rs.TimeEnd)
	assert.Positive(t, rs.Took.Duration, "Took should be positive")
	assert.Equal(t, end.Sub(start.Time), rs.Took.Duration)
}

func TestResultsJSONMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	rs := Results{
		{Scenario: "cov-s1", Success: true, Output: "ok"},
		{Scenario: "cov-s2", Success: false, Output: "err"},
	}

	data, err := rs.JSON()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	parsed, err := ParseResultsJSON(data)
	require.NoError(t, err)
	assert.Len(t, parsed, 2)
	assert.Equal(t, "cov-s1", parsed[0].Scenario)
	assert.True(t, parsed[0].Success)
	assert.Equal(t, "cov-s2", parsed[1].Scenario)
	assert.False(t, parsed[1].Success)
}

func TestResultsYAMLMarshalUnmarshal(t *testing.T) {
	t.Parallel()

	rs := Results{
		{Scenario: "cov-yaml1", Success: true},
	}

	data, err := rs.YAML()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	parsed, err := ParseResultsYAML(data)
	require.NoError(t, err)
	assert.Len(t, parsed, 1)
	assert.Equal(t, "cov-yaml1", parsed[0].Scenario)
}

func TestResultsSyncJSONNewDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "deep", "nested", "results.json")

	rs := Results{
		{Scenario: "cov-syncjson", Success: true},
	}
	err := rs.SyncJSON(file)
	require.NoError(t, err)

	loaded, err := LoadResultsJSON(file)
	require.NoError(t, err)
	assert.Len(t, loaded, 1)
	assert.Equal(t, "cov-syncjson", loaded[0].Scenario)
}

func TestResultsSyncYAMLNewDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "deep", "nested", "results.yaml")

	rs := Results{
		{Scenario: "cov-syncyaml", Success: false, Output: "err"},
	}
	err := rs.SyncYAML(file)
	require.NoError(t, err)

	loaded, err := LoadResultsYAML(file)
	require.NoError(t, err)
	assert.Len(t, loaded, 1)
	assert.Equal(t, "cov-syncyaml", loaded[0].Scenario)
}

func TestResultsSyncJSONToExistingDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.json")

	rs := Results{{Scenario: "cov-existing", Success: true}}
	err := rs.SyncJSON(file)
	require.NoError(t, err)

	_, err = os.Stat(file)
	require.NoError(t, err)
}

func TestResultsSyncYAMLToExistingDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	file := filepath.Join(tmpDir, "results.yaml")

	rs := Results{{Scenario: "cov-existing", Success: true}}
	err := rs.SyncYAML(file)
	require.NoError(t, err)

	_, err = os.Stat(file)
	require.NoError(t, err)
}
