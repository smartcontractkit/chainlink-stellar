package e2e_tests

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	chainsel "github.com/smartcontractkit/chain-selectors"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/smartcontractkit/mcms"
	"github.com/smartcontractkit/mcms/sdk"
	stellarsdk "github.com/smartcontractkit/mcms/sdk/stellar"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/adapters"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestStellarMCMSSDKFlow drives Stellar timelock proposals through the mcms
// SDK end to end (convert, sign, SetRoot, Execute, timelock execute) instead
// of the hand-built Merkle root in the governance helpers. Chain metadata
// comes from StellarMCMSReader, the reader the CLD changesets use. It covers
// schedule, bypass, cancel, and a proposal signed by a rotated signer set.
func TestStellarMCMSSDKFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase := helpers.SetupTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := &helpers.E2ETestEnv{
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: passphrase,
		StellarRoot:       projectRoot,
	}
	stack := helpers.DeployMCMSStackWithRoles(t, ctx, env, sel, cciputils.CLLQualifier, helpers.DefaultMCMSTimelockMinDelaySec)

	ds := datastore.NewMemoryDataStore()
	for _, ref := range govStackRefs(t, sel, stack.Qualifier, stack) {
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
	signer := bindings.NewStellarKeypairSigner(deployerKP)
	inspector, err := stellarsdk.NewInspectorWithNetworkPassphrase(rpcClient, signer, passphrase)
	require.NoError(t, err)
	timelockExecutor, err := stellarsdk.NewTimelockExecutorWithNetworkPassphrase(rpcClient, signer, passphrase, deployerKP.Address())
	require.NoError(t, err)
	f := &sdkFlow{
		t:     t,
		ctx:   ctx,
		sel:   mcmstypes.ChainSelector(sel),
		stack: stack,
		cldfEnv: cldf.Environment{
			Logger:     cldflogger.Test(t),
			GetContext: func() context.Context { return ctx },
			DataStore:  ds.Seal(),
			BlockChains: cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: cldf_stellar.Chain{
				ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
				Signer:            signer,
				Client:            rpcClient,
				NetworkPassphrase: passphrase,
			}}),
		},
		inspector:        inspector,
		timelockExecutor: timelockExecutor,
	}
	tl := stack.TimelockClient
	initialKey := stack.SignerPK

	// Schedule: proposer MCMS schedules update_delay, the timelock executes it
	// after the delay.
	schedule := f.proposal(mcmstypes.TimelockActionSchedule, 3, f.timelockSelfCall("update_delay", scval.Uint64ToScVal(4)))
	f.setRootAndExecute(schedule, initialKey)
	f.executeTimelock(schedule)
	delay, err := tl.GetMinDelay(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(4), delay)

	// Bypass: bypasser MCMS runs block_function with no delay.
	blockedFn := "e2e_blocked_fn"
	bypass := f.proposal(mcmstypes.TimelockActionBypass, 0, f.timelockSelfCall("block_function",
		scval.AddressToScVal(stack.BypasserMCMSID), scval.SymbolToScVal(blockedFn)))
	f.setRootAndExecute(bypass, initialKey)
	blocked, err := tl.IsFunctionBlocked(ctx, stack.BypasserMCMSID, blockedFn)
	require.NoError(t, err)
	require.True(t, blocked)

	// Cancel: schedule an update_delay, then cancel it through the canceller MCMS.
	toCancel := f.proposal(mcmstypes.TimelockActionSchedule, 4, f.timelockSelfCall("update_delay", scval.Uint64ToScVal(60)))
	f.setRootAndExecute(toCancel, initialKey)
	opID, err := toCancel.OperationID(ctx, 0)
	require.NoError(t, err)
	pending, err := tl.IsOperationPending(ctx, [32]byte(opID))
	require.NoError(t, err)
	require.True(t, pending)
	cancellation, err := toCancel.DeriveCancellationProposal(map[mcmstypes.ChainSelector]mcmstypes.ChainMetadata{
		f.sel: f.metadata(mcmstypes.TimelockActionCancel),
	})
	require.NoError(t, err)
	f.setRootAndExecute(&cancellation, initialKey)
	exists, err := tl.IsOperation(ctx, [32]byte(opID))
	require.NoError(t, err)
	require.False(t, exists, "cancelled operation must be removed")
	delay, err = tl.GetMinDelay(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(4), delay, "cancelled update_delay must not run")

	// Rotation: the timelock runs set_config on the proposer MCMS, which bumps
	// its config version. The next proposal must be signed by the new key and
	// carry the new config version in its metadata.
	versionBefore, err := stack.MCMSClient.GetConfigVersion(ctx)
	require.NoError(t, err)
	rotatedKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	rotate := f.proposal(mcmstypes.TimelockActionSchedule, 4, f.setConfigCall(stack.ProposerMCMSID, rotatedKey))
	f.setRootAndExecute(rotate, initialKey)
	f.executeTimelock(rotate)
	versionAfter, err := stack.MCMSClient.GetConfigVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, versionBefore+1, versionAfter)

	afterRotation := f.proposal(mcmstypes.TimelockActionSchedule, 4, f.timelockSelfCall("update_delay", scval.Uint64ToScVal(3)))
	var fields struct {
		ConfigVersion uint64 `json:"configVersion"`
	}
	require.NoError(t, json.Unmarshal(afterRotation.ChainMetadata[f.sel].AdditionalFields, &fields))
	require.Equal(t, versionAfter, fields.ConfigVersion)
	f.setRootAndExecute(afterRotation, rotatedKey)
	f.executeTimelock(afterRotation)
	delay, err = tl.GetMinDelay(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(3), delay)
}

