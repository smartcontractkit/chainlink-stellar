package changesets

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

const defaultActivateRMNDescription = "Accept Stellar RMN Remote ownership on the RMNMCMS timelock"

// ActivateRMNConfig is the input of the ActivateRMN changeset. Empty
// qualifiers default to RMNMCMS (governance) and UltraFastCurse (curse admin).
type ActivateRMNConfig struct {
	ChainSelector       uint64   `json:"chainSelector" yaml:"chainSelector"`
	GovernanceQualifier string   `json:"governanceQualifier,omitempty" yaml:"governanceQualifier,omitempty"`
	FastCurseQualifier  string   `json:"fastCurseQualifier,omitempty" yaml:"fastCurseQualifier,omitempty"`
	ExtraCurseAdmins    []string `json:"extraCurseAdmins,omitempty" yaml:"extraCurseAdmins,omitempty"`
}

// ActivateRMN hands an existing, deployer-owned RMN Remote to MCMS
// governance. Unlike the component changesets it emits an MCMS proposal:
// it grants curse admin to the fast-curse timelock, transfers ownership
// directly, and returns one timelock proposal on the governance stack that
// runs accept_ownership.
func ActivateRMN(mcmsRegistry *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[ActivateRMNConfig]] {
	return cldf.CreateChangeSet(
		func(e cldf.Environment, input changesets.WithMCMS[ActivateRMNConfig]) (cldf.ChangesetOutput, error) {
			return applyActivateRMN(e, mcmsRegistry, input)
		},
		verifyActivateRMN,
	)
}

func verifyActivateRMN(e cldf.Environment, input changesets.WithMCMS[ActivateRMNConfig]) error {
	cfg := input.Cfg
	rmnDep := []componentDep{{name: "RMN Remote", ref: stellarccip.RMNRemoteDatastoreRef()}}
	return verifyComponent(e, cfg.ChainSelector, rmnDep, func() error {
		refs, err := e.DataStore.Addresses().Fetch()
		if err != nil {
			return fmt.Errorf("fetch existing address refs: %w", err)
		}
		for _, qual := range []string{governanceQualifier(cfg), fastCurseQualifier(cfg)} {
			if _, ok := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, qual); !ok {
				return fmt.Errorf("deploy the %s MCMS stack (RBACTimelock) on chain %d first", qual, cfg.ChainSelector)
			}
		}
		return nil
	})
}

func applyActivateRMN(
	e cldf.Environment,
	mcmsRegistry *changesets.MCMSReaderRegistry,
	input changesets.WithMCMS[ActivateRMNConfig],
) (cldf.ChangesetOutput, error) {
	cfg := input.Cfg
	govQual := governanceQualifier(cfg)
	refs, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("fetch existing address refs: %w", err)
	}
	govTL, ok := mcmsutil.FindExistingStellarTimelock(refs, cfg.ChainSelector, govQual)
	if !ok {
		return cldf.ChangesetOutput{}, fmt.Errorf("no RBACTimelock for qualifier %q on chain %d", govQual, cfg.ChainSelector)
	}

	activate, err := cldf_ops.ExecuteSequence(e.OperationsBundle, sequences.StellarActivateRMN, e.BlockChains, sequences.StellarActivateRMNInput{
		ChainSelector:       cfg.ChainSelector,
		ExistingAddresses:   refs,
		GovernanceQualifier: govQual,
		FastCurseQualifier:  fastCurseQualifier(cfg),
		ExtraCurseAdmins:    cfg.ExtraCurseAdmins,
	})
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("activate RMN Remote on chain %d: %w", cfg.ChainSelector, err)
	}
	if len(activate.Output.Addresses) != 1 {
		return cldf.ChangesetOutput{}, fmt.Errorf("activate RMN Remote: expected 1 recorded ref, got %d", len(activate.Output.Addresses))
	}
	rmnRef := activate.Output.Addresses[0]

	// The ownership ops dispatch on the stellar-local RMN Remote type; the
	// recorded ref keeps the upstream type the datastore uses.
	acceptRef := rmnRef
	acceptRef.Type = datastore.ContractType(rmnremoteops.ContractType)
	accept, err := cldf_ops.ExecuteSequence(e.OperationsBundle, sequences.StellarAcceptOwnership, e.BlockChains, sequences.StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: cfg.ChainSelector,
			ContractRef:   []datastore.AddressRef{acceptRef},
			ProposedOwner: govTL,
		},
		GovernanceAddr: govTL,
	})
	if err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("accept RMN Remote ownership on chain %d: %w", cfg.ChainSelector, err)
	}

	ds := datastore.NewMemoryDataStore()
	if err := ds.Addresses().Upsert(rmnRef); err != nil {
		return cldf.ChangesetOutput{}, fmt.Errorf("record RMN Remote ref: %w", err)
	}
	return changesets.NewOutputBuilder(e, mcmsRegistry).
		WithReports(slices.Concat(activate.ExecutionReports, accept.ExecutionReports)).
		WithDataStore(ds).
		WithBatchOps(slices.Concat(activate.Output.BatchOps, accept.Output.BatchOps)).
		Build(mcmsutils.Input{
			OverridePreviousRoot: input.MCMS.OverridePreviousRoot,
			ValidUntil:           input.MCMS.ValidUntil,
			TimelockAction:       mcmstypes.TimelockActionSchedule,
			Qualifier:            govQual,
			Description:          cmp.Or(input.MCMS.Description, defaultActivateRMNDescription),
		})
}

func governanceQualifier(cfg ActivateRMNConfig) string {
	return cmp.Or(cfg.GovernanceQualifier, cciputils.RMNTimelockQualifier)
}

func fastCurseQualifier(cfg ActivateRMNConfig) string {
	return cmp.Or(cfg.FastCurseQualifier, cciputils.UltraFastCurseMCMSQualifier)
}
