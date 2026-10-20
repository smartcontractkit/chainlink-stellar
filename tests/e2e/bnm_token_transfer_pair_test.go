package e2e_tests

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/token_pool"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi/token_transfer"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/pkg/utils/tests"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// newBnmTokenTransferEnv loads the shared e2e env for the token-transfer
// case funcs (one top-level func per case type; see helpers.NewE2ETestEnv).
func newBnmTokenTransferEnv(t *testing.T) (*helpers.E2ETestEnv, context.Context, *zerolog.Logger, ccv.Lib, string) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode; requires a running devenv environment")
	}
	configOutputPath := "../env/env-stellar-evm-out.toml"
	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)
	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath,
		chainsel.STELLAR_LOCALNET.ChainID, chainsel.STELLAR_LOCALNET.Selector)
	return env, ctx, l, env.Lib, configOutputPath
}

// bnmTokenTransferArgs returns one direction's Args: every stellar BnM
// token is 7-dec and every EVM burn-mint token 18-dec, so one Args covers
// every combo (1000 tokens with the exact ×/÷10^11 conversion).
func bnmTokenTransferArgs(env *helpers.E2ETestEnv, src uint64) token_transfer.Args {
	transferAmount, destIncrease := big.NewInt(1000), big.NewInt(0)
	if src == env.SourceChainDetails.ChainSelector {
		destIncrease = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(11), nil)) // ×10^11 up-conversion
	} else {
		transferAmount = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
		destIncrease = new(big.Int).Mul(big.NewInt(1000), new(big.Int).Exp(big.NewInt(10), big.NewInt(7), nil)) // ÷10^11 down-conversion
	}
	return token_transfer.Args{
		Run:                 bnmTokenTransferRun(),
		TransferAmount:      transferAmount,
		DestBalanceIncrease: destIncrease,
	}
}

// bnmEOACases returns both EOA cases per combo, flat and in order: the
// combo-finality variant (token_transfer.All parity) and the default-finality
// variant (All17 parity, 2.0.0 pairs only). Cases run sequentially, so the
// shared (receiver, token) balance per combo cannot interleave.
func bnmEOACases(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) []tcapi.TestCase {
	args := bnmTokenTransferArgs(env, src)
	var cases []tcapi.TestCase
	for _, combo := range bnmCombinations(t, lib, configOutputPath, src, dst) {
		qual := combo.LocalPoolAddressRef().Qualifier
		cases = append(cases, token_transfer.TokenTransfer(lib, src, dst, combo, combo.FinalityConfig(), true,
			fmt.Sprintf("token transfer EOA (%s)", qual), args))
		if devenvcommon.Is17Combination(combo) {
			cases = append(cases, token_transfer.TokenTransfer(lib, src, dst, combo, 0, true,
				fmt.Sprintf("token transfer 1.7.0 EOA default finality (%s)", qual), args))
		}
	}
	return cases
}

// bnmMockReceiverCases returns the mock-receiver cases per 2.0.0 pair
// (token_transfer.All17 parity, default finality). Different combos use
// different tokens, so the cases share no balances and run concurrently.
func bnmMockReceiverCases(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) []tcapi.TestCase {
	args := bnmTokenTransferArgs(env, src)
	var cases []tcapi.TestCase
	for _, combo := range bnmCombinations(t, lib, configOutputPath, src, dst) {
		if !devenvcommon.Is17Combination(combo) {
			continue
		}
		qual := combo.LocalPoolAddressRef().Qualifier
		cases = append(cases, token_transfer.TokenTransfer(lib, src, dst, combo, 0, false,
			fmt.Sprintf("token transfer 1.7.0 mock receiver default finality (%s)", qual), args))
	}
	return cases
}

// bnmMockReceiverFastFinalityCases returns the mock-receiver fast-finality
// token transfers per 2.0.0 pair (combo finality = a block-depth-1 request):
// the contract-receiver × FTF combination the standard suite covers only
// separately (All17 mock = default finality; All FTF = EOA only).
func bnmMockReceiverFastFinalityCases(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) []tcapi.TestCase {
	args := bnmTokenTransferArgs(env, src)
	var cases []tcapi.TestCase
	for _, combo := range bnmCombinations(t, lib, configOutputPath, src, dst) {
		if !devenvcommon.Is17Combination(combo) {
			continue
		}
		qual := combo.LocalPoolAddressRef().Qualifier
		cases = append(cases, token_transfer.TokenTransfer(lib, src, dst, combo, combo.FinalityConfig(), false,
			fmt.Sprintf("token transfer 1.7.0 mock receiver fast finality (%s)", qual), args))
	}
	return cases
}

