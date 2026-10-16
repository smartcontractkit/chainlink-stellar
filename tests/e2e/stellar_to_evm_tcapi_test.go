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

// TestStellarToEVMTCAPIBnmTokenTransfers runs the tcapi token-transfer cases
// on the Stellar→EVM lane: the pair-matrix suite plus the rate-limit
// exhaustion error path (see bnmTokenTransferCases).
func TestStellarToEVMTCAPIBnmTokenTransfers(t *testing.T) {
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
	src := env.SourceChainDetails.ChainSelector // Stellar
	dst := env.DestChainDetails.ChainSelector   // EVM

	cases := bnmTokenTransferCases(t, env, lib, configOutputPath, src, dst)
	for _, tc := range cases {
		if !tc.HavePrerequisites(ctx) {
			t.Logf("Skipping %s because current environment does not have the prerequisites", tc.Name())
			continue
		}
		t.Run(tc.Name(), func(t *testing.T) {
			// Cases are independent (each combo is its own pool pair and
			// token), so they run concurrently; serial cases (shared pool
			// config mutation) defer past the loop and run alone.
			if s, ok := tc.(serialTestCase); !ok || !s.Serial() {
				t.Parallel()
			}
			res, runErr := tc.Run(ccv.Plog.WithContext(t.Context()))
			if runErr != nil && len(res.Dest.Event.ReturnData) > 0 {
				// The on-chain CCIPError code rides in the dest event's
				// ReturnData; surface it for diagnosis (4-byte selector).
				t.Logf("%s: dest ReturnData: 0x%x", tc.Name(), res.Dest.Event.ReturnData)
			}
			require.NoError(t, runErr, tc.Name())
		})
	}
}
