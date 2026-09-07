//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// encodeApplyOnrampUpdatesArgs builds timelock Call.ArgsXdr for ramp registry
// apply_onramp_updates(Vec<OnRampUpdate>) with one on-ramp upsert (Some(addr)).
// Args-only XDR Vec<Val>; the function name lives in Call.Function.
func encodeApplyOnrampUpdatesArgs(destChainSelector uint64, onramp string) ([]byte, error) {
	u := rampbindings.OnRampUpdate{
		DestChainSelector: destChainSelector,
		Onramp:            &onramp,
	}
	return helpers.EncodeTimelockCallArgs([]xdr.ScVal{
		scval.StructSliceToScVal([]rampbindings.OnRampUpdate{u}),
	})
}

func randSalt(t *testing.T) [32]byte {
	t.Helper()
	var s [32]byte
	if _, err := rand.Read(s[:]); err != nil {
		t.Fatalf("rand salt: %v", err)
	}
	return s
}

// assertApplyOnrampUpdatesRejectsNonOwner covers two Soroban/RPC behaviors:
//   - Simulation traps with Error(Contract, #Unauthorized), or
//   - Simulation succeeds when owner.require_auth targets another contract (timelock), but
//     InvokeContract fails after submit ("transaction failed" / require_auth diagnostics).
func assertApplyOnrampUpdatesRejectsNonOwner(t *testing.T, ctx context.Context, dep *deployment.Deployer, registryID string, updates []rampbindings.OnRampUpdate) {
	t.Helper()
	code := timelockbindings.CCIPErrorUnauthorized
	args := []xdr.ScVal{scval.StructSliceToScVal(updates)}
	_, simErr := dep.SimulateContract(ctx, registryID, "apply_onramp_updates", args)
	if simErr != nil {
		msg := simErr.Error()
		if strings.Contains(msg, "Error(Contract") && strings.Contains(msg, fmt.Sprintf("#%d", code)) {
			return
		}
	}
	client := rampbindings.NewRampRegistryClient(dep, registryID)
	invokeErr := client.ApplyOnrampUpdates(ctx, updates)
	if invokeErr == nil {
		t.Fatal("expected apply_onramp_updates to fail for unauthorized caller")
	}
	msg := invokeErr.Error()
	hasContract := strings.Contains(msg, "Error(Contract") && strings.Contains(msg, fmt.Sprintf("#%d", code))
	hasTxFail := strings.Contains(msg, "transaction failed")
	if !hasContract && !hasTxFail {
		t.Fatalf("expected Error(Contract, #%d) or transaction failed, got: %v", code, invokeErr)
	}
}

