package e2e_tests

import (
	"context"
	"encoding/binary"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/tests/e2e/tcapi"
	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// bnmCombinations builds the full tcapi combo list for one direction: every
// env-declared token_pool_pair whose local/remote selectors match (the pairs
// the shared pipeline deployed and wired — symmetric 2.0.0 and mixed
// 1.6.1↔2.0.0), resolved against the datastore. Cases whose pool refs a
// given environment lacks skip per-case via HavePrerequisites, not here.
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

// bnmTokenTransferRun is the shared run config for the BnM suite cases.
func bnmTokenTransferRun() tcapi.RunConfig {
	return tcapi.RunConfig{
		ConfirmSentTimeout: 30 * time.Second,
		ConfirmExecTimeout: 7 * time.Minute,
	}
}

// runBnmTokenTransferCase gates one tcapi token-transfer case on
// prerequisites and runs it as a named subtest, mirroring the basic-suite
// loop: cases the environment cannot serve skip, they do not fail.
func runBnmTokenTransferCase(t *testing.T, ctx context.Context, tc tcapi.TestCase) {
	if !tc.HavePrerequisites(ctx) {
		t.Logf("Skipping %s: prerequisites not met in this environment", tc.Name())
		return
	}
	t.Run(tc.Name(), func(t *testing.T) {
		res, runErr := tc.Run(ccv.Plog.WithContext(t.Context()))
		if runErr != nil && len(res.Dest.Event.ReturnData) > 0 {
			// The on-chain CCIPError code rides in the dest event's ReturnData;
			// surface it for diagnosis (4-byte selector).
			t.Logf("%s: dest ReturnData: 0x%x", tc.Name(), res.Dest.Event.ReturnData)
		}
		require.NoError(t, runErr, tc.Name())
	})
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
