package ccip_receiver

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	recvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels the example CCIP receiver contract.
const ContractType = "CCIPReceiver"

// Deploy uploads ccip_receiver_example.wasm.
var Deploy = stellarops.NewDeployOperation("ccip-receiver:deploy", "Deploys the example CCIP receiver Soroban contract from WASM")

// InitializeInput wires owner and router on the receiver.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Owner      string `json:"owner"`
	Router     string `json:"router"`
}

// Initialize calls example receiver `initialize`.
var Initialize = cldfops.NewOperation(
	"ccip-receiver:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes CCIP example receiver with owner and router",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := recvbindings.NewExampleCcipReceiverClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Router); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// EnableRemoteChainInput enables inbound CCIP receive from a remote chain selector.
type EnableRemoteChainInput struct {
	ContractID            string `json:"contract_id"`
	Caller                string `json:"caller"`
	RemoteChainSelector   uint64 `json:"remote_chain_selector"`
	ExtraArgs             []byte `json:"extra_args"`
	AllowedFinalityConfig uint32 `json:"allowed_finality_config"`
}

// EnableRemoteChain calls `enable_remote_chain`.
var EnableRemoteChain = cldfops.NewOperation(
	"ccip-receiver:enable-remote-chain",
	stellarops.ContractDeploymentVersion,
	"Enables CCIP receiver processing for a remote chain selector",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in EnableRemoteChainInput) (stellarops.Void, error) {
		c := recvbindings.NewExampleCcipReceiverClient(d.Invoker, in.ContractID)
		if err := c.EnableRemoteChain(b.GetContext(), in.Caller, in.RemoteChainSelector, in.ExtraArgs, in.AllowedFinalityConfig); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// TransferOwnershipInput starts two-step ownership transfer.
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on the example CCIP receiver.
var TransferOwnership = cldfops.NewOperation(
	"ccip-receiver:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers CCIP example receiver ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := recvbindings.NewExampleCcipReceiverClient(d.Invoker, in.ContractID)
		if err := c.TransferOwnership(b.GetContext(), in.NewOwner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AcceptOwnershipInput completes two-step ownership transfer for the caller.
type AcceptOwnershipInput struct {
	ContractID string `json:"contract_id"`
}

// AcceptOwnership calls `accept_ownership` on the example CCIP receiver.
var AcceptOwnership = cldfops.NewOperation(
	"ccip-receiver:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts CCIP example receiver ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := recvbindings.NewExampleCcipReceiverClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the example CCIP receiver contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on example CCIP receiver (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"ccip-receiver:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the example CCIP receiver Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := recvbindings.NewExampleCcipReceiverClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
