package sequences

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	creforwarderops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/cre_forwarder"
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

func TestFindExistingStellarCREForwarder_MatchesRefsWrittenAtVersion2(t *testing.T) {
	t.Parallel()
	// #given a forwarder ref recorded before deploy reports moved to 3.0.0
	sel, qual := uint64(1), "q"
	ref := datastore.AddressRef{
		ChainSelector: sel, Type: datastore.ContractType(creforwarderops.ContractType),
		Version: semver.MustParse("2.0.0"), Qualifier: qual, Address: "CFORWARDER",
	}
	// #when
	addr, ok := FindExistingStellarCREForwarder([]datastore.AddressRef{ref}, sel, qual)
	// #then the ref is found, so a rerun does not redeploy at the same salt
	require.True(t, ok)
	require.Equal(t, "CFORWARDER", addr)
	require.Equal(t, "2.0.0", StellarCREForwarderDatastoreRef(sel, qual, "CFORWARDER").Version.String())
}
