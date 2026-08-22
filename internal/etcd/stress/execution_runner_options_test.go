package stress

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/internal/etcd/stress/scenarios"
)

func TestNormalizeEndpoints(t *testing.T) {
	t.Parallel()
	endpoints := normalizeEndpoints([]string{" ", "https://one", "  ", "http://two"})
	require.Equal(t, []string{"https://one", "http://two"}, endpoints)

	endpoints = normalizeEndpoints(nil)
	require.Equal(t, []string{DefaultEndpoints}, endpoints)

	endpoints = normalizeEndpoints([]string{"   "})
	require.Equal(t, []string{DefaultEndpoints}, endpoints)
}

func TestResolveScenarioIDs(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"id-123"}, resolveScenarioIDs("id-123"))
	require.Equal(t, []string{"id-123", "id-456"}, resolveScenarioIDs("id-123, id-456"))

	ids := resolveScenarioIDs("")
	require.NotEmpty(t, ids)

	expected := make([]string, 0, len(scenarios.StressIDStringToRunnerFunc))
	for id := range scenarios.StressIDStringToRunnerFunc {
		expected = append(expected, id)
	}
	sort.Strings(expected)
	require.Equal(t, expected, ids)
}

func TestNormalizeTestKeyPrefix(t *testing.T) {
	t.Parallel()
	require.Equal(t, DefaultTestKeyPrefix, normalizeTestKeyPrefix(""))
	require.Equal(t, "/custom/", normalizeTestKeyPrefix(" /custom/ "))
}

func TestCountResults(t *testing.T) {
	t.Parallel()
	passed, failed := countResults([]scenarios.Result{{Success: true}, {Success: false}, {Success: true}})
	require.Equal(t, 2, passed)
	require.Equal(t, 1, failed)
}
