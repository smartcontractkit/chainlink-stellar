package helpers

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	lrpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/lock_release_pool"
	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	lrpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	timelockops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/timelock"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const DefaultMCMSTimelockMinDelaySec uint64 = 3

// MCMSGovernanceStack holds deployed MCMS + timelock contracts wired for MCMS-mediated governance.
// Deployed via the real DeployStellarMCMS sequence (DeployMCMSStackWithRoles), it also
// carries the role-specific MCMS instances; the legacy DeployMCMSAndTimelock stack leaves
// the role fields unset.
type MCMSGovernanceStack struct {
	MCMSID         string
	TimelockID     string
	MCMSClient     *mcmsbindings.McmsClient
	TimelockClient *timelockbindings.TimelockClient
	MCMSRaw        [32]byte
	TimelockRaw    [32]byte
	ChainNetID     [32]byte
	SignerPK       *ecdsa.PrivateKey
	MinDelaySec    uint64

	// Qualifier labels the MCMS stack this governance set was deployed for.
	Qualifier string
	// ProposerMCMSID / CancellerMCMSID / BypasserMCMSID are the role-specific
	// MCMS instances (strkeys); the legacy single-instance stack sets them to MCMSID.
	ProposerMCMSID  string
	CancellerMCMSID string
	BypasserMCMSID  string
	// BypasserClient signs bypasser roots for the no-delay execution path.
	BypasserClient *mcmsbindings.McmsClient
}

// ContractIDToBytes32 decodes a Soroban contract strkey into a 32-byte contract id.
func ContractIDToBytes32(contractID string) ([32]byte, error) {
	var out [32]byte
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return out, err
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("contract id raw length %d, want 32", len(raw))
	}
	copy(out[:], raw)
	return out, nil
}

// SorobanScheduleBatch encodes the timelock schedule_batch arguments as StellarOp.ArgsXdr
// (args-only XDR Vec<Val>; the function name lives in StellarOp.Function).
func SorobanScheduleBatch(
	caller string,
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
	delay uint64,
) ([]byte, error) {
	callsVal, err := calls.ToScVal()
	if err != nil {
		return nil, err
	}
	return mcmsutil.EncodeSorobanInvokeArgs([]xdr.ScVal{
		scval.AddressToScVal(caller),
		callsVal,
		scval.Bytes32ToScVal(predecessor),
		scval.Bytes32ToScVal(salt),
		scval.Uint64ToScVal(delay),
	})
}

// SorobanExecuteBatch encodes the timelock execute_batch arguments as StellarOp.ArgsXdr
// (args-only XDR Vec<Val>). execute_batch is permissionless and takes no caller argument.
func SorobanExecuteBatch(
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
) ([]byte, error) {
	callsVal, err := calls.ToScVal()
	if err != nil {
		return nil, err
	}
	return mcmsutil.EncodeSorobanInvokeArgs([]xdr.ScVal{
		callsVal,
		scval.Bytes32ToScVal(predecessor),
		scval.Bytes32ToScVal(salt),
	})
}

// MCMSValidUntilSeconds returns a deadline for MCMS set_root: must be >= host ledger timestamp
// and ≤ now + 90d (contracts/mcms MAX_ROOT_VALIDITY_SECS).
func MCMSValidUntilSeconds(ctx context.Context, rpc *rpcclient.Client) (uint32, error) {
	latest, err := rpc.GetLatestLedger(ctx)
	if err != nil {
		return 0, fmt.Errorf("GetLatestLedger: %w", err)
	}
	now := latest.LedgerCloseTime
	if now < 0 {
		return 0, fmt.Errorf("unexpected negative ledger close time: %d", now)
	}
	const marginSec int64 = 90 * 24 * 3600 // must stay ≤ MCMS MAX_ROOT_VALIDITY_SECS (contracts/mcms)
	sum := now + marginSec
	maxU32 := int64(^uint32(0))
	if sum > maxU32 {
		return ^uint32(0), nil
	}
	return uint32(sum), nil
}

