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

// bnmEOACaseGroups returns one group per combo with both EOA cases: the
// combo-finality variant (token_transfer.All parity) and the default-finality
// variant (All17 parity, 2.0.0 pairs only) — same (receiver, token) per combo.
func bnmEOACaseGroups(t *testing.T, env *helpers.E2ETestEnv, lib ccv.Lib, configOutputPath string, src, dst uint64) []bnmCaseGroup {
	args := bnmTokenTransferArgs(env, src)
	var groups []bnmCaseGroup
	for _, combo := range bnmCombinations(t, lib, configOutputPath, src, dst) {
		qual := combo.LocalPoolAddressRef().Qualifier
		group := []tcapi.TestCase{
			token_transfer.TokenTransfer(lib, src, dst, combo, combo.FinalityConfig(), true,
				fmt.Sprintf("token transfer EOA (%s)", qual), args),
		}
		if devenvcommon.Is17Combination(combo) {
			group = append(group, token_transfer.TokenTransfer(lib, src, dst, combo, 0, true,
				fmt.Sprintf("token transfer 1.7.0 EOA default finality (%s)", qual), args))
		}
		groups = append(groups, bnmCaseGroup{name: qual, cases: group})
	}
	return groups
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

// runBnmTokenTransferGroups runs each group as a parallel subtest with its
// cases sequential (a group's cases share the (receiver, token) balance).
func runBnmTokenTransferGroups(t *testing.T, groups []bnmCaseGroup) {
	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range g.cases {
				runBnmTokenTransferCase(t, tc)
			}
		})
	}
}

// runBnmTokenTransferCases runs prerequisite-gated cases as parallel
// subtests — only for case sets that share no (receiver, token) balance.
func runBnmTokenTransferCases(t *testing.T, cases []tcapi.TestCase) {
	for _, tc := range cases {
		t.Run(tc.Name(), func(t *testing.T) {
			t.Parallel()
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

// bnmCaseGroup is one combo's token-transfer cases. Every group shares its
// combo's (receiver, token) balances — the stellar receiver contract is the
// receiver of every dest-side case — so groups run concurrently while the
// cases within a group run sequentially (balance reads must not interleave).
type bnmCaseGroup struct {
	name  string
	cases []tcapi.TestCase
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
	return combos
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
	var outbound evmtokenpool.Config
	if tight {
		outbound = evmtokenpool.Config{IsEnabled: true, Capacity: big.NewInt(1)}
	}
	_, err := cldf_ops.ExecuteOperation(env.CLDFEnv.OperationsBundle, evmtokenpool.SetRateLimitConfig, evmChain,
		evmcontract.FunctionInput[[]evmtokenpool.RateLimitConfigArgs]{
			Address:       common.HexToAddress(poolAddr),
			ChainSelector: src,
			Args: []evmtokenpool.RateLimitConfigArgs{{
				RemoteChainSelector:       dst,
				OutboundRateLimiterConfig: outbound,
			}},
		})
	return err
}
