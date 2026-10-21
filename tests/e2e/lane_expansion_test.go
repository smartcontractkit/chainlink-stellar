package e2e_tests

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	ccvchangesets "github.com/smartcontractkit/chainlink-ccv/deployment/changesets"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"

	// The stellar ChainFamily + LaneConfigAdapter registration (register.go
	// in tests/ccv/chain blank-imports this via NewE2ETestEnv; kept explicit so
	// the dependency is visible at the use site).
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
)

// TestLaneExpansion drives the ccv LaneExpansion changeset through the
// registered stellar LaneConfigAdapter against the pre-wired devenv lane.
//
// The devenv wires the EVM↔Stellar pairs at construction through the ccip
// topology changeset, so a genuine new pair would need a chain this
// environment does not have. The realistic exercise — and what this test does —
// is the idempotent re-apply of an existing pair: both sides' LaneConfig
// adapters resolve their contracts, read the current on-chain state, and emit
// no writes for an already-correct lane. The raw on-chain assertions that
// follow (OnRamp dest config, OffRamp source whitelist, Router entries) are
// the first in the suite to pin stellar lane state directly.
func TestLaneExpansion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode; requires a running devenv environment")
	}
	configOutputPath := "../env/env-stellar-evm-out.toml"
	ctx := ccv.Plog.WithContext(t.Context())
	l := zerolog.Ctx(ctx)
	env := helpers.NewE2ETestEnv(t, ctx, l, configOutputPath,
		chainsel.STELLAR_LOCALNET.ChainID, chainsel.STELLAR_LOCALNET.Selector)

	stellar := env.SourceChainDetails.ChainSelector
	evm := env.DestChainDetails.ChainSelector

	// The remote-family ramp encodings the changeset threads through: the
	// EVM OnRamp as the EVM family writes it into its messages (32-byte
	// abi-encoded), and the EVM OffRamp raw (20 bytes, unpadded destination
	// form) — both resolved via the remote chain's own registered adapter,
	// the same registry the changeset dispatches through.
	evmLaneAdapter, err := ccvdeploymentadapters.GetLaneConfigRegistry().Get(evm)
	require.NoError(t, err, "EVM LaneConfigAdapter should be registered (via the devenv package import)")
	evmOnRampWire, err := evmLaneAdapter.GetOnRampAddress(env.CLDFEnv.DataStore, evm)
	require.NoError(t, err)
	require.Len(t, evmOnRampWire, 32, "the EVM OnRamp wire form is abi.encode(address)")
	evmOffRampRaw, err := evmLaneAdapter.GetOffRampAddress(env.CLDFEnv.DataStore, evm)
	require.NoError(t, err)
	require.Len(t, evmOffRampRaw, 20, "destination-side addresses travel unpadded")

	input := ccvchangesets.LaneExpansionInput{
		SrcChainSelector:  evm,
		DestChainSelector: stellar,
		// Prod router: the pair is already wired; a re-apply must not touch
		// the mapping (AllowOnrampOverride stays false in the adapter).
		UseTestRouter: false,
		// Qualifiers omitted: executor and both CCV qualifier sets resolve
		// the "default" instances; InboundSigners omitted: the quorum stays
		// owned by the committee changesets (an empty SignatureConfig).
		MCMS: nil,
	}

	cs := ccvchangesets.LaneExpansion()
	require.NoError(t, cs.VerifyPreconditions(*env.CLDFEnv, input))

	// The idempotent re-apply: deployer-key mode on both sides, no MCMS
	// proposals, no error.
	out, err := cs.Apply(*env.CLDFEnv, input)
	require.NoError(t, err, "LaneExpansion re-apply on a wired pair should succeed idempotently")
	require.Empty(t, out.MCMSTimelockProposals, "deployer-key mode emits no proposals")

	// A second apply on a fresh operations bundle (a reused bundle replays
	// recorded reports): still clean, still no proposals.
	env.CLDFEnv.OperationsBundle = cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	out, err = cs.Apply(*env.CLDFEnv, input)
	require.NoError(t, err)
	require.Empty(t, out.MCMSTimelockProposals)

	// The raw lane-state assertions — the first in the suite to pin the
	// stellar side of a lane directly.
	onRampID, err := stellarccip.OnRampDatastoreRef().LookupStrkey(env.CLDFEnv.DataStore, stellar)
	require.NoError(t, err)
	offRampID, err := stellarccip.OffRampDatastoreRef().LookupStrkey(env.CLDFEnv.DataStore, stellar)
	require.NoError(t, err)
	routerID, err := stellarccip.RouterDatastoreRef().LookupStrkey(env.CLDFEnv.DataStore, stellar)
	require.NoError(t, err)

	t.Run("onramp dest config wires the EVM destination", func(t *testing.T) {
		onRampClient := onrampbindings.NewOnRampClient(env.Deployer, onRampID)
		destCfg, err := onRampClient.GetDestChainConfig(ctx, evm)
		require.NoError(t, err)
		require.NotNil(t, destCfg)
		require.Equal(t, routerID, destCfg.Router)
		require.Len(t, destCfg.OffRamp, 20, "the stored EVM OffRamp is the unpadded destination form")
		require.Equal(t, evmOffRampRaw, destCfg.OffRamp)
		require.Equal(t, uint32(20), destCfg.AddressBytesLength)
		require.NotEmpty(t, destCfg.DefaultExecutor, "the default executor resolves and rides the dest config")
	})

	t.Run("offramp source whitelist wires the EVM source onramp", func(t *testing.T) {
		offRampClient := offrampbindings.NewOffRampClient(env.Deployer, offRampID)
		srcCfg, err := offRampClient.GetSourceChainConfig(ctx, evm)
		require.NoError(t, err)
		require.NotNil(t, srcCfg)
		require.True(t, srcCfg.IsEnabled, "the lane allows traffic from the source chain")
		require.Equal(t, routerID, srcCfg.Router)
		require.Len(t, srcCfg.OnRamps, 1, "one whitelisted source onramp")
		require.Equal(t, evmOnRampWire, srcCfg.OnRamps[0], "the whitelist holds the EVM onramp's exact wire bytes")
	})

	t.Run("router routes the lane both ways", func(t *testing.T) {
		routerClient := routerbindings.NewRouterClient(env.Deployer, routerID)
		gotOnRamp, err := routerClient.GetOnramp(ctx, evm)
		require.NoError(t, err)
		require.Equal(t, onRampID, gotOnRamp, "the router maps the EVM destination to the stellar OnRamp")

		offRamps, err := routerClient.GetOfframps(ctx)
		require.NoError(t, err)
		found := false
		for _, entry := range offRamps {
			if entry.SourceChainSelector == evm {
				require.Equal(t, offRampID, entry.Offramp, "the router maps the EVM source to the stellar OffRamp")
				found = true
			}
		}
		require.True(t, found, "the router's offramp table has an entry for the EVM source chain")
	})
}
