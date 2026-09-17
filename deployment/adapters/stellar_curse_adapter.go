package adapters

import (
	"fmt"
	"slices"

	"github.com/Masterminds/semver/v3"

	chainsel "github.com/smartcontractkit/chain-selectors"
	api "github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	datastore_utils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
)

var (
	_ api.CurseAdapter        = (*StellarCurseAdapter)(nil)
	_ api.CurseSubjectAdapter = (*StellarCurseAdapter)(nil)
)

// StellarCurseAdapter implements both CurseAdapter and CurseSubjectAdapter for Stellar.
// Initialize caches the on-chain facts the curse sequences need to route proposals:
// the RMN Remote / Router contract IDs (strkeys), the RMN owner and curse-admin
// list, and the RBACTimelock of every deployed MCMS stack keyed by qualifier.
// The owner read is mandatory and fatal: routing (and the owner-only uncurse arm)
// is fail-closed on it, so an RPC failure surfaces from Initialize instead of
// degrading to an empty value. Direct-only deployments (no MCMS stacks) remain
// legitimate: missing timelocks and an unreadable admin list degrade to empty
// values, never an error.
type StellarCurseAdapter struct {
	rmnContractID    map[uint64]string
	routerContractID map[uint64]string
	owners           map[uint64]string
	curseAdmins      map[uint64][]string
	timelocks        map[uint64]map[string]string
}

func NewStellarCurseAdapter() *StellarCurseAdapter {
	return &StellarCurseAdapter{}
}

