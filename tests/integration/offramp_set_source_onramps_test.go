//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccipchangesets "github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	v2changesets "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/changesets"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-stellar/bindings"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters" // registers the ChainFamily adapter the changeset type-asserts
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestOffRampSetSourceOnRamps drives the ccip offramp_set_source_onramps
// changeset against a live OffRamp through the stellar ChainFamily adapter's
// OffRampSourceOnRampSetter/Reader: an equal whitelist skips (nothing changes
// on chain), a deployer-owned OffRamp patches directly, the reader round-trips,
// and a timelock-owned OffRamp routes the patch to a one-transaction MCMS
// proposal that executes through the timelock.
func TestOffRampSetSourceOnRamps(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	dst := chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector

	e2eEnv := &helpers.E2ETestEnv{
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: passphrase,
		StellarRoot:       projectRoot,
	}
	newBundle := func() cldfops.Bundle {
		return cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	}
	ds := datastore.NewMemoryDataStore()
	cldfEnv := cldf.Environment{
		Logger:     cldflogger.Test(t),
		GetContext: func() context.Context { return ctx },
		DataStore:  ds.Seal(),
		BlockChains: cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: cldf_stellar.Chain{
			ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
			Signer:            bindings.NewStellarKeypairSigner(deployerKP),
			Client:            rpcClient,
			NetworkPassphrase: passphrase,
		}}),
		OperationsBundle: newBundle(),
	}
	apply := func(name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, ds.Merge(out.Seal()), "%s: merge output", name)
		cldfEnv.DataStore = ds.Seal()
	}

	// Lean stack: RMN Remote → RMN Proxy → TAR → OffRamp (the OffRamp deploy's deps).
	apply("RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(cldfEnv, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("RMN Proxy", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNProxy{}.Apply(cldfEnv, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("TokenAdminRegistry", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployTokenAdminRegistry{}.Apply(cldfEnv, changesets.DeployTokenAdminRegistryConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("OffRamp", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployOffRamp{}.Apply(cldfEnv, changesets.DeployOffRampConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	offRampID, err := ccip.OffRampDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
	offRampClient := offrampbindings.NewOffRampClient(deployer, offRampID)

	initialOnRamp := bytes.Repeat([]byte{0xaa}, 32)
	patchedOnRamp := bytes.Repeat([]byte{0xbb}, 32)
	finalOnRamp := bytes.Repeat([]byte{0xcc}, 32)

	// Pre-wire the source lane raw: the setter is a patch tool by design and
	// fails fast on an unconfigured lane (the same raw call the devenv
	// PostConnect path makes). The Router field is preserved by the setter on
	// every patch; any on-chain contract address works for the fixture.
	tarStr, err := ccip.TokenAdminRegistryDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
	require.NoError(t, offRampClient.ApplySourceChainCfgUpdates(ctx, []offrampbindings.SourceChainConfigArgs{{
		SourceChainSelector: dst,
		Router:              tarStr,
		IsEnabled:           true,
		OnRamps:             [][]byte{initialOnRamp},
	}}))

	readOnRamps := func() [][]byte {
		cfg, err := offRampClient.GetSourceChainConfig(ctx, dst)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		return cfg.OnRamps
	}

	cs := v2changesets.OffRampSetSourceOnRamps(ccvadapters.GetChainFamilyRegistry(), ccipchangesets.GetRegistry())
	entry := func(onRamp []byte) v2changesets.OffRampSetSourceOnRampsInput {
		return v2changesets.OffRampSetSourceOnRampsInput{
			Updates: []ccvadapters.OffRampSetSourceOnRampsEntry{{
				LocalChainSelector:  sel,
				SourceChainSelector: dst,
				OnRamps:             []string{"0x" + hex.EncodeToString(onRamp)},
			}},
		}
	}

	t.Run("equal whitelist skips", func(t *testing.T) {
		out, err := cs.Apply(cldfEnv, entry(initialOnRamp))
		require.NoError(t, err)
		require.Empty(t, out.MCMSTimelockProposals)
		require.Equal(t, [][]byte{initialOnRamp}, readOnRamps(), "an equal whitelist must not change on-chain state")
	})

	t.Run("deployer-owned patch applies directly and the reader round-trips", func(t *testing.T) {
		cldfEnv.OperationsBundle = newBundle()
		out, err := cs.Apply(cldfEnv, entry(patchedOnRamp))
		require.NoError(t, err)
		require.Empty(t, out.MCMSTimelockProposals, "deployer-key mode submits directly")
		require.Equal(t, [][]byte{patchedOnRamp}, readOnRamps())

		// The reader on the registered ChainFamily adapter returns the same
		// whitelist in the wire form the source chain uses.
		family, ok := ccvadapters.GetChainFamilyRegistry().GetChainFamily(chainsel.FamilyStellar)
		require.True(t, ok)
		reader, ok := family.(ccvadapters.OffRampSourceOnRampReader)
		require.True(t, ok)
		got, err := reader.GetOffRampSourceOnRamps(cldfEnv, sel, dst)
		require.NoError(t, err)
		require.Equal(t, [][]byte{patchedOnRamp}, got)
	})

	t.Run("timelock-owned patch routes to an executed MCMS proposal", func(t *testing.T) {
		gov := helpers.DeployMCMSStackWithRoles(t, ctx, e2eEnv, sel, cciputils.CLLQualifier, helpers.DefaultMCMSTimelockMinDelaySec)
		require.NoError(t, ds.Addresses().Upsert(mcmsutil.StellarTimelockDatastoreRef(sel, cciputils.CLLQualifier, gov.TimelockID)))
		cldfEnv.DataStore = ds.Seal()

		// Hand the OffRamp to the timelock: raw transfer, then accept through
		// the timelock (the changeset under test only patches whitelists).
		require.NoError(t, offRampClient.TransferOwnership(ctx, gov.TimelockID))
		acceptArgs, err := mcmsutil.EncodeSorobanInvokeArgs(nil)
		require.NoError(t, err)
		var acceptSalt [32]byte
		acceptSalt[31] = 1
		callsAccept := timelockbindings.Calls{
			Inner: []timelockbindings.Call{{Target: offRampID, Function: "accept_ownership", ArgsXdr: acceptArgs}},
		}
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, callsAccept, [32]byte{}, acceptSalt)

		cldfEnv.OperationsBundle = newBundle()
		mcmsInput := mcms.Input{
			Qualifier:      cciputils.CLLQualifier,
			TimelockAction: mcmstypes.TimelockActionSchedule,
			ValidUntil:     uint32(time.Now().Add(time.Hour).UTC().Unix()),
		}
		input := v2changesets.OffRampSetSourceOnRampsInput{
			Updates: []ccvadapters.OffRampSetSourceOnRampsEntry{{
				LocalChainSelector:  sel,
				SourceChainSelector: dst,
				OnRamps:             []string{"0x" + hex.EncodeToString(finalOnRamp)},
			}},
			MCMS: mcmsInput,
		}
		require.NoError(t, cs.VerifyPreconditions(cldfEnv, input))
		out, err := cs.Apply(cldfEnv, input)
		require.NoError(t, err)
		require.Len(t, out.MCMSTimelockProposals, 1)
		proposal := out.MCMSTimelockProposals[0]
		require.Len(t, proposal.Operations, 1)
		require.Len(t, proposal.Operations[0].Transactions, 1)
		tx := proposal.Operations[0].Transactions[0]
		require.Equal(t, offRampID, tx.To)
		fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
		require.NoError(t, err)
		require.Equal(t, "apply_source_chain_cfg_updates", fn)

		calls, err := helpers.TimelockCallsFromProposalTx(tx)
		require.NoError(t, err)
		var pred, salt [32]byte
		salt[31] = 2
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, calls, pred, salt)
		require.Equal(t, [][]byte{finalOnRamp}, readOnRamps())
	})
}
