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

// TestStellarToEVMTCAPI runs ONE chain-agnostic tcapi basic case (EOA receiver
// and default committee verifier) on the Stellar→EVM lane — the reverse of
// TestStellarTCAPI. This is the pilot for Stellar-as-source: the case resolves
// CCV/executor from the SOURCE-side (Stellar) resolver and encodes them through
// the stellar V3SourceFactory (BuildV3ExtraArgs → Soroban GenericExtraArgsV3
// XDR), which is the encoding the Stellar OnRamp parses.
//
// The remaining basic cases light up here once the same run is green; until
// then this single case isolates the Stellar→EVM executor/aggregator wiring
// (historically TODO'd in stellar_to_evm_exec_test.go) from the known EVM-side
// tcapi issues on the opposite lane.
//
// Contracts must be compiled before running:
//
//	make build
//
// Start the devenv from the chainlink-stellar root:
//
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// Once the devenv is running, run the test:
//
//	go test -v -timeout 25m ./tests/e2e/... -run 'TestStellar(ToEVM)?TCAPI'
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
		tc := basic.EOAReceiverDefaultVerifier(lib, src, dst, args)
		if !tc.HavePrerequisites(ctx) {
			t.Skipf("Skipping %s because current environment does not have the prerequisites", tc.Name())
		}
		subtestCtx := ccv.Plog.WithContext(t.Context())
		_, runErr := tc.Run(subtestCtx)
		require.NoError(t, runErr, tc.Name())
	})
}
