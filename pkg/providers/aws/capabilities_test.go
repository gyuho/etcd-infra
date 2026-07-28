package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.tbd/etcd-infra/pkg/providers/compute"
)

func TestManagerCapabilities(t *testing.T) {
	t.Parallel()

	mgr := &Manager{}
	caps := mgr.Capabilities()

	required := []compute.Capability{
		compute.CapabilityLifecycleCreateDelete,
		compute.CapabilityPowerControl,
		compute.CapabilityInventoryRead,
		compute.CapabilityCommandExecution,
		compute.CapabilitySSHAccess,
		compute.CapabilityReadinessWait,
		compute.CapabilityInstanceMetadata,
	}
	for _, cap := range required {
		require.True(t, caps.Has(cap), "missing capability %q", cap)
	}

	assert.False(t, caps.Has(compute.CapabilityCommandStreaming),
		"aws manager should not advertise streaming command capability")

	// CoordinationIP and EtcdPeerInterface are infrastructure-layer operations
	// (aws/headscaleinfra/ and aws/eni/), not compute.Provider capabilities.
	assert.False(t, caps.Has(compute.CapabilityCoordinationIP),
		"aws manager should not advertise coordination-ip capability (infrastructure-layer)")
	assert.False(t, caps.Has(compute.CapabilityEtcdPeerInterface),
		"aws manager should not advertise etcd-peer-interface capability (infrastructure-layer)")

	// GroupScaling is only advertised when an ASG client is provided.
	assert.False(t, caps.Has(compute.CapabilityGroupScaling),
		"aws manager without ASG client should not advertise group-scaling capability")

	mgrWithASG := &Manager{asg: &fakeASG{}}
	capsWithASG := mgrWithASG.Capabilities()
	assert.True(t, capsWithASG.Has(compute.CapabilityGroupScaling),
		"aws manager with ASG client should advertise group-scaling capability")
}