// DeployMCMSAndTimelock deploys and initializes MCMS + RBAC timelock via CLDF operations
// (mcmsops/timelockops Deploy and Initialize), using mcmsutil WASM paths and deploy salts.
//
// MCMS is the timelock PROPOSER so MCMS.execute can drive schedule_batch; execute_batch is
// permissionless (same wiring as integration TestMcmsMerkleTimelockScheduleAndExecute).
func DeployMCMSAndTimelock(
	t *testing.T,
	ctx context.Context,
	env *E2ETestEnv,
	chainSelector uint64,
	qualifier string,
) *MCMSGovernanceStack {
	t.Helper()

	pk, err := crypto.HexToECDSA(Anvil0SKHex)
	require.NoError(t, err)

	chainNetID := mcmsutil.ChainNetworkID(env.NetworkPassphrase)

	projectRoot := FindProjectRoot(t)
	mcmsWasm := filepath.Join(projectRoot, mcmsutil.DefaultMCMSWasmRelative)
	tlWasm := filepath.Join(projectRoot, mcmsutil.DefaultTimelockWasmRelative)

	bundle := cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
	deps := stellardeps.FromDeployer(env.Deployer)

	mcmsDep, err := cldfops.ExecuteOperation(bundle, mcmsops.Deploy, deps, stellarops.DeployInput{
		WasmPath: mcmsWasm,
		Salt:     mcmsutil.MCMSRoleDeploySalt(chainSelector, qualifier, mcmsutil.RoleProposer),
	})
	require.NoError(t, err)
	mcmsID := mcmsDep.Output.ContractID

	var groupQuorums [32]byte
	groupQuorums[0] = 1
	var groupParents [32]byte
	paddedSigner := PaddedEthAddress(&pk.PublicKey)
	// initialize applies the signer config atomically; no separate set_config needed.
	_, err = cldfops.ExecuteOperation(bundle, mcmsops.Initialize, deps, mcmsops.InitializeInput{
		ContractID:      mcmsID,
		Owner:           env.DeployerKP.Address(),
		ChainNetworkID:  chainNetID,
		SignerAddresses: mcmsbindings.SignerAddresses{Inner: [][32]byte{paddedSigner}},
		SignerGroups:    mcmsbindings.SignerGroups{Inner: []uint32{0}},
		GroupQuorums:    groupQuorums,
		GroupParents:    groupParents,
		InstanceLabel:   "PROPOSER",
	})
	require.NoError(t, err)

	tlDep, err := cldfops.ExecuteOperation(bundle, timelockops.Deploy, deps, stellarops.DeployInput{
		WasmPath: tlWasm,
		Salt:     mcmsutil.TimelockDeploySalt(chainSelector, qualifier),
	})
	require.NoError(t, err)
	tlID := tlDep.Output.ContractID

	minDelay := DefaultMCMSTimelockMinDelaySec
	_, err = cldfops.ExecuteOperation(bundle, timelockops.Initialize, deps, timelockops.InitializeInput{
		ContractID: tlID,
		MinDelay:   minDelay,
		Proposers:  []string{mcmsID},
		Cancellers: []string{},
		Bypassers:  []string{},
	})
	require.NoError(t, err)

	mcmsRaw, err := ContractIDToBytes32(mcmsID)
	require.NoError(t, err)
	tlRaw, err := ContractIDToBytes32(tlID)
	require.NoError(t, err)

	return &MCMSGovernanceStack{
		MCMSID:         mcmsID,
		TimelockID:     tlID,
		MCMSClient:     mcmsbindings.NewMcmsClient(env.Deployer, mcmsID),
		TimelockClient: timelockbindings.NewTimelockClient(env.Deployer, tlID),
		MCMSRaw:        mcmsRaw,
		TimelockRaw:    tlRaw,
		ChainNetID:     chainNetID,
		SignerPK:       pk,
		MinDelaySec:    minDelay,
	}
}

// EncodeTimelockCallArgs builds timelock Call.ArgsXdr for a Soroban contract function
// invocation (args-only XDR Vec<Val>; the function name lives in Call.Function).
func EncodeTimelockCallArgs(argScVals []xdr.ScVal) ([]byte, error) {
	return mcmsutil.EncodeSorobanInvokeArgs(argScVals)
}

