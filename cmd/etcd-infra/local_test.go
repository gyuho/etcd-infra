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

	localprovider "git.tbd/etcd-infra/pkg/providers/local"
)

func TestLocalThreeMemberTopology(t *testing.T) {
	t.Parallel()

	members := localMembers("test-etcd", 3, 12379)
	require.Len(t, members, 3)
	assert.Equal(t, "test-etcd-1=http://test-etcd-1:2380,test-etcd-2=http://test-etcd-2:2380,test-etcd-3=http://test-etcd-3:2380", initialCluster(members))

	args := etcdServerArgs(members[1], members, "test-etcd", localprovider.DataDir)
	assert.True(t, slices.Contains(args, localprovider.DataDir))
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

	require.Equal(t, "label=etcd-infra.cluster=test", localprovider.ClusterFilter("test"))
}

func TestParseLocalEnv(t *testing.T) {
	t.Parallel()

	env, err := parseLocalEnv("")
	require.NoError(t, err)
	assert.Empty(t, env)

	env, err = parseLocalEnv("GOFAIL_HTTP=0.0.0.0:2234, FOO=bar")
	require.NoError(t, err)
	assert.Equal(t, []string{"GOFAIL_HTTP=0.0.0.0:2234", "FOO=bar"}, env)

	_, err = parseLocalEnv("MISSING_EQUALS")
	require.ErrorContains(t, err, "invalid --env entry")
	_, err = parseLocalEnv("=novalue-key")
	require.ErrorContains(t, err, "invalid --env entry")
}

func TestParseLocalAuxPort(t *testing.T) {
	t.Parallel()

	mapping, err := parseLocalAuxPort("", 3)
	require.NoError(t, err)
	assert.Nil(t, mapping)

	mapping, err = parseLocalAuxPort("2234:32479", 3)
	require.NoError(t, err)
	require.NotNil(t, mapping)
	assert.Equal(t, 2234, mapping.ContainerPort)
	assert.Equal(t, 32479, mapping.HostPort)
	assert.Equal(t, "127.0.0.1", mapping.HostIP)

	_, err = parseLocalAuxPort("2234", 3)
	require.ErrorContains(t, err, "expected containerPort:firstHostPort")
	_, err = parseLocalAuxPort("2379:32479", 3)
	require.ErrorContains(t, err, "invalid --aux-port container port")
	_, err = parseLocalAuxPort("2234:0", 3)
	require.ErrorContains(t, err, "invalid --aux-port host port range")
	_, err = parseLocalAuxPort("2234:65535", 3)
	require.ErrorContains(t, err, "invalid --aux-port host port range")
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
