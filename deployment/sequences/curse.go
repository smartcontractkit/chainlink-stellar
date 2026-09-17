package sequences

import (
	"fmt"
	"slices"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	api "github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// StellarCurseInput extends the shared CurseInput with the resolved RMN Remote
// contract ID and the on-chain facts needed to route the curse to the timelock
// that will execute it. All addresses are strkeys.
type StellarCurseInput struct {
	api.CurseInput
	// RMNContractID is the RMN Remote contract the curse targets (strkey).
	RMNContractID string
	// Owner is the RMN Remote's current owner (the owner is implicitly
	// curse-authorized even though it is never listed in get_curse_admins).
	Owner string
	// CurseAdmins is the raw stored curse-admin list.
	CurseAdmins []string
	// Timelocks maps MCMS qualifier → RBACTimelock contract ID deployed for that
	// qualifier on the chain. An absent qualifier means that stack is not deployed.
	Timelocks map[string]string
}

// authorizeCurseCaller picks the caller that may execute the curse on chain and
// returns it already verified. The caller argument of rmn_remote.curse must equal
// the invoking address, so on MCMS runs it is the executing timelock's contract ID.
//
// Order:
//  1. the deployer is the owner or a curse admin → direct execution, no proposal;
//  2. MCMSQualifier set → that qualifier's timelock, verified to be owner-or-admin;
//     a missing stack or an unauthorized timelock is a build-time error;
//  3. MCMSQualifier empty (direct/no-MCMS or pre-qualifier callers) → documented
//     fallback: the RMNMCMS timelock when authorized, else the UltraFastCurse
//     timelock, with a warning naming the assumption;
//  4. nothing authorized → fail closed with an actionable error.
func authorizeCurseCaller(in StellarCurseInput, deployerAddr string) (string, error) {
	if in.Owner == deployerAddr || slices.Contains(in.CurseAdmins, deployerAddr) {
		return deployerAddr, nil
	}

	if in.MCMSQualifier != "" {
		tl, ok := in.Timelocks[in.MCMSQualifier]
		if !ok {
			return "", fmt.Errorf(
				"no RBACTimelock deployed for qualifier %q on chain %d; deploy the stack first",
				in.MCMSQualifier, in.ChainSelector,
			)
		}
		if tl != in.Owner && !slices.Contains(in.CurseAdmins, tl) {
			return "", fmt.Errorf(
				"curse via qualifier %q is not authorized: its timelock %s is neither the owner %s of RMN Remote %s nor in its curse admins %v; grant it with apply_curse_admin_updates",
				in.MCMSQualifier, tl, in.Owner, in.RMNContractID, in.CurseAdmins,
			)
		}
		return tl, nil
	}

	// Fallback order: RMNMCMS (owner) first, then UltraFastCurse (curse-admin).
	if tl, ok := in.Timelocks[cciputils.RMNTimelockQualifier]; ok && tl == in.Owner {
		return tl, nil
	}
	if tl, ok := in.Timelocks[cciputils.UltraFastCurseMCMSQualifier]; ok && slices.Contains(in.CurseAdmins, tl) {
		return tl, nil
	}

	return "", fmt.Errorf(
		"no authorized curse caller on chain %d: deployer %s is neither the owner %s of RMN Remote %s nor a curse admin (%v), and no authorized timelock was found in %v; grant curse-admin access with apply_curse_admin_updates",
		in.ChainSelector, deployerAddr, in.Owner, in.RMNContractID, in.CurseAdmins, in.Timelocks,
	)
}

// StellarCurse curses subjects on a Stellar RMN Remote contract: directly when
// the deployer is authorized, otherwise as an MCMS proposal whose caller is the
// timelock of the qualifier chosen for this run.
var StellarCurse = cldfops.NewSequence(
	"stellar-curse-rmn-remote",
	stellarops.ContractDeploymentVersion,
	"Curse subjects on Stellar RMN Remote",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarCurseInput) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)
		deployerAddr := dep.SignerAddress()

		caller, err := authorizeCurseCaller(in, deployerAddr)
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("curse on chain %d: %w", in.ChainSelector, err)
		}
		if in.MCMSQualifier == "" && caller != deployerAddr {
			b.Logger.Warnw("MCMS qualifier not supplied; assuming the curse executes via an auto-selected timelock",
				"assumedTimelock", caller, "chainSelector", in.ChainSelector)
		}

		if caller == deployerAddr {
			if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Curse, deps, rmnremoteops.CurseInput{
				ContractID: in.RMNContractID,
				Caller:     caller,
				Subjects:   in.Subjects,
			}); err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("curse on chain %d: %w", in.ChainSelector, err)
			}
			return directExecOutput(b, in.ChainSelector, in.RMNContractID, "stellar-direct-curse")
		}

		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("curse", []xdr.ScVal{
			scval.AddressToScVal(caller),
			scval.Bytes16SliceToScVal(in.Subjects),
		})
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		b.Logger.Infow("proposing stellar curse via MCMS",
			"qualifier", in.MCMSQualifier, "timelock", caller, "chainSelector", in.ChainSelector)
		return seqcore.OnChainOutput{
			BatchOps: []mcmstypes.BatchOperation{{
				ChainSelector: mcmstypes.ChainSelector(in.ChainSelector),
				Transactions: []mcmstypes.Transaction{{
					OperationMetadata: mcmstypes.OperationMetadata{ContractType: rmnremoteops.ContractType},
					To:                in.RMNContractID,
					Data:              data,
					AdditionalFields:  stellarMCMSTxAdditionalFields(),
				}},
			}},
		}, nil
	},
)

