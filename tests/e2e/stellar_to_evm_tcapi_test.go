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

// TestStellarToEVMTCAPI runs the chain-agnostic tcapi basic cases that pass on
// the EVM→Stellar lane (default verifier, safe-tag finality, secondary
// committee, custom executor, max data size, and the five receiver-verifier
// cases against the
// EVM-side per-qualifier mock receivers) on the Stellar→EVM lane — the reverse
// of TestEVMToStellarTCAPI (evm_to_stellar_tcapi_test.go). The
// cases resolve CCV/executor from the SOURCE-side (Stellar) resolver and encode
// them through the stellar V3SourceFactory (BuildV3ExtraArgs → Soroban
// GenericExtraArgsV3 XDR), which is the encoding the Stellar OnRamp parses.
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
//	go test -v -timeout 25m ./e2e/... -run 'TestStellar(ToEVM)?TCAPI'
func TestStellarToEVMTCAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode; requires a running devenv environment")
	}

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
		// The four cases that pass on the EVM→Stellar lane (see
		// TestEVMToStellarTCAPI): default verifier, safe-tag finality, the secondary
		// committee, and the custom executor. Max data size runs since the ccv
		// fix (#1510) is pinned and the stellar Chain implements the
		// source-side MessageOverheadProvider this lane needs.
		//
		// The custom executor case names the stellar-side custom executor
		// (GetExecutor(stellar, "custom"), deployed by the full deploy via
		// nonDefaultExecutorQualifiers). The message's 32-byte executor ID only
		// matters off-chain: the EVM dest decodes the envelope with
		// MessageV1Codec, which carries an unchecked ccv_and_executor_hash, and
		// the executor plugin byte-matches it against
		// defaultExecutorAddress[stellar source] — populated in the EVM custom
		// executor's job spec from the stellar chain config (the stellar
		// entry the executor_pools.custom pool carries).
		cases := []tcapi.TestCase{
			basic.EOAReceiverDefaultVerifier(lib, src, dst, args),
			basic.EOAReceiverDefaultVerifier_SafeTag(lib, src, dst, args),
			basic.EOAReceiverSecondaryVerifier(lib, src, dst, args),
			basic.CustomExecutor(lib, src, dst, args),
			// The receiver-verifier cases: on this lane the dest is EVM,
			// where ccv's EVM phase deploys the per-qualifier mock receivers
			// (buildMockReceivers) configured with the EVM-side VVR ids,
			// while the stellar source's executor report + aggregator stamps
			// travel through the stellar offchain pipeline — exercising
			// cross-family CCV identity agreement between the executor's
			// submitted identity and the EVM receivers' required CCVs.
			// Cases whose prerequisites the environment lacks (e.g. a
			// missing committee) skip via HavePrerequisites below.
			basic.ReceiverSecondaryVerifierRequired(lib, src, dst, args),
			basic.ReceiverSecondaryRequiredTertiaryOptionalThreshold1(lib, src, dst, args),
			basic.ReceiverQuaternaryAllThreeVerifiers(lib, src, dst, args),
			basic.ReceiverQuaternaryDefaultAndSecondary(lib, src, dst, args),
			basic.ReceiverQuaternaryDefaultAndTertiary(lib, src, dst, args),
			// Max data size: the stellar source implements
			// MessageOverheadProvider, so the case hydrates on this lane.
			basic.MaxDataSize(lib, src, dst, args),
		}
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