// CleanupMCMSTestPool restores the shared devenv lock-release pool after MCMS e2e tests.
// Assumes the timelock owns the pool and outbound rate limits are enabled: transfers ownership
// back to the deployer via MCMS, deployer accept_ownership, then disables rate limits directly.
// Errors are logged but do not fail the test (runs from t.Cleanup).
func CleanupMCMSTestPool(
	t *testing.T,
	ctx context.Context,
	env *E2ETestEnv,
	gov *MCMSGovernanceStack,
	poolContractID string,
	remoteSelector uint64,
	deployerAddr string,
	predecessor, saltTransfer [32]byte,
) {
	t.Helper()
	if gov == nil {
		return
	}

	bundle := cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
	deps := stellardeps.FromDeployer(env.Deployer)

	transferArgs, err := EncodeTimelockCallArgs([]xdr.ScVal{
		scval.AddressToScVal(deployerAddr),
	})
	if err != nil {
		t.Logf("mcms pool cleanup: encode transfer_ownership: %v", err)
		return
	}
	transferCalls := timelockbindings.Calls{
		Inner: []timelockbindings.Call{{Target: poolContractID, Function: "transfer_ownership", ArgsXdr: transferArgs}},
	}
	if err := MCMSTimelockScheduleAndExecuteErr(ctx, env, gov, transferCalls, predecessor, saltTransfer); err != nil {
		t.Logf("mcms pool cleanup: transfer ownership to deployer via MCMS: %v", err)
		return
	}
	if _, err := cldfops.ExecuteOperation(bundle, lrpops.AcceptOwnership, deps, lrpops.AcceptOwnershipInput{
		ContractID: poolContractID,
	}); err != nil {
		t.Logf("mcms pool cleanup: deployer accept_ownership: %v", err)
		return
	}
	if _, err := cldfops.ExecuteOperation(bundle, lrpops.SetRateLimitConfig, deps, lrpops.SetRateLimitConfigInput{
		ContractID:          poolContractID,
		RemoteChainSelector: remoteSelector,
		FastFinality:        false,
	}); err != nil {
		t.Logf("mcms pool cleanup: disable rate limit: %v", err)
		return
	}

	stateAfter, err := cldfops.ExecuteOperation(bundle, lrpops.GetCurrentRateLimiterState, deps, lrpops.GetCurrentRateLimiterStateInput{
		ContractID:          poolContractID,
		RemoteChainSelector: remoteSelector,
		FastFinality:        false,
	})
	if err != nil {
		t.Logf("mcms pool cleanup: verify rate limiter state: %v", err)
		return
	}
	if stateAfter.Output.State != nil && stateAfter.Output.State.Outbound.IsEnabled {
		t.Log("mcms pool cleanup: warning: outbound rate limit still enabled after cleanup")
		return
	}

	poolClient := lrpbindings.NewLockReleasePoolClient(env.Deployer, poolContractID)
	owner, err := poolClient.Owner(ctx)
	if err != nil {
		t.Logf("mcms pool cleanup: verify owner: %v", err)
		return
	}
	if owner == nil || *owner != deployerAddr {
		t.Logf("mcms pool cleanup: warning: pool owner is %v, want deployer %s", owner, deployerAddr)
		return
	}

	t.Log("mcms pool cleanup: ownership restored to deployer and rate limits disabled")
}

// WaitTimelockOperationReady polls until the scheduled timelock batch is executable.
func WaitTimelockOperationReady(
	ctx context.Context,
	t *testing.T,
	tlClient *timelockbindings.TimelockClient,
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
) {
	t.Helper()
	require.NoError(t, waitTimelockOperationReadyErr(ctx, tlClient, calls, predecessor, salt))
}

// MCMSTimelockScheduleAndExecute drives schedule_batch → wait → execute_batch through MCMS
// SetRoot + Execute (two-leaf Merkle tree, EIP-191 signing). Uses the current MCMS op count
// as the schedule nonce; execute uses nonce+1.
func MCMSTimelockScheduleAndExecute(
	t *testing.T,
	ctx context.Context,
	env *E2ETestEnv,
	gov *MCMSGovernanceStack,
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
) {
	t.Helper()
	require.NoError(t, MCMSTimelockScheduleAndExecuteErr(ctx, env, gov, calls, predecessor, salt))
}

