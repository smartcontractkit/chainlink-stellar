package sequences

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

// InstanceStateReader reports whether a Soroban contract instance exists on
// chain and which WASM hash it runs. Satisfied by *deployment.Deployer.
type InstanceStateReader interface {
	ContractInstanceState(ctx context.Context, contractID string) (bool, xdr.Hash, error)
}

// ComponentDeps is the host-free chain I/O every component sequence needs: the
// plain StellarDeps plus the inputs of the skip-if-exists check that must NOT
// enter the sequence input hash (they are environment, not parameters).
type ComponentDeps struct {
	stellardeps.StellarDeps
	NetworkPassphrase string
	DeployerAddress   string
	Ledger            InstanceStateReader
}

// ComponentDeployOutput is what every component sequence returns.
type ComponentDeployOutput struct {
	ContractID  string                 `json:"contract_id"`
	Deployed    bool                   `json:"deployed"`
	Initialized bool                   `json:"initialized"`
	Refs        []datastore.AddressRef `json:"refs,omitempty"`
}

// componentContract is the outcome of skip layers 1 and 2.
type componentContract struct {
	ID       string
	Deployed bool
	Adopted  bool
	FromRef  bool
	// RefAddress is the raw datastore address a FromRef reuse came from, used
	// to detect and rewrite hand-recorded strkey rows.
	RefAddress string
}

// componentIdempotencyKey scopes component op reports to one network and one
// component instance, so neither the same deployer on two Stellar networks nor
// two instances of a multi-instance component (per-token pools, per-token
// hooks) on one network reuses another instance's result. instance is the
// component's salt label for deploy ops and its qualifier for init ops.
func componentIdempotencyKey(chainSelector uint64, instance string) string {
	key := strconv.FormatUint(chainSelector, 10)
	if instance != "" {
		key += ":" + instance
	}
	return key
}

// componentSaltLabel builds a deterministic salt label that stays unique per
// component instance, appending the qualifier for multi-instance components so
// two instances never share a predicted contract ID.
func componentSaltLabel(name, qualifier string) string {
	if qualifier == "" {
		return name
	}
	return name + "-" + qualifier
}

// resolveComponentContract applies skip layers 1 and 2 (design "Skip-if-exists"):
// a datastore ref in existing wins; otherwise the deterministic contract ID is
// predicted from (network, deployer, salt) and checked against the ledger. A
// matching instance is adopted; a WASM-hash mismatch is an error, never a
// silent second deploy.
func resolveComponentContract(
	ctx context.Context,
	b cldf_ops.Bundle,
	deps ComponentDeps,
	deployOp *cldf_ops.Operation[stellarops.DeployInput, stellarops.DeployOutput, stellardeps.StellarDeps],
	ref stellarccip.DatastoreSorobanContractRef,
	chainSelector uint64,
	saltLabel, wasmPath string,
	existing []datastore.AddressRef,
) (componentContract, error) {
	if r := findExistingComponentRef(existing, ref, chainSelector); r != nil {
		// The ref address may be the hex the datastore Record* helpers write or a
		// strkey recorded by hand (our own WASM-mismatch error suggests that);
		// accept both.
		id, err := ownership.NormalizeContractAddress(r.Address)
		if err != nil {
			return componentContract{}, fmt.Errorf("existing %s ref: %w", ref.Type, err)
		}
		return componentContract{ID: id, FromRef: true, RefAddress: r.Address}, nil
	}

	salt := stellardeployment.GenerateDeterministicSalt(deps.DeployerAddress, saltLabel)
	predicted, err := stellardeployment.ComputeContractID(deps.NetworkPassphrase, deps.DeployerAddress, salt)
	if err != nil {
		return componentContract{}, fmt.Errorf("predict %s contract ID: %w", ref.Type, err)
	}
	exists, onChainWasm, err := deps.Ledger.ContractInstanceState(ctx, predicted)
	if err != nil {
		return componentContract{}, fmt.Errorf("read %s instance state: %w", ref.Type, err)
	}
	if !exists {
		out, err := execStellarCCIPOp(b, deps.StellarDeps, deployOp, stellarops.DeployInput{WasmPath: wasmPath, Salt: salt},
			cldf_ops.WithIdempotencyKey[stellarops.DeployInput, stellardeps.StellarDeps](componentIdempotencyKey(chainSelector, saltLabel)))
		if err != nil {
			return componentContract{}, fmt.Errorf("deploy %s: %w", ref.Type, err)
		}
		// The chain must land the contract at the predicted ID; anything else means
		// ComponentDeps (passphrase or deployer address) does not match the key that
		// actually signed, and every later rerun would predict the wrong ID.
		if out.ContractID != predicted {
			return componentContract{}, fmt.Errorf(
				"deploy %s returned contract %s but the salt predicted %s: check ComponentDeps NetworkPassphrase and DeployerAddress",
				ref.Type, out.ContractID, predicted)
		}
		return componentContract{ID: out.ContractID, Deployed: true}, nil
	}

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return componentContract{}, fmt.Errorf("read %s wasm: %w", ref.Type, err)
	}
	localWasm := sha256.Sum256(wasmBytes)
	if xdr.Hash(localWasm) != onChainWasm {
		return componentContract{}, fmt.Errorf(
			"%s contract %s runs wasm %x, local wasm hash is %x: record the ref by hand or deploy with another deployer",
			ref.Type, predicted, onChainWasm, localWasm)
	}
	return componentContract{ID: predicted, Adopted: true}, nil
}

