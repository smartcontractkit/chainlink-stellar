package ccip

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
)

func TestMergeExistingAddressRefs(t *testing.T) {
	ds := datastore.NewMemoryDataStore()
	ref := datastore.AddressRef{
		Address:       "0x01",
		ChainSelector: 42,
		Type:          datastore.ContractType("Foo"),
		Version:       stellarops.ContractRefVersion,
	}
	require.NoError(t, MergeExistingAddressRefs(ds, []datastore.AddressRef{ref}))
	got, err := ds.Addresses().Get(datastore.NewAddressRefKey(42, datastore.ContractType("Foo"), stellarops.ContractRefVersion, ""))
	require.NoError(t, err)
	require.Equal(t, ref.Address, got.Address)
}
