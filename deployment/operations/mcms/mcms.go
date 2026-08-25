package mcms

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"

	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels MCMS.
const ContractType = "MCMS"

// Deploy uploads mcms.wasm.
var Deploy = stellarops.NewDeployOperation("mcms:deploy", "Deploys the MCMS Soroban contract from WASM")

// InitializeInput configures MCMS owner and chain network id.
type InitializeInput struct {
	ContractID     string   `json:"contract_id"`
	Owner          string   `json:"owner"`
	ChainNetworkID [32]byte `json:"chain_network_id"`
}

// Initialize calls MCMS `initialize`.
var Initialize = cldfops.NewOperation(
	"mcms:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes MCMS with owner and chain network id",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		// TODO changes broke this, but keep for when CCIP picks up stellar again
		//c := mcmsbindings.NewMcmsClient(d.Invoker, in.ContractID)
		//if err := c.Initialize(b.GetContext(), in.Owner, in.ChainNetworkID); err != nil {
		//return stellarops.Void{}, err
		//}
		return stellarops.Void{}, nil
	},
)

// SetConfigInput configures MCMS signer addresses, group hierarchy, quorums, and clear-root flag.
type SetConfigInput struct {
	ContractID      string                       `json:"contract_id"`
	SignerAddresses mcmsbindings.SignerAddresses `json:"signer_addresses"`
	SignerGroups    mcmsbindings.SignerGroups    `json:"signer_groups"`
	GroupQuorums    [32]byte                     `json:"group_quorums"`
	GroupParents    [32]byte                     `json:"group_parents"`
	ClearRoot       bool                         `json:"clear_root"`
}

// SetConfig calls MCMS `set_config`.
var SetConfig = cldfops.NewOperation(
	"mcms:set-config",
	stellarops.ContractDeploymentVersion,
	"Updates MCMS signer set, group hierarchy, and quorums",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetConfigInput) (stellarops.Void, error) {
		c := mcmsbindings.NewMcmsClient(d.Invoker, in.ContractID)
		if err := c.SetConfig(b.GetContext(), in.SignerAddresses, in.SignerGroups, in.GroupQuorums, in.GroupParents, in.ClearRoot); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetOpCountInput identifies the MCMS instance to read the on-chain op counter from.
type GetOpCountInput struct {
	ContractID string `json:"contract_id"`
}

// GetOpCountOutput is the current MCMS op count, used by MCMSReader to derive a fresh op for new proposals.
type GetOpCountOutput struct {
	OpCount uint64 `json:"op_count"`
}

// GetOpCount calls MCMS `get_op_count` (simulation, read-only).
var GetOpCount = cldfops.NewOperation(
	"mcms:get-op-count",
	stellarops.ContractDeploymentVersion,
	"Reads the current MCMS op count via simulation",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetOpCountInput) (GetOpCountOutput, error) {
		c := mcmsbindings.NewMcmsClient(d.Invoker, in.ContractID)
		count, err := c.GetOpCount(b.GetContext())
		if err != nil {
			return GetOpCountOutput{}, err
		}
		return GetOpCountOutput{OpCount: count}, nil
	},
)

// GetConfigInput identifies the MCMS instance to read the on-chain config from.
type GetConfigInput struct {
	ContractID string `json:"contract_id"`
}

// GetConfigOutput is the current MCMS configuration, used by MCMSReader to compare on-chain state against desired state.
type GetConfigOutput struct {
	Config *mcmsbindings.Config `json:"config"`
}

// GetConfig calls MCMS `get_config` (simulation, read-only).
var GetConfig = cldfops.NewOperation(
	"mcms:get-config",
	stellarops.ContractDeploymentVersion,
	"Reads the current MCMS signer/group configuration via simulation",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetConfigInput) (GetConfigOutput, error) {
		c := mcmsbindings.NewMcmsClient(d.Invoker, in.ContractID)
		cfg, err := c.GetConfig(b.GetContext())
		if err != nil {
			return GetConfigOutput{}, err
		}
		return GetConfigOutput{Config: cfg}, nil
	},
)

// TransferOwnershipInput starts two-step MCMS ownership transfer.
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on MCMS.
var TransferOwnership = cldfops.NewOperation(
	"mcms:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers MCMS ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := mcmsbindings.NewMcmsClient(d.Invoker, in.ContractID)
		if err := c.TransferOwnership(b.GetContext(), in.NewOwner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AcceptOwnershipInput completes two-step MCMS ownership transfer for the caller.
type AcceptOwnershipInput struct {
	ContractID string `json:"contract_id"`
}

// AcceptOwnership calls `accept_ownership` on MCMS.
var AcceptOwnership = cldfops.NewOperation(
	"mcms:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts MCMS ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := mcmsbindings.NewMcmsClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
