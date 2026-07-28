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
	inspected.Config.Labels = map[string]string{clusterLabelKey: "test-etcd"}
	inspected.HostConfig.PortBindings = map[string][]portBinding{
		"2379/tcp": {{HostIP: "127.0.0.1", HostPort: "12380"}},
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
		"--volume", "test-etcd-2-data:/etcd-data",
		"gcr.io/etcd-development/etcd:v3.7.1",
		"/usr/local/bin/etcd", "--name", "test-etcd-2",
	}, spec.runArgs())
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
