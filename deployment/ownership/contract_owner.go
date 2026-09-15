package ownership

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	burnmintbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/burn_mint_pool"
	cciprecvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	crebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/cre"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	lrpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/lock_release_pool"
	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	slrpbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tlbbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_lock_box"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	burnmintops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	cciprecvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	creforwarderops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/cre_forwarder"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	lrpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	offrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	rrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
	rmnproxyops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	slrpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

func optionalStellarOwner(o *string) (string, error) {
	if o == nil {
		return "", fmt.Errorf("contract returned no owner")
	}
	return *o, nil
}

// ContractOwner returns the current owner address string for a Soroban CCIP contract ref (simulation read).
func ContractOwner(ctx context.Context, deps stellardeps.StellarDeps, ref datastore.AddressRef) (string, error) {
	cid := ref.Address
	switch string(ref.Type) {
	case mcmsops.ContractType:
		o, err := mcmsbindings.NewMcmsClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case offrampops.ContractType:
		o, err := offrampbindings.NewOffRampClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case onrampops.ContractType:
		o, err := onrampbindings.NewOnRampClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case routerops.ContractType:
		o, err := routerbindings.NewRouterClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case fqops.ContractType:
		o, err := fqbindings.NewFeeQuoterClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case rmnremoteops.ContractType:
		o, err := rmnremotebindings.NewRmnRemoteClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case rmnproxyops.ContractType:
		o, err := rmnproxybindings.NewRmnProxyClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case rrops.ContractType:
		o, err := rrbindings.NewRampRegistryClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case tarops.ContractType:
		o, err := tarbindings.NewTokenAdminRegistryClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case cvops.ContractType:
		o, err := cvbindings.NewCommitteeVerifierClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case vvrops.ContractType:
		o, err := vvrbindings.NewVersionedVerifierResolverClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case lrpops.ContractType:
		o, err := lrpbindings.NewLockReleasePoolClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case slrpops.ContractType:
		o, err := slrpbindings.NewSiloedLockReleasePoolClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case burnmintops.ContractType:
		o, err := burnmintbindings.NewBurnMintPoolClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case tlbops.ContractType:
		o, err := tlbbindings.NewTokenLockBoxClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case cciprecvops.ContractType:
		o, err := cciprecvbindings.NewExampleCcipReceiverClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	case creforwarderops.ContractType:
		o, err := crebindings.NewForwarderClient(deps.Invoker, cid).Owner(ctx)
		if err != nil {
			return "", err
		}
		return optionalStellarOwner(o)
	default:
		return "", fmt.Errorf("stellar ownership: unsupported contract type %q for %s", ref.Type, cid)
	}
}
