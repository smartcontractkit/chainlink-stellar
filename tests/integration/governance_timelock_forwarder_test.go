//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	crebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/cre"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// setConfigCall builds a timelock Call for the CRE forwarder
// set_config(don_id: u32, config_version: u32, f: u32, signers: Vec<BytesN<32>>).
func setConfigCall(t *testing.T, forwarderID string, donID, configVersion, f uint32, signers [][32]byte) timelockbindings.Call {
	t.Helper()
	args, err := helpers.EncodeTimelockCallArgs([]xdr.ScVal{
		scval.Uint32ToScVal(donID),
		scval.Uint32ToScVal(configVersion),
		scval.Uint32ToScVal(f),
		scval.Bytes32SliceToScVal(signers),
	})
	if err != nil {
		t.Fatalf("encode set_config args: %v", err)
	}
	return timelockbindings.Call{
		Target:   forwarderID,
		Function: "set_config",
		ArgsXdr:  args,
	}
}

// addForwarderCall builds a timelock Call for the CRE forwarder add_forwarder(addr: Address).
func addForwarderCall(t *testing.T, forwarderID, transmitter string) timelockbindings.Call {
	t.Helper()
	args, err := helpers.EncodeTimelockCallArgs([]xdr.ScVal{
		scval.AddressToScVal(transmitter),
	})
	if err != nil {
		t.Fatalf("encode add_forwarder args: %v", err)
	}
	return timelockbindings.Call{
		Target:   forwarderID,
		Function: "add_forwarder",
		ArgsXdr:  args,
	}
}

// acceptOwnershipCall builds a timelock Call for accept_ownership() (no args).
func acceptOwnershipCall(t *testing.T, forwarderID string) timelockbindings.Call {
	t.Helper()
	args, err := helpers.EncodeTimelockCallArgs(nil)
	if err != nil {
		t.Fatalf("encode accept_ownership args: %v", err)
	}
	return timelockbindings.Call{
		Target:   forwarderID,
		Function: "accept_ownership",
		ArgsXdr:  args,
	}
}

// assertForwarderRejectsNonOwner asserts an owner-gated forwarder entrypoint rejects a caller
// that is not the current owner. Post-handoff the owner is the timelock, so a direct EOA call
// fails the Ownable require_owner -> owner.require_auth() check. Soroban surfaces this either as
// a simulation trap (Error(Contract, #<code>)) or as a post-submit "transaction failed".
func assertForwarderRejectsNonOwner(t *testing.T, ctx context.Context, dep *deployment.Deployer, forwarderID, function string, args []xdr.ScVal) {
	t.Helper()
	if _, simErr := dep.SimulateContract(ctx, forwarderID, function, args); simErr != nil {
		if strings.Contains(simErr.Error(), "Error(Contract") {
			return
		}
	}
	_, invokeErr := dep.InvokeContract(ctx, forwarderID, function, args)
	if invokeErr == nil {
		t.Fatalf("expected %s to fail for unauthorized caller", function)
	}
	msg := invokeErr.Error()
	hasContract := strings.Contains(msg, "Error(Contract") && strings.Contains(msg, fmt.Sprintf("#%d", crebindings.CCIPErrorNotOwner))
	hasTxFail := strings.Contains(msg, "transaction failed")
	if !hasContract && !hasTxFail {
		t.Fatalf("expected Error(Contract, #%d) or transaction failed for %s, got: %v",
			crebindings.CCIPErrorNotOwner, function, invokeErr)
	}
}

// distinctSigners returns n non-zero, mutually-distinct 32-byte signer pubkeys. The forwarder
// set_config validation requires f != 0, len <= 31, len >= 3f+1, and unique non-zero signers.
func distinctSigners(t *testing.T, n int, seed byte) [][32]byte {
	t.Helper()
	if n > 31 {
		t.Fatalf("distinctSigners: n=%d exceeds forwarder max 31", n)
	}
	out := make([][32]byte, n)
	for i := 0; i < n; i++ {
		var s [32]byte
		s[0] = seed
		s[1] = byte(i + 1)
		out[i] = s
	}
	return out
}

func waitForReady(t *testing.T, ctx context.Context, reader *timelockbindings.TimelockClient, opID [32]byte) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		ready, err := reader.IsOperationReady(ctx, opID)
		if err != nil {
			t.Fatalf("IsOperationReady: %v", err)
		}
		if ready {
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	if ok, _ := reader.IsOperationReady(ctx, opID); !ok {
		t.Fatalf("operation %x never became ready", opID)
	}
}

