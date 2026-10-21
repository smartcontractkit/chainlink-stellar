package e2e_tests

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stretchr/testify/require"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccipchangesets "github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	rmnbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestStellarActivateRMNChangeset applies the ActivateRMN changeset to a
// deployer-owned RMN Remote, executes the accept_ownership proposal it emits
// through the RMNMCMS stack, and checks that a rerun is a no-op.
func TestStellarActivateRMNChangeset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase := helpers.SetupTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	b := cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	deps := stellardeps.FromDeployer(deployer)

	rmnWasm := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	// Deploy the RMN Remote through the component deploy sequence (skip-if-exists,
	// deterministic singleton salt "rmn-remote"). Owner defaults to the deployer.
	rmnRep, err := cldfops.ExecuteSequence(b, sequences.DeployRMNRemote, sequences.ComponentDeps{
		StellarDeps:       deps,
		NetworkPassphrase: passphrase,
		DeployerAddress:   deployerKP.Address(),
		Ledger:            deployer,
	}, sequences.DeployRMNRemoteInput{
		ChainSelector: sel,
		WasmPath:      rmnWasm,
	})
	require.NoError(t, err)
	rmnID := rmnRep.Output.ContractID
	rmnClient := rmnbindings.NewRmnRemoteClient(deployer, rmnID)

	env := &helpers.E2ETestEnv{
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: passphrase,
		StellarRoot:       projectRoot,
	}
	govStack := helpers.DeployMCMSStackWithRoles(t, ctx, env, sel, cciputils.RMNTimelockQualifier, helpers.DefaultMCMSTimelockMinDelaySec)
	fastStack := helpers.DeployMCMSStackWithRoles(t, ctx, env, sel, cciputils.UltraFastCurseMCMSQualifier, helpers.DefaultMCMSTimelockMinDelaySec)

	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.RMNRemoteDatastoreRef().UpsertDeployedStrKey(ds, sel, rmnID))
	for _, ref := range govStackRefs(t, sel, cciputils.RMNTimelockQualifier, govStack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
	for _, ref := range govStackRefs(t, sel, cciputils.UltraFastCurseMCMSQualifier, fastStack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
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
		OperationsBundle: b,
	}

	cs := changesets.ActivateRMN(ccipchangesets.GetRegistry())
	input := ccipchangesets.WithMCMS[changesets.ActivateRMNConfig]{Cfg: changesets.ActivateRMNConfig{ChainSelector: sel}}
	require.NoError(t, cs.VerifyPreconditions(cldfEnv, input))
	out, err := cs.Apply(cldfEnv, input)
	require.NoError(t, err)

	admins, err := rmnClient.GetCurseAdmins(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{fastStack.TimelockID}, admins, "only the fast-curse timelock is a curse admin")
	pendingOwner, err := rmnClient.GetPendingOwner(ctx)
	require.NoError(t, err)
	require.NotNil(t, pendingOwner)
	require.Equal(t, govStack.TimelockID, *pendingOwner)

	require.Len(t, out.MCMSTimelockProposals, 1)
	proposal := out.MCMSTimelockProposals[0]
	cs64 := mcmstypes.ChainSelector(sel)
	require.Equal(t, mcmstypes.TimelockActionSchedule, proposal.Action)
	require.Equal(t, govStack.TimelockID, proposal.TimelockAddresses[cs64])
	require.Equal(t, govStack.ProposerMCMSID, proposal.ChainMetadata[cs64].MCMAddress)
	require.Len(t, proposal.Operations, 1)
	require.Len(t, proposal.Operations[0].Transactions, 1)
	tx := proposal.Operations[0].Transactions[0]
	require.Equal(t, rmnID, tx.To)
	fn, _, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
	require.NoError(t, err)
	require.Equal(t, "accept_ownership", fn)

	calls, err := helpers.TimelockCallsFromProposalTx(tx)
	require.NoError(t, err)
	var pred, salt [32]byte
	salt[31] = 1
	helpers.MCMSTimelockScheduleAndExecute(t, ctx, env, govStack, calls, pred, salt)

	owner, err := rmnClient.Owner(ctx)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, govStack.TimelockID, *owner)

	// A fresh bundle, as a new pipeline run would have: the first run's bundle
	// replays its recorded sequence reports for identical inputs.
	cldfEnv.OperationsBundle = newBundle(ctx, t)
	rerun, err := cs.Apply(cldfEnv, input)
	require.NoError(t, err)
	require.Empty(t, rerun.MCMSTimelockProposals, "a rerun after activation emits no proposal")
}
