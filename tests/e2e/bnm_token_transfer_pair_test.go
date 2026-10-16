package e2e_tests

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/token_pool"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi/token_transfer"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// bnmTokenTransferCases builds every BnM token-transfer tcapi case for one
// direction: the pair-matrix suite (All: EOA + combo finality; All17:
// 2.0.0 pairs × EOA/mock receiver) plus the rate-limit exhaustion error
// path. Cases gate themselves per-environment via HavePrerequisites.
func bnmTokenTransferCases(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) []tcapi.TestCase {
	srcIsStellar := src == env.SourceChainDetails.ChainSelector
	transferAmount, destIncrease := big.NewInt(1000), big.NewInt(0)
	if srcIsStellar {
		destIncrease = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(11), nil)) // ×10^11 up-conversion
	} else {
		transferAmount = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
		destIncrease = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(7), nil)) // ÷10^11 down-conversion
	}
	// One Args per direction: every stellar BnM token is 7-dec and every EVM
	// burn-mint token 18-dec, so the conversion is identical for every combo.
	args := token_transfer.Args{
		Run:                 bnmTokenTransferRun(),
		TransferAmount:      transferAmount,
		DestBalanceIncrease: destIncrease,
	}
	combos := bnmCombinations(t, lib, configOutputPath, src, dst)
	cases := token_transfer.All(lib, src, dst, combos, args)
	cases = append(cases, token_transfer.All17(lib, src, dst, combos, args)...)
	cases = append(cases, bnmRateLimitCase(t, env, lib, configOutputPath, src, dst))
	return cases
}

// bnmCombinations builds the full tcapi combo list for one direction: every
// env-declared token_pool_pair whose local/remote selectors match (the pairs
// the shared pipeline deployed and wired — symmetric 2.0.0 and mixed
// 1.6.1↔2.0.0), resolved against the datastore.
func bnmCombinations(t *testing.T, lib ccv.Lib, configOutputPath string, localSel, remoteSel uint64) []devenvcommon.TokenCombination {
	in, err := ccv.LoadOutput[ccv.Cfg](configOutputPath)
	require.NoError(t, err)
	local, remote := strconv.FormatUint(localSel, 10), strconv.FormatUint(remoteSel, 10)
	var pairs []devenvcommon.TokenPoolPair
	for _, p := range in.TokenPoolPairs {
		if p.LocalSelector == local && p.RemoteSelector == remote {
			pairs = append(pairs, p)
		}
	}
	ds, err := lib.DataStore()
	require.NoError(t, err)
	combos, err := devenvcommon.TokenCombinationsFromPoolPairs(ds, pairs)
	require.NoError(t, err)
	return combos
}

// bnmTokenTransferRun is the shared run config for the BnM suite cases.
func bnmTokenTransferRun() tcapi.RunConfig {
	return tcapi.RunConfig{
		ConfirmSentTimeout: 30 * time.Second,
		ConfirmExecTimeout: 7 * time.Minute,
	}
}

// seedStellarReceiverPolicy enables the stellar example receiver for the EVM
// source chain with the {WaitForSafe, depth 1} finality policy the EVM mock
// receivers get; the stellar OffRamp enforces the receiver policy at execute
// time, so non-default-finality deliveries to the contract fail without it.
// The deployed default (WAIT_FOR_FINALITY only) is restored on cleanup.
func seedStellarReceiverPolicy(t *testing.T, env *helpers.E2ETestEnv, l *zerolog.Logger, src, dst uint64) {
	ctx := context.Background()
	stellarChain, ok := env.Chains[dst].(*ccvchain.Chain)
	require.True(t, ok, "stellar chain must be *ccvchain.Chain to access the receiver contract id")
	receiverID, err := stellarChain.GetReceiverContractAddress()
	require.NoError(t, err)
	recvClient := recvbindings.NewExampleCcipReceiverClient(env.Deployer, receiverID)
	ownerAddr := env.Deployer.SignerAddress()
	// enable_remote_chain rejects empty extra args; [0x01] is the placeholder
	// the deploy sequence and the FTF e2e use.
	placeholderExtra := []byte{0x01}
	allowed := finality.Config{BlockDepth: 1, WaitForSafe: true}.Raw()
	require.NoError(t, recvClient.EnableRemoteChain(ctx, ownerAddr, src, placeholderExtra,
		binary.BigEndian.Uint32(allowed[:])))
	// Restore the default so the permissive policy does not leak into other
	// suites sharing the devenv (the FTF e2e negative control asserts 315).
	t.Cleanup(func() {
		if err := recvClient.EnableRemoteChain(ctx, ownerAddr, src, placeholderExtra, 0); err != nil {
			l.Warn().Err(err).Msg("failed to reset receiver allowed_finality_config in cleanup")
		}
	})
}

