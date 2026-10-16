package e2e_tests

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi/basic"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestStellarToEVMTCAPI runs the tcapi basic messaging suite (basic.All:
// default verifier, safe-tag finality, secondary committee, custom executor,
// max data size, and the five receiver-verifier cases against the EVM-side
// per-qualifier mock receivers) on the Stellar→EVM lane — the reverse of
// TestEVMToStellarTCAPI (evm_to_stellar_tcapi_test.go). The cases resolve
// CCV/executor from the SOURCE-side (Stellar) resolver and encode them
// through the stellar V3SourceFactory (BuildV3ExtraArgs → Soroban
// GenericExtraArgsV3 XDR), which is the encoding the Stellar OnRamp parses.
// Token transfers live in their own suite: TestStellarToEVMTCAPIBnmTokenTransfers.
//
// Contracts must be compiled before running (from the repo root):
//
//	make build
//
// Start the devenv (from the tests dir, its own Go module):
//
//	CTF_CONFIGS=env/env-stellar-evm.toml go run ./testutils/cmd/devenv
//
// Once the devenv is running, run the test (also from the tests dir):
//
//	go test -v -timeout 25m ./e2e/... -run 'TestStellar(ToEVM)?TCAPI$'
func TestStellarToEVMTCAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode; requires a running devenv environment")
	}
	// The two direction funcs run concurrently; their cases are independent
	// (separate lanes/pools, messageID-keyed assertions).
	t.Parallel()

	configOutputPath := "../env/env-stellar-evm-out.toml"
	stellarChainID := chainsel.STELLAR_LOCALNET.ChainID
	stellarSelector := chainsel.STELLAR_LOCALNET.Selector

	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)

	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath, stellarChainID, stellarSelector)
	lib := env.Lib

	// Opposite lane orientation from TestEVMToStellarTCAPI: Stellar sources, EVM
	// receives (the harness names Stellar the env "source" chain).
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM

	args := basic.Args{
		Run: tcapi.RunConfig{
			ConfirmSentTimeout: 30 * time.Second,
			ConfirmExecTimeout: 7 * time.Minute,
		},
	}

	t.Run("extra args v3 messaging", func(t *testing.T) {
		cases := basic.All(lib, src, dst, args)
		for _, tc := range cases {
			if !tc.HavePrerequisites(ctx) {
				t.Logf("Skipping %s because current environment does not have the prerequisites", tc.Name())
				continue
			}
			t.Run(tc.Name(), func(t *testing.T) {
				subtestCtx := ccv.Plog.WithContext(t.Context())
				res, runErr := tc.Run(subtestCtx)
				if runErr != nil && len(res.Dest.Event.ReturnData) > 0 {
					// The on-chain CCIPError code rides in the dest event's
					// ReturnData; surface it for diagnosis (4-byte selector).
					t.Logf("%s: dest ReturnData: 0x%x", tc.Name(), res.Dest.Event.ReturnData)
				}
				require.NoError(t, runErr, tc.Name())
			})
		}
	})
}

// TestStellarToEVMTCAPIBnmTokenTransferEOA runs the EOA-receiver token
// transfers on the Stellar→EVM lane: both finality variants per combo
// (see bnmEOACaseGroups). Own CI leg (see test-e2e-tcapi.yaml).
func TestStellarToEVMTCAPIBnmTokenTransferEOA(t *testing.T) {
	env, _, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM
	runBnmTokenTransferCases(t, bnmEOACases(t, env, lib, configOutputPath, src, dst))
}

// TestStellarToEVMTCAPIBnmTokenTransferMockReceiver runs the mock-receiver
// token transfers on the Stellar→EVM lane (see bnmMockReceiverCases).
func TestStellarToEVMTCAPIBnmTokenTransferMockReceiver(t *testing.T) {
	env, _, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM
	runBnmTokenTransferCases(t, bnmMockReceiverCases(t, env, lib, configOutputPath, src, dst))
}

// TestStellarToEVMTCAPIBnmTokenTransferMockReceiverFastFinality runs the
// mock-receiver fast-finality token transfers on the Stellar→EVM lane
// (see bnmMockReceiverFastFinalityCases).
func TestStellarToEVMTCAPIBnmTokenTransferMockReceiverFastFinality(t *testing.T) {
	env, _, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM
	runBnmTokenTransferCases(t, bnmMockReceiverFastFinalityCases(t, env, lib, configOutputPath, src, dst))
}

// TestStellarToEVMTCAPIBnmTokenTransferRateLimit runs the rate-limit
// exhaustion error path on the Stellar→EVM lane (see bnmRateLimitCase).
func TestStellarToEVMTCAPIBnmTokenTransferRateLimit(t *testing.T) {
	env, _, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM
	runBnmTokenTransferCase(t, bnmRateLimitCase(t, env, lib, configOutputPath, src, dst))
}

// TestStellarToEVMTCAPIBnmTokenTransferSeparateTokenReceiver runs the
// separate-token-receiver case on the Stellar→EVM lane.
func TestStellarToEVMTCAPIBnmTokenTransferSeparateTokenReceiver(t *testing.T) {
	env, _, _, lib, configOutputPath := newBnmTokenTransferEnv(t)
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM
	args := bnmTokenTransferArgs(env, src)
	runBnmTokenTransferCase(t, bnmSeparateTokenReceiverCase(t, env, lib, configOutputPath, src, dst, args))
}
