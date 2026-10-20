package changesets

import (
	"cmp"
	"fmt"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	cciprecvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
	rmnproxyops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// This file is the single per-component in-place upgrade changeset: one
// constructor per upgradeable contract, all sharing one config, one spec and
// one Apply body. Coverage is every in-place-upgradeable Soroban CCIP
// contract (17). Excluded: MCMS (upgraded through its own dedicated flow),
// the timelock (its upgrade entrypoint takes an explicit caller and is
// admin-gated, not owner-gated), the link/bnm tokens, the pool-hooks
// extractor and the registry scaffold (no upgrade entrypoint), and the
// cre/data-feeds contracts (out of scope).

// UpgradeComponentConfig is the input of every per-component in-place upgrade
// changeset. Qualifier selects the contract instance for multi-instance
// components (Committee Verifier, VVR, CCIP Receiver, Executor map empty to
// their default instance; pools require one, e.g. the token symbol); it is
// independent of the MCMS input's qualifier, which selects the MCMS/timelock
// stack.
type UpgradeComponentConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	Qualifier     string `json:"qualifier,omitempty" yaml:"qualifier,omitempty"`
}

// upgradeComponentSpec fixes one component changeset to its datastore ref.
type upgradeComponentSpec struct {
	name string
	// refFn resolves the component's datastore ref from the instance
	// qualifier: singletons ignore it, per-instance components map empty to
	// their default, pools require a non-empty one (enforced by
	// requiresQualifier).
	refFn             func(qualifier string) stellarccip.DatastoreSorobanContractRef
	requiresQualifier bool
	// opsType is the Stellar ops contract type, set when the datastore type
	// differs (e.g. "ARMProxy" vs "RmnProxy"); the upgrade sequence keys on it.
	opsType string
}

func (s upgradeComponentSpec) contractType(ref stellarccip.DatastoreSorobanContractRef) string {
	return cmp.Or(s.opsType, string(ref.Type))
}

// fixedRef lifts a singleton ref constructor into the qualifier-resolving shape.
func fixedRef(fn func() stellarccip.DatastoreSorobanContractRef) func(string) stellarccip.DatastoreSorobanContractRef {
	return func(string) stellarccip.DatastoreSorobanContractRef { return fn() }
}

// UpgradeOnRamp upgrades the deployed Stellar OnRamp in place (wasm swap at
// the same contract ID, address and storage preserved).
func UpgradeOnRamp(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "OnRamp",
		refFn: fixedRef(stellarccip.OnRampDatastoreRef),
	})
}

// UpgradeOffRamp upgrades the deployed Stellar OffRamp in place.
func UpgradeOffRamp(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "OffRamp",
		refFn: fixedRef(stellarccip.OffRampDatastoreRef),
	})
}

// UpgradeRouter upgrades the deployed Stellar Router in place.
func UpgradeRouter(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "Router",
		refFn: fixedRef(stellarccip.RouterDatastoreRef),
	})
}

// UpgradeFeeQuoter upgrades the deployed Stellar FeeQuoter in place.
func UpgradeFeeQuoter(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "FeeQuoter",
		refFn: fixedRef(stellarccip.FeeQuoterDatastoreRef),
	})
}

// UpgradeTokenAdminRegistry upgrades the deployed Stellar TokenAdminRegistry in place.
func UpgradeTokenAdminRegistry(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:    "TokenAdminRegistry",
		refFn:   fixedRef(stellarccip.TokenAdminRegistryDatastoreRef),
		opsType: tarops.ContractType,
	})
}

// UpgradeRmnProxy upgrades the deployed Stellar RMN Proxy in place.
func UpgradeRmnProxy(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:    "RMN Proxy",
		refFn:   fixedRef(stellarccip.RMNProxyDatastoreRef),
		opsType: rmnproxyops.ContractType,
	})
}

// UpgradeRmnRemote upgrades the deployed Stellar RMN Remote in place.
func UpgradeRmnRemote(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:    "RMN Remote",
		refFn:   fixedRef(stellarccip.RMNRemoteDatastoreRef),
		opsType: rmnremoteops.ContractType,
	})
}

// UpgradeRampRegistry upgrades the deployed Stellar Ramp Registry in place.
func UpgradeRampRegistry(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "RampRegistry",
		refFn: fixedRef(stellarccip.RampRegistryDatastoreRef),
	})
}

// UpgradeCommitteeVerifier upgrades one deployed Stellar Committee Verifier
// in place; the qualifier selects the instance (empty keeps the default).
func UpgradeCommitteeVerifier(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "Committee Verifier",
		refFn: stellarccip.CommitteeVerifierDatastoreRefWithQualifier,
	})
}

// UpgradeVersionedVerifierResolver upgrades one deployed Stellar
// VersionedVerifierResolver in place; the qualifier selects the committee
// instance (empty keeps the default).
func UpgradeVersionedVerifierResolver(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:    "VersionedVerifierResolver",
		refFn:   stellarccip.VVRDatastoreRefWithQualifier,
		opsType: vvrops.ContractType,
	})
}

// UpgradeCCIPReceiver upgrades one deployed Stellar CCIP Receiver in place;
// the qualifier selects the receiver instance (empty keeps the unqualified ref).
func UpgradeCCIPReceiver(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:    "CCIP Receiver",
		refFn:   stellarccip.CCIPReceiverDatastoreRefWithQualifier,
		opsType: cciprecvops.ContractType,
	})
}

