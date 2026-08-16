package local

import (
	"testing"

	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

func TestReplacementPreservesIdentityAndCommand(t *testing.T) {
	t.Parallel()

	var inspected containerInspect
	inspected.ID = "old-container-id"
	inspected.Name = "/test-etcd-2"
	inspected.Config.Image = "gcr.io/etcd-development/etcd:v3.7.1"
	inspected.Config.Cmd = []string{"/usr/local/bin/etcd", "--name", "test-etcd-2"}
	inspected.Config.Env = []string{"GOFAIL_HTTP=0.0.0.0:2234"}
	inspected.Config.Labels = map[string]string{clusterLabelKey: "test-etcd"}
	inspected.HostConfig.PortBindings = map[string][]portBinding{
		"2379/tcp": {{HostIP: "127.0.0.1", HostPort: "12380"}},
		"2234/tcp": {{HostIP: "127.0.0.1", HostPort: "22342"}},
	}
	inspected.NetworkSettings.Networks = map[string]networkEndpoint{
		"test-etcd-net": {IPAddress: "10.88.0.3"},
	}
	inspected.Mounts = []containerMount{{Type: "volume", Name: "test-etcd-2-data", Destination: DataDir}}

	spec, err := replacementSpecFromInspect(inspected, "test-etcd")
	require.NoError(t, err)
	require.Equal(t, []string{
		"run", "--detach",
		"--name", "test-etcd-2",
		"--label", "etcd-infra.cluster=test-etcd",
		"--network", "test-etcd-net",
		"--ip", "10.88.0.3",
		"--publish", "127.0.0.1:12380:2379",
		"--publish", "127.0.0.1:22342:2234",
		"--env", "GOFAIL_HTTP=0.0.0.0:2234",
		"--volume", "test-etcd-2-data:/etcd-data",
		"gcr.io/etcd-development/etcd:v3.7.1",
		"/usr/local/bin/etcd", "--name", "test-etcd-2",
	}, spec.runArgs())
}

func TestReplacementRejectsMultipleAuxPortBindings(t *testing.T) {
	t.Parallel()

	var inspected containerInspect
	inspected.ID = "old-container-id"
	inspected.Name = "/test-etcd-2"
	inspected.Config.Image = "img"
	inspected.Config.Cmd = []string{"etcd"}
	inspected.Config.Labels = map[string]string{clusterLabelKey: "test-etcd"}
	inspected.HostConfig.PortBindings = map[string][]portBinding{
		"2379/tcp": {{HostIP: "127.0.0.1", HostPort: "12380"}},
		"2234/tcp": {{HostIP: "127.0.0.1", HostPort: "22342"}},
		"2235/tcp": {{HostIP: "127.0.0.1", HostPort: "22352"}},
	}
	inspected.NetworkSettings.Networks = map[string]networkEndpoint{
		"test-etcd-net": {IPAddress: "10.88.0.3"},
	}
	inspected.Mounts = []containerMount{{Type: "volume", Name: "test-etcd-2-data", Destination: DataDir}}

	_, err := replacementSpecFromInspect(inspected, "test-etcd")
	require.ErrorContains(t, err, "more than one auxiliary port binding")
}

func TestCapabilities(t *testing.T) {
	t.Parallel()

	caps := New("docker", "test-etcd", 0).Capabilities()
	require.True(t, caps.All(
		compute.CapabilityLifecycleCreateDelete,
		compute.CapabilityPowerControl,
		compute.CapabilityInventoryRead,
		compute.CapabilityCommandExecution,
	))
}

func TestSameEnvIsOrderInsensitive(t *testing.T) {
	t.Parallel()

	// Podman reorders Config.Env entries in inspect output; the replacement
	// identity check must treat the same set in a different order as equal.
	require.True(t, sameEnv(
		[]string{"container=podman", "PATH=/usr/bin", "HOME=/root", "HOSTNAME=abc"},
		[]string{"HOSTNAME=abc", "PATH=/usr/bin", "container=podman", "HOME=/root"},
	))
	require.False(t, sameEnv([]string{"A=1"}, []string{"A=1", "B=2"}))
	require.False(t, sameEnv([]string{"A=1", "A=1"}, []string{"A=1", "A=2"}))
	require.True(t, sameEnv(nil, nil))
}

func TestCreateRunArgs(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{
		"run", "--detach",
		"--name", "test-etcd-2",
		"--label", "etcd-infra.cluster=test-etcd",
		"--network", "test-etcd-net",
		"--publish", "127.0.0.1:12380:2379",
		"--volume", "test-etcd-2-data:/etcd-data",
		"gcr.io/etcd-development/etcd:v3.7.1",
		"/usr/local/bin/etcd", "--name", "test-etcd-2",
	}, createRunArgs(
		"test-etcd",
		"test-etcd-2",
		"gcr.io/etcd-development/etcd:v3.7.1",
		"test-etcd-2-data",
		compute.PortMapping{ContainerPort: 2379, HostPort: 12380, HostIP: "127.0.0.1"},
		CreateConfig{Command: []string{"/usr/local/bin/etcd", "--name", "test-etcd-2"}},
	))
}

