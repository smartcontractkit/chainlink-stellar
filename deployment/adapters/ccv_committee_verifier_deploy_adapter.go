package adapters

import (
	"fmt"
	"path/filepath"
	"strings"

	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// StellarCCVCommitteeVerifierDeployAdapter implements
// ccvdeploymentadapters.CommitteeVerifierDeployAdapter for Stellar chains. It
// deploys the CommitteeVerifier AND its VersionedVerifierResolver (EVM deploys
// the verifier and its resolver as a pair) by reusing the existing
// skip-if-exists component sequences, which reconcile rather than redeploy on
// a re-run. The resolver run sees the verifier's refs as existing addresses,
// so the pair is skip-consistent across both halves.
//
// Field mapping: StorageLocations strings pass through as bytes; FeeAggregator
// and AllowlistAdmin normalize to strkeys (AllowlistAdmin empty → the sequence
// defaults it to the owner); Qualifier "" deploys the default instance; the
// RMNProxy strkey is resolved from ExistingAddresses. Params.Version is
// ignored — the stellar deploy uses the pinned release wasm. DeployerContract
// has no stellar meaning (no deployer factory) and is ignored. Contracts
// remain owned by the deployer key (the sequence's owner default); BatchOps
// stays nil — Soroban deploys are EOA-only.
type StellarCCVCommitteeVerifierDeployAdapter struct{}

var _ ccvdeploymentadapters.CommitteeVerifierDeployAdapter = (*StellarCCVCommitteeVerifierDeployAdapter)(nil)

var stellarDeployCommitteeVerifierAndResolver = cldf_ops.NewSequence(
	"stellar-deploy-committee-verifier-and-resolver",
	stellarops.ContractDeploymentVersion,
	"Deploys and initializes the CommitteeVerifier and its VersionedVerifierResolver on a Stellar chain (skip-if-exists)",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input ccvdeploymentadapters.CommitteeVerifierDeployInput) (ccvdeploymentadapters.CommitteeVerifierDeployOutput, error) {
		ch, ok := chains.StellarChains()[input.ChainSelector]
		if !ok {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{},
				fmt.Errorf("stellar chain not found for selector %d", input.ChainSelector)
		}

		cvInput, vvrInput, err := toStellarCommitteeVerifierDeployInputs(input)
		if err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{}, err
		}

		deps, err := stellarComponentDeps(ch, b)
		if err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{}, err
		}

		cvReport, err := cldf_ops.ExecuteSequence(b, sequences.DeployCommitteeVerifier, deps, cvInput)
		if err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{},
				fmt.Errorf("stellar DeployCommitteeVerifier failed: %w", err)
		}

		// The resolver sees the verifier's refs as existing so a verifier
		// deployed by this run is never re-derived from a salt prediction.
		vvrInput.ExistingAddresses = append(vvrInput.ExistingAddresses, cvReport.Output.Refs...)
		vvrReport, err := cldf_ops.ExecuteSequence(b, sequences.DeployVVR, deps, vvrInput)
		if err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{},
				fmt.Errorf("stellar DeployVVR failed: %w", err)
		}

		// Wire the resolver to this committee's verifier and give the verifier
		// its remote-chain configs, filling only what is missing: a rerun must
		// not need owner-gated writes (ownership may have moved to a timelock)
		// and must not overwrite mappings or settings changed since deploy —
		// intentional changes go through the config changesets. Without the
		// wiring a lane consulting the resolver fails with
		// Inbound/OutboundImplementationNotFound; without the remote configs the
		// verifier's get_fee fails with RemoteChainNotSupported.
		verifier := cvReport.Output.ContractID
		selects := chains.ListChainSelectors()
		if err := wireVVRImplementations(b, deps.StellarDeps, vvrReport.Output.ContractID, verifier, selects); err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{},
				fmt.Errorf("wire resolver for committee %q: %w", input.Params.Qualifier, err)
		}
		routerStrkey, err := lookupStellarComponentStrkey(input.ExistingAddresses, input.ChainSelector, stellarccip.RouterDatastoreRef(), "Router")
		if err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{}, err
		}
		if err := fillCommitteeVerifierRemoteConfigs(b, deps.StellarDeps, verifier, routerStrkey, selects); err != nil {
			return ccvdeploymentadapters.CommitteeVerifierDeployOutput{},
				fmt.Errorf("fill remote chain configs for committee %q: %w", input.Params.Qualifier, err)
		}

		addresses := append(cvReport.Output.Refs, vvrReport.Output.Refs...)
		return ccvdeploymentadapters.CommitteeVerifierDeployOutput{Addresses: addresses}, nil
	},
)

