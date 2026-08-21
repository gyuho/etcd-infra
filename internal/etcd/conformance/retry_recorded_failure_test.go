package conformance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/conformance/scenarios"
)

// TestLastRecordedFailure verifies the retry loop's failure detection: a
// scenario that records Success=false without a wrapper error must be found.
func TestLastRecordedFailure(t *testing.T) {
	t.Parallel()

	results := scenarios.Results{
		{Scenario: "other", Success: true},
		{Scenario: "target", Success: false, Output: "timed out waiting for puts"},
	}
	failure := lastRecordedFailure(results, 0, "target")
	require.NotNil(t, failure)
	assert.Equal(t, "timed out waiting for puts", failure.Output)

	// A passing record is not a failure.
	assert.Nil(t, lastRecordedFailure(scenarios.Results{{Scenario: "target", Success: true}}, 0, "target"))
	// Records from before the attempt window are ignored (before is the
	// results length captured before the attempt, so its records start there).
	assert.Nil(t, lastRecordedFailure(results, 2, "target"))
	// Unknown scenarios have no failure.
	assert.Nil(t, lastRecordedFailure(results, 0, "absent"))
}

// TestDropResultsFrom verifies the retry loop can discard a failed attempt's
// record so the final tally reflects only the final attempt.
func TestDropResultsFrom(t *testing.T) {
	t.Parallel()

	r := &runner{results: make([]scenarios.Result, 0)}
	r.RecordResult(scenarios.Result{Scenario: "a", Success: true})
	r.RecordResult(scenarios.Result{Scenario: "b", Success: false})
	r.DropResultsFrom(1)
	require.Len(t, r.Results(), 1)
	assert.Equal(t, "a", r.Results()[0].Scenario)

	// Out-of-range and equal are no-ops.
	r.DropResultsFrom(5)
	assert.Len(t, r.Results(), 1)
	r.DropResultsFrom(1)
	assert.Len(t, r.Results(), 1)
}

// TestCountResults is covered by workflow_test.go; this guards the pairing of
// drop + record: after dropping a failed attempt and recording the retry's
// success, the tally has exactly one entry per scenario.
func TestDropThenRecordKeepsTallyClean(t *testing.T) {
	t.Parallel()

	r := &runner{results: make([]scenarios.Result, 0)}
	before := len(r.Results())
	r.RecordResult(scenarios.Result{Scenario: "s", Success: false, Output: "flake"})
	r.DropResultsFrom(before)
	r.RecordResult(scenarios.Result{Scenario: "s", Success: true})
	require.Len(t, r.Results(), 1)
	passed, failed := countResults(r.Results())
	assert.Equal(t, 1, passed)
	assert.Equal(t, 0, failed)
}