// TestGovernanceTimelockForwarder uses the CRE forwarder as the Ownable target: deployer-owned
// baseline -> transfer_ownership(timelock) -> timelock schedules accept_ownership -> permissionless
// execute. Post-handoff, owner-only set_config and add_forwarder are denied for the former owner
// and strangers, and succeed only via schedule -> wait -> permissionless execute_batch. Only
// PROPOSER can schedule. The forwarder exposes no config getter and the CRE Go bindings expose no
// event methods, so governed config ops are proven by execution success (execute_batch returns
// nil) and direct non-owner calls reverting; ownership is asserted via Owner/GetPendingOwner.
func TestGovernanceTimelockForwarder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase, friendbotURL := GetSharedTestEnv(ctx, t)

	proposerKP := keypair.MustRandom()
	anyoneKP := keypair.MustRandom()
	strangerKP := keypair.MustRandom()
	for _, label := range []struct {
		name string
		kp   *keypair.Full
	}{
		{"proposer", proposerKP},
		{"anyone", anyoneKP},
		{"stranger", strangerKP},
	} {
		if err := helpers.FundViaFriendbot(friendbotURL, label.kp.Address()); err != nil {
			t.Fatalf("Friendbot fund %s: %v", label.name, err)
		}
	}

	proposerDep := deployment.NewDeployer(rpcClient, passphrase, proposerKP)
	anyoneDep := deployment.NewDeployer(rpcClient, passphrase, anyoneKP)
	strangerDep := deployment.NewDeployer(rpcClient, passphrase, strangerKP)

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

	forwarderID := deploy("gov-tl-cre-forwarder", "forwarder.wasm")
	timelockID := deploy("gov-tl-fwd-timelock", "timelock.wasm")

	tlReader := timelockbindings.NewTimelockClient(deployer, timelockID)
	tlProposer := timelockbindings.NewTimelockClient(proposerDep, timelockID)
	tlAnyone := timelockbindings.NewTimelockClient(anyoneDep, timelockID)
	tlStranger := timelockbindings.NewTimelockClient(strangerDep, timelockID)

	fwd := crebindings.NewForwarderClient(deployer, forwarderID)

	const minDelaySec uint64 = 3

	// The v2 timelock grants ADMIN to itself; there is no admin input and no executor role.
	if err := tlReader.Initialize(ctx, minDelaySec,
		[]string{proposerKP.Address()},
		[]string{},
		[]string{},
	); err != nil {
		t.Fatalf("Timelock Initialize: %v", err)
	}

	// Deployer-owned forwarder with a baseline DON config (deployer is still owner here).
	if err := fwd.Initialize(ctx, deployerKP.Address()); err != nil {
		t.Fatalf("Forwarder Initialize: %v", err)
	}
	const (
		donID         uint32 = 1
		configVersion uint32 = 1
		f             uint32 = 1 // 4 signers satisfies 3f+1
	)
	baselineSigners := distinctSigners(t, 4, 0xA1)
	if err := fwd.SetConfig(ctx, donID, configVersion, f, baselineSigners); err != nil {
		t.Fatalf("baseline SetConfig (deployer-signed): %v", err)
	}

	// Two-step handoff step 1: deployer transfers ownership to the timelock.
	if err := fwd.TransferOwnership(ctx, timelockID); err != nil {
		t.Fatalf("TransferOwnership to timelock: %v", err)
	}
	pending, err := fwd.GetPendingOwner(ctx)
	if err != nil {
		t.Fatalf("GetPendingOwner: %v", err)
	}
	if pending == nil || *pending != timelockID {
		t.Fatalf("pending owner = %v, want timelock %s", pending, timelockID)
	}

	// Two-step handoff step 2: accept_ownership requires the pending owner's auth, so only the
	// timelock can call it. Schedule it through the timelock and execute permissionlessly.
	var predecessor [32]byte
	saltAccept := randSalt(t)
	callsAccept := singleCallBatch(acceptOwnershipCall(t, forwarderID))
	if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsAccept, predecessor, saltAccept, minDelaySec); err != nil {
		t.Fatalf("ScheduleBatch accept_ownership: %v", err)
	}
	opIDAccept, err := tlReader.HashOperationBatch(ctx, callsAccept, predecessor, saltAccept)
	if err != nil {
		t.Fatalf("HashOperationBatch accept: %v", err)
	}
	waitForReady(t, ctx, tlReader, opIDAccept)

	// Permissionless execution: an unrelated account submits the ready operation.
	if err := tlAnyone.ExecuteBatch(ctx, callsAccept, predecessor, saltAccept); err != nil {
		t.Fatalf("ExecuteBatch accept_ownership: %v", err)
	}
	ownerAfter, err := fwd.Owner(ctx)
	if err != nil {
		t.Fatalf("Owner after accept: %v", err)
	}
	if ownerAfter == nil || *ownerAfter != timelockID {
		t.Fatalf("owner = %v, want timelock %s", ownerAfter, timelockID)
	}

	// Direct owner-only calls now fail for the former owner and strangers.
	t.Run("former owner cannot set_config", func(t *testing.T) {
		assertForwarderRejectsNonOwner(t, ctx, deployer, forwarderID, "set_config", []xdr.ScVal{
			scval.Uint32ToScVal(donID),
			scval.Uint32ToScVal(configVersion + 1),
			scval.Uint32ToScVal(f),
			scval.Bytes32SliceToScVal(distinctSigners(t, 4, 0xB2)),
		})
	})
	t.Run("stranger cannot set_config", func(t *testing.T) {
		assertForwarderRejectsNonOwner(t, ctx, strangerDep, forwarderID, "set_config", []xdr.ScVal{
			scval.Uint32ToScVal(donID),
			scval.Uint32ToScVal(configVersion + 2),
			scval.Uint32ToScVal(f),
			scval.Bytes32SliceToScVal(distinctSigners(t, 4, 0xC3)),
		})
	})
	t.Run("non-proposer cannot schedule", func(t *testing.T) {
		callsOp := singleCallBatch(setConfigCall(t, forwarderID, donID, configVersion+1, f, distinctSigners(t, 4, 0xD4)))
		if err := tlAnyone.ScheduleBatch(ctx, anyoneKP.Address(), callsOp, predecessor, randSalt(t), minDelaySec); err == nil {
			t.Fatal("expected ScheduleBatch to fail for account without PROPOSER")
		}
	})
	t.Run("set_config before delay cannot execute", func(t *testing.T) {
		callsOp := singleCallBatch(setConfigCall(t, forwarderID, donID, configVersion+1, f, distinctSigners(t, 4, 0xE5)))
		saltEarly := randSalt(t)
		if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsOp, predecessor, saltEarly, minDelaySec); err != nil {
			t.Fatalf("ScheduleBatch set_config: %v", err)
		}
		if err := tlAnyone.ExecuteBatch(ctx, callsOp, predecessor, saltEarly); err == nil {
			t.Fatal("expected ExecuteBatch before min_delay to fail")
		}
	})

	// Governed set_config(don, v+1, ...) via timelock: schedule -> wait -> permissionless execute.
	// The forwarder has no config getter, so execution success (nil error) is the proof.
	t.Run("governed set_config via timelock", func(t *testing.T) {
		callsOp := singleCallBatch(setConfigCall(t, forwarderID, donID, configVersion+1, f, distinctSigners(t, 4, 0xF6)))
		saltCfg := randSalt(t)
		if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsOp, predecessor, saltCfg, minDelaySec); err != nil {
			t.Fatalf("ScheduleBatch set_config: %v", err)
		}
		opCfg, err := tlReader.HashOperationBatch(ctx, callsOp, predecessor, saltCfg)
		if err != nil {
			t.Fatalf("HashOperationBatch set_config: %v", err)
		}
		waitForReady(t, ctx, tlReader, opCfg)
		if err := tlStranger.ExecuteBatch(ctx, callsOp, predecessor, saltCfg); err != nil {
			t.Fatalf("ExecuteBatch set_config: %v", err)
		}
	})

	// Governed add_forwarder via timelock: register a transmitter through governance.
	t.Run("governed add_forwarder via timelock", func(t *testing.T) {
		callsOp := singleCallBatch(addForwarderCall(t, forwarderID, anyoneKP.Address()))
		saltFwd := randSalt(t)
		if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsOp, predecessor, saltFwd, minDelaySec); err != nil {
			t.Fatalf("ScheduleBatch add_forwarder: %v", err)
		}
		opFwd, err := tlReader.HashOperationBatch(ctx, callsOp, predecessor, saltFwd)
		if err != nil {
			t.Fatalf("HashOperationBatch add_forwarder: %v", err)
		}
		waitForReady(t, ctx, tlReader, opFwd)
		if err := tlStranger.ExecuteBatch(ctx, callsOp, predecessor, saltFwd); err != nil {
			t.Fatalf("ExecuteBatch add_forwarder: %v", err)
		}
	})

	// Final negative: stranger cannot schedule.
	t.Run("stranger cannot schedule", func(t *testing.T) {
		callsOp := singleCallBatch(addForwarderCall(t, forwarderID, strangerKP.Address()))
		if err := tlStranger.ScheduleBatch(ctx, strangerKP.Address(), callsOp, predecessor, randSalt(t), minDelaySec); err == nil {
			t.Fatal("expected ScheduleBatch to fail for account without PROPOSER")
		}
	})
}