func (a *StellarCCVCommitteeVerifierDeployAdapter) DeployCommitteeVerifier() *cldf_ops.Sequence[ccvdeploymentadapters.CommitteeVerifierDeployInput, ccvdeploymentadapters.CommitteeVerifierDeployOutput, cldf_chain.BlockChains] {
	return stellarDeployCommitteeVerifierAndResolver
}

// toStellarCommitteeVerifierDeployInputs converts the chain-agnostic input
// into the CommitteeVerifier and VVR sequence inputs.
func toStellarCommitteeVerifierDeployInputs(input ccvdeploymentadapters.CommitteeVerifierDeployInput) (sequences.DeployCommitteeVerifierInput, sequences.DeployVVRInput, error) {
	if len(input.Params.StorageLocations) == 0 {
		return sequences.DeployCommitteeVerifierInput{}, sequences.DeployVVRInput{},
			fmt.Errorf("committee %q: storageLocations is required: pass the committee storage locations", input.Params.Qualifier)
	}

	rmnProxy, err := lookupStellarComponentStrkey(input.ExistingAddresses, input.ChainSelector, stellarccip.RMNProxyDatastoreRef(), "RMN Proxy")
	if err != nil {
		return sequences.DeployCommitteeVerifierInput{}, sequences.DeployVVRInput{}, err
	}

	feeAggregator, err := normalizePoolConfigAddress(input.Params.FeeAggregator, fmt.Sprintf("committee %q FeeAggregator", input.Params.Qualifier), true)
	if err != nil {
		return sequences.DeployCommitteeVerifierInput{}, sequences.DeployVVRInput{}, err
	}

	allowlistAdmin := ""
	if input.Params.AllowlistAdmin != "" {
		allowlistAdmin, err = normalizePoolConfigAddress(input.Params.AllowlistAdmin, fmt.Sprintf("committee %q AllowlistAdmin", input.Params.Qualifier), true)
		if err != nil {
			return sequences.DeployCommitteeVerifierInput{}, sequences.DeployVVRInput{}, err
		}
	}

	cvWasm, err := stellarComponentWasmPath("ccvs_committee_verifier.wasm")
	if err != nil {
		return sequences.DeployCommitteeVerifierInput{}, sequences.DeployVVRInput{}, err
	}
	vvrWasm, err := stellarComponentWasmPath("ccvs_versioned_verifier_resolver.wasm")
	if err != nil {
		return sequences.DeployCommitteeVerifierInput{}, sequences.DeployVVRInput{}, err
	}

	storageLocations := make([][]byte, len(input.Params.StorageLocations))
	for i, s := range input.Params.StorageLocations {
		storageLocations[i] = []byte(s)
	}

	cv := sequences.DeployCommitteeVerifierInput{
		ChainSelector:     input.ChainSelector,
		Qualifier:         input.Params.Qualifier,
		WasmPath:          cvWasm,
		StorageLocations:  storageLocations,
		RmnProxy:          rmnProxy,
		AllowlistAdmin:    allowlistAdmin,
		FeeAggregator:     feeAggregator,
		ExistingAddresses: input.ExistingAddresses,
	}
	vvr := sequences.DeployVVRInput{
		ChainSelector:     input.ChainSelector,
		Qualifier:         input.Params.Qualifier,
		WasmPath:          vvrWasm,
		FeeAggregator:     feeAggregator,
		ExistingAddresses: input.ExistingAddresses,
	}
	return cv, vvr, nil
}

// stellarComponentDeps builds the ComponentDeps the component deploy sequences
// take, from the CLDF stellar chain entry and the run's bundle.
func stellarComponentDeps(ch cldf_stellar.Chain, b cldf_ops.Bundle) (sequences.ComponentDeps, error) {
	dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
	if err != nil {
		return sequences.ComponentDeps{}, fmt.Errorf("stellar deployer from chain: %w", err)
	}
	signerAddr := dep.SignerAddress()
	if signerAddr == "" {
		return sequences.ComponentDeps{}, fmt.Errorf("chain signer exposes no address")
	}
	return sequences.ComponentDeps{
		StellarDeps:       stellardeps.FromDeployer(dep),
		NetworkPassphrase: ch.NetworkPassphrase,
		DeployerAddress:   signerAddr,
		Ledger:            dep,
	}, nil
}

// lookupStellarComponentStrkey resolves a component's strkey from
// ExistingAddresses through its datastore ref.
func lookupStellarComponentStrkey(refs []datastore.AddressRef, chainSelector uint64, ref stellarccip.DatastoreSorobanContractRef, name string) (string, error) {
	strkey, err := ref.LookupStrkey(laneLocalDataStore(refs), chainSelector)
	if err != nil {
		return "", fmt.Errorf("deploy %s first: %w", name, err)
	}
	return strkey, nil
}

