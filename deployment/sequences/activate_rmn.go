package sequences

import (
	"fmt"
	"slices"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// StellarActivateRMNInput configures activation of an existing RMN Remote:
// grant curse-admin to the fast-curse timelock (plus extras), migrate curse
// subjects while the deployer still owns, and transfer ownership to the
// governance (RMNMCMS) timelock. Mirrors EVM DeployAndActivateRMN: the UltraFastCurse
// timelock is granted curse-admin; the RMNMCMS timelock's curse authority comes from
// ownership and it is never listed in get_curse_admins.
type StellarActivateRMNInput struct {
	ChainSelector uint64
	// ExistingAddresses carries the chain's recorded refs. The RMN Remote ref is
	// resolved from it (stored hex, converted to the strkey form the ownership
	// helpers require); the governance and fast-curse RBACTimelocks are resolved
	// by qualifier.
	ExistingAddresses []datastore.AddressRef
	// RMNRemoteRef optionally overrides the RMN Remote ref resolved from
	// ExistingAddresses. Address must be the contract strkey.
	RMNRemoteRef *datastore.AddressRef
	// GovernanceQualifier names the MCMS stack that will own the RMN after
	// activation. Defaults to the RMNMCMS qualifier.
	GovernanceQualifier string
	// FastCurseQualifier names the MCMS stack granted curse-admin. Defaults to
	// the UltraFastCurse qualifier.
	FastCurseQualifier string
	// ExtraCurseAdmins are granted curse-admin alongside the fast-curse timelock.
	// Addresses must be strkeys.
	ExtraCurseAdmins []string
	// SubjectsToMigrate are cursed directly while the deployer still owns the RMN
	// (idempotent on chain). Only valid pre-transfer: a non-empty list errors once
	// ownership has moved off the deployer.
	SubjectsToMigrate [][16]byte
	// Description labels emitted MCMS proposals.
	Description string
}

// StellarActivateRMN activates an existing RMN Remote for MCMS governance.
var StellarActivateRMN = cldfops.NewSequence(
	"stellar-seq-activate-rmn",
	deploy.MCMSVersion,
	"Grants curse admins and transfers Stellar RMN Remote ownership to the governance timelock",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarActivateRMNInput) (output seqcore.OnChainOutput, err error) {
		govQual := in.GovernanceQualifier
		if govQual == "" {
			govQual = cciputils.RMNTimelockQualifier
		}
		fastQual := in.FastCurseQualifier
		if fastQual == "" {
			fastQual = cciputils.UltraFastCurseMCMSQualifier
		}

		govTL, ok := mcmsutil.FindExistingStellarTimelock(in.ExistingAddresses, in.ChainSelector, govQual)
		if !ok {
			return output, fmt.Errorf("no RBACTimelock deployed for qualifier %q on chain %d; deploy the %s MCMS stack first", govQual, in.ChainSelector, govQual)
		}
		fastTL, ok := mcmsutil.FindExistingStellarTimelock(in.ExistingAddresses, in.ChainSelector, fastQual)
		if !ok {
			return output, fmt.Errorf("no RBACTimelock deployed for qualifier %q on chain %d; deploy the fast-curse MCMS stack first", fastQual, in.ChainSelector)
		}

		rmnRecordedRef, opsRef, err := resolveRMNRemoteRefs(in)
		if err != nil {
			return output, err
		}

		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return output, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return output, err
		}
		deps := stellardeps.FromDeployer(dep)
		deployerAddr := dep.SignerAddress()
		ctx := b.GetContext()

		// The canonical RMN Remote ref type is the upstream "RMNRemote" (kept for
		// datastore compatibility); the ownership helpers and ops match the
		// stellar-local "RmnRemote" constant. resolveRMNRemoteRefs already remapped
		// only the in-memory ops ref — the recorded ref is untouched — so ownership
		// lookups resolve.
		owner, err := ownership.ContractOwner(ctx, deps, opsRef)
		if err != nil {
			return output, fmt.Errorf("read RMN Remote owner: %w", err)
		}
		currentAdmins, err := ownership.CurseAdmins(ctx, deps, opsRef)
		if err != nil {
			return output, fmt.Errorf("read RMN Remote curse admins: %w", err)
		}

		// Initial grant: fast-curse timelock + extras only (owner-implicit).
		added := dedupeStrkeys(append([]string{fastTL}, in.ExtraCurseAdmins...))
		if slices.Contains(added, owner) {
			b.Logger.Warnw("RMN Remote owner is being added to the curse-admin list; on chain nothing filters the owner out of the stored list", "owner", owner)
		}
		var toAdd []string
		for _, addr := range added {
			if !slices.Contains(currentAdmins, addr) {
				toAdd = append(toAdd, addr)
			}
		}

		var batchOps []mcmstypes.BatchOperation

		if len(toAdd) > 0 {
			route, err := routeCurseAdminGrant(owner, deployerAddr, govTL)
			if err != nil {
				return output, err
			}
			switch route {
			case curseAdminGrantDirect:
				if err := ownership.ExecuteApplyCurseAdminUpdates(b, deps, opsRef, toAdd, nil); err != nil {
					return output, fmt.Errorf("apply curse admin updates: %w", err)
				}
			case curseAdminGrantPropose:
				data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("apply_curse_admin_updates", []xdr.ScVal{
					scval.AddressSliceToScVal(toAdd),
					scval.AddressSliceToScVal(nil),
				})
				if err != nil {
					return output, err
				}
				batchOps = append(batchOps, mcmsTxForRef(opsRef, data))
			}
		}

		// Subject migration is only valid while the deployer still owns the RMN.
		if len(in.SubjectsToMigrate) > 0 {
			if owner != deployerAddr {
				return output, fmt.Errorf(
					"cannot migrate curse subjects: RMN Remote owner %q is not the deployer %q; migration must happen before ownership transfer",
					owner, deployerAddr,
				)
			}
			for _, subject := range in.SubjectsToMigrate {
				if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Curse, deps, rmnremoteops.CurseInput{
					ContractID: opsRef.Address,
					Caller:     deployerAddr,
					Subjects:   [][16]byte{subject},
				}); err != nil {
					return output, fmt.Errorf("migrate curse subject %x: %w", subject, err)
				}
			}
		}

		// Ownership transfer to the governance timelock. The governed
		// accept_ownership completes it (the deployer-owner branch of the transfer
		// sequence records the pending owner directly).
		transferReport, err := cldfops.ExecuteSequence(b, StellarTransferOwnershipViaMCMS, chains, StellarTransferOwnershipInput{
			TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
				ChainSelector: in.ChainSelector,
				ContractRef:   []datastore.AddressRef{opsRef},
				ProposedOwner: govTL,
			},
			GovernanceAddr: govTL,
		})
		if err != nil {
			return output, fmt.Errorf("transfer RMN Remote ownership: %w", err)
		}
		batchOps = append(batchOps, transferReport.Output.BatchOps...)

		output.BatchOps = batchOps
		// Emit the recorded (hex-addressed, upstream-typed) ref — the datastore
		// convention every RMN lookup relies on. The strkey opsRef never leaves
		// this sequence.
		output.Addresses = append(output.Addresses, rmnRecordedRef)
		return output, nil
	},
)

