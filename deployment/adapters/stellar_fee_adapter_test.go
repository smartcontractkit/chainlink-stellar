package adapters

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	fqopstype "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
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
	_, ok = reg.GetFeeResolver("stellar")
	require.True(t, ok, "StellarFeeAdapter should be registered as the fee resolver from init()")
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

func TestStellarFeeAggregatorAdapter_WithdrawFeeTokens_nonNil(t *testing.T) {
	a := &StellarFeeAggregatorAdapter{}
	env := envWithDatastore(newSealedDatastore())
	seq := a.WithdrawFeeTokens(env)
	require.NotNil(t, seq)
	require.Equal(t, stellarsequences.StellarWithdrawFeeTokensSequenceID, seq.ID())
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

func TestStellarFeeAdapter_GetOnchainTokenTransferFeeConfig(t *testing.T) {
	t.Parallel()
	fqStrkey := stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-get-token-fee-test")
	token := stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-token-test")

	t.Run("round-trips the write-side mapping", func(t *testing.T) {
		t.Parallel()
		want := fees.TokenTransferFeeArgs{
			DestBytesOverhead: 10,
			DestGasOverhead:   50_000,
			MinFeeUSDCents:    3,
			IsEnabled:         true,
		}
		// What stellarsequences.StellarSetTokenTransferFee writes for these args.
		onchain := fqbindings.TokenTransferFeeConfig{
			DestBytesOverhead: want.DestBytesOverhead,
			DestGasOverhead:   want.DestGasOverhead,
			FeeUsdCents:       want.MinFeeUSDCents,
			IsEnabled:         want.IsEnabled,
		}
		val, err := onchain.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(fqStrkey, "get_token_fee_config", &val)
		got, err := getOnchainTokenTransferFeeConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, fqStrkey, 42, 7, token)
		require.NoError(t, err)
		// MaxFeeUSDCents/DeciBps have no on-chain counterpart; expected zero.
		require.Equal(t, want, got)
	})

	t.Run("unconfigured token maps to the disabled default", func(t *testing.T) {
		t.Parallel()
		val, err := (fqbindings.TokenTransferFeeConfig{}).ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(fqStrkey, "get_token_fee_config", &val)
		got, err := getOnchainTokenTransferFeeConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, fqStrkey, 42, 7, token)
		require.NoError(t, err)
		require.Equal(t, fees.TokenTransferFeeArgs{}, got)
	})

	t.Run("invalid token strkey", func(t *testing.T) {
		t.Parallel()
		_, err := getOnchainTokenTransferFeeConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{}, fqStrkey, 42, 7, "not-a-strkey")
		require.ErrorContains(t, err, "invalid token address")
	})
}

func TestStellarFeeAdapter_GetOnchainDestChainConfig(t *testing.T) {
	t.Parallel()
	fqStrkey := stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-get-dest-chain-test")

	t.Run("round-trips the write-side mapping", func(t *testing.T) {
		t.Parallel()
		want := lanes.FeeQuoterDestChainConfig{
			IsEnabled:                   true,
			MaxDataBytes:                12_451,
			MaxPerMsgGasLimit:           4_000_000,
			DestGasOverhead:             350_000,
			DestGasPerPayloadByteBase:   16,
			DefaultTokenFeeUSDCents:     50,
			DefaultTokenDestGasOverhead: 50_000,
			DefaultTxGasLimit:           200_000,
			NetworkFeeUSDCents:          100,
			V2Params:                    &lanes.FeeQuoterV2Params{LinkFeeMultiplierPercent: 90},
		}
		// What stellarsequences.StellarApplyDestChainConfig writes for this config.
		onchain := fqbindings.DestChainConfig{
			IsEnabled:             want.IsEnabled,
			MaxDataBytes:          want.MaxDataBytes,
			MaxPerMsgGasLimit:     want.MaxPerMsgGasLimit,
			DestGasOverhead:       want.DestGasOverhead,
			DestGasPerPayloadByte: uint32(want.DestGasPerPayloadByteBase),
			DefaultTokenFeeUsd:    uint32(want.DefaultTokenFeeUSDCents),
			DefaultTokenDestGas:   want.DefaultTokenDestGasOverhead,
			DefaultTxGasLimit:     want.DefaultTxGasLimit,
			NetworkFeeUsdCents:    uint32(want.NetworkFeeUSDCents),
			LinkPremiumPercent:    uint32(want.V2Params.LinkFeeMultiplierPercent),
		}
		val, err := onchain.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(fqStrkey, "get_dest_chain_config", &val)
		got, err := getOnchainDestChainConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, fqStrkey, 42, 7)
		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("unrepresentable on-chain values are rejected", func(t *testing.T) {
		t.Parallel()
		// On-chain fields are u32; the shared lane type narrows four of them.
		// A value like a 300% link premium must error, not truncate to 44 and
		// risk being written back by a read/modify/write changeset.
		onchain := fqbindings.DestChainConfig{
			IsEnabled:          true,
			LinkPremiumPercent: 300,
		}
		val, err := onchain.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(fqStrkey, "get_dest_chain_config", &val)
		_, err = getOnchainDestChainConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, fqStrkey, 42, 7)
		require.ErrorContains(t, err, "link premium percent 300 exceeds the uint8 lane field")
	})

	t.Run("not configured returns the zero config", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(fqStrkey, "get_dest_chain_config",
				operationstest.ContractErrorCode(stellarops.DestinationChainNotEnabledCode))
		got, err := getOnchainDestChainConfig(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, fqStrkey, 42, 7)
		require.NoError(t, err)
		require.Equal(t, lanes.FeeQuoterDestChainConfig{}, got)
	})
}