// Uses ccip-ramp-registry as the Ownable target: transfer_ownership → timelock schedules
// accept_ownership → execute; owner-only apply_onramp_updates is denied for the former owner and strangers,
// and only succeeds via schedule → wait → execute. Scheduling requires PROPOSER; execute_batch is
// permissionless once the operation is ready (contracts/timelock/src/lib.rs).
func TestGovernanceTimelockRampRegistry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase, friendbotURL := GetSharedTestEnv(ctx, t)

	proposerKP := keypair.MustRandom()
	executorKP := keypair.MustRandom()
	strangerKP := keypair.MustRandom()
	for _, label := range []struct {
		name string
		kp   *keypair.Full
	}{
		{"proposer", proposerKP},
		{"executor", executorKP},
		{"stranger", strangerKP},
	} {
		if err := helpers.FundViaFriendbot(friendbotURL, label.kp.Address()); err != nil {
			t.Fatalf("Friendbot fund %s: %v", label.name, err)
		}
	}

	proposerDep := deployment.NewDeployer(rpcClient, passphrase, proposerKP)
	executorDep := deployment.NewDeployer(rpcClient, passphrase, executorKP)
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

	registryID := deploy("gov-tl-ramp-registry", "ccip_ramp_registry.wasm")
	timelockID := deploy("gov-tl-timelock", "timelock.wasm")

	tlAdmin := timelockbindings.NewTimelockClient(deployer, timelockID)
	tlProposer := timelockbindings.NewTimelockClient(proposerDep, timelockID)
	tlExecutor := timelockbindings.NewTimelockClient(executorDep, timelockID)
	tlStranger := timelockbindings.NewTimelockClient(strangerDep, timelockID)

	const minDelaySec uint64 = 3

	if err := tlAdmin.Initialize(ctx, minDelaySec,
		[]string{proposerKP.Address()},
		[]string{},
		[]string{},
	); err != nil {
		t.Fatalf("Timelock Initialize: %v", err)
	}

	reg := rampbindings.NewRampRegistryClient(deployer, registryID)
	if err := reg.Initialize(ctx, deployerKP.Address()); err != nil {
		t.Fatalf("RampRegistry Initialize: %v", err)
	}

	if err := reg.TransferOwnership(ctx, timelockID); err != nil {
		t.Fatalf("TransferOwnership to timelock: %v", err)
	}
	pending, err := reg.GetPendingOwner(ctx)
	if err != nil {
		t.Fatalf("GetPendingOwner: %v", err)
	}
	if pending == nil || *pending != timelockID {
		t.Fatalf("pending owner = %v, want timelock %s", pending, timelockID)
	}

	acceptArgs, err := helpers.EncodeTimelockCallArgs(nil)
	if err != nil {
		t.Fatalf("encode accept_ownership args: %v", err)
	}

	var predecessor [32]byte
	saltAccept := randSalt(t)
	callsAccept := timelockbindings.Calls{
		Inner: []timelockbindings.Call{
			{Target: registryID, Function: "accept_ownership", ArgsXdr: acceptArgs},
		},
	}

	if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsAccept, predecessor, saltAccept, minDelaySec); err != nil {
		t.Fatalf("ScheduleBatch accept_ownership: %v", err)
	}

	opIDAccept, err := tlAdmin.HashOperationBatch(ctx, callsAccept, predecessor, saltAccept)
	if err != nil {
		t.Fatalf("HashOperationBatch accept: %v", err)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		ready, err := tlAdmin.IsOperationReady(ctx, opIDAccept)
		if err != nil {
			t.Fatalf("IsOperationReady: %v", err)
		}
		if ready {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}

	okAccept, err := tlAdmin.IsOperationReady(ctx, opIDAccept)
	if err != nil || !okAccept {
		t.Fatalf("accept operation never became ready: ready=%v err=%v", okAccept, err)
	}

	if err := tlExecutor.ExecuteBatch(ctx, callsAccept, predecessor, saltAccept); err != nil {
		t.Fatalf("ExecuteBatch accept_ownership: %v", err)
	}

	ownerAfter, err := reg.Owner(ctx)
	if err != nil {
		t.Fatalf("Owner after accept: %v", err)
	}
	if ownerAfter == nil || *ownerAfter != timelockID {
		t.Fatalf("owner = %v, want timelock %s", ownerAfter, timelockID)
	}

	const (
		chainReject   uint64 = 910001
		chainEarly    uint64 = 910002
		chainGate     uint64 = 910003
		chainExecutor uint64 = 910005
		chainStranger uint64 = 910006
	)

	mockReject := helpers.GenerateMockContractID(t, deployerKP.Address(), "gov-tl-mock-reject")
	mockEarly := helpers.GenerateMockContractID(t, deployerKP.Address(), "gov-tl-mock-early")
	mockGate := helpers.GenerateMockContractID(t, deployerKP.Address(), "gov-tl-mock-gate")
	mockExecutor := helpers.GenerateMockContractID(t, deployerKP.Address(), "gov-tl-mock-exec")
	mockStranger := helpers.GenerateMockContractID(t, deployerKP.Address(), "gov-tl-mock-stranger")

	// Non-owner cannot call apply_onramp_updates: see assertApplyOnrampUpdatesRejectsNonOwner.
	t.Run("former owner cannot apply_onramp_updates", func(t *testing.T) {
		assertApplyOnrampUpdatesRejectsNonOwner(t, ctx, deployer, registryID, []rampbindings.OnRampUpdate{{
			DestChainSelector: chainReject,
			Onramp:            &mockReject,
		}})
	})

	t.Run("stranger cannot apply_onramp_updates", func(t *testing.T) {
		assertApplyOnrampUpdatesRejectsNonOwner(t, ctx, strangerDep, registryID, []rampbindings.OnRampUpdate{{
			DestChainSelector: chainStranger,
			Onramp:            &mockStranger,
		}})
	})

	t.Run("non-proposer cannot schedule", func(t *testing.T) {
		opArgs, err := encodeApplyOnrampUpdatesArgs(chainExecutor, mockExecutor)
		if err != nil {
			t.Fatal(err)
		}
		saltBump := randSalt(t)
		callsOp := timelockbindings.Calls{
			Inner: []timelockbindings.Call{{Target: registryID, Function: "apply_onramp_updates", ArgsXdr: opArgs}},
		}
		err = tlExecutor.ScheduleBatch(ctx, executorKP.Address(), callsOp, predecessor, saltBump, minDelaySec)
		if err == nil {
			t.Fatal("expected ScheduleBatch to fail when caller lacks PROPOSER")
		}
	})

	t.Run("apply_onramp_updates before delay cannot execute", func(t *testing.T) {
		opArgs, err := encodeApplyOnrampUpdatesArgs(chainEarly, mockEarly)
		if err != nil {
			t.Fatal(err)
		}
		saltEarly := randSalt(t)
		callsOp := timelockbindings.Calls{
			Inner: []timelockbindings.Call{{Target: registryID, Function: "apply_onramp_updates", ArgsXdr: opArgs}},
		}
		if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsOp, predecessor, saltEarly, minDelaySec); err != nil {
			t.Fatalf("ScheduleBatch apply_onramp_updates: %v", err)
		}
		err = tlExecutor.ExecuteBatch(ctx, callsOp, predecessor, saltEarly)
		if err == nil {
			t.Fatal("expected ExecuteBatch before min_delay to fail")
		}
	})

	t.Run("gated apply_onramp_updates via timelock", func(t *testing.T) {
		if _, err := reg.GetOnramp(ctx, chainGate); err == nil {
			t.Fatal("expected GetOnramp to fail before route is configured")
		}

		opArgs, err := encodeApplyOnrampUpdatesArgs(chainGate, mockGate)
		if err != nil {
			t.Fatal(err)
		}
		saltBump := randSalt(t)
		callsOp := timelockbindings.Calls{
			Inner: []timelockbindings.Call{{Target: registryID, Function: "apply_onramp_updates", ArgsXdr: opArgs}},
		}
		if err := tlProposer.ScheduleBatch(ctx, proposerKP.Address(), callsOp, predecessor, saltBump, minDelaySec); err != nil {
			t.Fatalf("ScheduleBatch apply_onramp_updates: %v", err)
		}
		opBump, err := tlAdmin.HashOperationBatch(ctx, callsOp, predecessor, saltBump)
		if err != nil {
			t.Fatal(err)
		}

		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			ready, err := tlAdmin.IsOperationReady(ctx, opBump)
			if err != nil {
				t.Fatalf("IsOperationReady: %v", err)
			}
			if ready {
				break
			}
			time.Sleep(400 * time.Millisecond)
		}
		if ok, _ := tlAdmin.IsOperationReady(ctx, opBump); !ok {
			t.Fatal("apply_onramp_updates operation never became ready")
		}

		if err := tlExecutor.ExecuteBatch(ctx, callsOp, predecessor, saltBump); err != nil {
			t.Fatalf("ExecuteBatch apply_onramp_updates: %v", err)
		}

		got, err := reg.GetOnramp(ctx, chainGate)
		if err != nil {
			t.Fatalf("GetOnramp after: %v", err)
		}
		if got != mockGate {
			t.Fatalf("get_onramp = %s, want %s", got, mockGate)
		}
	})

	t.Run("stranger cannot schedule", func(t *testing.T) {
		opArgs, err := encodeApplyOnrampUpdatesArgs(chainGate, mockGate)
		if err != nil {
			t.Fatal(err)
		}
		saltBump := randSalt(t)
		callsOp := timelockbindings.Calls{
			Inner: []timelockbindings.Call{{Target: registryID, Function: "apply_onramp_updates", ArgsXdr: opArgs}},
		}
		err = tlStranger.ScheduleBatch(ctx, strangerKP.Address(), callsOp, predecessor, saltBump, minDelaySec)
		if err == nil {
			t.Fatal("expected ScheduleBatch to fail for account without PROPOSER")
		}
	})
}
