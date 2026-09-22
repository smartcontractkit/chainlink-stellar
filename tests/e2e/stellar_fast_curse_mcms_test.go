package e2e_tests

import (
	"context"
	"fmt"
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
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"

	api "github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	rmnbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	routerdeployops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// fastCurseSubjectFor returns an inert firedrill-style subject keyed by tag:
// no lane derives these bytes, so cursing them exercises the full MCMS path
// without touching a real lane. Distinct tags give distinct subjects so each
// curse arm can assert its own false → true transition.
func fastCurseSubjectFor(tag byte) api.Subject {
	s := api.FiredrillSubject()
	s[1] = tag
	return s
}

// TestStellarFastCurseViaMCMS exercises the full fast-curse governance flow on a
// standalone Stellar localnet: two qualifier-keyed MCMS stacks (RMNMCMS owner,
// UltraFastCurse curse-admin), activation, bypasser-path curses through both
// stacks, owner-only uncurse, role separation, and the fail-closed error.
func TestStellarFastCurseViaMCMS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase := helpers.SetupTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector

	b := cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
	deps := stellardeps.FromDeployer(deployer)
	deployerAddr := deployer.SignerAddress()

	// ① Fresh RMN Remote with no curse admins.
	rmnWasm := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	rmnSalt := stellardeployment.GenerateDeterministicSalt(deployerKP.Address(), "e2e-fast-curse-rmn")
	rmnOut, err := cldfops.ExecuteOperation(b, rmnremoteops.Deploy, deps, stellarops.DeployInput{WasmPath: rmnWasm, Salt: rmnSalt})
	require.NoError(t, err)
	rmnID := rmnOut.Output.ContractID
	_, err = cldfops.ExecuteOperation(b, rmnremoteops.Initialize, deps, rmnremoteops.InitializeInput{
		ContractID:  rmnID,
		Owner:       deployerAddr,
		CurseAdmins: nil,
	})
	require.NoError(t, err)
	rmnClient := rmnbindings.NewRmnRemoteClient(deployer, rmnID)

	// ② Both MCMS stacks via the real deploy sequence.
	env := &helpers.E2ETestEnv{
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: passphrase,
		StellarRoot:       projectRoot,
	}
	govStack := helpers.DeployMCMSStackWithRoles(t, ctx, env, sel, cciputils.RMNTimelockQualifier, helpers.DefaultMCMSTimelockMinDelaySec)
	fastStack := helpers.DeployMCMSStackWithRoles(t, ctx, env, sel, cciputils.UltraFastCurseMCMSQualifier, helpers.DefaultMCMSTimelockMinDelaySec)

	// Record refs for the activation sequence and the curse adapter.
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.RMNRemoteDatastoreRef().UpsertDeployedStrKey(ds, sel, rmnID))
	routerID, err := deployTestRouter(t, b, deps, deployerKP, projectRoot)
	require.NoError(t, err)
	require.NoError(t, stellarccip.RecordRouter(ds, sel, routerID))
	for _, ref := range govStackRefs(t, sel, cciputils.RMNTimelockQualifier, govStack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
	for _, ref := range govStackRefs(t, sel, cciputils.UltraFastCurseMCMSQualifier, fastStack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}

	stellarChain := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            bindings.NewStellarKeypairSigner(deployerKP),
		Client:            rpcClient,
		NetworkPassphrase: passphrase,
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: stellarChain})
	adapterEnv := cldf.Environment{
		Logger:      cldflogger.Test(t),
		GetContext:  func() context.Context { return ctx },
		DataStore:   ds.Seal(),
		BlockChains: chains,
	}

	// ③ Activate: curse-admin grant to the fast timelock only (owner-implicit),
	// ownership transfer proposed to the governance timelock.
	activateOut, err := cldfops.ExecuteSequence(b, sequences.StellarActivateRMN, chains, sequences.StellarActivateRMNInput{
		ChainSelector:     sel,
		ExistingAddresses: ds.Addresses().Filter(),
		SubjectsToMigrate: nil,
	})
	require.NoError(t, err)
	for _, op := range activateOut.Output.BatchOps {
		require.Empty(t, op.Transactions, "deployer-owned activation must be direct: no proposal transactions")
	}
	admins, err := rmnClient.GetCurseAdmins(ctx)
	require.NoError(t, err)
	require.Len(t, admins, 1, "owner-implicit grant: exactly the fast timelock is listed, got %v", admins)
	require.Equal(t, fastStack.TimelockID, admins[0])
	require.NotEqual(t, govStack.TimelockID, admins[0], "governance timelock must never be listed as curse admin")

	pendingOwner, err := rmnClient.GetPendingOwner(ctx)
	require.NoError(t, err)
	require.NotNil(t, pendingOwner)
	require.Equal(t, govStack.TimelockID, *pendingOwner, "ownership must be pending the governance timelock")

	// ④ Governed accept_ownership completes the transfer (schedule path, min delay).
	acceptArgs, err := helpers.EncodeTimelockCallArgs(nil)
	require.NoError(t, err)
	var pred, acceptSalt [32]byte
	acceptSalt[31] = 7
	helpers.MCMSTimelockScheduleAndExecute(t, ctx, env, govStack, timelockCallsFor(rmnID, "accept_ownership", acceptArgs), pred, acceptSalt)

	owner, err := rmnClient.Owner(ctx)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, govStack.TimelockID, *owner, "RMN must be owned by the governance timelock after accept")

	// The adapter now routes: owner=govTL (from chain), admins=[fastTL], both timelocks cached.
	curseAdapter := adapters.NewStellarCurseAdapter()
	require.NoError(t, curseAdapter.Initialize(adapterEnv, sel))
	govSubject := fastCurseSubjectFor(1)
	fastSubject := fastCurseSubjectFor(2)

	// ⑤ Regular curse via the governance stack's bypasser: the ≤2-minute path.
	govProposal := curseViaAdapter(t, ctx, curseAdapter, chains, sel, govSubject, cciputils.RMNTimelockQualifier)
	require.Len(t, govProposal, 1)
	govCalls, err := helpers.TimelockCallsFromProposalTx(govProposal[0])
	require.NoError(t, err)
	require.Equal(t, rmnID, govCalls.Inner[0].Target)
	fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(govProposal[0].Data)
	require.NoError(t, err)
	require.Equal(t, "curse", fn)
	require.Len(t, args, 2)
	govCaller, err := scval.AddressFromScVal(args[0])
	require.NoError(t, err)
	require.Equal(t, govStack.TimelockID, govCaller, "caller must be the executing (governance) timelock")

	cursed, err := rmnClient.IsCursedBySubject(ctx, govSubject)
	require.NoError(t, err)
	require.False(t, cursed, "governance-arm subject must start uncursed")
	helpers.MCMSBypassAndExecute(t, ctx, env, govStack, govCalls)
	cursed, err = rmnClient.IsCursedBySubject(ctx, govSubject)
	require.NoError(t, err)
	require.True(t, cursed, "governance-arm subject must be cursed after the governance bypasser path")

	// ⑥ Ultra fast curse via the fast stack's bypasser — the whole point of the
	// third stack: same fully-wired RMN, different qualifier. A distinct subject
	// makes the assertion a real transition test: curse silently skips
	// already-cursed subjects, so reusing the ⑤ subject could never fail here.
	fastProposal := curseViaAdapter(t, ctx, curseAdapter, chains, sel, fastSubject, cciputils.UltraFastCurseMCMSQualifier)
	require.Len(t, fastProposal, 1)
	fastCalls, err := helpers.TimelockCallsFromProposalTx(fastProposal[0])
	require.NoError(t, err)
	fn, args, err = mcmsutil.DecodeSorobanMCMSInvokePayload(fastProposal[0].Data)
	require.NoError(t, err)
	require.Equal(t, "curse", fn)
	fastCaller, err := scval.AddressFromScVal(args[0])
	require.NoError(t, err)
	require.Equal(t, fastStack.TimelockID, fastCaller, "caller must be the fast-curse timelock")

	cursed, err = rmnClient.IsCursedBySubject(ctx, fastSubject)
	require.NoError(t, err)
	require.False(t, cursed, "ultra-fast-arm subject must start uncursed")
	helpers.MCMSBypassAndExecute(t, ctx, env, fastStack, fastCalls)
	cursed, err = rmnClient.IsCursedBySubject(ctx, fastSubject)
	require.NoError(t, err)
	require.True(t, cursed, "ultra-fast-arm subject must be cursed after the fast bypasser path")

	// ⑧ Uncurse via the governance path (schedule + wait + execute).
	uncurseProposal := uncurseViaAdapter(t, ctx, curseAdapter, chains, sel, govSubject, cciputils.RMNTimelockQualifier)
	require.Len(t, uncurseProposal, 1)
	fn, args, err = mcmsutil.DecodeSorobanMCMSInvokePayload(uncurseProposal[0].Data)
	require.NoError(t, err)
	require.Equal(t, "uncurse", fn)
	require.Len(t, args, 1, "uncurse takes no caller argument")
	uncurseCalls, err := helpers.TimelockCallsFromProposalTx(uncurseProposal[0])
	require.NoError(t, err)
	var uncPred, uncSalt [32]byte
	uncSalt[31] = 11
	helpers.MCMSTimelockScheduleAndExecute(t, ctx, env, govStack, uncurseCalls, uncPred, uncSalt)

	cursed, err = rmnClient.IsCursedBySubject(ctx, govSubject)
	require.NoError(t, err)
	require.False(t, cursed, "governance-arm subject must be uncursed after the governance path")

	// ⑦ Role separation: the proposer cannot bypass, the bypasser cannot schedule,
	// and the fast timelock cannot uncurse (owner-only).
	//
	// Deliberately runs AFTER ⑧ despite the numbering: every negative below drives
	// a SetRoot whose execute is then rejected, and a rejected execute after a
	// successful SetRoot leaves an active unexecuted root on that MCMS instance —
	// the next SetRoot on it (override_previous_root: false, as all helpers here
	// use) is rejected too. ⑧'s uncurse still needs a clean SetRoot on the
	// governance stack, so it must run first; nothing below this block reuses
	// these instances, so the negatives can safely leave the roots dirty.
	proposerAsBypasser := *govStack
	proposerAsBypasser.BypasserMCMSID = govStack.ProposerMCMSID
	proposerAsBypasser.BypasserClient = govStack.MCMSClient
	require.Error(t, helpers.MCMSBypassAndExecuteErr(ctx, env, &proposerAsBypasser, fastCalls),
		"proposer MCMS must not be able to bypasser_execute_batch")

	bypasserAsProposer := *govStack
	bypasserAsProposer.MCMSID = govStack.BypasserMCMSID
	bypasserAsProposer.MCMSClient = govStack.BypasserClient
	var schedPred, schedSalt [32]byte
	schedSalt[31] = 9
	require.Error(t, helpers.MCMSTimelockScheduleAndExecuteErr(ctx, env, &bypasserAsProposer, fastCalls, schedPred, schedSalt),
		"bypasser MCMS must not be able to schedule_batch")

	uncurseArgs, err := helpers.EncodeTimelockCallArgs([]xdr.ScVal{scval.Bytes16SliceToScVal([][16]byte{fastSubject})})
	require.NoError(t, err)
	fastUncurse := timelockCallsFor(rmnID, "uncurse", uncurseArgs)
	require.Error(t, helpers.MCMSBypassAndExecuteErr(ctx, env, fastStack, fastUncurse),
		"fast timelock is not the RMN owner: uncurse must fail require_owner")

	// ⑨ Fail closed: an RMN owned by a stranger with no admins refuses to route.
	strangerKP := keypair.MustRandom()
	strangerSalt := stellardeployment.GenerateDeterministicSalt(deployerKP.Address(), "e2e-fast-curse-stranger-rmn")
	strangerOut, err := cldfops.ExecuteOperation(b, rmnremoteops.Deploy, deps, stellarops.DeployInput{WasmPath: rmnWasm, Salt: strangerSalt})
	require.NoError(t, err)
	_, err = cldfops.ExecuteOperation(b, rmnremoteops.Initialize, deps, rmnremoteops.InitializeInput{
		ContractID:  strangerOut.Output.ContractID,
		Owner:       strangerKP.Address(),
		CurseAdmins: nil,
	})
	require.NoError(t, err)

	strangerEnv := cldf.Environment{
		Logger:      cldflogger.Test(t),
		GetContext:  func() context.Context { return ctx },
		DataStore:   strangerDatastore(t, sel, strangerOut.Output.ContractID, routerID, govStack, fastStack).Seal(),
		BlockChains: chains,
	}
	strangerAdapter := adapters.NewStellarCurseAdapter()
	require.NoError(t, strangerAdapter.Initialize(strangerEnv, sel))
	_, err = executeCurseSequence(t, ctx, strangerAdapter, chains, sel, fastSubject, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no authorized curse caller")
}

