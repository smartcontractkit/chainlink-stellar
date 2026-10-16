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

// TestEVMToStellarTCAPI runs the chain-agnostic tcapi basic messaging suite on
// the EVM→Stellar lane, the same standard cases every other chain family runs.
// Stellar plugs in via the V3DestinationFactory and AddressResolver registered
// in tests/ccv/chain/register.go.
//
// The full basic suite runs via basic.All; cases whose prerequisites the
// environment lacks skip via HavePrerequisites. The reverse lane
// (stellar_to_evm_tcapi_test.go, TestStellarToEVMTCAPI) runs the same set.
// Max data size works since the ccv fix (#1510) is pinned and the stellar
// Chain implements the destination-side ExecutionGasLimitProvider this lane
// needs.
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
//	go test -v -timeout 25m ./e2e/... -run 'Test(StellarToEVM|EVMToStellar)TCAPI$'
func TestEVMToStellarTCAPI(t *testing.T) {
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

	// The harness names Stellar the "source" and EVM the "dest" for its default
	// lane orientation; the tcapi cases here run the other direction.
	src := env.DestChainDetails.ChainSelector   // EVM
	dst := env.SourceChainDetails.ChainSelector // Stellar

	// The basic suite sends non-zero finality requests (block-depth 1 and
	// WaitForSafe); the shared seeding helper (see bnm_token_transfer_pair_
	// test.go) applies the policy the EVM mock receivers get in ccv's devenv.
	seedStellarReceiverPolicy(t, env, l, src, dst)

	// The Stellar lane is slower than the tcapi defaults (40s exec): the
	// stellar executor runs on a 15s interval and the existing hand-written
	// tests allow 7 minutes for execution.
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

// TestEVMToStellarTCAPIBnmTokenTransfers runs the tcapi token-transfer suite
// on the EVM→Stellar lane: All (EOA, combo finality) + All17 (2.0.0×2.0.0 ×
// EOA/mock receiver, default finality) over the full pair matrix.
// TestEVMToStellarTCAPIBnmTokenTransfers runs the tcapi token-transfer cases
// on the EVM→Stellar lane: the pair-matrix suite plus the rate-limit
// exhaustion error path (see bnmTokenTransferCases).
func TestEVMToStellarTCAPIBnmTokenTransfers(t *testing.T) {
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

	// The harness names Stellar the "source" and EVM the "dest" for its default
	// lane orientation; the tcapi cases here run the other direction.
	src := env.DestChainDetails.ChainSelector   // EVM
	dst := env.SourceChainDetails.ChainSelector // Stellar

	// Same seeding as the basic suite: All's combo-finality cases deliver to
	// the stellar example receiver, which rejects them without the policy.
	seedStellarReceiverPolicy(t, env, l, src, dst)

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
