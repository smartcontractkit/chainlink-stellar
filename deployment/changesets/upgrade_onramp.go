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
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

const defaultUpgradeOnRampDescription = "Upgrade the Stellar OnRamp in place"

// UpgradeOnRampConfig is the input of the UpgradeOnRamp changeset.
type UpgradeOnRampConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
}

// UpgradeOnRamp upgrades the deployed Stellar OnRamp in place: the freshly
// compiled release WASM is uploaded and swapped in at the same contract ID
// (address and storage preserved). A deployer-owned OnRamp upgrades directly;
// a timelock-owned one gets a proposal on the MCMS stack named by the MCMS
// qualifier (default CLLCCIP), with the requested timelock action (default
// schedule).
func UpgradeOnRamp(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeOnRampConfig]] {
	return cldf.CreateChangeSet(
		func(e cldf.Environment, input changesets.WithMCMS[UpgradeOnRampConfig]) (cldf.ChangesetOutput, error) {
			return applyUpgradeOnRamp(e, mcmsRegistry, input)
		},
		verifyUpgradeOnRamp,
	)
}

func verifyUpgradeOnRamp(e cldf.Environment, input changesets.WithMCMS[UpgradeOnRampConfig]) error {
	onRampDep := []componentDep{{name: "OnRamp", ref: stellarccip.OnRampDatastoreRef()}}
	return verifyComponent(e, input.Cfg.ChainSelector, onRampDep, nil)
}

func applyUpgradeOnRamp(
	e cldf.Environment,
	mcmsRegistry *changesets.MCMSReaderRegistry,
	input changesets.WithMCMS[UpgradeOnRampConfig],
) (cldf.ChangesetOutput, error) {
	cfg := input.Cfg
	onRampRef := stellarccip.OnRampDatastoreRef()
	onRampID, err := onRampRef.LookupStrkey(e.DataStore, cfg.ChainSelector)
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("resolve OnRamp: %w", err)
	}
	refs, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("fetch existing address refs: %w", err)
	}
	mcmsInput := input.MCMS
	mcmsInput.Qualifier = cmp.Or(mcmsInput.Qualifier, cciputils.CLLQualifier)
	mcmsInput.Description = cmp.Or(mcmsInput.Description, defaultUpgradeOnRampDescription)
	// The CLL timelock, when deployed, routes the upgrade proposal. An
	// EOA-owned OnRamp upgrades directly with no MCMS involvement, so a
	// missing timelock is not an error here: an empty governance address
	// makes the sequence route on the OnRamp owner read.
	governance, _ := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, mcmsInput.Qualifier)

	report, err := cldf_ops.ExecuteSequence(e.OperationsBundle, sequences.StellarUpgradeContractsViaMCMS, e.BlockChains, sequences.StellarUpgradeContractsInput{
		ChainSelector:  cfg.ChainSelector,
		ContractRef:    []datastore.AddressRef{onRampRef.FullAddressRef(cfg.ChainSelector, onRampID)},
		GovernanceAddr: governance,
	})
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("upgrade OnRamp on chain %d: %w", cfg.ChainSelector, err)
	}
	return changesets.NewOutputBuilder(e, mcmsRegistry).
		WithReports(report.ExecutionReports).
		WithBatchOps(report.Output.BatchOps).
		Build(mcmsInput)
}
