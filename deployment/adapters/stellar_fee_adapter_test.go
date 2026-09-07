package adapters

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	fqopstype "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// testFQRef returns a dummy AddressRef for FeeQuoter lookup tests.
// The actual lookup uses the type/version from the adapter, not this ref's address.
func testFQRef() datastore.AddressRef {
	return datastore.AddressRef{
		Type:    datastore.ContractType(fqopstype.ContractType),
		Version: semver.MustParse(fqopstype.Deploy.Version()),
	}
}

func TestStellarFeeAdapter_InterfaceCompliance(t *testing.T) {
	var _ fees.FeeAdapter = (*StellarFeeAdapter)(nil)
}

func TestStellarFeeAdapter_GetDefaultTokenTransferFeeConfig(t *testing.T) {
	a := &StellarFeeAdapter{}
	cfg := a.GetDefaultTokenTransferFeeConfig(0, 0)
	require.True(t, cfg.IsEnabled)
	require.Greater(t, cfg.MinFeeUSDCents, uint32(0))
	require.GreaterOrEqual(t, cfg.MaxFeeUSDCents, cfg.MinFeeUSDCents)
}

func TestStellarFeeAdapter_GetDefaultDestChainConfig(t *testing.T) {
	a := &StellarFeeAdapter{}
	cfg := a.GetDefaultDestChainConfig(0, 0)
	require.True(t, cfg.IsEnabled)
	require.Greater(t, cfg.MaxDataBytes, uint32(0))
	require.Greater(t, cfg.MaxPerMsgGasLimit, uint32(0))
}

func TestStellarFeeAdapter_GetFeeContractRef_emptyDatastore(t *testing.T) {
	a := &StellarFeeAdapter{}
	_, err := a.GetFeeContractRef(cldf_ops.Bundle{}, cldf_chain.NewBlockChains(nil), newSealedDatastore(), testFQRef(), 42, 0)
	require.Error(t, err)
}

func TestStellarFeeAdapter_GetFeeContractRef_found(t *testing.T) {
	a := &StellarFeeAdapter{}
	ds := datastore.NewMemoryDataStore()
	// Datastore stores Soroban contract ids as 0x-hex (see deployment/ccip.UpsertDeployedStrKey).
	fqStrkey := stellarutil.MustGenerateMockContractID("deployer", "fee-quoter-adapter-test")
	hexAddr, err := stellarutil.StrkeyToHex(fqStrkey)
	require.NoError(t, err)
	ref := datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(fqopstype.ContractType),
		Version:       semver.MustParse(fqopstype.Deploy.Version()),
		Address:       hexAddr,
	}
	require.NoError(t, ds.Addresses().Upsert(ref))
	got, err := a.GetFeeContractRef(cldf_ops.Bundle{}, cldf_chain.NewBlockChains(nil), ds.Seal(), testFQRef(), 42, 0)
	require.NoError(t, err)
	require.Equal(t, hexAddr, got.Address)

	roundTrip, err := scval.HexToContractStrkey(got.Address)
	require.NoError(t, err)
	require.Equal(t, fqStrkey, roundTrip)
}

func TestStellarFeeAdapter_SetTokenTransferFee_nonNil(t *testing.T) {
	a := &StellarFeeAdapter{}
	seq := a.SetTokenTransferFee(newSealedDatastore(), testFQRef())
	require.NotNil(t, seq)
}

func TestStellarFeeAdapter_ApplyDestChainConfigUpdates_nonNil(t *testing.T) {
	a := &StellarFeeAdapter{}
	seq := a.ApplyDestChainConfigUpdates(newSealedDatastore(), testFQRef())
	require.NotNil(t, seq)
}

func TestStellarFeeAdapter_Registration(t *testing.T) {
	reg := fees.GetRegistry()
	_, ok := reg.GetFeeAdapter("stellar", stellarops.ContractDeploymentVersion)
	if !ok {
		_, ok = reg.GetFeeAdapter("stellar", stellarops.ContractDeploymentVersion)
	}
	require.True(t, ok, "StellarFeeAdapter should be registered from init()")
}

func TestStellarFeeAggregatorAdapter_InterfaceCompliance(t *testing.T) {
	var _ fees.FeeAggregatorAdapter = (*StellarFeeAggregatorAdapter)(nil)
}

func TestStellarFeeAggregatorAdapter_SetFeeAggregator_nonNil(t *testing.T) {
	a := &StellarFeeAggregatorAdapter{}
	env := envWithDatastore(newSealedDatastore())
	seq := a.SetFeeAggregator(env)
	require.NotNil(t, seq)
	require.Equal(t, stellarsequences.StellarSetFeeAggregatorSequenceID, seq.ID())
	require.Equal(t, stellarops.ContractDeploymentVersion.String(), seq.Version())
}

func TestStellarFeeAggregatorAdapter_GetFeeAggregator_requiresChainAndDatastore(t *testing.T) {
	a := &StellarFeeAggregatorAdapter{}
	env := envWithDatastore(newSealedDatastore())
	_, err := a.GetFeeAggregator(env, 42)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarFeeAggregatorAdapter_Registration(t *testing.T) {
	reg := fees.GetFeeAggregatorRegistry()
	_, ok := reg.GetFeeAggregatorAdapter("stellar", stellarops.ContractDeploymentVersion)
	require.True(t, ok, "StellarFeeAggregatorAdapter should be registered from init()")
}
