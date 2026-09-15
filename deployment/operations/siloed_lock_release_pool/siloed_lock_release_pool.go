package siloed_lock_release_pool

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels siloed lock-release pool contracts.
const ContractType = "SiloedLockReleasePool"

// Deploy uploads pools_siloed_lock_release_pool.wasm.
var Deploy = stellarops.NewDeployOperation("siloed-lock-release-pool:deploy", "Deploys the siloed lock-release pool Soroban contract from WASM")

// InitializeInput matches siloed lock-release pool `initialize`.
type InitializeInput struct {
	ContractID    string `json:"contract_id"`
	Owner         string `json:"owner"`
	Token         string `json:"token"`
	TokenDecimals uint32 `json:"token_decimals"`
	Router        string `json:"router"`
	RampRegistry  string `json:"ramp_registry"`
}

// Initialize calls siloed lock-release pool `initialize`.
var Initialize = cldfops.NewOperation(
	"siloed-lock-release-pool:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes siloed lock-release pool with owner, token, router, and ramp registry",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.Token, in.TokenDecimals, in.Router, in.RampRegistry); err != nil {
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

// TransferOwnership calls `transfer_ownership` on siloed lock-release pool.
var TransferOwnership = cldfops.NewOperation(
	"siloed-lock-release-pool:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers siloed lock-release pool ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on siloed lock-release pool.
var AcceptOwnership = cldfops.NewOperation(
	"siloed-lock-release-pool:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts siloed lock-release pool ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyChainUpdatesInput adds or removes remote chain configs on the siloed lock-release pool.
type ApplyChainUpdatesInput struct {
	ContractID string                    `json:"contract_id"`
	Adds       []slrbindings.ChainUpdate `json:"adds"`
	Removes    []uint64                  `json:"removes"`
}

// ApplyChainUpdates calls siloed lock-release pool `apply_chain_updates`.
var ApplyChainUpdates = cldfops.NewOperation(
	"siloed-lock-release-pool:apply-chain-updates",
	stellarops.ContractDeploymentVersion,
	"Adds or removes remote chain configs on the siloed lock-release pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyChainUpdatesInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ApplyChainUpdates(b.GetContext(), in.Adds, in.Removes); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ConfigureLockBoxesInput maps remote chain selectors to token lock box addresses.
type ConfigureLockBoxesInput struct {
	ContractID string                     `json:"contract_id"`
	Configs    []slrbindings.LockBoxEntry `json:"configs"`
}

// ConfigureLockBoxes calls siloed lock-release pool `configure_lock_boxes`.
var ConfigureLockBoxes = cldfops.NewOperation(
	"siloed-lock-release-pool:configure-lock-boxes",
	stellarops.ContractDeploymentVersion,
	"Maps remote chain selectors to token lock box addresses on the siloed pool",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ConfigureLockBoxesInput) (stellarops.Void, error) {
		c := slrbindings.NewSiloedLockReleasePoolClient(d.Invoker, in.ContractID)
		if err := c.ConfigureLockBoxes(b.GetContext(), in.Configs); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
