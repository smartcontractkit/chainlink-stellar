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

const defaultUpgradeCommitteeVerifierDescription = "Upgrade the Stellar Committee Verifier in place"

// UpgradeCommitteeVerifierConfig is the input of the UpgradeCommitteeVerifier changeset.
type UpgradeCommitteeVerifierConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	// Qualifier selects the verifier; empty keeps the default instance.
	Qualifier string `json:"qualifier,omitempty" yaml:"qualifier,omitempty"`
}

// UpgradeCommitteeVerifier upgrades one deployed Stellar Committee Verifier in
// place: the freshly compiled release WASM is uploaded and swapped in at the
// same contract ID (address and storage preserved). A deployer-owned verifier
// upgrades directly; a timelock-owned one gets a proposal on the MCMS stack
// named by the MCMS qualifier (default CLLCCIP), with the requested timelock
// action (default schedule).
func UpgradeCommitteeVerifier(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeCommitteeVerifierConfig]] {
	return cldf.CreateChangeSet(
		func(e cldf.Environment, input changesets.WithMCMS[UpgradeCommitteeVerifierConfig]) (cldf.ChangesetOutput, error) {
			return applyUpgradeCommitteeVerifier(e, mcmsRegistry, input)
		},
		verifyUpgradeCommitteeVerifier,
	)
}

func verifyUpgradeCommitteeVerifier(e cldf.Environment, input changesets.WithMCMS[UpgradeCommitteeVerifierConfig]) error {
	cfg := input.Cfg
	cvDep := []componentDep{{name: "Committee Verifier", ref: stellarccip.CommitteeVerifierDatastoreRefWithQualifier(cfg.Qualifier)}}
	return verifyComponent(e, cfg.ChainSelector, cvDep, nil)
}

func applyUpgradeCommitteeVerifier(
	e cldf.Environment,
	mcmsRegistry *changesets.MCMSReaderRegistry,
	input changesets.WithMCMS[UpgradeCommitteeVerifierConfig],
) (cldf.ChangesetOutput, error) {
	cfg := input.Cfg
	cvRef := stellarccip.CommitteeVerifierDatastoreRefWithQualifier(cfg.Qualifier)
	cvID, err := cvRef.LookupStrkey(e.DataStore, cfg.ChainSelector)
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("resolve committee verifier: %w", err)
	}
	refs, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("fetch existing address refs: %w", err)
	}
	mcmsInput := input.MCMS
	mcmsInput.Qualifier = cmp.Or(mcmsInput.Qualifier, cciputils.CLLQualifier)
	mcmsInput.Description = cmp.Or(mcmsInput.Description, defaultUpgradeCommitteeVerifierDescription)
	// A deployer-owned verifier needs no timelock: an empty governance address
	// makes the sequence route on the owner read.
	governance, _ := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, mcmsInput.Qualifier)

	report, err := cldf_ops.ExecuteSequence(e.OperationsBundle, sequences.StellarUpgradeContractsViaMCMS, e.BlockChains, sequences.StellarUpgradeContractsInput{
		ChainSelector:  cfg.ChainSelector,
		ContractRef:    []datastore.AddressRef{cvRef.FullAddressRef(cfg.ChainSelector, cvID)},
		GovernanceAddr: governance,
	})
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("upgrade committee verifier on chain %d: %w", cfg.ChainSelector, err)
	}
	return changesets.NewOutputBuilder(e, mcmsRegistry).
		WithReports(report.ExecutionReports).
		WithBatchOps(report.Output.BatchOps).
		Build(mcmsInput)
}
