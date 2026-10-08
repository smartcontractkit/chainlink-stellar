package ccip

import (
	"testing"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatastoreContractRef_roundTripRouterRMN(t *testing.T) {
	ds := datastore.NewMemoryDataStore()
	sel := uint64(12345)

	routerID := stellarutil.MustGenerateMockContractID("deployer", "router-roundtrip")
	require.NoError(t, RecordRouter(ds, sel, routerID))
	gotRouter, err := RouterDatastoreRef().LookupStrkey(ds.Seal(), sel)
	require.NoError(t, err)
	assert.Equal(t, routerID, gotRouter)

	rmnID := stellarutil.MustGenerateMockContractID("deployer", "rmn-roundtrip")
	require.NoError(t, RecordRMNRemote(ds, sel, rmnID))
	gotRMN, err := RMNRemoteDatastoreRef().LookupStrkey(ds.Seal(), sel)
	require.NoError(t, err)
	assert.Equal(t, rmnID, gotRMN)
}

func TestDatastoreContractRef_onRampMetaStable(t *testing.T) {
	ref := OnRampDatastoreRef()
	require.NotNil(t, ref.Version)
	assert.Equal(t, "", ref.Qualifier)
}

func TestCommitteeVerifierDatastoreRefWithQualifier(t *testing.T) {
	defaultRef := CommitteeVerifierDatastoreRef()

	emptyRef := CommitteeVerifierDatastoreRefWithQualifier("")
	assert.Equal(t, defaultRef.Type, emptyRef.Type)
	assert.Equal(t, defaultRef.Version, emptyRef.Version)
	assert.Equal(t, DefaultCommitteeVerifierQualifier, emptyRef.Qualifier)

	qualifiedRef := CommitteeVerifierDatastoreRefWithQualifier("mcms-governance-test")
	assert.Equal(t, defaultRef.Type, qualifiedRef.Type)
	assert.Equal(t, defaultRef.Version, qualifiedRef.Version)
	assert.Equal(t, "mcms-governance-test", qualifiedRef.Qualifier)
}