// MCMSTimelockScheduleAndExecuteErr is the error-returning variant used by test cleanup hooks.
func MCMSTimelockScheduleAndExecuteErr(
	ctx context.Context,
	env *E2ETestEnv,
	gov *MCMSGovernanceStack,
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
) error {
	preOpCount, err := gov.MCMSClient.GetOpCount(ctx)
	if err != nil {
		return fmt.Errorf("get mcms op count: %w", err)
	}

	scheduleArgs, err := SorobanScheduleBatch(gov.MCMSID, calls, predecessor, salt, gov.MinDelaySec)
	if err != nil {
		return fmt.Errorf("encode schedule_batch: %w", err)
	}

	validUntil, err := MCMSValidUntilSeconds(ctx, env.RPCClient)
	if err != nil {
		return fmt.Errorf("mcms valid_until: %w", err)
	}

	if err := mcmsSetRootAndExecute(ctx, gov.MCMSClient, gov.ChainNetID, gov.MCMSID, gov.SignerPK,
		preOpCount, validUntil, gov.TimelockID, "schedule_batch", scheduleArgs, "schedule"); err != nil {
		return err
	}

	opID, err := gov.TimelockClient.HashOperationBatch(ctx, calls, predecessor, salt)
	if err != nil {
		return fmt.Errorf("hash scheduled operation: %w", err)
	}
	pending, err := gov.TimelockClient.IsOperationPending(ctx, opID)
	if err != nil {
		return fmt.Errorf("is operation pending: %w", err)
	}
	if !pending {
		return fmt.Errorf("expected scheduled operation to be pending")
	}

	if err := waitTimelockOperationReadyErr(ctx, gov.TimelockClient, calls, predecessor, salt); err != nil {
		return err
	}

	execArgs, err := SorobanExecuteBatch(calls, predecessor, salt)
	if err != nil {
		return fmt.Errorf("encode execute_batch: %w", err)
	}

	if err := mcmsSetRootAndExecute(ctx, gov.MCMSClient, gov.ChainNetID, gov.MCMSID, gov.SignerPK,
		preOpCount+1, validUntil, gov.TimelockID, "execute_batch", execArgs, "execute"); err != nil {
		return err
	}

	done, err := gov.TimelockClient.IsOperationDone(ctx, opID)
	if err != nil {
		return fmt.Errorf("is operation done: %w", err)
	}
	if !done {
		return fmt.Errorf("expected timelock operation to be done after execute_batch")
	}
	return nil
}

// mcmsSetRootAndExecute performs one MCMS SetRoot + Execute cycle for a single
// StellarOp on a 2-leaf Merkle tree, signed EIP-191 over the root. label prefixes
// error messages ("schedule"/"execute") and opCount is the MCMS nonce to consume.
// Errors are returned unchanged so negative tests can assert on them.
func mcmsSetRootAndExecute(
	ctx context.Context,
	client *mcmsbindings.McmsClient,
	netID [32]byte,
	mcmsID string,
	signerPK *ecdsa.PrivateKey,
	opCount uint64,
	validUntil uint32,
	target, function string,
	argsXdr []byte,
	label string,
) error {
	configVersion, err := client.GetConfigVersion(ctx)
	if err != nil {
		return fmt.Errorf("%s mcms get_config_version: %w", label, err)
	}

	op := mcmsbindings.StellarOp{
		NetworkId:       netID,
		Multisig:        mcmsID,
		Nonce:           opCount,
		Target:          target,
		Function:        function,
		ArgsXdr:         argsXdr,
		EncodingVersion: bindings.SorobanInvokeEncodingVersion,
	}
	meta := mcmsbindings.StellarRootMetadata{
		NetworkId:            netID,
		Multisig:             mcmsID,
		PreOpCount:           opCount,
		PostOpCount:          opCount + 1,
		OverridePreviousRoot: false,
		ConfigVersion:        configVersion,
		EncodingVersion:      bindings.SorobanInvokeEncodingVersion,
	}

	metaLeaf, err := HashRootMetadata(meta)
	if err != nil {
		return fmt.Errorf("hash %s metadata: %w", label, err)
	}
	opLeaf, err := HashStellarOp(op)
	if err != nil {
		return fmt.Errorf("hash %s op: %w", label, err)
	}
	leaves := [2][32]byte{metaLeaf, opLeaf}
	root := MerkleRootTwoLeaves(leaves[0], leaves[1])

	proofMeta := mcmsbindings.MerkleProof{Inner: MerkleProofTwoLeaves(leaves, 0)}
	sigs, err := SignaturesForSetRoot(signerPK, root, validUntil)
	if err != nil {
		return fmt.Errorf("sign %s set_root: %w", label, err)
	}
	if err := client.SetRoot(ctx, root, validUntil, meta, proofMeta, sigs); err != nil {
		return fmt.Errorf("%s set_root: %w", label, err)
	}

	proofOp := mcmsbindings.MerkleProof{Inner: MerkleProofTwoLeaves(leaves, 1)}
	if err := client.Execute(ctx, op, proofOp); err != nil {
		return fmt.Errorf("%s %s: %w", label, function, err)
	}
	return nil
}

