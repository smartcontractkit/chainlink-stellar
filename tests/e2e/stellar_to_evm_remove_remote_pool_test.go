package e2e_tests

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/token_pool"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi/token_transfer"
	ccvchangesets "github.com/smartcontractkit/chainlink-ccv/deployment/changesets"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellaradapters "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
)

// TestStellarToEVMTCAPIBnmTokenTransferRemoveRemotePool mirrors
// TestEVMToStellarTCAPIBnmTokenTransferRemoveRemotePool on the opposite lane,
// proving the EVM side of the chain-agnostic RemoveRemotePool changeset end
// to end with real transfers on the stellar→EVM lane (the EVM pool is the
// destination; removing the stellar pool from its remote list blocks the
// release gate):
//
//  1. a Stellar→EVM transfer on the symmetric pair succeeds;
//  2. the stellar adapter's ResolveRemotePoolAddress returns the exact padded
//     bytes the EVM pool stores for the stellar pool;
//  3. the changeset removes the stellar pool from the EVM pool's remote list
//     (the EVM release gate authorizes Stellar→EVM); the entry is restored
//     on cleanup;
//  4. the same transfer now fails — positively: unlike the stellar OffRamp
//     (whose pool failures revert the whole execute transaction with no
//     event), the EVM OffRamp catches pool failures and records a retryable
//     FAILURE event whose ReturnData carries TokenHandlingError wrapping the
//     pool's InvalidSourcePoolAddress revert.
func TestStellarToEVMTCAPIBnmTokenTransferRemoveRemotePool(t *testing.T) {
	env, ctx, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM
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

	// Family-native token strings for the adapter call and changeset input.
	srcToken, dstToken := removeRemotePoolTokens(t, lib, src, dst, combo)

	// 2. The stellar adapter must resolve the exact bytes the EVM pool stores
	//    for the stellar pool (padded form).
	adapter := &stellaradapters.StellarCCVTokenPoolOnchainAdapter{}
	resolved, err := adapter.ResolveRemotePoolAddress(*env.CLDFEnv, src, srcToken)
	require.NoError(t, err)
	require.Len(t, resolved, 32)
	evmChain := env.CLDFEnv.BlockChains.EVMChains()[dst]
	evmPoolHex, err := resolveComboPoolAddress(lib, dst, combo.RemotePoolAddressRef())
	require.NoError(t, err)
	evmPoolAddr := common.HexToAddress(evmPoolHex)
	read, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.GetRemotePools, evmChain,
		evmcontract.FunctionInput[uint64]{Address: evmPoolAddr, ChainSelector: dst, Args: src})
	require.NoError(t, err)
	padded := common.LeftPadBytes(resolved, 32)
	require.True(t, slices.ContainsFunc(read.Output, func(p []byte) bool { return bytes.Equal(p, padded) }),
		"the EVM pool must store the resolved stellar pool bytes")

	// 3. Remove the stellar pool from the EVM pool via the changeset (the
	//    changeset resolves the remote pool from the stellar token through the
	//    stellar adapter, then dispatches to the EVM adapter).
	_, err = ccvchangesets.RemoveRemotePool().Apply(*env.CLDFEnv, ccvchangesets.RemoveRemotePoolInput{
		ChainSelector:       dst,
		TokenAddress:        dstToken,
		RemoteChainSelector: src,
		RemoteTokenAddress:  srcToken,
	})
	require.NoError(t, err, "RemoveRemotePool should succeed")

	// Restore the remote pool entry so the lane keeps working locally.
	t.Cleanup(func() {
		if _, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.AddRemotePool, evmChain,
			evmcontract.FunctionInput[evmtokenpool.AddRemotePoolArgs]{
				Address:       evmPoolAddr,
				ChainSelector: dst,
				Args:          evmtokenpool.AddRemotePoolArgs{RemoteChainSelector: src, RemotePoolAddress: padded},
			}); err != nil {
			t.Logf("restore: add remote pool: %v", err)
		}
	})

	// 4. The same transfer now fails at the EVM pool's release gate. The run
	//    must get all the way to the destination execution (a failure at the
	//    send/confirm/observation stage would prove nothing about the removal),
	//    and the recorded FAILURE's ReturnData must decode to the EVM OffRamp's
	//    TokenHandlingError wrapping the pool's InvalidSourcePoolAddress
	//    revert — the exact rejection, not just any error.
	post := token_transfer.TokenTransfer(lib, src, dst, combo, 0, true, "remove-remote-pool after", args)
	postRes, postErr := post.Run(ctx)
	require.Error(t, postErr, "transfer should fail after the remote pool is removed")
	require.Equal(t, cciptestinterfaces.ExecutionStateFailure, postRes.Dest.Event.State,
		"the destination must record a FAILURE (a send/observation-stage error would not prove the removal); got run error: %v", postErr)

	poolRevert, err := decodeEVMTokenHandlingError(postRes.Dest.Event.ReturnData)
	require.NoError(t, err,
		"the FAILURE return data must be the EVM OffRamp's TokenHandlingError wrapping the pool revert; got run error: %v", postErr)
	poolABI, err := abi.JSON(strings.NewReader(evmtokenpool.TokenPoolABI))
	require.NoError(t, err)
	invalidSourcePool, ok := poolABI.Errors["InvalidSourcePoolAddress"]
	require.True(t, ok, "InvalidSourcePoolAddress must be in the TokenPool ABI")
	require.GreaterOrEqual(t, len(poolRevert), 4)
	require.True(t, bytes.Equal(poolRevert[:4], invalidSourcePool.ID[:4]),
		"the wrapped pool revert must be the release gate's InvalidSourcePoolAddress, got: 0x%x", poolRevert)
}

// decodeEVMTokenHandlingError unpacks the EVM OffRamp's
// TokenHandlingError(address, bytes) from an ExecutionStateChanged FAILURE's
// return data (the EVM OffRamp wraps pool releaseOrMint reverts in it —
// OffRamp.sol `_releaseOrMintSingleToken` try/catch) and returns the wrapped
// pool revert bytes.
func decodeEVMTokenHandlingError(returnData []byte) ([]byte, error) {
	offRampABI, err := abi.JSON(strings.NewReader(offrampoperations.OffRampABI))
	if err != nil {
		return nil, fmt.Errorf("parse OffRamp ABI: %w", err)
	}
	tokenErr, ok := offRampABI.Errors["TokenHandlingError"]
	if !ok {
		return nil, fmt.Errorf("TokenHandlingError not found in OffRamp ABI")
	}
	if len(returnData) < 4 || !bytes.Equal(returnData[:4], tokenErr.ID[:4]) {
		return nil, fmt.Errorf("return data 0x%x is not TokenHandlingError", returnData)
	}
	vals, err := tokenErr.Inputs.Unpack(returnData[4:])
	if err != nil {
		return nil, fmt.Errorf("unpack TokenHandlingError: %w", err)
	}
	if len(vals) != 2 {
		return nil, fmt.Errorf("TokenHandlingError unpacked %d values, want 2", len(vals))
	}
	poolRevert, ok := vals[1].([]byte)
	if !ok {
		return nil, fmt.Errorf("TokenHandlingError err field is %T, want []byte", vals[1])
	}
	return poolRevert, nil
}