// findExistingComponentRef returns the existing ref matching (chain, type,
// version, qualifier), or nil.
func findExistingComponentRef(existing []datastore.AddressRef, ref stellarccip.DatastoreSorobanContractRef, chainSelector uint64) *datastore.AddressRef {
	for i := range existing {
		r := &existing[i]
		if r.ChainSelector == chainSelector &&
			r.Type == ref.Type &&
			r.Qualifier == ref.Qualifier &&
			r.Version != nil && ref.Version != nil && r.Version.String() == ref.Version.String() {
			return r
		}
	}
	return nil
}

// componentOwner reads owner() on the contract: nil means uninitialized.
func componentOwner(ctx context.Context, deps ComponentDeps, contractID string) (*string, error) {
	res, err := deps.Invoker.SimulateContract(ctx, contractID, "owner", nil)
	if err != nil {
		return nil, fmt.Errorf("read owner of %s: %w", contractID, err)
	}
	if res == nil {
		return nil, fmt.Errorf("no return value from owner on %s", contractID)
	}
	return scval.OptionalAddressFromScVal(*res)
}

// shouldInitialize applies skip layer 3: owner() None → initialize; owner set
// on a ref-backed contract → skip (an earlier run finished, the owner may since
// be a timelock); owner set on an adopted contract must be the configured
// owner, else a third party initialized it and that is an error.
func shouldInitialize(ctx context.Context, deps ComponentDeps, contract componentContract, owner string) (bool, error) {
	current, err := componentOwner(ctx, deps, contract.ID)
	if err != nil {
		if contract.FromRef {
			return false, fmt.Errorf("datastore ref may be stale: contract %s cannot be read on chain: %w", contract.ID, err)
		}
		return false, err
	}
	switch {
	case current == nil:
		return true, nil
	case contract.FromRef:
		return false, nil
	case *current == owner:
		return false, nil
	default:
		return false, fmt.Errorf("contract %s was initialized by %s, expected owner %s", contract.ID, *current, owner)
	}
}

// deployAndInitialize runs the full three-layer skip for one component: resolve
// the contract (layers 1-2), then initialize only when owner() demands it
// (layer 3). init runs the component's initialize op.
func deployAndInitialize(
	ctx context.Context,
	b cldf_ops.Bundle,
	deps ComponentDeps,
	deployOp *cldf_ops.Operation[stellarops.DeployInput, stellarops.DeployOutput, stellardeps.StellarDeps],
	ref stellarccip.DatastoreSorobanContractRef,
	chainSelector uint64,
	saltLabel, wasmPath, owner string,
	existing []datastore.AddressRef,
	init func(contractID string) error,
) (ComponentDeployOutput, error) {
	contract, err := resolveComponentContract(ctx, b, deps, deployOp, ref, chainSelector, saltLabel, wasmPath, existing)
	if err != nil {
		return ComponentDeployOutput{}, err
	}
	out := ComponentDeployOutput{ContractID: contract.ID, Deployed: contract.Deployed}
	doInit, err := shouldInitialize(ctx, deps, contract, owner)
	if err != nil {
		return ComponentDeployOutput{}, err
	}
	if doInit {
		if err := init(contract.ID); err != nil {
			return ComponentDeployOutput{}, err
		}
		out.Initialized = true
	}
	refs, err := componentRefsToRecord(contract, ref, chainSelector)
	if err != nil {
		return ComponentDeployOutput{}, err
	}
	out.Refs = refs
	return out, nil
}