// runBnmTokenTransferCases runs the cases sequentially, one named subtest
// per case. Sequential by design: each CI leg runs one case type in one
// direction, so no two sends ever overlap — the EVM send path races
// same-nonce approve/send transactions and the stellar RPC rejects bursts
// with TRY_AGAIN_LATER when sends overlap. Each case runs in its own
// subtest, so an unmet prerequisite or a failure stays local to that case
// instead of skipping/aborting the remaining combinations.
func runBnmTokenTransferCases(t *testing.T, cases []tcapi.TestCase) {
	for _, tc := range cases {
		t.Run(tc.Name(), func(t *testing.T) {
			runBnmTokenTransferCase(t, tc)
		})
	}
}

// runBnmTokenTransferCase gates one case on prerequisites and runs it.
func runBnmTokenTransferCase(t *testing.T, tc tcapi.TestCase) {
	if !tc.HavePrerequisites(t.Context()) {
		t.Skipf("Skipping %s: prerequisites not met in this environment", tc.Name())
	}
	res, runErr := tc.Run(ccv.Plog.WithContext(t.Context()))
	if runErr != nil && len(res.Dest.Event.ReturnData) > 0 {
		// The on-chain CCIPError code rides in the dest event's ReturnData.
		t.Logf("%s: dest ReturnData: 0x%x", tc.Name(), res.Dest.Event.ReturnData)
	}
	require.NoError(t, runErr, tc.Name())
}

// firstIs17Combination picks the shared symmetric 2.0.0 pair combo for a
// direction (the first Is17Combination from the env-declared pairs).
func firstIs17Combination(t *testing.T, lib ccv.Lib, configOutputPath string, src, dst uint64) (devenvcommon.TokenCombination, bool) {
	for _, c := range bnmCombinations(t, lib, configOutputPath, src, dst) {
		if devenvcommon.Is17Combination(c) {
			return c, true
		}
	}
	return devenvcommon.TokenCombination{}, false
}

// bnmSeparateTokenReceiverCase returns the separate-token-receiver case for
// one direction: tokens land at the destination chain's EOA receiver (never
// a shared source account) while the message receiver is the tcapi
// token-only zero (see token_transfer Run).
func bnmSeparateTokenReceiverCase(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64, args token_transfer.Args) tcapi.TestCase {
	combo, found := firstIs17Combination(t, lib, configOutputPath, src, dst)
	return bnmTestCase{
		name:    "token transfer separate token receiver",
		prereqs: func(context.Context) bool { return found },
		run: func(ctx context.Context) (tcapi.RunResult, error) {
			if !found {
				return tcapi.RunResult{}, fmt.Errorf("no 2.0.0×2.0.0 pair combo in this environment")
			}
			tokenReceiver, err := env.Chains[dst].GetEOAReceiverAddress()
			if err != nil {
				return tcapi.RunResult{}, err
			}
			// Params in the dest family's raw form: both the dest's
			// GetTokenReceiver and the tcapi balance assertions take bytes.
			tt := token_transfer.TokenTransfer(lib, src, dst, combo, 0, true, "token transfer separate token receiver", token_transfer.Args{
				Run:                 args.Run,
				Send:                tcapi.SendArgs{TokenReceiverParams: []byte(tokenReceiver)},
				TransferAmount:      args.TransferAmount,
				DestBalanceIncrease: args.DestBalanceIncrease,
			})
			return tt.Run(ctx)
		},
	}
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
	return bnmSupportedCombinations(combos)
}

// bnmSupportedCombinations filters the env-declared combos to the CCV
// qualifier sets the tcapi token case supports: the default committee only
// ([] or [default]). Non-default qualifiers are excluded until the ccv-side
// case models their receipts and observes via their own aggregator — its
// SetupOffchainClients is default-committee-only today.
func bnmSupportedCombinations(combos []devenvcommon.TokenCombination) []devenvcommon.TokenCombination {
	var out []devenvcommon.TokenCombination
	for _, combo := range combos {
		if bnmQualifierSupported(combo.LocalPoolAddressRef().Qualifier) &&
			bnmQualifierSupported(combo.RemotePoolAddressRef().Qualifier) {
			out = append(out, combo)
		}
	}
	return out
}

