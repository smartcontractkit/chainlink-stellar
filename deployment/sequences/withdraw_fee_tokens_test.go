package sequences

import (
	"math/big"
	"testing"

	"github.com/Masterminds/semver/v3"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"
)

// withdrawTestChains returns BlockChains with a single Stellar chain; the chain has a nil
// client, which is fine for tests that error out before any on-chain call.
func withdrawTestChains(sel uint64) cldf_chain.BlockChains {
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	return cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
}

func withdrawTestTokens() []fees.FeeTokenWithdrawal {
	return []fees.FeeTokenWithdrawal{
		{Token: stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-token-a")},
		{Token: keypair.MustRandom().Address()},
	}
}

func TestApplyStellarWithdrawFeeTokens_RejectsNilDatastore(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	env := cldf.Environment{}
	_, err := ApplyStellarWithdrawFeeTokens(b, cldf_chain.NewBlockChains(nil), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: chainsel.STELLAR_LOCALNET.Selector,
		FeeTokens:     withdrawTestTokens(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DataStore")
}

func TestApplyStellarWithdrawFeeTokens_RejectsEmptyFeeTokens(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	_, err := ApplyStellarWithdrawFeeTokens(b, cldf_chain.NewBlockChains(nil), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one fee token")
}

func TestApplyStellarWithdrawFeeTokens_RejectsNonNilAmount(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	tokens := withdrawTestTokens()
	tokens[0].Amount = big.NewInt(100)
	_, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     tokens,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "amount is not supported")
}

func TestApplyStellarWithdrawFeeTokens_RejectsInvalidTokenAddress(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}

	for _, tok := range []string{"", "0x5ee8d41a877cfb8d29fbe6e09fcbd5c45a1a8f9c1d56a2e5c1c1c1c1c1c1c1c", "not-a-strkey"} {
		_, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
			ChainSelector: sel,
			FeeTokens:     []fees.FeeTokenWithdrawal{{Token: tok}},
		})
		require.Error(t, err, "token %q should be rejected", tok)
		require.Contains(t, err.Error(), "fee token")
	}
}

func TestApplyStellarWithdrawFeeTokens_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	_, err := ApplyStellarWithdrawFeeTokens(b, cldf_chain.NewBlockChains(nil), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     withdrawTestTokens(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestApplyStellarWithdrawFeeTokens_RejectsMissingDefaultRef(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420041)
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	_, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     withdrawTestTokens(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve OnRamp")
}

func TestApplyStellarWithdrawFeeTokens_RejectsUnsupportedContractType(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420042)
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	_, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     withdrawTestTokens(),
		Contracts: []datastore.AddressRef{
			{Type: stellarccip.OffRampDatastoreRef().Type},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot withdraw fee tokens from contract type")
}

func TestApplyStellarWithdrawFeeTokens_ExplicitContractsUseFullRefLookup(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420043)
	ds := datastore.NewMemoryDataStore()
	// The default OnRamp ref is recorded, but the explicit ref carries a different
	// version/qualifier that is not: proving the sequence resolves the full ref (Type +
	// Version + Qualifier) rather than falling back to the default.
	require.NoError(t, stellarccip.RecordOnRamp(ds, sel, stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-onramp-default")))
	env := cldf.Environment{DataStore: ds.Seal()}
	_, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     withdrawTestTokens(),
		Contracts: []datastore.AddressRef{
			{
				Type:      stellarccip.OnRampDatastoreRef().Type,
				Version:   semver.MustParse("0.0.1-doesnotexist"),
				Qualifier: "nonexistent",
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve OnRamp")
}