// UpgradeExecutor upgrades one deployed Stellar Executor in place; the
// qualifier selects the instance (empty keeps the default).
func UpgradeExecutor(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:  "Executor",
		refFn: stellarccip.ExecutorDatastoreRefWithQualifier,
	})
}

// UpgradeBurnMintPool upgrades one deployed Stellar burn-mint token pool in
// place; the qualifier identifies the pool instance, typically the token symbol.
func UpgradeBurnMintPool(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:              "BurnMintPool",
		refFn:             stellarccip.BurnMintPoolDatastoreRef,
		requiresQualifier: true,
	})
}

// UpgradeLockReleasePool upgrades one deployed Stellar lock-release token
// pool in place; the qualifier identifies the pool instance, typically the
// token symbol.
func UpgradeLockReleasePool(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:              "LockReleasePool",
		refFn:             stellarccip.LockReleasePoolDatastoreRef,
		requiresQualifier: true,
	})
}

// UpgradeSiloedLockReleasePool upgrades one deployed Stellar siloed
// lock-release token pool in place; the qualifier identifies the pool
// instance, typically the token symbol.
func UpgradeSiloedLockReleasePool(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:              "SiloedLockReleasePool",
		refFn:             stellarccip.SiloedLockReleasePoolDatastoreRef,
		requiresQualifier: true,
	})
}

// UpgradeTokenLockBox upgrades one deployed Stellar token lock box in place;
// the qualifier identifies the lock-box instance, typically the token symbol.
func UpgradeTokenLockBox(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:              "TokenLockBox",
		refFn:             stellarccip.TokenLockBoxDatastoreRef,
		requiresQualifier: true,
	})
}

// UpgradeAdvancedPoolHooks upgrades one deployed Stellar advanced pool hooks
// contract in place; the qualifier identifies the hooks instance, typically
// the token symbol.
func UpgradeAdvancedPoolHooks(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return newUpgradeComponent(mcmsRegistry, upgradeComponentSpec{
		name:              "AdvancedPoolHooks",
		refFn:             stellarccip.AdvancedPoolHooksDatastoreRef,
		requiresQualifier: true,
	})
}

// newUpgradeComponent builds one component's in-place upgrade changeset: the
// freshly compiled release WASM is uploaded and swapped in, routed by the
// current owner — directly while the deployer owns the component, as a
// proposal on the MCMS stack named by the MCMS qualifier (default CLLCCIP)
// once the CLL timelock does.
func newUpgradeComponent(mcmsRegistry *changesets.MCMSReaderRegistry, spec upgradeComponentSpec) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]] {
	return cldf.CreateChangeSet(
		func(e cldf.Environment, input changesets.WithMCMS[UpgradeComponentConfig]) (cldf.ChangesetOutput, error) {
			return applyUpgradeComponent(e, mcmsRegistry, spec, input)
		},
		func(e cldf.Environment, input changesets.WithMCMS[UpgradeComponentConfig]) error {
			// The qualifier check precedes ref resolution: an empty pool
			// qualifier would otherwise build an unqualified ref whose lookup
			// fails with the misleading "deploy <name> first".
			if spec.requiresQualifier && input.Cfg.Qualifier == "" {
				return fmt.Errorf("qualifier is required: one pool per token, identify the instance (e.g. the token symbol)")
			}
			ref := spec.refFn(input.Cfg.Qualifier)
			if _, ok := stellarutil.UpgradeWasmFilename(spec.contractType(ref)); !ok {
				return fmt.Errorf("no upgrade WASM mapping for %s type %q", spec.name, spec.contractType(ref))
			}
			return verifyComponent(e, input.Cfg.ChainSelector, []componentDep{{name: spec.name, ref: ref}}, nil)
		},
	)
}

func applyUpgradeComponent(
	e cldf.Environment,
	mcmsRegistry *changesets.MCMSReaderRegistry,
	spec upgradeComponentSpec,
	input changesets.WithMCMS[UpgradeComponentConfig],
) (cldf.ChangesetOutput, error) {
	cfg := input.Cfg
	ref := spec.refFn(cfg.Qualifier)
	componentID, err := ref.LookupStrkey(e.DataStore, cfg.ChainSelector)
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("resolve %s: %w", spec.name, err)
	}
	refs, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("fetch existing address refs: %w", err)
	}
	mcmsInput := input.MCMS
	mcmsInput.Qualifier = cmp.Or(mcmsInput.Qualifier, cciputils.CLLQualifier)
	mcmsInput.Description = cmp.Or(mcmsInput.Description, "Upgrade the Stellar "+spec.name+" in place")
	// An EOA-owned component upgrades directly with no MCMS involvement, so a
	// missing timelock is not an error: the empty governance address makes the
	// sequence route on the owner read instead.
	governance, _ := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, mcmsInput.Qualifier)

	addrRef := ref.FullAddressRef(cfg.ChainSelector, componentID)
	addrRef.Type = datastore.ContractType(spec.contractType(ref))

	report, err := cldf_ops.ExecuteSequence(e.OperationsBundle, sequences.StellarUpgradeContractsViaMCMS, e.BlockChains, sequences.StellarUpgradeContractsInput{
		ChainSelector:  cfg.ChainSelector,
		ContractRef:    []datastore.AddressRef{addrRef},
		GovernanceAddr: governance,
	})
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("upgrade %s on chain %d: %w", spec.name, cfg.ChainSelector, err)
	}
	return changesets.NewOutputBuilder(e, mcmsRegistry).
		WithReports(report.ExecutionReports).
		WithBatchOps(report.Output.BatchOps).
		Build(mcmsInput)
}
