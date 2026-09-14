package adapters

import (
	"fmt"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// StellarTransferOwnershipAdapter builds MCMS batch operations (or executes directly when the
// deployer account is still owner). Wiring matches EVM OpTransferOwnership / OpAcceptOwnership:
// the datastore timelock (governance) address gates the MCMS-batch path; MCMS contract address is
// not required to equal timelock.
type StellarTransferOwnershipAdapter struct {
	governanceAddr map[uint64]string
}

var _ deploy.TransferOwnershipAdapter = (*StellarTransferOwnershipAdapter)(nil)

// InitializeTimelockAddress caches the RBAC timelock (governance) address per chain from the
// datastore — same responsibility as the EVM adapter’s timelock cache.
func (a *StellarTransferOwnershipAdapter) InitializeTimelockAddress(e cldf.Environment, input mcms.Input) error {
	reader, ok := changesets.GetRegistry().GetMCMSReader(chainsel.FamilyStellar)
	if !ok {
		return fmt.Errorf("no MCMS reader registered for %s", chainsel.FamilyStellar)
	}
	if a.governanceAddr == nil {
		a.governanceAddr = make(map[uint64]string)
	}
	for sel := range e.BlockChains.StellarChains() {
		tlRef, err := reader.GetTimelockRef(e, sel, input)
		if err != nil {
			return err
		}
		if tlRef.Address == "" {
			return fmt.Errorf("empty timelock governance address for stellar chain %d", sel)
		}
		a.governanceAddr[sel] = tlRef.Address
	}
	return nil
}

func (a *StellarTransferOwnershipAdapter) SequenceTransferOwnershipViaMCMS() *cldfops.Sequence[deploy.TransferOwnershipPerChainInput, seqcore.OnChainOutput, cldfchain.BlockChains] {
	return a.wrapOwnershipSequence(stellarsequences.StellarTransferOwnershipViaMCMS)
}

func (a *StellarTransferOwnershipAdapter) SequenceAcceptOwnership() *cldfops.Sequence[deploy.TransferOwnershipPerChainInput, seqcore.OnChainOutput, cldfchain.BlockChains] {
	return a.wrapOwnershipSequence(stellarsequences.StellarAcceptOwnership)
}

// ShouldAcceptOwnershipWithTransferOwnership matches EVM: run accept when ProposedOwner is the
// timelock (governance) or the deployer.
func (a *StellarTransferOwnershipAdapter) ShouldAcceptOwnershipWithTransferOwnership(e cldf.Environment, in deploy.TransferOwnershipPerChainInput) (bool, error) {
	gov, ok := a.governanceAddr[in.ChainSelector]
	if !ok {
		return false, fmt.Errorf("governance address not initialized for chain %d", in.ChainSelector)
	}
	ch, ok := e.BlockChains.StellarChains()[in.ChainSelector]
	if !ok {
		return false, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return false, err
	}
	deployerAddr := dep.SignerAddress()
	if in.ProposedOwner != gov && in.ProposedOwner != deployerAddr {
		return false, nil
	}
	return true, nil
}

// wrapOwnershipSequence adapts a StellarTransferOwnershipInput sequence to the
// deploy.TransferOwnershipPerChainInput interface by injecting cached addresses.
func (a *StellarTransferOwnershipAdapter) wrapOwnershipSequence(
	inner *cldfops.Sequence[stellarsequences.StellarTransferOwnershipInput, seqcore.OnChainOutput, cldfchain.BlockChains],
) *cldfops.Sequence[deploy.TransferOwnershipPerChainInput, seqcore.OnChainOutput, cldfchain.BlockChains] {
	return cldfops.NewSequence(
		inner.ID(),
		deploy.MCMSVersion,
		inner.Description(),
		func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.TransferOwnershipPerChainInput) (seqcore.OnChainOutput, error) {
			gov, ok := a.governanceAddr[in.ChainSelector]
			if !ok {
				return seqcore.OnChainOutput{}, fmt.Errorf("governance address not initialized for chain %d", in.ChainSelector)
			}
			report, err := cldfops.ExecuteSequence(b, inner, chains, stellarsequences.StellarTransferOwnershipInput{
				TransferOwnershipPerChainInput: in,
				GovernanceAddr:                 gov,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			return report.Output, nil
		},
	)
}
