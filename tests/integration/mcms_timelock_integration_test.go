//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	chainsel "github.com/smartcontractkit/chain-selectors"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestMcmsMerkleTimelockScheduleAndExecute wires MCMS SetRoot + Execute into timelock schedule_batch
// and execute_batch after min delay (Go Merkle + EIP-191).
//
// Caller identity matches EVM-style wiring: the ManyChainMultiSig contract invokes the timelock, so
// msg.sender-style authority is the multisig contract. On Soroban, schedule_batch passes
// caller = MCMS contract address and MCMS holds PROPOSER on the timelock so nested
// require_auth() succeeds when MCMS.execute invokes the timelock; execute_batch is
// permissionless once the op is ready (see contracts/timelock/src/lib.rs).
func TestMcmsMerkleTimelockScheduleAndExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, _, _ := GetSharedTestEnv(ctx, t)

	pk, err := crypto.HexToECDSA(helpers.Anvil0SKHex)
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}
	paddedSigner := helpers.PaddedEthAddress(&pk.PublicKey)

	chainNetID, err := helpers.ChainNetworkIDFromHex(chainsel.STELLAR_LOCALNET.ChainID)
	if err != nil {
		t.Fatalf("chain network id: %v", err)
	}

	deploy := func(name, wasm string) string {
		t.Helper()
		salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), name)
		p := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", wasm)
		id, err := deployer.DeployContract(ctx, p, salt)
		if err != nil {
			t.Fatalf("deploy %s: %v", name, err)
		}
		return id
	}

	mcmsID := deploy("mcms-tl-mcms", "mcms.wasm")
	tlID := deploy("mcms-tl-timelock", "timelock.wasm")

	mcmsClient := mcmsbindings.NewMcmsClient(deployer, mcmsID)
	tlClient := timelockbindings.NewTimelockClient(deployer, tlID)

	var groupQuorums [32]byte
	groupQuorums[0] = 1
	var groupParents [32]byte
	if err := mcmsClient.Initialize(ctx, deployerKP.Address(), chainNetID,
		mcmsbindings.SignerAddresses{Inner: [][32]byte{paddedSigner}},
		mcmsbindings.SignerGroups{Inner: []uint32{0}},
		groupQuorums,
		groupParents,
		"PROPOSER",
	); err != nil {
		t.Fatalf("MCMS Initialize: %v", err)
	}

	const minDelaySec uint64 = 3
	// MCMS is proposer so MCMS-mediated ops authenticate (same logical caller as
	// ManyChainMultiSig calling RBACTimelock on EVM). The timelock grants ADMIN_ROLE only to
	// itself and execute_batch is permissionless; there is no admin/executor wiring.
	if err := tlClient.Initialize(ctx, minDelaySec,
		[]string{mcmsID},
		[]string{},
		[]string{},
	); err != nil {
		t.Fatalf("Timelock Initialize: %v", err)
	}

	var predecessor [32]byte
	var saltSched [32]byte
	saltSched[31] = 42

	emptyCalls := timelockbindings.Calls{Inner: []timelockbindings.Call{}}

	scheduleArgs, err := helpers.SorobanScheduleBatch(mcmsID, emptyCalls, predecessor, saltSched, minDelaySec)
	if err != nil {
		t.Fatalf("encode schedule_batch: %v", err)
	}

	validUntil, err := helpers.MCMSValidUntilSeconds(ctx, rpcClient)
	if err != nil {
		t.Fatalf("mcms valid_until: %v", err)
	}

	configVersion, err := mcmsClient.GetConfigVersion(ctx)
	if err != nil {
		t.Fatalf("mcms get_config_version: %v", err)
	}

	opSchedule := mcmsbindings.StellarOp{
		NetworkId:       chainNetID,
		Multisig:        mcmsID,
		Nonce:           0,
		Target:          tlID,
		Function:        "schedule_batch",
		ArgsXdr:         scheduleArgs,
		EncodingVersion: bindings.SorobanInvokeEncodingVersion,
	}
	meta1 := mcmsbindings.StellarRootMetadata{
		NetworkId:            chainNetID,
		Multisig:             mcmsID,
		PreOpCount:           0,
		PostOpCount:          1,
		OverridePreviousRoot: false,
		ConfigVersion:        configVersion,
		EncodingVersion:      bindings.SorobanInvokeEncodingVersion,
	}

	metaLeaf1, err := helpers.HashRootMetadata(meta1)
	if err != nil {
		t.Fatalf("hash meta1: %v", err)
	}
	opLeaf1, err := helpers.HashStellarOp(opSchedule)
	if err != nil {
		t.Fatalf("hash op schedule: %v", err)
	}
	leaves1 := [2][32]byte{metaLeaf1, opLeaf1}
	root1 := helpers.MerkleRootTwoLeaves(leaves1[0], leaves1[1])

	proofMeta1 := mcmsbindings.MerkleProof{Inner: helpers.MerkleProofTwoLeaves(leaves1, 0)}
	sigs1, err := helpers.SignaturesForSetRoot(pk, root1, validUntil)
	if err != nil {
		t.Fatalf("sign set_root 1: %v", err)
	}
	if err := mcmsClient.SetRoot(ctx, root1, validUntil, meta1, proofMeta1, sigs1); err != nil {
		t.Fatalf("SetRoot 1: %v", err)
	}

	proofOp1 := mcmsbindings.MerkleProof{Inner: helpers.MerkleProofTwoLeaves(leaves1, 1)}
	if err := mcmsClient.Execute(ctx, opSchedule, proofOp1); err != nil {
		t.Fatalf("Execute schedule_batch: %v", err)
	}

	opID, err := tlClient.HashOperationBatch(ctx, emptyCalls, predecessor, saltSched)
	if err != nil {
		t.Fatalf("HashOperationBatch: %v", err)
	}
	pending, err := tlClient.IsOperationPending(ctx, opID)
	if err != nil || !pending {
		t.Fatalf("expected pending scheduled op: pending=%v err=%v", pending, err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		ready, err := tlClient.IsOperationReady(ctx, opID)
		if err != nil {
			t.Fatalf("IsOperationReady: %v", err)
		}
		if ready {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	okReady, err := tlClient.IsOperationReady(ctx, opID)
	if err != nil || !okReady {
		t.Fatalf("scheduled op never became ready: ready=%v err=%v", okReady, err)
	}

	execArgs, err := helpers.SorobanExecuteBatch(emptyCalls, predecessor, saltSched)
	if err != nil {
		t.Fatalf("encode execute_batch: %v", err)
	}

	opExec := mcmsbindings.StellarOp{
		NetworkId:       chainNetID,
		Multisig:        mcmsID,
		Nonce:           1,
		Target:          tlID,
		Function:        "execute_batch",
		ArgsXdr:         execArgs,
		EncodingVersion: bindings.SorobanInvokeEncodingVersion,
	}
	meta2 := mcmsbindings.StellarRootMetadata{
		NetworkId:            chainNetID,
		Multisig:             mcmsID,
		PreOpCount:           1,
		PostOpCount:          2,
		OverridePreviousRoot: false,
		ConfigVersion:        configVersion,
		EncodingVersion:      bindings.SorobanInvokeEncodingVersion,
	}

	metaLeaf2, err := helpers.HashRootMetadata(meta2)
	if err != nil {
		t.Fatalf("hash meta2: %v", err)
	}
	opLeaf2, err := helpers.HashStellarOp(opExec)
	if err != nil {
		t.Fatalf("hash op exec: %v", err)
	}
	leaves2 := [2][32]byte{metaLeaf2, opLeaf2}
	root2 := helpers.MerkleRootTwoLeaves(leaves2[0], leaves2[1])

	proofMeta2 := mcmsbindings.MerkleProof{Inner: helpers.MerkleProofTwoLeaves(leaves2, 0)}
	sigs2, err := helpers.SignaturesForSetRoot(pk, root2, validUntil)
	if err != nil {
		t.Fatalf("sign set_root 2: %v", err)
	}
	if err := mcmsClient.SetRoot(ctx, root2, validUntil, meta2, proofMeta2, sigs2); err != nil {
		t.Fatalf("SetRoot 2: %v", err)
	}

	proofOp2 := mcmsbindings.MerkleProof{Inner: helpers.MerkleProofTwoLeaves(leaves2, 1)}
	if err := mcmsClient.Execute(ctx, opExec, proofOp2); err != nil {
		t.Fatalf("Execute execute_batch: %v", err)
	}

	done, err := tlClient.IsOperationDone(ctx, opID)
	if err != nil || !done {
		t.Fatalf("expected operation done: done=%v err=%v", done, err)
	}
}
