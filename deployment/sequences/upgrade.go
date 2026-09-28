package sequences

import (
	"context"
	"fmt"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// StellarUpgradeContractsInput drives an in-place upgrade of one or more
// deployed Soroban CCIP contracts on a single chain. Each ref is upgraded
// independently: the freshly-compiled release WASM for its contract type is
// uploaded, the contract's current owner is read, and the upgrade is routed
// either as a direct on-chain call (owner == deployer EOA) or as an MCMS
// proposal transaction (owner == governance/timelock) — the same two-path
// model as StellarTransferOwnershipViaMCMS.
type StellarUpgradeContractsInput struct {
	ChainSelector  uint64                 `json:"chain_selector"`
	ContractRef    []datastore.AddressRef `json:"contract_ref"`
	GovernanceAddr string                 `json:"governance_addr"`
}

// wasmUploader uploads freshly-compiled WASM bytes and returns their code hash.
// *stellardeployment.Deployer satisfies it via UploadContractWASM. It is an
// interface seam so the per-ref routing can be unit-tested with a stub uploader
// (no network, arm64-safe).
type wasmUploader interface {
	UploadContractWASM(ctx context.Context, wasmPath string) (xdr.Hash, error)
}

// upgradeContractRef performs the upload + owner read + two-path routing for a
// single contract ref, returning the WriteOutput to fold into the batch. It is
// extracted from the sequence body so the MCMS/governance branch can be
// exercised without a live RPC client.
func upgradeContractRef(
	ctx context.Context,
	b cldfops.Bundle,
	deps stellardeps.StellarDeps,
	uploader wasmUploader,
	deployerAddr, governanceAddr string,
	chainSelector uint64,
	ref datastore.AddressRef,
) (evmcontract.WriteOutput, error) {
	// Recorded refs may carry the hex form; the ops and the proposal To need
	// the strkey, so normalize once up front.
	cid, err := ownership.NormalizeContractAddress(ref.Address)
	if err != nil {
		return evmcontract.WriteOutput{}, fmt.Errorf("upgrade on chain %d: %w", chainSelector, err)
	}
	ref.Address = cid

	// Upload the freshly-compiled release WASM for this contract type → hash.
	wasmPath, err := stellarutil.ResolveUpgradeWasmPath(string(ref.Type))
	if err != nil {
		return evmcontract.WriteOutput{}, fmt.Errorf("upgrade %s: %w", cid, err)
	}
	hash, err := uploader.UploadContractWASM(ctx, wasmPath)
	if err != nil {
		return evmcontract.WriteOutput{}, fmt.Errorf("upload wasm for %s: %w", cid, err)
	}

	// Route on owner (same two-path model as transfer_ownership.go).
	owner, err := ownership.ContractOwner(ctx, deps, ref)
	if err != nil {
		return evmcontract.WriteOutput{}, fmt.Errorf("read owner %s: %w", cid, err)
	}
	switch {
	case owner == deployerAddr:
		if err := ownership.ExecuteUpgrade(b, deps, ref, hash); err != nil {
			return evmcontract.WriteOutput{}, fmt.Errorf("upgrade %s: %w", cid, err)
		}
		return evmcontract.WriteOutput{
			ChainSelector: chainSelector,
			ExecInfo:      &evmcontract.ExecInfo{Hash: "stellar-direct-upgrade"},
		}, nil
	case owner == governanceAddr:
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("upgrade", []xdr.ScVal{scval.Bytes32ToScVal(hash)})
		if err != nil {
			return evmcontract.WriteOutput{}, err
		}
		return evmcontract.WriteOutput{
			ChainSelector: chainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{
					ContractType: string(ref.Type),
				},
				To:               ref.Address,
				Data:             data,
				AdditionalFields: stellarMCMSTxAdditionalFields(),
			},
		}, nil
	default:
		return evmcontract.WriteOutput{}, fmt.Errorf(
			"contract %s owner %q is neither deployer %q nor governance %q",
			cid, owner, deployerAddr, governanceAddr,
		)
	}
}

// StellarUpgradeContractsViaMCMS upgrades Soroban CCIP contracts in place. For
// each ref it uploads the freshly-compiled release WASM for that contract type,
// reads the current owner, and either executes the upgrade directly (EOA owner)
// or emits an MCMS proposal transaction (governance/timelock owner). Mirrors
// StellarTransferOwnershipViaMCMS; there is no EVM OpUpgrade to mirror (EVM
// upgrades by deploy-new-and-repoint, while Soroban swaps the Wasm hash in
// place via update_current_contract_wasm).
var StellarUpgradeContractsViaMCMS = cldfops.NewSequence(
	"stellar-seq-upgrade-contracts-via-mcms",
	deploy.MCMSVersion,
	"Upgrades Soroban CCIP contracts in place via MCMS or deployer",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarUpgradeContractsInput) (output seqcore.OnChainOutput, err error) {
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
			wo, err := upgradeContractRef(ctx, b, deps, dep, deployerAddr, in.GovernanceAddr, in.ChainSelector, ref)
			if err != nil {
				return output, err
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
