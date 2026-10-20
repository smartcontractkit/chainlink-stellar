package e2e_tests

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/token_pool"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi/token_transfer"
	ccvchangesets "github.com/smartcontractkit/chainlink-ccv/deployment/changesets"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellaradapters "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
)

// TestEVMToStellarTCAPIBnmTokenTransferRemoveRemotePool proves the stellar
// TokenPoolOnchainAdapter end to end, driving the chain-agnostic
// RemoveRemotePool changeset with real transfers:
//
//  1. an EVM→Stellar transfer on the symmetric pair succeeds;
//  2. ResolveRemotePoolAddress returns the exact padded bytes the EVM pool
//     stores for the stellar pool;
//  3. the changeset removes the EVM pool from the stellar pool's remote
//     list (the stellar release gate authorizes EVM→Stellar);
//  4. the same transfer now fails; the entry is restored on cleanup.
func TestEVMToStellarTCAPIBnmTokenTransferRemoveRemotePool(t *testing.T) {
	env, ctx, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.DestChainDetails.ChainSelector   // EVM
	dst := env.SourceChainDetails.ChainSelector // Stellar
	combo, found := firstIs17Combination(t, lib, configOutputPath, src, dst)
	if !found {
		t.Skip("no 2.0.0×2.0.0 pair combo in this environment")
	}
	args := bnmTokenTransferArgs(env, src)

	// 1. The transfer works before the removal.
	pre := token_transfer.TokenTransfer(lib, src, dst, combo, 0, true, "remove-remote-pool before", args)
	if !pre.HavePrerequisites(ctx) {
		t.Skipf("Skipping %s: prerequisites not met in this environment", pre.Name())
	}
	if _, err := pre.Run(ctx); err != nil {
		require.NoError(t, err, "transfer should succeed before the remote pool is removed")
	}

	// Family-native token strings for the changeset input.
	srcToken, dstToken := removeRemotePoolTokens(t, lib, src, dst, combo)

	// 2. ResolveRemotePoolAddress must return the exact bytes the EVM pool
	// stores for the stellar pool (padded form).
	adapter := &stellaradapters.StellarCCVTokenPoolOnchainAdapter{}
	resolved, err := adapter.ResolveRemotePoolAddress(*env.CLDFEnv, dst, dstToken)
	require.NoError(t, err)
	require.Len(t, resolved, 32)
	evmChain := env.CLDFEnv.BlockChains.EVMChains()[src]
	evmPoolHex, err := resolveComboPoolAddress(lib, src, combo.LocalPoolAddressRef())
	require.NoError(t, err)
	evmPoolAddr := common.HexToAddress(evmPoolHex)
	read, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.GetRemotePools, evmChain,
		evmcontract.FunctionInput[uint64]{Address: evmPoolAddr, ChainSelector: src, Args: dst})
	require.NoError(t, err)
	padded := common.LeftPadBytes(resolved, 32)
	require.True(t, slices.ContainsFunc(read.Output, func(p []byte) bool { return bytes.Equal(p, padded) }),
		"the EVM pool must store the resolved stellar pool bytes")

	// 3. Remove the EVM pool from the stellar pool via the changeset.
	_, err = ccvchangesets.RemoveRemotePool().Apply(*env.CLDFEnv, ccvchangesets.RemoveRemotePoolInput{
		ChainSelector:       dst,
		TokenAddress:        dstToken,
		RemoteChainSelector: src,
		RemoteTokenAddress:  srcToken,
	})
	require.NoError(t, err, "RemoveRemotePool should succeed")

	// Restore the remote pool entry so the lane keeps working locally.
	t.Cleanup(func() {
		stellarPoolHex, err := resolveComboPoolAddress(lib, dst, combo.RemotePoolAddressRef())
		if err != nil {
			t.Logf("restore: resolve pool address: %v", err)
			return
		}
		poolStrkey, err := scval.HexToContractStrkey(stellarPoolHex)
		if err != nil {
			t.Logf("restore: convert pool address: %v", err)
			return
		}
		poolClient := tokenpoolbindings.NewTokenPoolClient(env.Deployer, poolStrkey)
		if err := poolClient.AddRemotePool(context.Background(), src, common.LeftPadBytes(evmPoolAddr.Bytes(), 32)); err != nil {
			t.Logf("restore: add remote pool: %v", err)
		}
	})

	// 4. The same transfer now fails at the stellar pool's release gate —
	//    proven positively, not by "some error happened": the source send
	//    still confirms (removal blocks only execution), and a direct
	//    execute of the verified message reverts with the pool's
	//    InvalidSourcePoolAddress (319). Pool failures revert the entire
	//    execute transaction without an execution event, so the relayer's
	//    exec-event wait timing out would be indistinguishable from a
	//    source-side failure — the manual execute pins the exact rejection.
	_, srcTokenAddr, err := rateLimitSource(ctx, env, lib, src, combo)
	require.NoError(t, err)
	receiver, err := env.Chains[dst].GetEOAReceiverAddress()
	require.NoError(t, err)
	evmSrc := env.Chains[src]
	sentEvent, err := evmSrc.SendMessage(ctx, dst,
		cciptestinterfaces.MessageFields{
			Receiver:    receiver,
			TokenAmount: cciptestinterfaces.TokenAmount{Amount: args.TransferAmount, TokenAddress: srcTokenAddr},
		},
		cciptestinterfaces.MessageOptions{}, messageV3Version)
	require.NoError(t, err, "the EVM send itself must succeed after removal")
	_, err = evmSrc.ConfirmSendOnSource(ctx, dst,
		cciptestinterfaces.MessageEventKey{MessageID: sentEvent.MessageID}, evmSentTimeout)
	require.NoError(t, err, "the source send must confirm after removal (only execution is blocked)")

	// Wait for verification + indexing to assemble the manual-execute
	// arguments, exactly as the permissionless-exec test and the `manualexec`
	// CLI do (each verification contributes its CCV address and blob).
	defaultAggregatorClient := env.AggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
	require.NotNil(t, defaultAggregatorClient)
	testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, defaultAggregatorClient, env.IndexerMonitor)
	result, err := testCtx.AssertMessage(protocol.Bytes32(sentEvent.MessageID), e2e.AssertMessageOptions{
		TickInterval:            1 * time.Second,
		ExpectedVerifierResults: combo.ExpectedVerifierResults(),
		Timeout:                 tests.WaitTimeout(t),
		AssertVerifierLogs:      false,
		AssertExecutorLogs:      false,
	})
	require.NoError(t, err, "the message must still verify and index after removal (only execution is blocked)")
	verifs := result.IndexedVerifications.Results
	require.NotEmpty(t, verifs, "need ≥1 indexed verification to assemble manual-execute arguments")
	ccvs := make([]protocol.UnknownAddress, 0, len(verifs))
	verifierResults := make([][]byte, 0, len(verifs))
	for _, v := range verifs {
		ccvs = append(ccvs, v.VerifierResult.VerifierDestAddress)
		verifierResults = append(verifierResults, v.VerifierResult.CCVData)
	}

	// Direct execute of the verified message: the release gate rejects the
	// removed source pool, reverting the whole execute transaction with the
	// pool's InvalidSourcePoolAddress (319) contract error.
	stellarChain := env.Chains[dst]
	_, execErr := stellarChain.ManuallyExecuteMessage(ctx, verifs[0].VerifierResult.Message, 0, ccvs, verifierResults)
	require.Error(t, execErr, "execute must fail after the remote pool is removed")
	require.True(t, stellarops.IsContractErrorCode(execErr, tokenpoolbindings.CCIPErrorInvalidSourcePoolAddress),
		"execute must fail at the pool's release gate with InvalidSourcePoolAddress (319), got: %v", execErr)
}

// removeRemotePoolTokens resolves the combo's src/dest tokens as the chains'
// family-native address strings (EVM hex, stellar strkey), the forms the
// RemoveRemotePool changeset input takes.
func removeRemotePoolTokens(t *testing.T, lib ccv.Lib, src, dst uint64, combo devenvcommon.TokenCombination) (string, string) {
	t.Helper()
	ds, err := lib.DataStore()
	require.NoError(t, err)
	resolve := func(sel uint64, poolRef datastore.AddressRef) string {
		family, err := chainsel.GetSelectorFamily(sel)
		require.NoError(t, err)
		reg, err := chainreg.GetRegistry().Get(family)
		require.NoError(t, err)
		token, err := reg.AddressResolver.GetToken(ds, sel, poolRef)
		require.NoError(t, err)
		switch family {
		case chainsel.FamilyEVM:
			return common.BytesToAddress(token).Hex()
		default:
			raw, err := strkey.Encode(strkey.VersionByteContract, token)
			require.NoError(t, err)
			return raw
		}
	}
	return resolve(src, combo.LocalPoolAddressRef()), resolve(dst, combo.RemotePoolAddressRef())
}
