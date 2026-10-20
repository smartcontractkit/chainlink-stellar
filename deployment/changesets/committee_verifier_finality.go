package changesets

import (
	"cmp"
	"fmt"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

const defaultSetCommitteeVerifierFinalityDescription = "Set the Stellar Committee Verifier allowed finality"

// SetCommitteeVerifierFinalityConfig is the input of the SetCommitteeVerifierFinality changeset.
type SetCommitteeVerifierFinalityConfig struct {
	ChainSelector uint64 `json:"chainSelector" yaml:"chainSelector"`
	// Qualifier selects the verifier; empty keeps the default instance.
	Qualifier       string          `json:"qualifier,omitempty" yaml:"qualifier,omitempty"`
	AllowedFinality finality.Config `json:"allowedFinality" yaml:"allowedFinality"`
}

// SetCommitteeVerifierFinality sets the allowed finality of one Committee Verifier. A
// deployer-owned verifier is updated directly; a timelock-owned one gets a proposal on the
// MCMS stack named by the MCMS qualifier (default CLLCCIP), with the requested timelock action
// (default schedule).
func SetCommitteeVerifierFinality(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[SetCommitteeVerifierFinalityConfig]] {
	return cldf.CreateChangeSet(
		func(e cldf.Environment, input changesets.WithMCMS[SetCommitteeVerifierFinalityConfig]) (cldf.ChangesetOutput, error) {
			return applySetCommitteeVerifierFinality(e, mcmsRegistry, input)
		},
		verifySetCommitteeVerifierFinality,
	)
}

func verifySetCommitteeVerifierFinality(e cldf.Environment, input changesets.WithMCMS[SetCommitteeVerifierFinalityConfig]) error {
	cfg := input.Cfg
	cvDep := []componentDep{{name: "Committee Verifier", ref: stellarccip.CommitteeVerifierDatastoreRefWithQualifier(cfg.Qualifier)}}
	return verifyComponent(e, cfg.ChainSelector, cvDep, func() error {
		if err := cfg.AllowedFinality.Validate(); err != nil {
			return err
		}
		refs, err := e.DataStore.Addresses().Fetch()
		if err != nil {
			return fmt.Errorf("fetch existing address refs: %w", err)
		}
		qual := mcmsQualifier(input)
		if _, ok := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, qual); !ok {
			return fmt.Errorf("deploy the %s MCMS stack (RBACTimelock) on chain %d first", qual, cfg.ChainSelector)
		}
		return nil
	})
}

func applySetCommitteeVerifierFinality(
	e cldf.Environment,
	mcmsRegistry *changesets.MCMSReaderRegistry,
	input changesets.WithMCMS[SetCommitteeVerifierFinalityConfig],
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
	mcmsInput.Qualifier = mcmsQualifier(input)
	mcmsInput.Description = cmp.Or(mcmsInput.Description, defaultSetCommitteeVerifierFinalityDescription)
	timelock, ok := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, mcmsInput.Qualifier)
	if !ok {
		return cldf.ChangesetOutput{}, fmt.Errorf("no RBACTimelock for qualifier %q on chain %d", mcmsInput.Qualifier, cfg.ChainSelector)
	}

	report, err := cldf_ops.ExecuteSequence(e.OperationsBundle, sequences.StellarSetCommitteeVerifierFinality, e.BlockChains, sequences.StellarSetCommitteeVerifierFinalityInput{
		ChainSelector:     cfg.ChainSelector,
		CommitteeVerifier: cvRef.FullAddressRef(cfg.ChainSelector, cvID),
		Timelock:          timelock,
		AllowedFinality:   cfg.AllowedFinality,
	})
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("set committee verifier finality on chain %d: %w", cfg.ChainSelector, err)
	}
	return changesets.NewOutputBuilder(e, mcmsRegistry).
		WithReports(report.ExecutionReports).
		WithBatchOps(report.Output.BatchOps).
		Build(mcmsInput)
}

func mcmsQualifier(input changesets.WithMCMS[SetCommitteeVerifierFinalityConfig]) string {
	return cmp.Or(input.MCMS.Qualifier, cciputils.CLLQualifier)
}