func (a *StellarCurseAdapter) Initialize(e cldf.Environment, selector uint64) error {
	if _, ok := e.BlockChains.StellarChains()[selector]; !ok {
		return fmt.Errorf("stellar chain %d not found in environment", selector)
	}

	if a.rmnContractID == nil {
		a.rmnContractID = make(map[uint64]string)
	}
	if a.routerContractID == nil {
		a.routerContractID = make(map[uint64]string)
	}

	if _, exists := a.rmnContractID[selector]; !exists {
		addr, err := stellarContractIDOnChain(e, selector, stellarccip.RMNRemoteDatastoreRef())
		if err != nil {
			return fmt.Errorf("resolve RMN Remote on chain %d: %w", selector, err)
		}
		a.rmnContractID[selector] = addr
	}

	if _, exists := a.routerContractID[selector]; !exists {
		addr, err := stellarContractIDOnChain(e, selector, stellarccip.RouterDatastoreRef())
		if err != nil {
			return fmt.Errorf("resolve Router on chain %d: %w", selector, err)
		}
		a.routerContractID[selector] = addr
	}

	if a.owners == nil {
		a.owners = make(map[uint64]string)
	}
	if a.curseAdmins == nil {
		a.curseAdmins = make(map[uint64][]string)
	}
	if a.timelocks == nil {
		a.timelocks = make(map[uint64]map[string]string)
	}
	if _, exists := a.timelocks[selector]; !exists {
		tls := make(map[string]string)
		// All three governance stacks are resolved: CLLCCIP is cached for
		// diagnostics only — it holds no RMN role, but naming its timelock in
		// build-time errors makes the likeliest operator mistake actionable.
		for _, qual := range []string{cciputils.CLLQualifier, cciputils.RMNTimelockQualifier, cciputils.UltraFastCurseMCMSQualifier} {
			if tl, ok := mcmsutil.FindExistingStellarTimelock(datastoreRefs(e), selector, qual); ok {
				tls[qual] = tl
			} else {
				e.Logger.Debugw("no RBACTimelock deployed for qualifier; it will be unavailable for curse routing",
					"chainSelector", selector, "qualifier", qual)
			}
		}
		a.timelocks[selector] = tls
	}

	_, ownerCached := a.owners[selector]
	_, adminsCached := a.curseAdmins[selector]
	if !ownerCached || !adminsCached {
		rmnID := a.rmnContractID[selector]
		// Canonical datastore type "RMNRemote"; the ownership helpers match the
		// stellar-local "RmnRemote" constant, so remap the in-memory ref type.
		rmnRef := stellarccip.RMNRemoteDatastoreRef().FullAddressRef(selector, rmnID)
		rmnRef.Type = datastore.ContractType(rmnremoteops.ContractType)
		ch, ok := e.BlockChains.StellarChains()[selector]
		if !ok {
			return fmt.Errorf("stellar chain %d not found in environment", selector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return fmt.Errorf("build deployer on chain %d: %w", selector, err)
		}
		deps := stellardeps.FromDeployer(dep)

		if !ownerCached {
			// The owner read is mandatory: fail-closed curse routing (and the
			// owner-only uncurse arm) cannot work without it, and a silent
			// degrade to an empty Owner produces misleading proposal-build
			// errors far from the actual RPC failure. Surface it here.
			owner, err := ownership.ContractOwner(e.GetContext(), deps, rmnRef)
			if err != nil {
				return fmt.Errorf("read RMN Remote owner on chain %d: %w", selector, err)
			}
			a.owners[selector] = owner
		}
		if !adminsCached {
			// The admin list is advisory — owner-based routing still works
			// without it — so a failed read only disables admin routing for
			// this run; the key stays absent so a later Initialize retries it.
			if admins, err := ownership.CurseAdmins(e.GetContext(), deps, rmnRef); err != nil {
				e.Logger.Debugw("RMN Remote curse admins unavailable; only owner-based routing will be available",
					"chainSelector", selector, "error", err.Error())
			} else {
				a.curseAdmins[selector] = admins
			}
		}
	}
	return nil
}

// datastoreRefs extracts the AddressRefs recorded in the environment datastore.
func datastoreRefs(e cldf.Environment) []datastore.AddressRef {
	if e.DataStore == nil {
		return nil
	}
	return e.DataStore.Addresses().Filter()
}

func (a *StellarCurseAdapter) IsSubjectCursedOnChain(e cldf.Environment, selector uint64, subject api.Subject) (bool, error) {
	rmnID, ok := a.rmnContractID[selector]
	if !ok {
		return false, fmt.Errorf("no RMN Remote cached for chain %d", selector)
	}
	ch, ok := e.BlockChains.StellarChains()[selector]
	if !ok {
		return false, fmt.Errorf("stellar chain %d not in environment", selector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return false, err
	}
	client := rmnremotebindings.NewRmnRemoteClient(dep, rmnID)
	return client.IsCursedBySubject(e.GetContext(), subject)
}

func (a *StellarCurseAdapter) IsChainConnectedToTargetChain(e cldf.Environment, selector uint64, targetSel uint64) (bool, error) {
	routerID, ok := a.routerContractID[selector]
	if !ok {
		return false, fmt.Errorf("no Router cached for chain %d", selector)
	}
	ch, ok := e.BlockChains.StellarChains()[selector]
	if !ok {
		return false, fmt.Errorf("stellar chain %d not in environment", selector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return false, err
	}
	client := routerbindings.NewRouterClient(dep, routerID)
	return client.IsChainSupported(e.GetContext(), targetSel)
}

func (a *StellarCurseAdapter) IsCurseEnabledForChain(_ cldf.Environment, selector uint64) (bool, error) {
	_, ok := a.rmnContractID[selector]
	return ok, nil
}

func (a *StellarCurseAdapter) SubjectToSelector(subject api.Subject) (uint64, error) {
	return api.GenericSubjectToSelector(subject)
}

func (a *StellarCurseAdapter) SelectorToSubject(selector uint64) api.Subject {
	return api.GenericSelectorToSubject(selector)
}

func (a *StellarCurseAdapter) DeriveCurseAdapterVersion(_ cldf.Environment, _ uint64) (*semver.Version, error) {
	return stellarops.ContractDeploymentVersion, nil
}

func (a *StellarCurseAdapter) Curse() *cldf_ops.Sequence[api.CurseInput, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return wrapCurseSequence(a, stellarsequences.StellarCurse)
}

func (a *StellarCurseAdapter) Uncurse() *cldf_ops.Sequence[api.CurseInput, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return wrapCurseSequence(a, stellarsequences.StellarUncurse)
}

// wrapCurseSequence adapts a StellarCurseInput sequence to the api.CurseInput interface
// by injecting the cached RMN contract ID.
func wrapCurseSequence(
	a *StellarCurseAdapter,
	inner *cldf_ops.Sequence[stellarsequences.StellarCurseInput, seqcore.OnChainOutput, cldf_chain.BlockChains],
) *cldf_ops.Sequence[api.CurseInput, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		inner.ID(),
		stellarops.ContractDeploymentVersion,
		inner.Description(),
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in api.CurseInput) (seqcore.OnChainOutput, error) {
			rmnID, ok := a.rmnContractID[in.ChainSelector]
			if !ok {
				return seqcore.OnChainOutput{}, fmt.Errorf("no RMN Remote cached for chain %d", in.ChainSelector)
			}
			report, err := cldf_ops.ExecuteSequence(b, inner, chains, stellarsequences.StellarCurseInput{
				CurseInput:    in,
				RMNContractID: rmnID,
				Owner:         a.owners[in.ChainSelector],
				CurseAdmins:   a.curseAdmins[in.ChainSelector],
				Timelocks:     a.timelocks[in.ChainSelector],
			})
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			return report.Output, nil
		},
	)
}

func (a *StellarCurseAdapter) ListConnectedChains(e cldf.Environment, selector uint64) ([]uint64, error) {
	routerID, ok := a.routerContractID[selector]
	if !ok {
		return nil, fmt.Errorf("no Router cached for chain %d", selector)
	}
	ch, ok := e.BlockChains.StellarChains()[selector]
	if !ok {
		return nil, fmt.Errorf("stellar chain %d not in environment", selector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return nil, err
	}
	client := routerbindings.NewRouterClient(dep, routerID)
	offRamps, err := client.GetOfframps(e.GetContext())
	if err != nil {
		return nil, fmt.Errorf("get offramps on chain %d: %w", selector, err)
	}
	var connected []uint64
	for _, entry := range offRamps {
		if entry.Offramp == "" {
			continue
		}
		family, err := chainsel.GetSelectorFamily(entry.SourceChainSelector)
		if err != nil {
			continue
		}
		if !api.GetCurseRegistry().IsFamilyRegistered(family) {
			continue
		}
		if !slices.Contains(connected, entry.SourceChainSelector) {
			connected = append(connected, entry.SourceChainSelector)
		}
	}
	return connected, nil
}

func stellarContractIDOnChain(e cldf.Environment, selector uint64, ref stellarccip.DatastoreSorobanContractRef) (string, error) {
	hexAddr, err := datastore_utils.FindAndFormatRef(e.DataStore, ref.PartialAddressRef(), selector, func(r datastore.AddressRef) (string, error) {
		return r.Address, nil
	})
	if err != nil {
		return "", err
	}
	return scval.HexToContractStrkey(hexAddr)
}