// bnmTestCase adapts a named run func to the tcapi.TestCase shape so
// repo-local cases loop exactly like the upstream suite cases.
type bnmTestCase struct {
	name    string
	run     func(ctx context.Context) (tcapi.RunResult, error)
	prereqs func(ctx context.Context) bool
	// serial marks cases that must not run concurrently with other cases
	// (they mutate shared pool config mid-run, e.g. the rate-limit case).
	serial bool
}

func (tc bnmTestCase) Name() string { return tc.name }

func (tc bnmTestCase) HavePrerequisites(ctx context.Context) bool {
	if tc.prereqs == nil {
		return true
	}
	return tc.prereqs(ctx)
}

func (tc bnmTestCase) Run(ctx context.Context) (tcapi.RunResult, error) {
	return tc.run(ctx)
}

// Serial implements serialTestCase.
func (tc bnmTestCase) Serial() bool { return tc.serial }

// serialTestCase is implemented by cases that must not run concurrently with
// other cases; the suite loop keeps them sequential (t.Parallel for the rest).
type serialTestCase interface{ Serial() bool }

// bnmRateLimitCase returns the rate-limit-exhaustion case for one direction:
// a capacity-1/rate-0 outbound bucket on the source pool for the remote
// rejects the send at the pool's rate-limit gate with no tokens locked; the
// disabled config is restored on test cleanup.
func bnmRateLimitCase(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) tcapi.TestCase {
	srcIsStellar := src == env.SourceChainDetails.ChainSelector
	var combo devenvcommon.TokenCombination
	found := false
	for _, c := range bnmCombinations(t, lib, configOutputPath, src, dst) {
		if devenvcommon.Is17Combination(c) {
			combo, found = c, true
			break
		}
	}
	return bnmTestCase{
		name:    "token transfer rate limit exceeded",
		serial:  true,
		prereqs: func(context.Context) bool { return found },
		run: func(ctx context.Context) (tcapi.RunResult, error) {
			if !found {
				return tcapi.RunResult{}, fmt.Errorf("no 2.0.0×2.0.0 pair combo in this environment")
			}
			return tcapi.RunResult{}, runRateLimitExhaustion(ctx, t, env, lib, src, dst, srcIsStellar, combo)
		},
	}
}

// runRateLimitExhaustion sets the tight outbound bucket, sends, asserts the
// rejection (stellar: contract error 312 = TokenRateLimitReached; EVM: the
// send error), and asserts the sender balance is unchanged (nothing locked).
func runRateLimitExhaustion(ctx context.Context, t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, src, dst uint64, srcIsStellar bool, combo devenvcommon.TokenCombination) error {
	l := zerolog.Ctx(ctx)
	sender, srcToken, err := rateLimitSource(ctx, env, lib, src, combo)
	if err != nil {
		return err
	}
	balReader, ok := env.Chains[src].(cciptestinterfaces.TokenBalanceReader)
	if !ok {
		return fmt.Errorf("source chain %d cannot read token balances", src)
	}
	startBal, err := balReader.GetTokenBalance(ctx, sender, srcToken)
	if err != nil {
		return err
	}
	if srcIsStellar {
		if err := applyStellarPoolRateLimit(ctx, env, dst, combo, true); err != nil {
			return err
		}
		t.Cleanup(func() {
			if err := applyStellarPoolRateLimit(context.Background(), env, dst, combo, false); err != nil {
				l.Warn().Err(err).Msg("failed to restore disabled rate limit in cleanup")
			}
		})
	} else {
		if err := applyEVMPoolRateLimit(env, src, dst, combo, true); err != nil {
			return err
		}
		t.Cleanup(func() {
			if err := applyEVMPoolRateLimit(env, src, dst, combo, false); err != nil {
				l.Warn().Err(err).Msg("failed to restore disabled rate limit in cleanup")
			}
		})
	}
	receiver, err := env.Chains[dst].GetEOAReceiverAddress()
	if err != nil {
		return err
	}
	amount := big.NewInt(1000)
	if !srcIsStellar {
		amount = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	}
	_, err = env.Chains[src].SendMessage(ctx, dst,
		cciptestinterfaces.MessageFields{
			Receiver:    receiver,
			TokenAmount: cciptestinterfaces.TokenAmount{Amount: amount, TokenAddress: srcToken},
		},
		cciptestinterfaces.MessageOptions{}, messageV3Version)
	if err == nil {
		return fmt.Errorf("send must be rejected at the pool rate-limit gate")
	}
	if srcIsStellar && !stellarops.IsContractErrorCode(err, tokenpoolbindings.CCIPErrorTokenRateLimitReached) {
		return fmt.Errorf("send failed, but not at the pool rate-limit gate (contract error 312): %w", err)
	}
	endBal, err := balReader.GetTokenBalance(ctx, sender, srcToken)
	if err != nil {
		return err
	}
	if startBal.Cmp(endBal) != 0 {
		return fmt.Errorf("no tokens may be locked on a rejected send: start %s, end %s", startBal, endBal)
	}
	return nil
}

