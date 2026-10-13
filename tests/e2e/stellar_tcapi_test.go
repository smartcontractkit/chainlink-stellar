package e2e_tests

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi/basic"
	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestStellarTCAPI runs the chain-agnostic tcapi basic messaging suite on the
// EVM→Stellar lane, the same standard cases every other chain family runs.
// Stellar plugs in via the V3DestinationFactory and AddressResolver registered
// in tests/ccv/chain/register.go.
//
// With the current env topology the runnable cases are: EOA receiver default
// verifier, safe-tag finality, and the secondary committee — the same three as
// the reverse lane (stellar_to_evm_tcapi_test.go). The other basic cases are
// excluded until fixed (custom executor, max data size) or until the env grows
// the contracts they need (required/optional receiver verifiers).
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
func TestStellarTCAPI(t *testing.T) {
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

	// The harness names Stellar the "source" and EVM the "dest" for its default
	// lane orientation; the tcapi cases here run the other direction.
	src := env.DestChainDetails.ChainSelector   // EVM
	dst := env.SourceChainDetails.ChainSelector // Stellar

	// The basic suite sends non-zero finality requests (block-depth 1 and
	// WaitForSafe). The stellar OffRamp enforces the receiver's
	// allowed_finality_config at execute time (the H-7 gate), and the example
	// receiver reports 0, WAIT_FOR_FINALITY only, until enable_remote_chain
	// seeds a policy — so every such case would fail on delivery with
	// InvalidRequestedFinality (315). Seed the same policy the EVM mock
	// receiver gets in ccv's devenv: finality.Config{BlockDepth: 1,
	// WaitForSafe: true}, which admits both requested tags.
	stellarChain, ok := env.Chains[dst].(*ccvchain.Chain)
	require.True(t, ok, "stellar chain must be *ccvchain.Chain to access the receiver contract id")
	receiverID, err := stellarChain.GetReceiverContractAddress()
	require.NoError(t, err)
	recvClient := recvbindings.NewExampleCcipReceiverClient(env.Deployer, receiverID)
	ownerAddr := env.Deployer.SignerAddress()
	// enable_remote_chain rejects empty extra args; [0x01] is the placeholder
	// the deploy sequence and the FTF e2e use.
	placeholderExtra := []byte{0x01}
	allowedFinality := finality.Config{BlockDepth: 1, WaitForSafe: true}.Raw()
	require.NoError(t, recvClient.EnableRemoteChain(ctx, ownerAddr, src, placeholderExtra,
		binary.BigEndian.Uint32(allowedFinality[:])))

	// Restore the deployed default (WAIT_FOR_FINALITY only) so the permissive
	// policy does not leak into the FTF e2e negative control, which asserts
	// 315 rejection, when both tests share a devenv.
	t.Cleanup(func() {
		if err := recvClient.EnableRemoteChain(context.Background(), ownerAddr, src, placeholderExtra, 0); err != nil {
			l.Warn().Err(err).Msg("failed to reset receiver allowed_finality_config in cleanup")
		}
	})

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
		// The three cases that pass on this lane; an explicit list rather than
		// basic.All so CI stays green. custom executor needs a second executor
		// instance in the env, max data size needs the ccv FeeQuoter
		// calldata-overhead fix (not in the pinned ccv yet), and the
		// required/optional receiver cases need contract support. Mirrored in
		// stellar_to_evm_tcapi_test.go.
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