type sdkFlow struct {
	t                *testing.T
	ctx              context.Context
	sel              mcmstypes.ChainSelector
	stack            *helpers.MCMSGovernanceStack
	cldfEnv          cldf.Environment
	inspector        *stellarsdk.Inspector
	timelockExecutor *stellarsdk.TimelockExecutor
}

// metadata reads chain metadata for the MCMS that runs action. Each read gets
// a fresh operations bundle: a shared bundle replays the first get_op_count
// report for every later proposal.
func (f *sdkFlow) metadata(action mcmstypes.TimelockAction) mcmstypes.ChainMetadata {
	f.t.Helper()
	e := f.cldfEnv
	e.OperationsBundle = newBundle(f.ctx, f.t)
	md, err := adapters.StellarMCMSReader{}.GetChainMetadata(e, uint64(f.sel), mcmsutils.Input{
		Qualifier:      f.stack.Qualifier,
		TimelockAction: action,
	})
	require.NoError(f.t, err)
	return md
}

func (f *sdkFlow) proposal(action mcmstypes.TimelockAction, delaySec uint64, tx mcmstypes.Transaction) *mcms.TimelockProposal {
	f.t.Helper()
	return &mcms.TimelockProposal{
		BaseProposal: mcms.BaseProposal{
			Version:       "v1",
			Kind:          mcmstypes.KindTimelockProposal,
			Description:   "Stellar mcms SDK flow " + string(action),
			ValidUntil:    uint32(time.Now().Add(time.Hour).Unix()),
			Signatures:    []mcmstypes.Signature{},
			ChainMetadata: map[mcmstypes.ChainSelector]mcmstypes.ChainMetadata{f.sel: f.metadata(action)},
		},
		Action:            action,
		Delay:             mcmstypes.NewDuration(time.Duration(delaySec) * time.Second),
		TimelockAddresses: map[mcmstypes.ChainSelector]string{f.sel: f.stack.TimelockID},
		Operations: []mcmstypes.BatchOperation{{
			ChainSelector: f.sel,
			Transactions:  []mcmstypes.Transaction{tx},
		}},
	}
}