// MCMSBypassAndExecute drives bypasser_execute_batch through the bypasser MCMS
// SetRoot + Execute: no delay wait. This is the production fast-curse path.
func MCMSBypassAndExecute(
	t *testing.T,
	ctx context.Context,
	env *E2ETestEnv,
	gov *MCMSGovernanceStack,
	calls timelockbindings.Calls,
) {
	t.Helper()
	require.NoError(t, MCMSBypassAndExecuteErr(ctx, env, gov, calls))
}

// MCMSBypassAndExecuteErr is the error-returning variant of MCMSBypassAndExecute.
func MCMSBypassAndExecuteErr(
	ctx context.Context,
	env *E2ETestEnv,
	gov *MCMSGovernanceStack,
	calls timelockbindings.Calls,
) error {
	bypasserID := gov.BypasserMCMSID
	if bypasserID == "" {
		bypasserID = gov.MCMSID
	}
	client := gov.BypasserClient
	if client == nil {
		client = gov.MCMSClient
	}

	preOpCount, err := client.GetOpCount(ctx)
	if err != nil {
		return fmt.Errorf("get bypasser mcms op count: %w", err)
	}

	callsVal, err := calls.ToScVal()
	if err != nil {
		return fmt.Errorf("encode bypasser calls: %w", err)
	}
	args, err := mcmsutil.EncodeSorobanInvokeArgs([]xdr.ScVal{
		scval.AddressToScVal(bypasserID),
		callsVal,
	})
	if err != nil {
		return fmt.Errorf("encode bypasser_execute_batch: %w", err)
	}

	validUntil, err := MCMSValidUntilSeconds(ctx, env.RPCClient)
	if err != nil {
		return fmt.Errorf("mcms valid_until: %w", err)
	}

	if err := mcmsSetRootAndExecute(ctx, client, gov.ChainNetID, bypasserID, gov.SignerPK,
		preOpCount, validUntil, gov.TimelockID, "bypasser_execute_batch", args, "bypass"); err != nil {
		return err
	}
	return nil
}

// TimelockCallsFromProposalTx converts an MCMS proposal transaction into timelock
// Calls: it decodes the Soroban invoke payload into the function name and args,
// then re-encodes the args as ArgsXdr. This is the missing proposal→Calls
// conversion for executing Stellar proposals against a timelock.
func TimelockCallsFromProposalTx(tx mcmstypes.Transaction) (timelockbindings.Calls, error) {
	function, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
	if err != nil {
		return timelockbindings.Calls{}, fmt.Errorf("decode proposal tx data: %w", err)
	}
	argsXdr, err := mcmsutil.EncodeSorobanInvokeArgs(args)
	if err != nil {
		return timelockbindings.Calls{}, fmt.Errorf("re-encode proposal args: %w", err)
	}
	return timelockbindings.Calls{
		Inner: []timelockbindings.Call{{
			Target:   tx.To,
			Function: function,
			ArgsXdr:  argsXdr,
		}},
	}, nil
}

