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

// TestEVMToStellarTCAPI runs the chain-agnostic tcapi basic messaging suite on
// the EVM→Stellar lane, the same standard cases every other chain family runs.
// Stellar plugs in via the V3DestinationFactory and AddressResolver registered
// in tests/ccv/chain/register.go.
//
// The runnable cases are: EOA receiver default verifier, safe-tag finality,
// the secondary committee, the custom executor (a custom-qualified stellar
// executor NOP exists in the env), and the five receiver-verifier cases
// (required/optional receiver CCVs against the qualified receivers the full
// deploy records). The reverse lane (stellar_to_evm_tcapi_test.go,
// TestStellarToEVMTCAPI) runs the same set. Max data size stays excluded
// until its ccv FeeQuoter fix lands.
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
//	go test -v -timeout 25m ./e2e/... -run 'Test(StellarToEVM|EVMToStellar)TCAPI'
func TestEVMToStellarTCAPI(t *testing.T) {
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
		// The four cases that pass on this lane; an explicit list rather than
		// basic.All so CI stays green. custom executor works since the env grew
		// a custom-qualified stellar executor NOP (stellar-executor-2 in
		// env-stellar-evm.toml; the EVM source side's custom executor contract
		// is deployed by ccv's EVM phase). max data size needs the ccv
		// FeeQuoter calldata-overhead fix (not in the pinned ccv yet).
		cases := []tcapi.TestCase{
			basic.EOAReceiverDefaultVerifier(lib, src, dst, args),
			basic.EOAReceiverDefaultVerifier_SafeTag(lib, src, dst, args),
			basic.EOAReceiverSecondaryVerifier(lib, src, dst, args),
			basic.CustomExecutor(lib, src, dst, args),
			// The receiver-verifier cases: the stellar full deploy deploys
			// qualified receivers (secondary/tertiary/quaternary) configured
			// with the stellar-side VVR ids, matching this lane's dest. They
			// rely on cross-family identity agreement between the receiver's
			// required CCVs and the executor's report + aggregator-stamped
			// identity (the EVM-side offchain pipeline): executor
			// ErrInsufficientVerifiers / empty VerifierDestAddress ⇒ ccv
			// aggregator-config gap; on-chain CCVQuorumNotMet ⇒ stellar
			// lane-config quorum gap. Cases whose prerequisites the
			// environment lacks (e.g. a missing committee) skip via
			// HavePrerequisites below.
			basic.ReceiverSecondaryVerifierRequired(lib, src, dst, args),
			basic.ReceiverSecondaryRequiredTertiaryOptionalThreshold1(lib, src, dst, args),
			basic.ReceiverQuaternaryAllThreeVerifiers(lib, src, dst, args),
			basic.ReceiverQuaternaryDefaultAndSecondary(lib, src, dst, args),
			basic.ReceiverQuaternaryDefaultAndTertiary(lib, src, dst, args),
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
