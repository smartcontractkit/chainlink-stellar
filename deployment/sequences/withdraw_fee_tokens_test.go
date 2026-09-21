package sequences

import (
	"context"
	"errors"
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
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
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

// decodeWithdrawPayload decodes an EncodeSorobanMCMSInvokePayload payload back into its
// function name and argument ScVals.
func decodeWithdrawPayload(t *testing.T, data []byte) (string, []xdr.ScVal) {
	t.Helper()
	var sc xdr.ScVal
	require.NoError(t, sc.UnmarshalBinary(data))
	require.Equal(t, xdr.ScValTypeScvVec, sc.Type)
	vec := *sc.MustVec()
	require.NotEmpty(t, vec)
	require.Equal(t, xdr.ScValTypeScvSymbol, vec[0].Type)
	return string(vec[0].MustSym()), vec[1:]
}

// stubContractOwner replaces the readContractOwner seam for the duration of the test. Tests
// using it must not run in parallel (they mutate a package-level var).
func stubContractOwner(t *testing.T, owner string, readErr error) {
	t.Helper()
	orig := readContractOwner
	t.Cleanup(func() { readContractOwner = orig })
	readContractOwner = func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
		return owner, readErr
	}
}

func TestWithdrawFeeTokensWrite_EncodesProposalPayload(t *testing.T) {
	t.Parallel()
	sel := uint64(424242420044)
	id := stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-onramp-propose")
	tokens := []string{
		stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-token-a"),
		keypair.MustRandom().Address(),
	}

	wo, err := withdrawFeeTokensWrite(sel, stellarccip.OnRampDatastoreRef().Type, id, tokens)
	require.NoError(t, err)
	require.Equal(t, sel, wo.ChainSelector)
	require.Nil(t, wo.ExecInfo, "proposal write must be unexecuted")
	require.Equal(t, id, wo.Tx.To)
	require.Equal(t, string(stellarccip.OnRampDatastoreRef().Type), wo.Tx.OperationMetadata.ContractType)
	require.JSONEq(t, `{"version":1,"family":"stellar"}`, string(wo.Tx.AdditionalFields))

	fn, args := decodeWithdrawPayload(t, wo.Tx.Data)
	require.Equal(t, "withdraw_fee_tokens", fn)
	require.Len(t, args, 1)
	require.Equal(t, xdr.ScValTypeScvVec, args[0].Type)
	vec := *args[0].MustVec()
	require.Len(t, vec, len(tokens))
	for i, tok := range tokens {
		addr, err := scval.AddressFromScVal(vec[i])
		require.NoError(t, err)
		require.Equal(t, tok, addr)
	}
}

func TestApplyStellarWithdrawFeeTokens_ProposesWhenDeployerIsNotOwner(t *testing.T) {
	// No t.Parallel: mutates the readContractOwner seam.
	b := newTestBundle(t)
	sel := uint64(424242420045)
	ds := datastore.NewMemoryDataStore()
	ids := map[string]string{
		"OnRamp":            stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-onramp"),
		"VersionedVerifier": stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-vvr"),
		"CommitteeVerifier": stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-cv"),
	}
	require.NoError(t, stellarccip.RecordOnRamp(ds, sel, ids["OnRamp"]))
	require.NoError(t, stellarccip.RecordVVR(ds, sel, ids["VersionedVerifier"]))
	require.NoError(t, stellarccip.RecordCommitteeVerifier(ds, sel, ids["CommitteeVerifier"]))
	env := cldf.Environment{DataStore: ds.Seal()}

	// An owner that is not the deployer: every swept contract routes to the proposal arm, which
	// performs no on-chain call, so a nil-client chain is fine here.
	stubContractOwner(t, keypair.MustRandom().Address(), nil)

	tokens := withdrawTestTokens()
	out, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     tokens,
	})
	require.NoError(t, err)
	require.Len(t, out.BatchOps, 3, "one proposal batch op per swept contract")

	want := map[string]string{ // contract type -> recorded strkey
		string(stellarccip.OnRampDatastoreRef().Type):            ids["OnRamp"],
		string(stellarccip.VVRDatastoreRef().Type):               ids["VersionedVerifier"],
		string(stellarccip.CommitteeVerifierDatastoreRef().Type): ids["CommitteeVerifier"],
	}
	seen := map[string]struct{}{}
	for _, bo := range out.BatchOps {
		require.Len(t, bo.Transactions, 1)
		tx := bo.Transactions[0]
		require.Contains(t, want, tx.OperationMetadata.ContractType)
		require.Equal(t, want[tx.OperationMetadata.ContractType], tx.To)
		seen[tx.OperationMetadata.ContractType] = struct{}{}

		fn, args := decodeWithdrawPayload(t, tx.Data)
		require.Equal(t, "withdraw_fee_tokens", fn)
		require.Len(t, args, 1)
		require.Equal(t, xdr.ScValTypeScvVec, args[0].Type)
		vec := *args[0].MustVec()
		require.Len(t, vec, len(tokens))
		for i, tok := range tokens {
			addr, err := scval.AddressFromScVal(vec[i])
			require.NoError(t, err)
			require.Equal(t, tok.Token, addr)
		}
	}
	require.Len(t, seen, 3, "each fee-holding contract type proposed exactly once")
}

func TestApplyStellarWithdrawFeeTokens_FailsClosedOnOwnerReadError(t *testing.T) {
	// No t.Parallel: mutates the readContractOwner seam.
	b := newTestBundle(t)
	sel := uint64(424242420046)
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.RecordOnRamp(ds, sel, stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-onramp")))
	env := cldf.Environment{DataStore: ds.Seal()}

	stubContractOwner(t, "", errors.New("owner read boom"))

	_, err := ApplyStellarWithdrawFeeTokens(b, withdrawTestChains(sel), env, fees.WithdrawFeeTokensForChain{
		ChainSelector: sel,
		FeeTokens:     withdrawTestTokens(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "read owner of")
}
