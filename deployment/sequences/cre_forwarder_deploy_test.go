package sequences

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
)

func TestFindExistingStellarCREForwarder(t *testing.T) {
	sel, qual := uint64(5), "q"
	refs := []datastore.AddressRef{StellarCREForwarderDatastoreRef(sel, qual, "CFORWARDER")}

	addr, ok := FindExistingStellarCREForwarder(refs, sel, qual)
	require.True(t, ok)
	require.Equal(t, "CFORWARDER", addr)

	_, ok = FindExistingStellarCREForwarder(refs, sel, "other")
	require.False(t, ok)
	_, ok = FindExistingStellarCREForwarder(refs, sel+1, qual)
	require.False(t, ok)
}

func TestCREForwarderDeploySalt(t *testing.T) {
	require.Equal(t, CREForwarderDeploySalt(1, "a"), CREForwarderDeploySalt(1, "a"))
	require.NotEqual(t, CREForwarderDeploySalt(1, "a"), CREForwarderDeploySalt(2, "a"))
	require.NotEqual(t, CREForwarderDeploySalt(1, "a"), CREForwarderDeploySalt(1, "b"))
}
