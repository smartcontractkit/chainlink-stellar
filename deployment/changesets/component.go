package changesets

import (
	"fmt"
	"path/filepath"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// componentDep pairs a datastore ref with the name used in "deploy <X> first"
// precondition errors.
type componentDep struct {
	name string
	ref  stellarccip.DatastoreSorobanContractRef
}

// verifyComponent checks the shared preconditions of every component
// changeset: the chain is in the environment, the datastore is loaded and
// every dependency ref exists. Component-specific param checks run through
// validate, which may be nil.
func verifyComponent(e cldf.Environment, chainSelector uint64, deps []componentDep, validate func() error) error {
	if _, ok := e.BlockChains.StellarChains()[chainSelector]; !ok {
		return fmt.Errorf("stellar chain %d not found in environment", chainSelector)
	}
	if e.DataStore == nil {
		return fmt.Errorf("environment datastore is nil")
	}
	for _, d := range deps {
		if _, err := d.ref.LookupStrkey(e.DataStore, chainSelector); err != nil {
			return fmt.Errorf("deploy %s first: %w", d.name, err)
		}
	}
	if validate != nil {
		return validate()
	}
	return nil
}

// applyComponent is the shared body of every component changeset's Apply: it
// builds the component deps from the chain's signer, resolves the dependency
// strkeys from the environment datastore, feeds the sequence the datastore's
// current refs as ExistingAddresses, and returns the refs the sequence
// recorded (new or adopted only) as the changeset output.
func applyComponent[IN any](
	e cldf.Environment,
	chainSelector uint64,
	deps []componentDep,
	wasmPathOverride, defaultWasmFile string,
	seq *cldf_ops.Sequence[IN, sequences.ComponentDeployOutput, sequences.ComponentDeps],
	buildInput func(resolved map[string]string, wasmPath string, existing []datastore.AddressRef) IN,
) (cldf.ChangesetOutput, error) {
	if e.DataStore == nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("environment datastore is nil")
	}
	componentDeps, err := componentDepsFromEnv(e, chainSelector)
	if err != nil {
		return cldf.ChangesetOutput{}, err
	}
	resolved := make(map[string]string, len(deps))
	for _, d := range deps {
		strkey, err := d.ref.LookupStrkey(e.DataStore, chainSelector)
		if err != nil {
			return cldf.ChangesetOutput{}, fmt.Errorf("deploy %s first: %w", d.name, err)
		}
		resolved[d.name] = strkey
	}
	wasmPath, err := resolveComponentWasmPath(wasmPathOverride, defaultWasmFile)
	if err != nil {
		return cldf.ChangesetOutput{}, err
	}
	existing, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("fetch existing address refs: %w", err)
	}
	rep, err := cldf_ops.ExecuteSequence(e.OperationsBundle, seq, componentDeps, buildInput(resolved, wasmPath, existing))
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("run %s: %w", seq.ID(), err)
	}
	out := datastore.NewMemoryDataStore()
	for _, r := range rep.Output.Refs {
		if err := out.AddressRefStore.Upsert(r); err != nil {
			return cldf.ChangesetOutput{}, fmt.Errorf("record %s ref: %w", r.Type, err)
		}
	}
	return cldf.ChangesetOutput{DataStore: out}, nil
}

// componentDepsFromEnv builds the sequences.ComponentDeps for a chain. The
// deployer comes from the chain's signer (KMS-compatible: no raw keypair
// required), and DeployerAddress is the signer's own address, so the salt and
// predicted contract IDs always match the key that signs the deploys.
func componentDepsFromEnv(e cldf.Environment, chainSelector uint64) (sequences.ComponentDeps, error) {
	ch, ok := e.BlockChains.StellarChains()[chainSelector]
	if !ok {
		return sequences.ComponentDeps{}, fmt.Errorf("stellar chain %d not found in environment", chainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return sequences.ComponentDeps{}, fmt.Errorf("build deployer from chain %d: %w", chainSelector, err)
	}
	signerAddr := dep.SignerAddress()
	if signerAddr == "" {
		return sequences.ComponentDeps{}, fmt.Errorf("chain %d signer exposes no address", chainSelector)
	}
	return sequences.ComponentDeps{
		StellarDeps:       stellardeps.FromDeployer(dep),
		NetworkPassphrase: ch.NetworkPassphrase,
		DeployerAddress:   signerAddr,
		Ledger:            dep,
	}, nil
}

// resolveComponentWasmPath returns the configured WASM path, or the
// conventional release-build path for defaultWasmFile under the
// chainlink-stellar root.
func resolveComponentWasmPath(wasmPathOverride, defaultWasmFile string) (string, error) {
	if wasmPathOverride != "" {
		return wasmPathOverride, nil
	}
	root, err := stellarutil.FindStellarRoot()
	if err != nil {
		return "", fmt.Errorf("locate chainlink-stellar root: %w", err)
	}
	return filepath.Join(root, "target", "wasm32v1-none", "release", defaultWasmFile), nil
}
