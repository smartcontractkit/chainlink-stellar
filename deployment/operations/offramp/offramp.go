// Package offramp defines CLDF operations for the Soroban OffRamp contract.
package offramp

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType is the datastore / lane tooling label for this contract family.
const ContractType = "OffRamp"

// Deploy uploads offramp.wasm (or another path) with a deterministic salt.
var Deploy = stellarops.NewDeployOperation(
	"offramp:deploy",
	"Deploys the OffRamp Soroban contract from a WASM file",
)

// InitializeInput runs the contract `initialize` entrypoint (owner + static config).
type InitializeInput struct {
	ContractID string                       `json:"contract_id"`
	Owner      string                       `json:"owner"`
	Config     offrampbindings.StaticConfig `json:"static_config"`
}

// Initialize sets chain selector, RMN proxy, and token admin registry on a new OffRamp.
var Initialize = cldfops.NewOperation(
	"offramp:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes OffRamp with owner and static configuration",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := offrampbindings.NewOffRampClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Config); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplySourceChainCfgUpdatesInput applies one or more source-chain configuration records.
type ApplySourceChainCfgUpdatesInput struct {
	ContractID string                                  `json:"contract_id"`
	Updates    []offrampbindings.SourceChainConfigArgs `json:"updates"`
}

// ApplySourceChainCfgUpdates wires remote chain selectors to OffRamp (on-ramps, routers, CCVs).
var ApplySourceChainCfgUpdates = cldfops.NewOperation(
	"offramp:apply-source-chain-cfg-updates",
	stellarops.ContractDeploymentVersion,
	"Applies OffRamp source chain configuration updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplySourceChainCfgUpdatesInput) (stellarops.Void, error) {
		c := offrampbindings.NewOffRampClient(d.Invoker, in.ContractID)
		if err := c.ApplySourceChainCfgUpdates(b.GetContext(), in.Updates); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ExecuteInput carries a committed message and verifier material for `execute`.
type ExecuteInput struct {
	ContractID       string   `json:"contract_id"`
	EncodedMessage   []byte   `json:"encoded_message"`
	Ccvs             []string `json:"ccvs"`
	VerifierResults  [][]byte `json:"verifier_results"`
	GasLimitOverride uint32   `json:"gas_limit_override"`
}

// Execute runs message execution on OffRamp (single committed message).
var Execute = cldfops.NewOperation(
	"offramp:execute",
	stellarops.ContractDeploymentVersion,
	"Executes a single committed CCIP message on OffRamp",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ExecuteInput) (stellarops.Void, error) {
		c := offrampbindings.NewOffRampClient(d.Invoker, in.ContractID)
		if err := c.Execute(b.GetContext(), in.EncodedMessage, in.Ccvs, in.VerifierResults, in.GasLimitOverride); err != nil {
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

// TransferOwnership calls `transfer_ownership` on OffRamp.
var TransferOwnership = cldfops.NewOperation(
	"offramp:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers OffRamp ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := offrampbindings.NewOffRampClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on OffRamp.
var AcceptOwnership = cldfops.NewOperation(
	"offramp:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts OffRamp ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := offrampbindings.NewOffRampClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the OffRamp contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on OffRamp (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"offramp:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the OffRamp Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := offrampbindings.NewOffRampClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
