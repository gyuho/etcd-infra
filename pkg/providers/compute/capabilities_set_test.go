package compute

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCapabilitySetHas(t *testing.T) {
	t.Parallel()

	set := NewCapabilitySet(
		CapabilityLifecycleCreateDelete,
		CapabilityInventoryRead,
	)

	assert.True(t, set.Has(CapabilityLifecycleCreateDelete))
	assert.False(t, set.Has(CapabilityCommandStreaming))
}

func TestCapabilitySetAll(t *testing.T) {
	t.Parallel()

	set := NewCapabilitySet(
		CapabilityLifecycleCreateDelete,
		CapabilityInventoryRead,
		CapabilityCommandExecution,
	)

	assert.True(t, set.All(CapabilityLifecycleCreateDelete, CapabilityInventoryRead),
		"All should return true when all queried capabilities are present")
	assert.False(t, set.All(CapabilityLifecycleCreateDelete, CapabilityCommandStreaming),
		"All should return false when any queried capability is missing")
	assert.True(t, set.All(),
		"All with no arguments should return true (vacuous truth)")

	var empty CapabilitySet
	assert.True(t, empty.All(),
		"All with no arguments on empty set should return true")
	assert.False(t, empty.All(CapabilityCommandExecution),
		"All on empty set should return false when capabilities are requested")
}

func TestCapabilitySetAny(t *testing.T) {
	t.Parallel()

	set := NewCapabilitySet(
		CapabilityLifecycleCreateDelete,
		CapabilityInventoryRead,
	)

	assert.True(t, set.Any(CapabilityCommandStreaming, CapabilityInventoryRead),
		"Any should return true when at least one capability is present")
	assert.False(t, set.Any(CapabilityCommandStreaming, CapabilityCommandPolling),
		"Any should return false when no queried capability is present")
	assert.False(t, set.Any(),
		"Any with no arguments should return false (vacuous disjunction)")
}

func TestCapabilitySetList(t *testing.T) {
	t.Parallel()

	set := NewCapabilitySet(
		CapabilityCommandExecution,
		CapabilityFileTransfer,
		CapabilityLifecycleCreateDelete,
	)

	list := set.List()
	require.Len(t, list, 3)
	// List returns sorted order.
	assert.Equal(t, CapabilityCommandExecution, list[0])
	assert.Equal(t, CapabilityFileTransfer, list[1])
	assert.Equal(t, CapabilityLifecycleCreateDelete, list[2])

	var empty CapabilitySet
	assert.Nil(t, empty.List(), "List on empty set should return nil")
}

func TestCapabilitySetLen(t *testing.T) {
	t.Parallel()

	set := NewCapabilitySet(
		CapabilityLifecycleCreateDelete,
		CapabilityInventoryRead,
	)
	assert.Equal(t, 2, set.Len())

	var empty CapabilitySet
	assert.Equal(t, 0, empty.Len())
}