// stellarComponentWasmPath returns the conventional release-build path for a
// component wasm under the chainlink-stellar root.
func stellarComponentWasmPath(defaultWasmFile string) (string, error) {
	root, err := stellarutil.FindStellarRoot()
	if err != nil {
		return "", fmt.Errorf("locate chainlink-stellar root: %w", err)
	}
	return filepath.Join(root, "target", "wasm32v1-none", "release", defaultWasmFile), nil
}

// wireVVRImplementations wires a resolver to its committee's verifier, filling
// only missing mappings: outbound per selector in the environment, inbound at
// the default version tag. Existing mappings — including ones changed since
// deploy — are preserved, so a rerun performs no owner-gated writes when
// everything is already wired.
func wireVVRImplementations(b cldf_ops.Bundle, deps stellardeps.StellarDeps, vvrID, cvID string, selectors []uint64) error {
	client := vvrbindings.NewVersionedVerifierResolverClient(deps.Invoker, vvrID)
	existingOutbound, err := client.GetAllOutboundImplementations(b.GetContext())
	if err != nil {
		return fmt.Errorf("read outbound implementations: %w", err)
	}
	wired := make(map[uint64]bool, len(existingOutbound))
	for _, impl := range existingOutbound {
		wired[impl.DestChainSelector] = true
	}
	missingOutbound := make([]vvrbindings.OutboundImplementationUpdate, 0, len(selectors))
	for _, sel := range selectors {
		if wired[sel] {
			continue
		}
		verifier := cvID
		missingOutbound = append(missingOutbound, vvrbindings.OutboundImplementationUpdate{
			DestChainSelector: sel,
			Verifier:          &verifier,
		})
	}
	if len(missingOutbound) > 0 {
		if _, err := cldf_ops.ExecuteOperation(b, vvrops.ApplyOutboundImplUpdates, deps, vvrops.ApplyOutboundImplUpdatesInput{
			ContractID:      vvrID,
			Implementations: missingOutbound,
		}); err != nil {
			return fmt.Errorf("apply outbound implementation updates: %w", err)
		}
	}

	existingInbound, err := client.GetAllInboundImplementations(b.GetContext())
	if err != nil {
		return fmt.Errorf("read inbound implementations: %w", err)
	}
	tag := stellarutil.DefaultCommitteeVerifierVersionTag()
	for _, impl := range existingInbound {
		if impl.Version == tag {
			return nil
		}
	}
	verifier := cvID
	if _, err := cldf_ops.ExecuteOperation(b, vvrops.ApplyInboundImplUpdates, deps, vvrops.ApplyInboundImplUpdatesInput{
		ContractID: vvrID,
		Implementations: []vvrbindings.InboundImplementationUpdate{{
			Version:  tag,
			Verifier: &verifier,
		}},
	}); err != nil {
		return fmt.Errorf("apply inbound implementation updates: %w", err)
	}
	return nil
}

// fillCommitteeVerifierRemoteConfigs fills the verifier's missing remote-chain
// configs — the full deploy is otherwise the only writer, so a committee added
// after the protocol stack cannot quote (get_fee fails with
// RemoteChainNotSupported). Router, per-family verification gas, and zero
// fee/payload defaults; existing entries are preserved.
func fillCommitteeVerifierRemoteConfigs(b cldf_ops.Bundle, deps stellardeps.StellarDeps, cvID, routerStrkey string, selectors []uint64) error {
	client := cvbindings.NewCommitteeVerifierClient(deps.Invoker, cvID)
	configs := make([]cvbindings.RemoteChainConfig, 0, len(selectors))
	for _, sel := range selectors {
		if _, err := client.GetRemoteChainConfig(b.GetContext(), sel); err == nil {
			continue // existing entry: preserve it
		} else if !strings.Contains(err.Error(), "RemoteChainNotSupported") {
			return fmt.Errorf("read remote chain config for %d: %w", sel, err)
		}
		gas, err := sequences.CvGasForVerificationForRemote(sel)
		if err != nil {
			return fmt.Errorf("gas for verification for remote %d: %w", sel, err)
		}
		router := routerStrkey
		configs = append(configs, cvbindings.RemoteChainConfig{
			RemoteChainSelector: sel,
			FeeUsdCents:         0,
			GasForVerification:  gas,
			PayloadSizeBytes:    0,
			AllowlistEnabled:    false,
			Router:              &router,
		})
	}
	if len(configs) == 0 {
		return nil
	}
	if _, err := cldf_ops.ExecuteOperation(b, cvops.ApplyRemoteChainCfgUpdates, deps, cvops.ApplyRemoteChainCfgUpdatesInput{
		ContractID: cvID,
		Configs:    configs,
	}); err != nil {
		return fmt.Errorf("apply remote chain config updates: %w", err)
	}
	return nil
}