// --- e2e-local helpers ---

func deployTestRouter(t *testing.T, b cldfops.Bundle, deps stellardeps.StellarDeps, deployerKP *keypair.Full, projectRoot string) (string, error) {
	t.Helper()
	routerWasm := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "router.wasm")
	routerSalt := stellardeployment.GenerateDeterministicSalt(deployerKP.Address(), "e2e-fast-curse-router")
	out, err := cldfops.ExecuteOperation(b, routerdeployops.Deploy, deps, stellarops.DeployInput{WasmPath: routerWasm, Salt: routerSalt})
	if err != nil {
		return "", err
	}
	return out.Output.ContractID, nil
}

func govStackRefs(t *testing.T, sel uint64, qualifier string, stack *helpers.MCMSGovernanceStack) []datastore.AddressRef {
	t.Helper()
	roles := []mcmsutil.MCMSRole{mcmsutil.RoleProposer, mcmsutil.RoleCanceller, mcmsutil.RoleBypasser}
	ids := map[mcmsutil.MCMSRole]string{
		mcmsutil.RoleProposer:  stack.ProposerMCMSID,
		mcmsutil.RoleCanceller: stack.CancellerMCMSID,
		mcmsutil.RoleBypasser:  stack.BypasserMCMSID,
	}
	refs := []datastore.AddressRef{mcmsutil.StellarTimelockDatastoreRef(sel, qualifier, stack.TimelockID)}
	for _, role := range roles {
		ref, err := mcmsutil.StellarMCMSRoleDatastoreRef(sel, qualifier, role, ids[role])
		require.NoError(t, err)
		refs = append(refs, ref)
	}
	return refs
}