// rateLimitSource resolves the sender and the source-side token for a combo,
// mirroring the tcapi hydrate (resolver registry GetToken).
func rateLimitSource(ctx context.Context, env *helpers.E2ETestEnv, lib ccv.Lib, src uint64, combo devenvcommon.TokenCombination) (protocol.UnknownAddress, protocol.UnknownAddress, error) {
	sender, err := env.Chains[src].GetSenderAddress()
	if err != nil {
		return nil, nil, err
	}
	family, err := chainsel.GetSelectorFamily(src)
	if err != nil {
		return nil, nil, err
	}
	srcReg, err := chainreg.GetRegistry().Get(family)
	if err != nil {
		return nil, nil, err
	}
	ds, err := lib.DataStore()
	if err != nil {
		return nil, nil, err
	}
	srcToken, err := srcReg.AddressResolver.GetToken(ds, src, combo.LocalPoolAddressRef())
	if err != nil {
		return nil, nil, err
	}
	return sender, srcToken, nil
}

// applyStellarPoolRateLimit sets (tight=true) or clears (tight=false) the
// outbound rate-limit bucket of the combo's stellar pool for the remote.
func applyStellarPoolRateLimit(ctx context.Context, env *helpers.E2ETestEnv, dst uint64, combo devenvcommon.TokenCombination, tight bool) error {
	poolStrkey, err := scval.HexToContractStrkey(combo.LocalPoolAddressRef().Address)
	if err != nil {
		return err
	}
	poolClient := tokenpoolbindings.NewTokenPoolClient(env.Deployer, poolStrkey)
	var outbound tokenpoolbindings.RateLimitConfig
	if tight {
		outbound = tokenpoolbindings.RateLimitConfig{IsEnabled: true, Capacity: scval.U128{Lo: 1}}
	}
	return poolClient.SetRateLimitConfig(ctx, env.Deployer.SignerAddress(), dst,
		outbound, tokenpoolbindings.RateLimitConfig{}, false)
}

// applyEVMPoolRateLimit sets (tight=true) or clears (tight=false) the
// outbound rate-limit bucket of the combo's EVM pool for the remote.
func applyEVMPoolRateLimit(env *helpers.E2ETestEnv, src, dst uint64, combo devenvcommon.TokenCombination, tight bool) error {
	evmChain, ok := env.CLDFEnv.BlockChains.EVMChains()[src]
	if !ok {
		return fmt.Errorf("EVM chain %d not found in environment", src)
	}
	var outbound evmtokenpool.Config
	if tight {
		outbound = evmtokenpool.Config{IsEnabled: true, Capacity: big.NewInt(1)}
	}
	_, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.SetRateLimitConfig, evmChain,
		evmcontract.FunctionInput[[]evmtokenpool.RateLimitConfigArgs]{
			Address:       common.HexToAddress(combo.LocalPoolAddressRef().Address),
			ChainSelector: src,
			Args: []evmtokenpool.RateLimitConfigArgs{{
				RemoteChainSelector:       dst,
				OutboundRateLimiterConfig: outbound,
			}},
		})
	return err
}
