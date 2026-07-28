package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseTag(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "v3.7.1", releaseTag("3.7.1"))
	assert.Equal(t, "v3.7.1", releaseTag("v3.7.1"))
}

func TestResolveVersionRejectsInvalidVersion(t *testing.T) {
	t.Parallel()

	_, err := resolveVersion(context.Background(), "not-a-version")
	require.ErrorContains(t, err, "invalid etcd version")
}

func TestSplitEndpoints(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		[]string{"http://127.0.0.1:2379", "http://127.0.0.1:2380"},
		splitEndpoints(" http://127.0.0.1:2379, ,http://127.0.0.1:2380 "),
	)
}

func TestEtcdTestCommandsExposeCopiedRunners(t *testing.T) {
	t.Parallel()

	err := runConformance([]string{"--scenario", "NOT_A_SCENARIO"})
	require.ErrorContains(t, err, "unknown scenario")
	err = runStress([]string{"--scenario", "NOT_A_SCENARIO", "--duration", "1", "--workers", "1"})
	require.ErrorContains(t, err, "unknown scenario")
}
