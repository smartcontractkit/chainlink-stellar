package sequences

import (
	"encoding/json"
	"fmt"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// StellarTransferOwnershipInput extends the shared input with the RBAC timelock
// (governance) address from the datastore — same role as EVM OpTransferOwnershipInput.TimelockAddress.
type StellarTransferOwnershipInput struct {
	deploy.TransferOwnershipPerChainInput
	GovernanceAddr string
}

func stellarMCMSTxAdditionalFields() json.RawMessage {
	return json.RawMessage(`{"version":1,"family":"stellar"}`)
}

// StellarTransferOwnershipViaMCMS mirrors EVM chains/evm/.../operations/mcms OpTransferOwnership:
// deployer sends the tx directly; when the current owner is the timelock (governance), emit an
// MCMS batch op (MCMS is the transport, not compared to timelock).
var StellarTransferOwnershipViaMCMS = cldfops.NewSequence(
	"stellar-seq-transfer-ownership-via-mcms",
	deploy.MCMSVersion,
	"Transfers Soroban contract ownership via MCMS or deployer",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarTransferOwnershipInput) (output seqcore.OnChainOutput, err error) {
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

		for _, ref := range in.ContractRef {
			// Recorded refs may carry the hex form; the ops and the proposal To
			// below need the strkey, so normalize once up front.
			cid, err := ownership.NormalizeContractAddress(ref.Address)
			if err != nil {
				return output, fmt.Errorf("transfer ownership on chain %d: %w", in.ChainSelector, err)
			}
			ref.Address = cid
			owner, err := ownership.ContractOwner(ctx, deps, ref)
			if err != nil {
				return output, fmt.Errorf("read owner %s: %w", ref.Address, err)
			}
			if owner == in.ProposedOwner {
				continue
			}
			var wo evmcontract.WriteOutput
			switch {
			case owner == deployerAddr:
				if err := ownership.ExecuteTransferOwnership(b, deps, ref, in.ProposedOwner); err != nil {
					return output, fmt.Errorf("transfer ownership %s: %w", ref.Address, err)
				}
				wo = evmcontract.WriteOutput{
					ChainSelector: in.ChainSelector,
					ExecInfo:      &evmcontract.ExecInfo{Hash: "stellar-direct-transfer"},
				}
			case owner == in.GovernanceAddr:
				data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("transfer_ownership", []xdr.ScVal{scval.AddressToScVal(in.ProposedOwner)})
				if err != nil {
					return output, err
				}
				wo = evmcontract.WriteOutput{
					ChainSelector: in.ChainSelector,
					Tx: mcmstypes.Transaction{
						OperationMetadata: mcmstypes.OperationMetadata{
							ContractType: string(ref.Type),
						},
						To:               ref.Address,
						Data:             data,
						AdditionalFields: stellarMCMSTxAdditionalFields(),
					},
				}
			default:
				return output, fmt.Errorf(
					"contract %s owner %q is neither deployer %q nor governance %q",
					ref.Address, owner, deployerAddr, in.GovernanceAddr,
				)
			}
			batchOp, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{wo})
			if err != nil {
				return output, err
			}
			output.BatchOps = append(output.BatchOps, batchOp)
		}
		return output, nil
	},
)

// StellarAcceptOwnership mirrors EVM OpAcceptOwnership: deployer accepts directly; when the
// pending owner is the timelock (governance), emit an MCMS batch op — no timelock==MCMS check.
var StellarAcceptOwnership = cldfops.NewSequence(
	"stellar-seq-accept-ownership",
	deploy.MCMSVersion,
	"Accepts Soroban contract ownership via MCMS or deployer",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarTransferOwnershipInput) (output seqcore.OnChainOutput, err error) {
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

		for _, ref := range in.ContractRef {
			// Recorded refs may carry the hex form; the ops and the proposal To
			// below need the strkey, so normalize once up front.
			cid, err := ownership.NormalizeContractAddress(ref.Address)
			if err != nil {
				return output, fmt.Errorf("accept ownership on chain %d: %w", in.ChainSelector, err)
			}
			ref.Address = cid
			owner, err := ownership.ContractOwner(ctx, deps, ref)
			if err != nil {
				return output, fmt.Errorf("read owner %s: %w", ref.Address, err)
			}
			if owner == in.ProposedOwner {
				continue
			}
			var wo evmcontract.WriteOutput
			switch {
			case in.ProposedOwner == deployerAddr:
				if err := ownership.ExecuteAcceptOwnership(b, deps, ref); err != nil {
					return output, fmt.Errorf("accept ownership %s: %w", ref.Address, err)
				}
				wo = evmcontract.WriteOutput{
					ChainSelector: in.ChainSelector,
					ExecInfo:      &evmcontract.ExecInfo{Hash: "stellar-direct-accept"},
				}
			case in.ProposedOwner == in.GovernanceAddr:
				data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("accept_ownership", nil)
				if err != nil {
					return output, err
				}
				wo = evmcontract.WriteOutput{
					ChainSelector: in.ChainSelector,
					Tx: mcmstypes.Transaction{
						OperationMetadata: mcmstypes.OperationMetadata{
							ContractType: string(ref.Type),
						},
						To:               ref.Address,
						Data:             data,
						AdditionalFields: stellarMCMSTxAdditionalFields(),
					},
				}
			default:
				return output, fmt.Errorf(
					"accept routing: proposed owner %q must be deployer %q or governance %q for %s",
					in.ProposedOwner, deployerAddr, in.GovernanceAddr, ref.Address,
				)
			}
			batchOp, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{wo})
			if err != nil {
				return output, err
			}
			output.BatchOps = append(output.BatchOps, batchOp)
		}
		return output, nil
	},
)