// resolveRMNRemoteRefs resolves the RMN Remote ref in two forms:
//   - recorded: exactly as it must be persisted in the datastore — upstream
//     "RMNRemote" type, hex address (the convention every RMN lookup relies on;
//     a strkey stored there fails hex decoding);
//   - ops: the strkey-addressed, stellar-local-typed form the ownership helpers
//     and ops require.
//
// The explicit RMNRemoteRef (strkey-addressed) wins when set; its address is
// converted back to hex for the recorded form. Remap only the in-memory ops
// ref — never the recorded ref.
func resolveRMNRemoteRefs(in StellarActivateRMNInput) (recorded datastore.AddressRef, ops datastore.AddressRef, err error) {
	if in.RMNRemoteRef != nil {
		ref := *in.RMNRemoteRef
		if ref.Address == "" {
			return datastore.AddressRef{}, datastore.AddressRef{}, fmt.Errorf("RMNRemoteRef set but Address is empty")
		}
		hexAddr, err := stellarutil.StrkeyToHex(ref.Address)
		if err != nil {
			return datastore.AddressRef{}, datastore.AddressRef{}, fmt.Errorf("convert RMNRemoteRef address %s to hex: %w", ref.Address, err)
		}
		recorded = ref
		recorded.Address = hexAddr
		ops = ref
		ops.Type = datastore.ContractType(rmnremoteops.ContractType)
		return recorded, ops, nil
	}
	want := stellarccip.RMNRemoteDatastoreRef().PartialAddressRef()
	for _, r := range in.ExistingAddresses {
		if r.ChainSelector != in.ChainSelector || r.Address == "" {
			continue
		}
		if r.Type != want.Type || r.Qualifier != want.Qualifier || !r.Version.Equal(want.Version) {
			continue
		}
		strkeyAddr, err := scval.HexToContractStrkey(r.Address)
		if err != nil {
			return datastore.AddressRef{}, datastore.AddressRef{}, fmt.Errorf("convert RMN Remote ref %s to strkey: %w", r.Address, err)
		}
		ops = r
		ops.Address = strkeyAddr
		ops.Type = datastore.ContractType(rmnremoteops.ContractType)
		return r, ops, nil
	}
	return datastore.AddressRef{}, datastore.AddressRef{}, fmt.Errorf("no RMN Remote ref found for chain %d in ExistingAddresses", in.ChainSelector)
}

func dedupeStrkeys(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

type curseAdminGrantRoute int

const (
	// curseAdminGrantDirect executes apply_curse_admin_updates as the deployer.
	curseAdminGrantDirect curseAdminGrantRoute = iota
	// curseAdminGrantPropose emits an MCMS proposal executed by the governance timelock.
	curseAdminGrantPropose
)

// routeCurseAdminGrant picks how the initial curse-admin grant reaches the RMN
// Remote: directly when the deployer still owns it, or via an MCMS proposal when
// the governance timelock already owns it. Any other owner is an error.
func routeCurseAdminGrant(owner, deployerAddr, govTL string) (curseAdminGrantRoute, error) {
	switch owner {
	case deployerAddr:
		return curseAdminGrantDirect, nil
	case govTL:
		return curseAdminGrantPropose, nil
	default:
		return 0, fmt.Errorf(
			"cannot grant curse admins: RMN Remote owner %q is neither deployer %q nor governance timelock %q",
			owner, deployerAddr, govTL,
		)
	}
}

func mcmsTxForRef(ref datastore.AddressRef, data []byte) mcmstypes.BatchOperation {
	return mcmstypes.BatchOperation{
		ChainSelector: mcmstypes.ChainSelector(ref.ChainSelector),
		Transactions: []mcmstypes.Transaction{{
			OperationMetadata: mcmstypes.OperationMetadata{
				ContractType: string(ref.Type),
			},
			To:               ref.Address,
			Data:             data,
			AdditionalFields: stellarMCMSTxAdditionalFields(),
		}},
	}
}