func strangerDatastore(t *testing.T, sel uint64, rmnID, routerID string, govStack, fastStack *helpers.MCMSGovernanceStack) *datastore.MemoryDataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.RMNRemoteDatastoreRef().UpsertDeployedStrKey(ds, sel, rmnID))
	require.NoError(t, stellarccip.RecordRouter(ds, sel, routerID))
	for _, ref := range govStackRefs(t, sel, cciputils.RMNTimelockQualifier, govStack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
	for _, ref := range govStackRefs(t, sel, cciputils.UltraFastCurseMCMSQualifier, fastStack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
	return ds
}

func timelockCallsFor(target, function string, argsXdr []byte) timelockbindings.Calls {
	return timelockbindings.Calls{Inner: []timelockbindings.Call{{
		Target:   target,
		Function: function,
		ArgsXdr:  argsXdr,
	}}}
}

func curseViaAdapter(t *testing.T, ctx context.Context, a *adapters.StellarCurseAdapter, chains cldf_chain.BlockChains, sel uint64, subject api.Subject, qualifier string) []mcmstypes.Transaction {
	t.Helper()
	ops, err := executeCurseSequence(t, ctx, a, chains, sel, subject, qualifier)
	require.NoError(t, err)
	return ops
}

func uncurseViaAdapter(t *testing.T, ctx context.Context, a *adapters.StellarCurseAdapter, chains cldf_chain.BlockChains, sel uint64, subject api.Subject, qualifier string) []mcmstypes.Transaction {
	t.Helper()
	report, err := cldfops.ExecuteSequence(newBundle(ctx, t), a.Uncurse(), chains, api.CurseInput{
		Subjects:      []api.Subject{subject},
		ChainSelector: sel,
		MCMSQualifier: qualifier,
	})
	require.NoError(t, err)
	require.Len(t, report.Output.BatchOps, 1)
	require.Len(t, report.Output.BatchOps[0].Transactions, 1)
	return report.Output.BatchOps[0].Transactions
}

func executeCurseSequence(t *testing.T, ctx context.Context, a *adapters.StellarCurseAdapter, chains cldf_chain.BlockChains, sel uint64, subject api.Subject, qualifier string) ([]mcmstypes.Transaction, error) {
	t.Helper()
	report, err := cldfops.ExecuteSequence(newBundle(ctx, t), a.Curse(), chains, api.CurseInput{
		Subjects:      []api.Subject{subject},
		ChainSelector: sel,
		MCMSQualifier: qualifier,
	})
	if err != nil {
		return nil, err
	}
	if len(report.Output.BatchOps) != 1 || len(report.Output.BatchOps[0].Transactions) != 1 {
		// Guard the index: with zero batch ops the naive message would panic with
		// index-out-of-range instead of reporting the routing regression.
		txCount := 0
		if len(report.Output.BatchOps) == 1 {
			txCount = len(report.Output.BatchOps[0].Transactions)
		}
		return nil, fmt.Errorf("expected exactly one proposal transaction, got %d ops / %d txs",
			len(report.Output.BatchOps), txCount)
	}
	return report.Output.BatchOps[0].Transactions, nil
}

func newBundle(ctx context.Context, t *testing.T) cldfops.Bundle {
	t.Helper()
	return cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
}
