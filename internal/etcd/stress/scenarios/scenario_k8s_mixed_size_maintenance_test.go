//nolint:testpackage // Tests use package internals.
package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompactTargetNeverPassesCutoff(t *testing.T) {
	t.Parallel()
	base := time.Unix(1000, 0)
	samples := []revisionSample{
		{at: base, rev: 10},
		{at: base.Add(time.Second), rev: 30},
		{at: base.Add(2 * time.Second), rev: 20},
		{at: base.Add(3 * time.Second), rev: 99},
	}

	require.Zero(t, compactTarget(samples, base.Add(-time.Nanosecond)), "nothing acknowledged before the cutoff")
	require.Equal(t, int64(10), compactTarget(samples, base), "a sample exactly at the cutoff is eligible")
	// Out-of-order acknowledgments: the highest revision seen by the cutoff wins.
	require.Equal(t, int64(30), compactTarget(samples, base.Add(2*time.Second)))
	require.Equal(t, int64(99), compactTarget(samples, base.Add(time.Hour)))
}

func TestJudgeK8sMixedFailsOnStallsEvenWithoutErrors(t *testing.T) {
	t.Parallel()
	ok := &Result{Classes: []ClassResult{{Class: "pod", ValueBytes: 30000, P99LatencyMs: 5}}}
	pass, _ := judgeK8sMixed(ok, Statistics{}, 0, 1)
	require.True(t, pass)

	missed := &Result{MissedArrivals: 1, Classes: ok.Classes}
	pass, out := judgeK8sMixed(missed, Statistics{}, 0, 1)
	require.False(t, pass)
	require.Contains(t, out, "1 missed arrivals")

	slow := &Result{Classes: []ClassResult{{Class: "lease", ValueBytes: 400, P99LatencyMs: 5000}}}
	pass, out = judgeK8sMixed(slow, Statistics{}, 0, 1)
	require.False(t, pass, "a small-object class stalled behind maintenance must fail the run")
	require.Contains(t, out, "lease p99")

	maint := &Result{Classes: ok.Classes, Maintenance: []MaintenanceEvent{{Kind: "defrag", Member: "m1", Error: "deadline exceeded"}}}
	pass, out = judgeK8sMixed(maint, Statistics{}, 0, 1)
	require.False(t, pass)
	require.Contains(t, out, "defrag m1 failed")
}

func TestK8sClassStatesHonorSizeAndScale(t *testing.T) {
	t.Parallel()
	states := newK8sClassStates(StressConfig{ValueSizeBytes: 100000, KeyspaceScale: 2})
	byName := map[string]*k8sClassState{}
	for _, s := range states {
		byName[s.name] = s
	}
	require.Equal(t, 100000, byName["pod"].bytes, "--value-bytes sets the pod size under test")
	require.Equal(t, 400, byName["lease"].bytes, "other classes keep their Kubernetes shapes")
	require.Equal(t, 10000, byName["pod"].keys)
	require.Len(t, byName["pod"].value(12345), 100000)
}