func (f *sdkFlow) timelockSelfCall(function string, args ...xdr.ScVal) mcmstypes.Transaction {
	f.t.Helper()
	tx, err := stellarsdk.NewTransaction(f.stack.TimelockID, function,
		append([]xdr.ScVal{scval.AddressToScVal(f.stack.TimelockID)}, args...), string(cciputils.RBACTimelock), nil)
	require.NoError(f.t, err)
	return tx
}

func (f *sdkFlow) setConfigCall(mcmsID string, signerKey *ecdsa.PrivateKey) mcmstypes.Transaction {
	f.t.Helper()
	cfg := mcmstypes.Config{Quorum: 1, Signers: []common.Address{crypto.PubkeyToAddress(signerKey.PublicKey)}}
	addrs, groups, quorums, parents, clearRoot, err := mcmsutil.ConfigToStellarSetConfig(&cfg, false)
	require.NoError(f.t, err)
	addrsVal, err := addrs.ToScVal()
	require.NoError(f.t, err)
	groupsVal, err := groups.ToScVal()
	require.NoError(f.t, err)
	tx, err := stellarsdk.NewTransaction(mcmsID, "set_config", []xdr.ScVal{
		addrsVal,
		groupsVal,
		scval.Bytes32ToScVal(quorums),
		scval.Bytes32ToScVal(parents),
		scval.BoolToScVal(clearRoot),
	}, mcmsops.ContractType, nil)
	require.NoError(f.t, err)
	return tx
}

// setRootAndExecute converts the timelock proposal, signs it with key, sets
// the root on the MCMS named in its metadata and executes every operation.
func (f *sdkFlow) setRootAndExecute(tp *mcms.TimelockProposal, key *ecdsa.PrivateKey) {
	f.t.Helper()
	proposal, _, err := tp.Convert(f.ctx, map[mcmstypes.ChainSelector]sdk.TimelockConverter{f.sel: stellarsdk.NewTimelockConverter()})
	require.NoError(f.t, err)

	signable, err := mcms.NewSignable(&proposal, map[mcmstypes.ChainSelector]sdk.Inspector{f.sel: f.inspector})
	require.NoError(f.t, err)
	require.NoError(f.t, signable.ValidateConfigs(f.ctx))
	_, err = signable.SignAndAppend(mcms.NewPrivateKeySigner(key))
	require.NoError(f.t, err)
	quorumMet, err := signable.ValidateSignatures(f.ctx)
	require.NoError(f.t, err)
	require.True(f.t, quorumMet)

	encoders, err := proposal.GetEncoders()
	require.NoError(f.t, err)
	encoder, ok := encoders[f.sel].(*stellarsdk.Encoder)
	require.True(f.t, ok, "proposal encoder is not a Stellar encoder")
	executor, err := stellarsdk.NewExecutor(encoder, f.inspector)
	require.NoError(f.t, err)
	executable, err := mcms.NewExecutable(&proposal, map[mcmstypes.ChainSelector]sdk.Executor{f.sel: executor})
	require.NoError(f.t, err)

	_, err = executable.SetRoot(f.ctx, f.sel)
	require.NoError(f.t, err)
	for i := range proposal.Operations {
		_, err = executable.Execute(f.ctx, i)
		require.NoError(f.t, err)
	}
}

// executeTimelock waits until the scheduled batch is ready by ledger time and
// runs execute_batch on the timelock.
func (f *sdkFlow) executeTimelock(tp *mcms.TimelockProposal) {
	f.t.Helper()
	executable, err := mcms.NewTimelockExecutable(f.ctx, tp, map[mcmstypes.ChainSelector]sdk.TimelockExecutor{f.sel: f.timelockExecutor})
	require.NoError(f.t, err)
	require.EventuallyWithT(f.t, func(c *assert.CollectT) {
		assert.NoError(c, executable.IsReady(f.ctx))
	}, time.Minute, 500*time.Millisecond, "timelock operation never became ready")
	_, err = executable.Execute(f.ctx, 0)
	require.NoError(f.t, err)
}
