package sequences

import (
	"context"
	"fmt"

	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	ccvdeployment "github.com/smartcontractkit/chainlink-ccv/deployment"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// RunStellarCCIPFullDeployForCCV runs the full Stellar CCIP Soroban deploy with CCV environment topology
// (converted to offchain). Used by ccv/chain and by [DeployStellarCCIPContracts].
// topology must be non-nil so committee verifier signature config can be derived after conversion.
func RunStellarCCIPFullDeployForCCV(
	ctx context.Context,
	opBundle cldfops.Bundle,
	deps stellardeps.StellarDeps,
	host stellarccip.CCIPDevenvHost,
	allSelectors []uint64,
	selector uint64,
	topology *ccvdeployment.EnvironmentTopology,
	existingAddresses []datastore.AddressRef,
) (seq_core.OnChainOutput, error) {
	if topology == nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("RunStellarCCIPFullDeployForCCV: CCV EnvironmentTopology is nil (required for Stellar committee verifier signature quorum config)")
	}
	offTopo, err := stellarccip.CCVEnvironmentTopologyToOffchain(topology)
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	return RunStellarCCIPFullDeploy(ctx, opBundle, deps, host, offTopo, DeployStellarCCIPInnerInput{
		ChainSelector:     selector,
		AllSelectors:      allSelectors,
		ExistingAddresses: existingAddresses,
	})
}

// DeployStellarCCIPContracts deploys the full Stellar CCIP stack and returns a sealed in-memory datastore
// containing address refs for merge into the environment datastore. opBundle must be the CLDF bundle for
// this run (parent changeset bundle from sequences, or [cldfops.NewBundle] for tests).
// topology must be non-nil so committee verifier signature config can be applied ([RunStellarCCIPFullDeployForCCV]).
func DeployStellarCCIPContracts(
	ctx context.Context,
	opBundle cldfops.Bundle,
	host stellarccip.CCIPDevenvHost,
	allSelectors []uint64,
	selector uint64,
	topology *ccvdeployment.EnvironmentTopology,
	existingAddresses []datastore.AddressRef,
) (datastore.DataStore, error) {
	if host == nil {
		return nil, fmt.Errorf("stellar CCIP deploy host is nil")
	}
	if opBundle.GetContext == nil {
		return nil, fmt.Errorf("stellar CCIP deploy: operations bundle must provide GetContext")
	}
	deps := stellardeps.FromDeployer(host.Deployer())
	out, err := RunStellarCCIPFullDeployForCCV(ctx, opBundle, deps, host, allSelectors, selector, topology, existingAddresses)
	if err != nil {
		return nil, err
	}
	mem := datastore.NewMemoryDataStore()
	for _, ref := range out.Addresses {
		if err := mem.AddressRefStore.Upsert(ref); err != nil {
			return nil, err
		}
	}
	return mem.Seal(), nil
}

// StellarCCIPDeploySession runs the full Stellar CCIP Soroban deploy via [DeployStellarCCIPContracts].
type StellarCCIPDeploySession struct {
	ctx               context.Context
	opBundle          cldfops.Bundle
	host              stellarccip.CCIPDevenvHost
	allSelectors      []uint64
	selector          uint64
	topology          *ccvdeployment.EnvironmentTopology
	existingAddresses []datastore.AddressRef
}

// NewStellarCCIPDeploySession validates inputs for a Stellar CCIP deploy run.
// opBundle must be a valid CLDF bundle (e.g. the parent changeset bundle from chainlink-ccv DeployContractsForSelector,
// or [cldfops.NewBundle] for standalone tests); all Soroban operations run on this bundle.
func NewStellarCCIPDeploySession(
	ctx context.Context,
	opBundle cldfops.Bundle,
	host stellarccip.CCIPDevenvHost,
	allSelectors []uint64,
	selector uint64,
	topology *ccvdeployment.EnvironmentTopology,
	existingAddresses []datastore.AddressRef,
) (*StellarCCIPDeploySession, error) {
	if host == nil {
		return nil, fmt.Errorf("stellar CCIP deploy host is nil")
	}
	if opBundle.GetContext == nil {
		return nil, fmt.Errorf("stellar CCIP deploy: operations bundle must provide GetContext")
	}
	return &StellarCCIPDeploySession{
		ctx:               ctx,
		opBundle:          opBundle,
		host:              host,
		allSelectors:      allSelectors,
		selector:          selector,
		topology:          topology,
		existingAddresses: existingAddresses,
	}, nil
}

// RunAllPhasesAndSeal deploys and configures the full stack and returns a sealed datastore view.
func (s *StellarCCIPDeploySession) RunAllPhasesAndSeal() (datastore.DataStore, error) {
	return DeployStellarCCIPContracts(s.ctx, s.opBundle, s.host, s.allSelectors, s.selector, s.topology, s.existingAddresses)
}