// deployStatelessComponent deploys a component with no initialize step — a
// stateless contract exposing only pure views, so there is no owner() to read
// and no third skip layer. Layers 1-2 (datastore ref, predicted contract ID +
// WASM hash) provide the same rerun safety as the initialized components.
func deployStatelessComponent(
	ctx context.Context,
	b cldf_ops.Bundle,
	deps ComponentDeps,
	deployOp *cldf_ops.Operation[stellarops.DeployInput, stellarops.DeployOutput, stellardeps.StellarDeps],
	ref stellarccip.DatastoreSorobanContractRef,
	chainSelector uint64,
	saltLabel, wasmPath string,
	existing []datastore.AddressRef,
) (ComponentDeployOutput, error) {
	contract, err := resolveComponentContract(ctx, b, deps, deployOp, ref, chainSelector, saltLabel, wasmPath, existing)
	if err != nil {
		return ComponentDeployOutput{}, err
	}
	refs, err := componentRefsToRecord(contract, ref, chainSelector)
	if err != nil {
		return ComponentDeployOutput{}, err
	}
	return ComponentDeployOutput{ContractID: contract.ID, Deployed: contract.Deployed, Refs: refs}, nil
}

// componentRefsToRecord returns the datastore refs a resolved component must
// record: new deploys and adoptions always; a ref reuse only when the row was
// hand-recorded as a strkey and needs the hex rewrite the datastore readers
// (adapters, lane config) expect.
func componentRefsToRecord(contract componentContract, ref stellarccip.DatastoreSorobanContractRef, chainSelector uint64) ([]datastore.AddressRef, error) {
	if contract.Deployed || contract.Adopted {
		return componentRefs(ref, chainSelector, contract.ID)
	}
	if contract.FromRef && !strings.HasPrefix(contract.RefAddress, "0x") {
		return componentRefs(ref, chainSelector, contract.ID)
	}
	return nil, nil
}

// componentRefs returns the datastore refs a newly deployed or adopted contract
// must record, in hex form. Components writing extra rows (the Executor's
// proxy row) append them in their sequence handler.
func componentRefs(ref stellarccip.DatastoreSorobanContractRef, chainSelector uint64, contractID string) ([]datastore.AddressRef, error) {
	hexAddr, err := stellarutil.StrkeyToHex(contractID)
	if err != nil {
		return nil, fmt.Errorf("strkey to hex for %s: %w", ref.Type, err)
	}
	return []datastore.AddressRef{ref.FullAddressRef(chainSelector, hexAddr)}, nil
}

// execComponentSequence runs a component sequence and returns its output.
func execComponentSequence[IN any](
	b cldf_ops.Bundle,
	deps ComponentDeps,
	seq *cldf_ops.Sequence[IN, ComponentDeployOutput, ComponentDeps],
	in IN,
) (ComponentDeployOutput, error) {
	rep, err := cldf_ops.ExecuteSequence(b, seq, deps, in)
	if err != nil {
		return ComponentDeployOutput{}, err
	}
	return rep.Output, nil
}

// upsertComponentRefs records the refs a component sequence returned into the
// orchestrator's datastore.
func upsertComponentRefs(ds *datastore.MemoryDataStore, refs []datastore.AddressRef) error {
	for _, r := range refs {
		if err := ds.AddressRefStore.Upsert(r); err != nil {
			return fmt.Errorf("upsert %s ref: %w", r.Type, err)
		}
	}
	return nil
}
