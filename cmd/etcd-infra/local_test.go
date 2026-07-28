package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalThreeMemberContainerArgs(t *testing.T) {
	t.Parallel()

	members := localMembers("test-etcd", 3, 12379)
	require.Len(t, members, 3)
	assert.Equal(t, "test-etcd-1=http://test-etcd-1:2380,test-etcd-2=http://test-etcd-2:2380,test-etcd-3=http://test-etcd-3:2380", initialCluster(members))

	args := containerRunArgs(members[1], members, "test-etcd", 12380, "3.7.1")
	assert.True(t, slices.Contains(args, "127.0.0.1:12380:2379"))
	assert.True(t, slices.Contains(args, "gcr.io/etcd-development/etcd:v3.7.1"))
	assert.True(t, slices.Contains(args, "etcd-infra.cluster=test-etcd"))
	assert.True(t, slices.Contains(args, "http://test-etcd-2:2380"))
	assert.Equal(t, 1, countArg(args, "--initial-cluster-token"))
}

func TestLocalContainerRuntimeFallsBackToPodman(t *testing.T) {
	dir := t.TempDir()
	falsePath, err := exec.LookPath("false")
	require.NoError(t, err)
	truePath, err := exec.LookPath("true")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(falsePath, filepath.Join(dir, "docker")))
	require.NoError(t, os.Symlink(truePath, filepath.Join(dir, "podman")))
	t.Setenv("PATH", dir)
	t.Setenv(containerRuntimeEnv, "")

	runtime, err := localContainerRuntime(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "podman", runtime)
}

func TestValidateLocalOptions(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateLocalOptions("etcd-infra", 3, 2379))
	require.ErrorContains(t, validateLocalOptions("bad,name", 3, 2379), "invalid cluster name")
	require.ErrorContains(t, validateLocalOptions("etcd-infra", 2, 2379), "members must be 1 or 3")
	require.ErrorContains(t, validateLocalOptions("etcd-infra", 3, 65534), "invalid client port range")
}

func TestLocalClusterFilter(t *testing.T) {
	t.Parallel()

	require.Equal(t, "label=etcd-infra.cluster=test", localClusterFilter("test"))
}

func countArg(args []string, target string) int {
	count := 0
	for _, arg := range args {
		if arg == target {
			count++
		}
	}
	return count
}
