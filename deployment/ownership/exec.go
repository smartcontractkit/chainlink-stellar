package ownership

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	burnmint "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	cciprecv "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
	cv "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	creforwarder "github.com/smartcontractkit/chainlink-stellar/deployment/operations/cre_forwarder"
	fq "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	lrp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	offramp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	onramp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	rr "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
	rmnproxy "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
	rmnremote "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	router "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	slrp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tar "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	tlb "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
	vvr "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

// ExecuteTransferOwnership runs the Soroban transfer_ownership op for the contract type in ref.
func ExecuteTransferOwnership(
	b cldfops.Bundle,
	deps stellardeps.StellarDeps,
	ref datastore.AddressRef,
	newOwner string,
) error {
	cid := ref.Address
	ct := string(ref.Type)
	switch ct {
	case mcmsops.ContractType:
		_, err := cldfops.ExecuteOperation(b, mcmsops.TransferOwnership, deps, mcmsops.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case offramp.ContractType:
		_, err := cldfops.ExecuteOperation(b, offramp.TransferOwnership, deps, offramp.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case onramp.ContractType:
		_, err := cldfops.ExecuteOperation(b, onramp.TransferOwnership, deps, onramp.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case router.ContractType:
		_, err := cldfops.ExecuteOperation(b, router.TransferOwnership, deps, router.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case fq.ContractType:
		_, err := cldfops.ExecuteOperation(b, fq.TransferOwnership, deps, fq.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case rmnremote.ContractType:
		_, err := cldfops.ExecuteOperation(b, rmnremote.TransferOwnership, deps, rmnremote.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case rmnproxy.ContractType:
		_, err := cldfops.ExecuteOperation(b, rmnproxy.TransferOwnership, deps, rmnproxy.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case rr.ContractType:
		_, err := cldfops.ExecuteOperation(b, rr.TransferOwnership, deps, rr.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case tar.ContractType:
		_, err := cldfops.ExecuteOperation(b, tar.TransferOwnership, deps, tar.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case cv.ContractType:
		_, err := cldfops.ExecuteOperation(b, cv.TransferOwnership, deps, cv.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case vvr.ContractType:
		_, err := cldfops.ExecuteOperation(b, vvr.TransferOwnership, deps, vvr.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case lrp.ContractType:
		_, err := cldfops.ExecuteOperation(b, lrp.TransferOwnership, deps, lrp.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case slrp.ContractType:
		_, err := cldfops.ExecuteOperation(b, slrp.TransferOwnership, deps, slrp.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case burnmint.ContractType:
		_, err := cldfops.ExecuteOperation(b, burnmint.TransferOwnership, deps, burnmint.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case tlb.ContractType:
		_, err := cldfops.ExecuteOperation(b, tlb.TransferOwnership, deps, tlb.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case cciprecv.ContractType:
		_, err := cldfops.ExecuteOperation(b, cciprecv.TransferOwnership, deps, cciprecv.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	case creforwarder.ContractType:
		_, err := cldfops.ExecuteOperation(b, creforwarder.TransferOwnership, deps, creforwarder.TransferOwnershipInput{ContractID: cid, NewOwner: newOwner})
		return err
	default:
		return fmt.Errorf("stellar transfer ownership: unsupported contract type %q for %s", ct, cid)
	}
}

// ExecuteAcceptOwnership runs the Soroban accept_ownership op for the contract type in ref.
func ExecuteAcceptOwnership(b cldfops.Bundle, deps stellardeps.StellarDeps, ref datastore.AddressRef) error {
	cid := ref.Address
	ct := string(ref.Type)
	switch ct {
	case mcmsops.ContractType:
		_, err := cldfops.ExecuteOperation(b, mcmsops.AcceptOwnership, deps, mcmsops.AcceptOwnershipInput{ContractID: cid})
		return err
	case offramp.ContractType:
		_, err := cldfops.ExecuteOperation(b, offramp.AcceptOwnership, deps, offramp.AcceptOwnershipInput{ContractID: cid})
		return err
	case onramp.ContractType:
		_, err := cldfops.ExecuteOperation(b, onramp.AcceptOwnership, deps, onramp.AcceptOwnershipInput{ContractID: cid})
		return err
	case router.ContractType:
		_, err := cldfops.ExecuteOperation(b, router.AcceptOwnership, deps, router.AcceptOwnershipInput{ContractID: cid})
		return err
	case fq.ContractType:
		_, err := cldfops.ExecuteOperation(b, fq.AcceptOwnership, deps, fq.AcceptOwnershipInput{ContractID: cid})
		return err
	case rmnremote.ContractType:
		_, err := cldfops.ExecuteOperation(b, rmnremote.AcceptOwnership, deps, rmnremote.AcceptOwnershipInput{ContractID: cid})
		return err
	case rmnproxy.ContractType:
		_, err := cldfops.ExecuteOperation(b, rmnproxy.AcceptOwnership, deps, rmnproxy.AcceptOwnershipInput{ContractID: cid})
		return err
	case rr.ContractType:
		_, err := cldfops.ExecuteOperation(b, rr.AcceptOwnership, deps, rr.AcceptOwnershipInput{ContractID: cid})
		return err
	case tar.ContractType:
		_, err := cldfops.ExecuteOperation(b, tar.AcceptOwnership, deps, tar.AcceptOwnershipInput{ContractID: cid})
		return err
	case cv.ContractType:
		_, err := cldfops.ExecuteOperation(b, cv.AcceptOwnership, deps, cv.AcceptOwnershipInput{ContractID: cid})
		return err
	case vvr.ContractType:
		_, err := cldfops.ExecuteOperation(b, vvr.AcceptOwnership, deps, vvr.AcceptOwnershipInput{ContractID: cid})
		return err
	case lrp.ContractType:
		_, err := cldfops.ExecuteOperation(b, lrp.AcceptOwnership, deps, lrp.AcceptOwnershipInput{ContractID: cid})
		return err
	case slrp.ContractType:
		_, err := cldfops.ExecuteOperation(b, slrp.AcceptOwnership, deps, slrp.AcceptOwnershipInput{ContractID: cid})
		return err
	case burnmint.ContractType:
		_, err := cldfops.ExecuteOperation(b, burnmint.AcceptOwnership, deps, burnmint.AcceptOwnershipInput{ContractID: cid})
		return err
	case tlb.ContractType:
		_, err := cldfops.ExecuteOperation(b, tlb.AcceptOwnership, deps, tlb.AcceptOwnershipInput{ContractID: cid})
		return err
	case cciprecv.ContractType:
		_, err := cldfops.ExecuteOperation(b, cciprecv.AcceptOwnership, deps, cciprecv.AcceptOwnershipInput{ContractID: cid})
		return err
	case creforwarder.ContractType:
		_, err := cldfops.ExecuteOperation(b, creforwarder.AcceptOwnership, deps, creforwarder.AcceptOwnershipInput{ContractID: cid})
		return err
	default:
		return fmt.Errorf("stellar accept ownership: unsupported contract type %q for %s", ct, cid)
	}
}

// ExecuteApplyCurseAdminUpdates runs the Soroban apply_curse_admin_updates op
// for the contract type in ref (owner-only on chain). Only RMN Remote supports
// curse admins; other contract types error.
func ExecuteApplyCurseAdminUpdates(
	b cldfops.Bundle,
	deps stellardeps.StellarDeps,
	ref datastore.AddressRef,
	added, removed []string,
) error {
	cid := ref.Address
	ct := string(ref.Type)
	switch ct {
	case rmnremote.ContractType:
		_, err := cldfops.ExecuteOperation(b, rmnremote.ApplyCurseAdminUpdates, deps, rmnremote.ApplyCurseAdminUpdatesInput{
			ContractID:    cid,
			AddedAdmins:   added,
			RemovedAdmins: removed,
		})
		return err
	default:
		return fmt.Errorf("stellar apply curse admin updates: unsupported contract type %q for %s", ct, cid)
	}
}
