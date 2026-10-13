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
// committee) on the Stellar→EVM lane — the reverse of TestStellarTCAPI. The
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

	// Opposite lane orientation from TestStellarTCAPI: Stellar sources, EVM
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
		// The three cases that pass on the EVM→Stellar lane (see
		// TestStellarTCAPI): default verifier, safe-tag finality, and the
		// secondary committee. The remaining basic cases stay out until their
		// EVM→Stellar counterparts are fixed — custom executor and max data
		// size fail there by design, and the required/optional receiver cases
		// need contract support that neither lane has yet.
		cases := []tcapi.TestCase{
			basic.EOAReceiverDefaultVerifier(lib, src, dst, args),
			basic.EOAReceiverDefaultVerifier_SafeTag(lib, src, dst, args),
			basic.EOAReceiverSecondaryVerifier(lib, src, dst, args),
		}
		for _, tc := range cases {
			if !tc.HavePrerequisites(ctx) {
				t.Logf("Skipping %s because current environment does not have the prerequisites", tc.Name())
				continue
			}
			t.Run(tc.Name(), func(t *testing.T) {
				subtestCtx := ccv.Plog.WithContext(t.Context())
				_, runErr := tc.Run(subtestCtx)
				require.NoError(t, runErr, tc.Name())
			})
		}
	})
}