// DeployMCMSStackWithRoles deploys a full qualifier-keyed MCMS stack via the real
// DeployStellarMCMS sequence: Proposer/Canceller/Bypasser multisigs (one shared
// 1-of-1 test signer in all three roles) plus a self-administered RBACTimelock.
func DeployMCMSStackWithRoles(
	t *testing.T,
	ctx context.Context,
	env *E2ETestEnv,
	chainSelector uint64,
	qualifier string,
	minDelaySec uint64,
) *MCMSGovernanceStack {
	t.Helper()

	// DeployStellarMCMS resolves its WASM via mcmsutil.ResolveMCMSWasmPath, which only
	// consults STELLAR_*_WASM / CHAINLINK_STELLAR_ROOT / the process cwd — there is no
	// upward walk from the test binary's package directory. Set the root from the
	// environment so the sequence finds target/wasm32v1-none/release/*.wasm from any cwd.
	require.NotEmpty(t, env.StellarRoot, "E2ETestEnv.StellarRoot must be set: DeployStellarMCMS resolves WASM from CHAINLINK_STELLAR_ROOT")
	t.Setenv("CHAINLINK_STELLAR_ROOT", env.StellarRoot)

	pk, err := crypto.HexToECDSA(Anvil0SKHex)
	require.NoError(t, err)

	chainNetID := mcmsutil.ChainNetworkID(env.NetworkPassphrase)

	bundle := cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)

	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: chainSelector},
		Signer:            stellarbindings.NewStellarKeypairSigner(env.DeployerKP),
		Client:            env.RPCClient,
		NetworkPassphrase: env.NetworkPassphrase,
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{chainSelector: ch})

	delay := new(big.Int).SetUint64(minDelaySec)
	// The deploy sequence pads EVM-style addresses to the 32-byte form the
	// Soroban MCMS expects (mcmsutil.ConfigToStellarSetConfig).
	signerAddr := crypto.PubkeyToAddress(pk.PublicKey)
	cfg := mcmstypes.Config{Quorum: 1, Signers: []common.Address{signerAddr}}
	out, err := cldfops.ExecuteSequence(bundle, sequences.DeployStellarMCMS, chains, deploy.MCMSDeploymentConfigPerChainWithAddress{
		MCMSDeploymentConfigPerChain: deploy.MCMSDeploymentConfigPerChain{
			Canceller:        cfg,
			Bypasser:         cfg,
			Proposer:         cfg,
			TimelockMinDelay: delay,
			Qualifier:        &qualifier,
			ContractVersion:  deploy.MCMSVersion.String(),
		},
		ChainSelector:     chainSelector,
		ExistingAddresses: nil,
	})
	require.NoError(t, err)
	refs := out.Output.Addresses

	proposerID, _, err := mcmsutil.FindExistingStellarMCMSByRole(refs, chainSelector, qualifier, mcmsutil.RoleProposer)
	require.NoError(t, err)
	cancellerID, _, err := mcmsutil.FindExistingStellarMCMSByRole(refs, chainSelector, qualifier, mcmsutil.RoleCanceller)
	require.NoError(t, err)
	bypasserID, _, err := mcmsutil.FindExistingStellarMCMSByRole(refs, chainSelector, qualifier, mcmsutil.RoleBypasser)
	require.NoError(t, err)
	tlID, ok := mcmsutil.FindExistingStellarTimelock(refs, chainSelector, qualifier)
	require.True(t, ok, "timelock ref must resolve for qualifier %q", qualifier)

	// The proposer MCMS drives schedule_batch/execute_batch (PROPOSER role on the
	// timelock); the bypasser MCMS drives bypasser_execute_batch.
	mcmsRaw, err := ContractIDToBytes32(proposerID)
	require.NoError(t, err)
	tlRaw, err := ContractIDToBytes32(tlID)
	require.NoError(t, err)

	return &MCMSGovernanceStack{
		MCMSID:          proposerID,
		TimelockID:      tlID,
		MCMSClient:      mcmsbindings.NewMcmsClient(env.Deployer, proposerID),
		TimelockClient:  timelockbindings.NewTimelockClient(env.Deployer, tlID),
		MCMSRaw:         mcmsRaw,
		TimelockRaw:     tlRaw,
		ChainNetID:      chainNetID,
		SignerPK:        pk,
		MinDelaySec:     minDelaySec,
		Qualifier:       qualifier,
		ProposerMCMSID:  proposerID,
		CancellerMCMSID: cancellerID,
		BypasserMCMSID:  bypasserID,
		BypasserClient:  mcmsbindings.NewMcmsClient(env.Deployer, bypasserID),
	}
}

func waitTimelockOperationReadyErr(
	ctx context.Context,
	tlClient *timelockbindings.TimelockClient,
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
) error {
	opID, err := tlClient.HashOperationBatch(ctx, calls, predecessor, salt)
	if err != nil {
		return fmt.Errorf("hash operation batch: %w", err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		ready, err := tlClient.IsOperationReady(ctx, opID)
		if err != nil {
			return fmt.Errorf("is operation ready: %w", err)
		}
		if ready {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}

	ready, err := tlClient.IsOperationReady(ctx, opID)
	if err != nil {
		return fmt.Errorf("is operation ready: %w", err)
	}
	if !ready {
		return fmt.Errorf("timelock operation never became ready")
	}
	return nil
}

func mustHashOperationBatch(
	t *testing.T,
	ctx context.Context,
	tlClient *timelockbindings.TimelockClient,
	calls timelockbindings.Calls,
	predecessor, salt [32]byte,
) [32]byte {
	t.Helper()
	opID, err := tlClient.HashOperationBatch(ctx, calls, predecessor, salt)
	require.NoError(t, err)
	return opID
}