// StellarUncurse uncurses subjects on a Stellar RMN Remote contract. Uncurse is
// owner-only on chain and takes no caller argument, so it always routes through
// the RMNMCMS stack; a non-owner qualifier (including UltraFastCurse) is a
// build-time error: fast-uncurse is not supported.
var StellarUncurse = cldfops.NewSequence(
	"stellar-uncurse-rmn-remote",
	stellarops.ContractDeploymentVersion,
	"Uncurses subjects on Stellar RMN Remote",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarCurseInput) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)
		deployerAddr := dep.SignerAddress()

		if in.Owner == "" {
			return seqcore.OnChainOutput{}, fmt.Errorf(
				"uncurse on chain %d: RMN Remote owner is unknown; populate Owner in the input", in.ChainSelector)
		}

		if deployerAddr == in.Owner {
			if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Uncurse, deps, rmnremoteops.UncurseInput{
				ContractID: in.RMNContractID,
				Subjects:   in.Subjects,
			}); err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("uncurse on chain %d: %w", in.ChainSelector, err)
			}
			return directExecOutput(b, in.ChainSelector, in.RMNContractID, "stellar-direct-uncurse")
		}

		// Resolve the executing timelock and require it to be the RMN owner.
		caller, err := authorizeCurseCaller(in, deployerAddr)
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("uncurse on chain %d: %w", in.ChainSelector, err)
		}
		if caller != in.Owner {
			return seqcore.OnChainOutput{}, fmt.Errorf(
				"uncurse on chain %d via qualifier %q is not possible: uncurse is owner-only and fast-uncurse is not supported; route it through the owner timelock %s",
				in.ChainSelector, in.MCMSQualifier, in.Owner)
		}

		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("uncurse", []xdr.ScVal{
			scval.Bytes16SliceToScVal(in.Subjects),
		})
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		b.Logger.Infow("proposing stellar uncurse via MCMS",
			"qualifier", in.MCMSQualifier, "timelock", caller, "chainSelector", in.ChainSelector)
		return seqcore.OnChainOutput{
			BatchOps: []mcmstypes.BatchOperation{{
				ChainSelector: mcmstypes.ChainSelector(in.ChainSelector),
				Transactions: []mcmstypes.Transaction{{
					OperationMetadata: mcmstypes.OperationMetadata{ContractType: rmnremoteops.ContractType},
					To:                in.RMNContractID,
					Data:              data,
					AdditionalFields:  stellarMCMSTxAdditionalFields(),
				}},
			}},
		}, nil
	},
)

// directExecOutput mirrors the direct-execution arm of the ownership sequences:
// the ExecInfo marker makes NewBatchOperationFromWrites skip the write, so the
// returned batch op carries no transactions and the shared OutputBuilder drops
// it — direct runs produce no MCMS proposal.
func directExecOutput(b cldfops.Bundle, chainSelector uint64, contractID, marker string) (seqcore.OnChainOutput, error) {
	op, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
		ChainSelector: chainSelector,
		Tx: mcmstypes.Transaction{
			OperationMetadata: mcmstypes.OperationMetadata{ContractType: rmnremoteops.ContractType},
			To:                contractID,
		},
		ExecInfo: &evmcontract.ExecInfo{Hash: marker},
	}})
	if err != nil {
		return seqcore.OnChainOutput{}, err
	}
	return seqcore.OnChainOutput{BatchOps: []mcmstypes.BatchOperation{op}}, nil
}
