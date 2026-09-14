package mcmsutil

import (
	"fmt"

	frameworkdatastore "github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccipdatastore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
)

// MCMSRefTypeForAction returns the single datastore contract type for a timelock action.
// v2 deploys three independent role instances, so resolution is exact and fail-closed:
// there is no cross-role or base-MCMS fallback.
func MCMSRefTypeForAction(action mcmstypes.TimelockAction) (cldf.ContractType, error) {
	switch action {
	case mcmstypes.TimelockActionSchedule:
		return cldf.ContractType(utils.ProposerManyChainMultisig), nil
	case mcmstypes.TimelockActionBypass:
		return cldf.ContractType(utils.BypasserManyChainMultisig), nil
	case mcmstypes.TimelockActionCancel:
		return cldf.ContractType(utils.CancellerManyChainMultisig), nil
	default:
		return "", fmt.Errorf("unsupported timelock action: %s", action)
	}
}

// FindStellarMCMSAddressRef resolves the role-specific MCMS contract from the datastore.
// It fails closed: the exact role ref must exist, with no fallback to another role.
func FindStellarMCMSAddressRef(e cldf.Environment, chainSelector uint64, input mcmsutils.Input) (frameworkdatastore.AddressRef, error) {
	addrType, err := MCMSRefTypeForAction(input.TimelockAction)
	if err != nil {
		return frameworkdatastore.AddressRef{}, err
	}
	refs := e.DataStore.Addresses().Filter()
	ref := ccipdatastore.GetAddressRef(refs, chainSelector, addrType, deploy.MCMSVersion, input.Qualifier)
	if ref.Address == "" {
		return frameworkdatastore.AddressRef{}, fmt.Errorf(
			"no Stellar MCMS address found for chain %d qualifier %q role %s (action %s); role refs are not interchangeable",
			chainSelector, input.Qualifier, addrType, input.TimelockAction)
	}
	return ref, nil
}

// FindStellarTimelockAddressRef resolves RBACTimelock from the datastore. It fails closed:
// there is no fallback to an MCMS contract (v2 always deploys a distinct timelock).
func FindStellarTimelockAddressRef(e cldf.Environment, chainSelector uint64, input mcmsutils.Input) (frameworkdatastore.AddressRef, error) {
	refs := e.DataStore.Addresses().Filter()
	ref := ccipdatastore.GetAddressRef(refs, chainSelector, utils.RBACTimelock, deploy.MCMSVersion, input.Qualifier)
	if ref.Address == "" {
		return frameworkdatastore.AddressRef{}, fmt.Errorf(
			"no Stellar RBACTimelock address found for chain %d qualifier %q",
			chainSelector, input.Qualifier)
	}
	return ref, nil
}
