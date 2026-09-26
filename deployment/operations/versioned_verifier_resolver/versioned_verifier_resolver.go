package versioned_verifier_resolver

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels Versioned Verifier Resolver.
const ContractType = "VersionedVerifierResolver"

// Deploy uploads ccvs_versioned_verifier_resolver.wasm.
var Deploy = stellarops.NewDeployOperation("versioned-verifier-resolver:deploy", "Deploys the Versioned Verifier Resolver Soroban contract from WASM")

// InitializeInput sets VVR owner and fee aggregator.
type InitializeInput struct {
	ContractID    string `json:"contract_id"`
	Owner         string `json:"owner"`
	FeeAggregator string `json:"fee_aggregator"`
}

// Initialize calls VVR `initialize`.
var Initialize = cldfops.NewOperation(
	"versioned-verifier-resolver:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes Versioned Verifier Resolver with owner and fee aggregator",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.FeeAggregator); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyOutboundImplUpdatesInput binds outbound verifier implementations per destination chain.
type ApplyOutboundImplUpdatesInput struct {
	ContractID      string                                     `json:"contract_id"`
	Implementations []vvrbindings.OutboundImplementationUpdate `json:"implementations"`
}

// ApplyOutboundImplUpdates calls VVR `apply_outbound_impl_updates`.
var ApplyOutboundImplUpdates = cldfops.NewOperation(
	"versioned-verifier-resolver:apply-outbound-impl-updates",
	stellarops.ContractDeploymentVersion,
	"Applies Versioned Verifier Resolver outbound implementation updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyOutboundImplUpdatesInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
		if err := c.ApplyOutboundImplUpdates(b.GetContext(), in.Implementations); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyInboundImplUpdatesInput binds inbound verifier implementations by version tag.
type ApplyInboundImplUpdatesInput struct {
	ContractID      string                                    `json:"contract_id"`
	Implementations []vvrbindings.InboundImplementationUpdate `json:"implementations"`
}

// ApplyInboundImplUpdates calls VVR `apply_inbound_impl_updates`.
var ApplyInboundImplUpdates = cldfops.NewOperation(
	"versioned-verifier-resolver:apply-inbound-impl-updates",
	stellarops.ContractDeploymentVersion,
	"Applies Versioned Verifier Resolver inbound implementation updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyInboundImplUpdatesInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
		if err := c.ApplyInboundImplUpdates(b.GetContext(), in.Implementations); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// WithdrawFeeTokensInput lists fee token contract IDs to withdraw to the configured fee aggregator.
type WithdrawFeeTokensInput struct {
	ContractID string   `json:"contract_id"`
	FeeTokens  []string `json:"fee_tokens"`
}

// WithdrawFeeTokens calls VVR `withdraw_fee_tokens`.
var WithdrawFeeTokens = cldfops.NewOperation(
	"versioned-verifier-resolver:withdraw-fee-tokens",
	stellarops.ContractDeploymentVersion,
	"Withdraws listed fee token balances to the Versioned Verifier Resolver fee aggregator",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawFeeTokensInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
		if err := c.WithdrawFeeTokens(b.GetContext(), in.FeeTokens); err != nil {
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

// TransferOwnership calls `transfer_ownership` on Versioned Verifier Resolver.
var TransferOwnership = cldfops.NewOperation(
	"versioned-verifier-resolver:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers Versioned Verifier Resolver ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on Versioned Verifier Resolver.
var AcceptOwnership = cldfops.NewOperation(
	"versioned-verifier-resolver:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts Versioned Verifier Resolver ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the Versioned Verifier Resolver contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on Versioned Verifier Resolver (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"versioned-verifier-resolver:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the Versioned Verifier Resolver Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := vvrbindings.NewVersionedVerifierResolverClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