func TestStellarFeeAdapter_GetOnchain_errorPaths(t *testing.T) {
	t.Parallel()
	a := &StellarFeeAdapter{}
	chains := cldf_chain.NewBlockChains(nil)
	token := stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-token-test")

	t.Run("invalid fee quoter ref hex", func(t *testing.T) {
		t.Parallel()
		fqRef := testFQRef()
		fqRef.Address = "not-hex"
		_, err := a.GetOnchainTokenTransferFeeConfig(cldf_ops.Bundle{}, chains, fqRef, 42, 7, token)
		require.ErrorContains(t, err, "fee quoter datastore address to strkey")
	})

	t.Run("stellar chain not found", func(t *testing.T) {
		t.Parallel()
		hexAddr, err := stellarutil.StrkeyToHex(stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-fq"))
		require.NoError(t, err)
		fqRef := testFQRef()
		fqRef.Address = hexAddr
		_, err = a.GetOnchainTokenTransferFeeConfig(cldf_ops.Bundle{}, chains, fqRef, 42, 7, token)
		require.ErrorContains(t, err, "stellar chain 42 not found")
		_, err = a.GetOnchainDestChainConfig(cldf_ops.Bundle{}, chains, fqRef, 42, 7)
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

func TestStellarFeeAdapter_GetOnRampRef(t *testing.T) {
	t.Parallel()
	routerStrkey := stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-router-test")
	onRampStrkey := stellarutil.MustGenerateMockContractID("deployer", "fee-adapter-onramp-test")

	t.Run("routed lane returns the canonical onramp ref", func(t *testing.T) {
		t.Parallel()
		val := scval.AddressToScVal(onRampStrkey)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(routerStrkey, "get_onramp", &val)
		got, err := getOnRampRef(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, routerStrkey, 42, 7)
		require.NoError(t, err)
		hexAddr, err := stellarutil.StrkeyToHex(onRampStrkey)
		require.NoError(t, err)
		require.Equal(t, stellarccip.OnRampDatastoreRef().FullAddressRef(42, hexAddr), got)
	})

	t.Run("not routed lane errors", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(routerStrkey, "get_onramp",
				operationstest.ContractErrorCode(stellarops.UnsupportedDestinationChainCode))
		_, err := getOnRampRef(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, routerStrkey, 42, 7)
		require.ErrorContains(t, err, "no OnRamp contract found")
	})

	t.Run("router ref missing from datastore", func(t *testing.T) {
		t.Parallel()
		a := &StellarFeeAdapter{}
		_, err := a.GetOnRampRef(cldf_ops.Bundle{}, cldf_chain.NewBlockChains(nil), newSealedDatastore(), 42, 7)
		require.ErrorContains(t, err, "resolve Router ref")
	})

	t.Run("stellar chain not found", func(t *testing.T) {
		t.Parallel()
		a := &StellarFeeAdapter{}
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, stellarccip.RouterDatastoreRef().UpsertDeployedStrKey(ds, 42, routerStrkey))
		_, err := a.GetOnRampRef(cldf_ops.Bundle{}, cldf_chain.NewBlockChains(nil), ds.Seal(), 42, 7)
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}
