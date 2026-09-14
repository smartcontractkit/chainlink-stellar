package e2e_tests

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	lrpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/lock_release_pool"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	lrpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

const (
	// rateLimitTestOutboundCapacity is deliberately below tokenTransferAmount so the next
	// Stellar→EVM send fails once this outbound bucket is active (phase 5).
	rateLimitTestOutboundCapacity uint64 = 100_000
)

// TestStellarToEVMTokenTransferRateLimitViaMCMS exercises governance-controlled rate limiting
// on the Stellar lock-release token pool during Stellar→EVM token transfers.
//
// Phase 1: baseline token transfer succeeds (no rate limit).
// Phase 2: deploy MCMS + timelock wired for MCMS-mediated governance.
// Phase 3: transfer pool ownership to timelock; MCMS signs accept_ownership via timelock.
// Phase 4: configure a small outbound rate limit on the pool via MCMS-signed timelock op.
// Phase 5: Stellar→EVM token transfer is rejected because transfer amount exceeds outbound capacity.
//
// Prerequisites:
//
//	make build
//	make up
//
// Run:
//
//	go test -v -timeout 15m ./tests/e2e/... -run TestStellarToEVMTokenTransferRateLimitViaMCMS
func TestStellarToEVMTokenTransferRateLimitViaMCMS(t *testing.T) {
	configOutputPath := "../env/env-stellar-evm-out.toml"

	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	stellarSelector := chainsel.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID, stellarSelector)
	stellarDetails := env.SourceChainDetails
	evmDetails := env.DestChainDetails

	stellarChain := env.Chains[stellarDetails.ChainSelector]
	require.NotNil(t, stellarChain, "Stellar chain not found")

	evmChain := env.Chains[evmDetails.ChainSelector]
	require.NotNil(t, evmChain, "EVM chain not found")

	stellarCcvChain, ok := stellarChain.(*ccvchain.Chain)
	require.True(t, ok, "Stellar chain must be *ccvchain.Chain")

	tokenAddr, err := stellarCcvChain.GetTokenAddress()
	require.NoError(t, err, "test token must be deployed")
	l.Info().Str("tokenAddress", tokenAddr).Msg("Using test SAC token")

	tokenRaw, err := strkey.Decode(strkey.VersionByteContract, tokenAddr)
	require.NoError(t, err)

	poolRef, err := env.DataStore.Addresses().Get(
		stellarccip.SiloedLockReleasePoolDevenvDatastoreRef().AddressRefKey(stellarDetails.ChainSelector),
	)
	require.NoError(t, err)
	require.NotEmpty(t, poolRef.Address)
	poolContractID, err := scval.HexToContractStrkey(poolRef.Address)
	require.NoError(t, err)
	poolClient := lrpbindings.NewLockReleasePoolClient(env.Deployer, poolContractID)
	l.Info().Str("poolContractID", poolContractID).Msg("Using lock-release token pool")

	senderAddr, err := stellarChain.GetSenderAddress()
	require.NoError(t, err)

	evmReceiver, err := evmChain.GetEOAReceiverAddress()
	require.NoError(t, err)

	opsBundle := cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
	stellarDeps := stellardeps.FromDeployer(env.Deployer)

	var gov *helpers.MCMSGovernanceStack
	var timelockPredecessor [32]byte

	t.Cleanup(func() {
		var saltTransfer [32]byte
		saltTransfer[31] = 3
		helpers.CleanupMCMSTestPool(
			t, context.Background(), env, gov,
			poolContractID, evmDetails.ChainSelector,
			env.DeployerKP.Address(),
			timelockPredecessor, saltTransfer,
		)
	})

	t.Run("phase1_baseline_transfer_succeeds", func(t *testing.T) {
		balBefore, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)
		l.Info().Str("balance", balBefore.String()).Msg("Sender token balance before baseline transfer")
		require.True(t, balBefore.Int64() >= tokenTransferAmount,
			"sender must have enough tokens; balance=%s, need=%d", balBefore, tokenTransferAmount)

		seqNo, err := stellarChain.GetExpectedNextSequenceNumber(ctx, evmDetails.ChainSelector)
		require.NoError(t, err)

		sendResult, err := stellarChain.SendMessage(ctx, evmDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: evmReceiver,
				Data:     []byte("rate-limit-mcms-baseline"),
				TokenAmount: cciptestinterfaces.TokenAmount{
					Amount:       big.NewInt(tokenTransferAmount),
					TokenAddress: protocol.UnknownAddress(tokenRaw),
				},
			},
			cciptestinterfaces.MessageOptions{},
			messageV3Version,
		)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sendResult.MessageID[:])).
			Msg("Baseline token transfer sent from Stellar")

		sentEvent, err := stellarChain.ConfirmSendOnSource(ctx, evmDetails.ChainSelector,
			cciptestinterfaces.MessageEventKey{SeqNum: seqNo}, tokenTransferSentTimeout)
		require.NoError(t, err)
		l.Info().
			Str("messageID", hex.EncodeToString(sentEvent.MessageID[:])).
			Msg("Baseline sent event confirmed")

		balAfter, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)

		locked := new(big.Int).Sub(balBefore, balAfter)
		require.Equal(t, tokenTransferAmount, locked.Int64(),
			"sender balance should decrease by exactly the transfer amount")

		l.Info().Int64("lockedAmount", locked.Int64()).Msg("Phase 1 complete: baseline transfer locked tokens on source")
	})

	t.Run("phase2_deploy_mcms_and_timelock", func(t *testing.T) {
		gov = helpers.DeployMCMSAndTimelock(t, ctx, env, stellarDetails.ChainSelector, "e2e-rate-limit-mcms")
		l.Info().
			Str("mcmsID", gov.MCMSID).
			Str("timelockID", gov.TimelockID).
			Uint64("minDelaySec", gov.MinDelaySec).
			Msg("MCMS and timelock deployed")

		owner, err := poolClient.Owner(ctx)
		require.NoError(t, err)
		require.NotNil(t, owner)
		require.Equal(t, env.DeployerKP.Address(), *owner,
			"pool should still be owned by deployer before governance transfer")

		state, err := poolClient.GetCurrentRateLimiterState(ctx, evmDetails.ChainSelector, false)
		require.NoError(t, err)
		require.False(t, state.Outbound.IsEnabled, "outbound rate limit should start disabled")

		l.Info().Msg("Phase 2 complete: MCMS + timelock ready for ownership transfer and rate-limit config")
	})

	t.Run("phase3_transfer_pool_ownership_to_timelock", func(t *testing.T) {
		require.NotNil(t, gov, "phase 2 must deploy MCMS and timelock first")

		_, err := cldfops.ExecuteOperation(opsBundle, lrpops.TransferOwnership, stellarDeps, lrpops.TransferOwnershipInput{
			ContractID: poolContractID,
			NewOwner:   gov.TimelockID,
		})
		require.NoError(t, err)

		pending, err := poolClient.GetPendingOwner(ctx)
		require.NoError(t, err)
		require.NotNil(t, pending)
		require.Equal(t, gov.TimelockID, *pending)

		acceptArgs, err := helpers.EncodeTimelockCallArgs(nil)
		require.NoError(t, err)

		var saltAccept [32]byte
		saltAccept[31] = 1
		callsAccept := timelockbindings.Calls{
			Inner: []timelockbindings.Call{
				{Target: poolContractID, Function: "accept_ownership", ArgsXdr: acceptArgs},
			},
		}

		helpers.MCMSTimelockScheduleAndExecute(t, ctx, env, gov, callsAccept, timelockPredecessor, saltAccept)

		owner, err := poolClient.Owner(ctx)
		require.NoError(t, err)
		require.NotNil(t, owner)
		require.Equal(t, gov.TimelockID, *owner, "timelock should own the pool after MCMS-gated accept_ownership")

		l.Info().Str("timelockID", gov.TimelockID).Msg("Phase 3 complete: pool ownership transferred to timelock via MCMS")
	})

	t.Run("phase4_configure_outbound_rate_limit_via_mcms", func(t *testing.T) {
		require.NotNil(t, gov, "phase 2 must deploy MCMS and timelock first")

		outbound := lrpbindings.RateLimitConfig{
			IsEnabled: true,
			Capacity:  scval.U128(xdr.UInt128Parts{Hi: 0, Lo: xdr.Uint64(rateLimitTestOutboundCapacity)}),
			Rate:      scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 1}),
		}
		inbound := lrpbindings.RateLimitConfig{}

		rateLimitArgs, err := helpers.EncodeTimelockCallArgs([]xdr.ScVal{
			scval.Uint64ToScVal(evmDetails.ChainSelector),
			scval.MustToScVal(outbound.ToScVal()),
			scval.MustToScVal(inbound.ToScVal()),
			scval.BoolToScVal(false),
		})
		require.NoError(t, err)

		var saltRateLimit [32]byte
		saltRateLimit[31] = 2
		callsRateLimit := timelockbindings.Calls{
			Inner: []timelockbindings.Call{
				{Target: poolContractID, Function: "set_rate_limit_config", ArgsXdr: rateLimitArgs},
			},
		}

		helpers.MCMSTimelockScheduleAndExecute(t, ctx, env, gov, callsRateLimit, timelockPredecessor, saltRateLimit)

		state, err := poolClient.GetCurrentRateLimiterState(ctx, evmDetails.ChainSelector, false)
		require.NoError(t, err)
		require.True(t, state.Outbound.IsEnabled, "outbound rate limit should be enabled after MCMS config")
		require.Equal(t, xdr.Uint64(rateLimitTestOutboundCapacity), state.Outbound.Capacity.Lo,
			"outbound capacity should match MCMS-configured limit")

		l.Info().
			Uint64("capacity", rateLimitTestOutboundCapacity).
			Msg("Phase 4 complete: small outbound rate limit configured via MCMS + timelock")
	})

	t.Run("phase5_rate_limited_transfer_rejected", func(t *testing.T) {
		require.NotNil(t, gov, "phase 2 must deploy MCMS and timelock first")
		require.Greater(t, tokenTransferAmount, int64(rateLimitTestOutboundCapacity),
			"test transfer amount must exceed configured outbound capacity")

		balBefore, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)
		l.Info().Str("balance", balBefore.String()).Msg("Sender token balance before rate-limited transfer attempt")

		_, sendErr := stellarChain.SendMessage(ctx, evmDetails.ChainSelector,
			cciptestinterfaces.MessageFields{
				Receiver: evmReceiver,
				Data:     []byte("rate-limit-mcms-blocked"),
				TokenAmount: cciptestinterfaces.TokenAmount{
					Amount:       big.NewInt(tokenTransferAmount),
					TokenAddress: protocol.UnknownAddress(tokenRaw),
				},
			},
			cciptestinterfaces.MessageOptions{},
			messageV3Version,
		)
		require.Error(t, sendErr, "transfer exceeding outbound capacity should fail at lock_or_burn")
		require.True(t, isRateLimitSendError(sendErr),
			"expected rate limit error (311 or 312), got: %v", sendErr)
		l.Info().Err(sendErr).Msg("Transfer rejected as expected due to outbound rate limit")

		balAfter, err := stellarChain.GetTokenBalance(ctx, senderAddr, protocol.UnknownAddress(tokenRaw))
		require.NoError(t, err)
		require.Equal(t, 0, balBefore.Cmp(balAfter),
			"sender balance should be unchanged after rejected transfer; before=%s after=%s",
			balBefore, balAfter)

		l.Info().Msg("Phase 5 complete: rate-limited transfer blocked on source")
	})
}

// isRateLimitSendError reports whether err is a pool outbound rate limit rejection surfaced
// during ccip_send (lock_or_burn). Amount > capacity yields TokenMaxCapacityExceeded (311);
// amount <= capacity with insufficient tokens yields TokenRateLimitReached (312).
func isRateLimitSendError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, fmt.Sprintf("#%d", lrpbindings.CCIPErrorTokenMaxCapacityExceeded)) ||
		strings.Contains(msg, fmt.Sprintf("#%d", lrpbindings.CCIPErrorTokenRateLimitReached)) ||
		strings.Contains(msg, "TokenMaxCapacityExceeded") ||
		strings.Contains(msg, "TokenRateLimitReached")
}
