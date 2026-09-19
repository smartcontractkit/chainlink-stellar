package ccip

import (
	"testing"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatastoreLookup_GetStrkeyHelpers_NotFound(t *testing.T) {
	t.Parallel()
	ds := datastore.NewMemoryDataStore().Seal()
	sel := uint64(91001)

	tests := []struct {
		name string
		fn   func(datastore.DataStore, uint64) (string, error)
	}{
		{"OnRamp", GetOnRampStrkey},
		{"OffRamp", GetOffRampStrkey},
		{"Router", GetRouterStrkey},
		{"FeeQuoter", GetFeeQuoterStrkey},
		{"TokenAdminRegistry", GetTokenAdminRegistryStrkey},
		{"VVR", GetVVRStrkey},
		{"CommitteeVerifier", GetCommitteeVerifierStrkey},
		{"RampRegistry", GetRampRegistryStrkey},
		{"RMNRemote", GetRMNRemoteStrkey},
		{"RMNProxy", GetRMNProxyStrkey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tc.fn(ds, sel)
			require.Error(t, err)
		})
	}
}

func TestDatastoreLookup_GetStrkeyHelpers_RecordRoundTrip(t *testing.T) {
	t.Parallel()
	sel := uint64(91002)
	base := "deployer"

	tests := []struct {
		name   string
		record func(*datastore.MemoryDataStore, uint64, string) error
		lookup func(datastore.DataStore, uint64) (string, error)
		suffix string
	}{
		{"OnRamp", RecordOnRamp, GetOnRampStrkey, "onr"},
		{"OffRamp", RecordOffRamp, GetOffRampStrkey, "offr"},
		{"Router", RecordRouter, GetRouterStrkey, "router"},
		{"FeeQuoter", RecordFeeQuoter, GetFeeQuoterStrkey, "fq"},
		{"TokenAdminRegistry", RecordTokenAdminRegistry, GetTokenAdminRegistryStrkey, "tar"},
		{"VVR", RecordVVR, GetVVRStrkey, "vvr"},
		{"CommitteeVerifier", RecordCommitteeVerifier, GetCommitteeVerifierStrkey, "cv"},
		{"RampRegistry", RecordRampRegistry, GetRampRegistryStrkey, "rr"},
		{"RMNRemote", RecordRMNRemote, GetRMNRemoteStrkey, "rmn"},
		{"RMNProxy", RecordRMNProxy, GetRMNProxyStrkey, "rmnp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ds := datastore.NewMemoryDataStore()
			id := stellarutil.MustGenerateMockContractID(base, tc.suffix)
			require.NoError(t, tc.record(ds, sel, id))
			got, err := tc.lookup(ds.Seal(), sel)
			require.NoError(t, err)
			assert.Equal(t, id, got)
		})
	}
}