// bnmQualifierBracket extracts one CCV qualifier bracket ([...]) from a
// pool qualifier string.
var bnmQualifierBracket = regexp.MustCompile(`\[([^\]]*)\]`)

// bnmQualifierSupported reports whether every CCV bracket in a pool qualifier
// is the default committee (empty or "default").
func bnmQualifierSupported(qualifier string) bool {
	for _, m := range bnmQualifierBracket.FindAllStringSubmatch(qualifier, -1) {
		if m[1] != "" && m[1] != "default" {
			return false
		}
	}
	return true
}

// resolveComboPoolAddress resolves a combo pool ref's address (the datastore
// rows carry it; the combo refs are lookup keys — their Address is empty).
func resolveComboPoolAddress(lib ccv.Lib, selector uint64, ref datastore.AddressRef) (string, error) {
	ds, err := lib.DataStore()
	if err != nil {
		return "", err
	}
	all, err := ds.Addresses().Fetch()
	if err != nil {
		return "", err
	}
	for _, r := range all {
		if r.ChainSelector == selector && string(r.Type) == string(ref.Type) &&
			r.Version != nil && ref.Version != nil && r.Version.Equal(ref.Version) &&
			r.Qualifier == ref.Qualifier {
			return r.Address, nil
		}
	}
	return "", fmt.Errorf("no pool ref found for chain %d type %s version %s qualifier %q", selector, ref.Type, ref.Version, ref.Qualifier)
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

// bnmRateLimitCase returns the rate-limit-exhaustion case for one direction:
// a capacity-1/rate-0 outbound bucket on the source pool for the remote
// rejects the send at the pool's rate-limit gate with no tokens locked; the
// disabled config is restored on test cleanup.
func bnmRateLimitCase(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) tcapi.TestCase {
	srcIsStellar := src == env.SourceChainDetails.ChainSelector
	combo, found := firstIs17Combination(t, lib, configOutputPath, src, dst)
	return bnmTestCase{
		name:    "token transfer rate limit exceeded",
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
	poolAddr, err := resolveComboPoolAddress(lib, src, combo.LocalPoolAddressRef())
	if err != nil {
		return err
	}
	if srcIsStellar {
		if err := applyStellarPoolRateLimit(ctx, env, dst, poolAddr, true); err != nil {
			return err
		}
		t.Cleanup(func() {
			if err := applyStellarPoolRateLimit(context.Background(), env, dst, poolAddr, false); err != nil {
				l.Warn().Err(err).Msg("failed to restore disabled rate limit in cleanup")
			}
		})
	} else {
		if err := applyEVMPoolRateLimit(env, src, dst, poolAddr, true); err != nil {
			return err
		}
		t.Cleanup(func() {
			if err := applyEVMPoolRateLimit(env, src, dst, poolAddr, false); err != nil {
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
	// The rejection must be the pool's rate-limit gate: for a stellar source
	// the contract error code (311 = TokenMaxCapacityExceeded, 312 =
	// TokenRateLimitReached); for an EVM source the send revert decodes to the
	// pool's rate-limit custom error. A loose error check would let an
	// approval failure or an RPC submission error pass with the token
	// balance unchanged, proving nothing about the limiter.
	if srcIsStellar {
		if !stellarops.IsContractErrorCode(err, tokenpoolbindings.CCIPErrorTokenMaxCapacityExceeded) &&
			!stellarops.IsContractErrorCode(err, tokenpoolbindings.CCIPErrorTokenRateLimitReached) {
			return fmt.Errorf("send failed, but not at the pool rate-limit gate (contract error 311/312): %w", err)
		}
	} else if !evmRateLimitRevertHit(err) {
		return fmt.Errorf("send failed, but not at the pool rate-limit gate (TokenMaxCapacityExceeded/TokenRateLimitReached): %w", err)
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

// bnmInboundRateLimitCase returns the inbound rate-limit-exhaustion case for
// one direction: a capacity-1/rate-0 inbound bucket on the destination pool
// for the remote rejects execution at the pool's inbound rate-limit gate
// (the send and verification are unaffected — the gate is destination-side);
// nothing is delivered; the disabled config is restored on test cleanup.
func bnmInboundRateLimitCase(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) tcapi.TestCase {
	srcIsStellar := src == env.SourceChainDetails.ChainSelector
	combo, found := firstIs17Combination(t, lib, configOutputPath, src, dst)
	return bnmTestCase{
		name:    "token transfer inbound rate limit exceeded",
		prereqs: func(context.Context) bool { return found },
		run: func(ctx context.Context) (tcapi.RunResult, error) {
			if !found {
				return tcapi.RunResult{}, fmt.Errorf("no 2.0.0×2.0.0 pair combo in this environment")
			}
			return tcapi.RunResult{}, runInboundRateLimitExhaustion(ctx, t, env, lib, src, dst, srcIsStellar, combo)
		},
	}
}

// runInboundRateLimitExhaustion sets the tight inbound bucket on the
// destination pool, sends, and asserts the execution rejection at the pool's
// inbound rate-limit gate (311 = TokenMaxCapacityExceeded, 312 =
// TokenRateLimitReached; capacity 1 with these amounts always trips 311). On
// a stellar destination a pool failure reverts the entire execute transaction
// without an execution event, so the rejection is proven by a direct manual
// execute of the verified message; on an EVM destination the OffRamp catches
// the pool failure and records a retryable FAILURE whose ReturnData carries
// TokenHandlingError wrapping the pool's rate-limit revert.
func runInboundRateLimitExhaustion(ctx context.Context, t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, src, dst uint64, srcIsStellar bool, combo devenvcommon.TokenCombination) error {
	args := bnmTokenTransferArgs(env, src)
	poolAddr, err := resolveComboPoolAddress(lib, dst, combo.RemotePoolAddressRef())
	if err != nil {
		return err
	}
	if srcIsStellar {
		// Destination pool is EVM: the send succeeds and the destination
		// records FAILURE; the return data must decode to the exact pool
		// rate-limit rejection — a failure at an earlier run stage would
		// prove nothing about the inbound gate.
		if err := applyEVMPoolInboundRateLimit(env, dst, src, poolAddr, true); err != nil {
			return err
		}
		t.Cleanup(func() {
			if err := applyEVMPoolInboundRateLimit(env, dst, src, poolAddr, false); err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to restore disabled inbound rate limit in cleanup")
			}
		})
		post := token_transfer.TokenTransfer(lib, src, dst, combo, 0, true, "inbound rate limit after", args)
		postRes, postErr := post.Run(ctx)
		if postErr == nil {
			return fmt.Errorf("execution must be rejected at the pool inbound rate-limit gate")
		}
		if postRes.Dest.Event.State != cciptestinterfaces.ExecutionStateFailure {
			return fmt.Errorf("the destination must record a FAILURE at the inbound gate; a send/observation-stage failure would prove nothing (run error: %v)", postErr)
		}
		poolRevert, decErr := decodeEVMTokenHandlingError(postRes.Dest.Event.ReturnData)
		if decErr != nil {
			return fmt.Errorf("FAILURE return data must be TokenHandlingError wrapping the pool revert (run error: %v): %w", postErr, decErr)
		}
		if !evmRateLimitRevertDataHit(poolRevert) {
			return fmt.Errorf("execute failed, but not at the pool inbound rate-limit gate (TokenMaxCapacityExceeded/TokenRateLimitReached), pool revert: 0x%x (run error: %v)", poolRevert, postErr)
		}
		return nil
	}
	// Destination pool is stellar: a pool failure reverts the whole execute
	// transaction with no execution event, so the rejection is proven by a
	// manual execute of the verified message (see
	// manuallyExecuteEVMToStellarTransfer).
	if err := applyStellarPoolInboundRateLimit(ctx, env, src, poolAddr, true); err != nil {
		return err
	}
	t.Cleanup(func() {
		if err := applyStellarPoolInboundRateLimit(context.Background(), env, src, poolAddr, false); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to restore disabled inbound rate limit in cleanup")
		}
	})
	execErr := manuallyExecuteEVMToStellarTransfer(ctx, t, env, lib, src, dst, combo, args.TransferAmount)
	if execErr == nil {
		return fmt.Errorf("execution must be rejected at the pool inbound rate-limit gate")
	}
	if !stellarops.IsContractErrorCode(execErr, tokenpoolbindings.CCIPErrorTokenMaxCapacityExceeded) &&
		!stellarops.IsContractErrorCode(execErr, tokenpoolbindings.CCIPErrorTokenRateLimitReached) {
		return fmt.Errorf("execute failed, but not at the pool inbound rate-limit gate (contract error 311/312): %w", execErr)
	}
	return nil
}

// evmRateLimitErrorSelectors returns the 4-byte selectors of the EVM pool's
// rate-limit custom errors (TokenMaxCapacityExceeded, TokenRateLimitReached)
// from the shared 2.0.0 TokenPool ABI.
func evmRateLimitErrorSelectors() [][4]byte {
	parsed, err := abi.JSON(strings.NewReader(evmtokenpool.TokenPoolABI))
	if err != nil {
		// The ABI is a compile-time constant; a parse failure is a bug, and
		// treating it as "no hit" would silently loosen the rejection check.
		panic(fmt.Sprintf("parse TokenPool ABI: %v", err))
	}
	var selectors [][4]byte
	for _, name := range []string{"TokenMaxCapacityExceeded", "TokenRateLimitReached"} {
		e, ok := parsed.Errors[name]
		if !ok {
			panic(fmt.Sprintf("error %q not found in TokenPool ABI", name))
		}
		var sel [4]byte
		copy(sel[:], e.ID[:4])
		selectors = append(selectors, sel)
	}
	return selectors
}

// evmRateLimitRevertHit reports whether an EVM source send error carries one
// of the pool's rate-limit custom-error selectors. A rate-limit rejection
// reverts the send transaction at lockOrBurn, and the confirm layer embeds the
// raw revert data in the returned error's text, so the "0x"-prefixed 4-byte
// selector is matchable there — the string-substring analog of the stellar
// IsContractErrorCode pattern.
func evmRateLimitRevertHit(sendErr error) bool {
	if sendErr == nil {
		return false
	}
	msg := sendErr.Error()
	for _, sel := range evmRateLimitErrorSelectors() {
		if strings.Contains(msg, hexutil.Encode(sel[:])) {
			return true
		}
	}
	return false
}

// evmRateLimitRevertDataHit reports whether raw EVM pool revert data carries
// one of the pool's rate-limit custom-error selectors (the wrapped revert a
// destination FAILURE records inside TokenHandlingError).
func evmRateLimitRevertDataHit(revertData []byte) bool {
	if len(revertData) < 4 {
		return false
	}
	for _, sel := range evmRateLimitErrorSelectors() {
		if bytes.Equal(revertData[:4], sel[:]) {
			return true
		}
	}
	return false
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
func applyStellarPoolRateLimit(ctx context.Context, env *helpers.E2ETestEnv, dst uint64, poolAddr string, tight bool) error {
	poolStrkey, err := scval.HexToContractStrkey(poolAddr)
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
func applyEVMPoolRateLimit(env *helpers.E2ETestEnv, src, dst uint64, poolAddr string, tight bool) error {
	evmChain, ok := env.CLDFEnv.BlockChains.EVMChains()[src]
	if !ok {
		return fmt.Errorf("EVM chain %d not found in environment", src)
	}
	// All Config fields explicit: nil *big.Int pointers panic the ABI packer.
	outbound := evmtokenpool.Config{Capacity: big.NewInt(0), Rate: big.NewInt(0)}
	if tight {
		outbound = evmtokenpool.Config{IsEnabled: true, Capacity: big.NewInt(1), Rate: big.NewInt(0)}
	}
	inbound := evmtokenpool.Config{Capacity: big.NewInt(0), Rate: big.NewInt(0)}
	_, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.SetRateLimitConfig, evmChain,
		evmcontract.FunctionInput[[]evmtokenpool.RateLimitConfigArgs]{
			Address:       common.HexToAddress(poolAddr),
			ChainSelector: src,
			Args: []evmtokenpool.RateLimitConfigArgs{{
				RemoteChainSelector:       dst,
				OutboundRateLimiterConfig: outbound,
				InboundRateLimiterConfig:  inbound,
			}},
		})
	return err
}

// applyStellarPoolInboundRateLimit sets (tight=true) or clears (tight=false)
// the inbound rate-limit bucket of the combo's stellar pool for the remote.
func applyStellarPoolInboundRateLimit(ctx context.Context, env *helpers.E2ETestEnv, remote uint64, poolAddr string, tight bool) error {
	poolStrkey, err := scval.HexToContractStrkey(poolAddr)
	if err != nil {
		return err
	}
	poolClient := tokenpoolbindings.NewTokenPoolClient(env.Deployer, poolStrkey)
	var inbound tokenpoolbindings.RateLimitConfig
	if tight {
		inbound = tokenpoolbindings.RateLimitConfig{IsEnabled: true, Capacity: scval.U128{Lo: 1}}
	}
	return poolClient.SetRateLimitConfig(ctx, env.Deployer.SignerAddress(), remote,
		tokenpoolbindings.RateLimitConfig{}, inbound, false)
}

// applyEVMPoolInboundRateLimit sets (tight=true) or clears (tight=false) the
// inbound rate-limit bucket of the combo's EVM pool for the remote.
func applyEVMPoolInboundRateLimit(env *helpers.E2ETestEnv, poolChain, remote uint64, poolAddr string, tight bool) error {
	evmChain, ok := env.CLDFEnv.BlockChains.EVMChains()[poolChain]
	if !ok {
		return fmt.Errorf("EVM chain %d not found in environment", poolChain)
	}
	// All Config fields explicit: nil *big.Int pointers panic the ABI packer.
	inbound := evmtokenpool.Config{Capacity: big.NewInt(0), Rate: big.NewInt(0)}
	if tight {
		inbound = evmtokenpool.Config{IsEnabled: true, Capacity: big.NewInt(1), Rate: big.NewInt(0)}
	}
	outbound := evmtokenpool.Config{Capacity: big.NewInt(0), Rate: big.NewInt(0)}
	_, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.SetRateLimitConfig, evmChain,
		evmcontract.FunctionInput[[]evmtokenpool.RateLimitConfigArgs]{
			Address:       common.HexToAddress(poolAddr),
			ChainSelector: poolChain,
			Args: []evmtokenpool.RateLimitConfigArgs{{
				RemoteChainSelector:       remote,
				OutboundRateLimiterConfig: outbound,
				InboundRateLimiterConfig:  inbound,
			}},
		})
	return err
}

// manuallyExecuteEVMToStellarTransfer sends an EVM→Stellar token transfer,
// confirms the source send, waits for verification and indexing, then directly
// executes the verified message (the permissionless execute path) and returns
// the raw execute error. A stellar pool failure reverts the entire execute
// transaction without an execution event — the relayer's exec-event wait
// timing out would be indistinguishable from a source-side failure — so a
// destination-gate rejection is proven by executing inline (the same recipe
// as the remove-remote-pool and permissionless-exec tests).
func manuallyExecuteEVMToStellarTransfer(ctx context.Context, t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, src, dst uint64, combo devenvcommon.TokenCombination, amount *big.Int) error {
	_, srcToken, err := rateLimitSource(ctx, env, lib, src, combo)
	if err != nil {
		return err
	}
	receiver, err := env.Chains[dst].GetEOAReceiverAddress()
	if err != nil {
		return err
	}
	evmSrc := env.Chains[src]
	sentEvent, err := evmSrc.SendMessage(ctx, dst,
		cciptestinterfaces.MessageFields{
			Receiver:    receiver,
			TokenAmount: cciptestinterfaces.TokenAmount{Amount: amount, TokenAddress: srcToken},
		},
		cciptestinterfaces.MessageOptions{}, messageV3Version)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	if _, err := evmSrc.ConfirmSendOnSource(ctx, dst,
		cciptestinterfaces.MessageEventKey{MessageID: sentEvent.MessageID}, evmSentTimeout); err != nil {
		return fmt.Errorf("wait for sent event: %w", err)
	}
	defaultAggregatorClient := env.AggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
	if defaultAggregatorClient == nil {
		return fmt.Errorf("default committee verifier aggregator client not found")
	}
	testCtx := e2e.NewTestingContext(t, t.Context(), env.Chains, defaultAggregatorClient, env.IndexerMonitor)
	result, err := testCtx.AssertMessage(protocol.Bytes32(sentEvent.MessageID), e2e.AssertMessageOptions{
		TickInterval:            1 * time.Second,
		ExpectedVerifierResults: combo.ExpectedVerifierResults(),
		Timeout:                 tests.WaitTimeout(t),
		AssertVerifierLogs:      false,
		AssertExecutorLogs:      false,
	})
	if err != nil {
		return fmt.Errorf("observe message: %w", err)
	}
	verifs := result.IndexedVerifications.Results
	if len(verifs) == 0 {
		return fmt.Errorf("no indexed verifications to assemble manual-execute arguments")
	}
	ccvs := make([]protocol.UnknownAddress, 0, len(verifs))
	verifierResults := make([][]byte, 0, len(verifs))
	for _, v := range verifs {
		ccvs = append(ccvs, v.VerifierResult.VerifierDestAddress)
		verifierResults = append(verifierResults, v.VerifierResult.CCVData)
	}
	_, execErr := env.Chains[dst].ManuallyExecuteMessage(ctx, verifs[0].VerifierResult.Message, 0, ccvs, verifierResults)
	return execErr
}