func TestCreateRunArgsWithEnvAndAuxPort(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{
		"run", "--detach",
		"--name", "test-etcd-2",
		"--label", "etcd-infra.cluster=test-etcd",
		"--network", "test-etcd-net",
		"--publish", "127.0.0.1:12380:2379",
		"--publish", "127.0.0.1:22342:2234",
		"--env", "GOFAIL_HTTP=0.0.0.0:2234",
		"--env", "FOO=bar",
		"--volume", "test-etcd-2-data:/etcd-data",
		"localhost/etcd-infra-etcd:snapdb-fix",
		"/usr/local/bin/etcd", "--name", "test-etcd-2",
	}, createRunArgs(
		"test-etcd",
		"test-etcd-2",
		"localhost/etcd-infra-etcd:snapdb-fix",
		"test-etcd-2-data",
		compute.PortMapping{ContainerPort: 2379, HostPort: 12380, HostIP: "127.0.0.1"},
		CreateConfig{
			Command:        []string{"/usr/local/bin/etcd", "--name", "test-etcd-2"},
			Env:            []string{"GOFAIL_HTTP=0.0.0.0:2234", "FOO=bar"},
			AuxPortMapping: &compute.PortMapping{ContainerPort: 2234, HostPort: 22342, HostIP: "127.0.0.1"},
		},
	))
}

func TestValidateCreateConfig(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateCreateConfig(CreateConfig{Command: []string{"etcd"}}))
	require.NoError(t, validateCreateConfig(CreateConfig{
		Command:        []string{"etcd"},
		Env:            []string{"A=b"},
		AuxPortMapping: &compute.PortMapping{ContainerPort: 2234, HostPort: 22342},
	}))
	require.ErrorContains(t, validateCreateConfig(CreateConfig{
		Command: []string{"etcd"},
		Env:     []string{"no-equals"},
	}), "invalid environment entry")
	require.ErrorContains(t, validateCreateConfig(CreateConfig{
		Command:        []string{"etcd"},
		AuxPortMapping: &compute.PortMapping{ContainerPort: 2379, HostPort: 22342},
	}), "invalid auxiliary container port")
	require.ErrorContains(t, validateCreateConfig(CreateConfig{
		Command:        []string{"etcd"},
		AuxPortMapping: &compute.PortMapping{ContainerPort: 2234, HostPort: 0},
	}), "invalid auxiliary host port")
	require.ErrorContains(t, validateCreateConfig(CreateConfig{
		Command:        []string{"etcd"},
		AuxPortMapping: &compute.PortMapping{ContainerPort: 2234, HostPort: 22342, Protocol: "udp"},
	}), "auxiliary port mapping must use tcp")
}
