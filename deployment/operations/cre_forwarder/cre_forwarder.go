package cre_forwarder

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	crebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/cre"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels the CRE forwarder contract.
const ContractType = "CREForwarder"

// Deploy uploads forwarder.wasm.
var Deploy = stellarops.NewDeployOperation("cre-forwarder:deploy", "Deploys the CRE forwarder Soroban contract from WASM")

// InitializeInput wires the forwarder owner.
type InitializeInput struct {
	ContractID string `json:"contract_id"`
	Owner      string `json:"owner"`
}

// Initialize calls forwarder `initialize`.
var Initialize = cldfops.NewOperation(
	"cre-forwarder:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes the CRE forwarder with an owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// SetConfigInput configures the DON signer set for a (don_id, config_version) pair.
type SetConfigInput struct {
	ContractID    string     `json:"contract_id"`
	DonID         uint32     `json:"don_id"`
	ConfigVersion uint32     `json:"config_version"`
	F             uint32     `json:"f"`
	Signers       [][32]byte `json:"signers"`
}

// SetConfig calls forwarder `set_config` (owner-gated).
var SetConfig = cldfops.NewOperation(
	"cre-forwarder:set-config",
	stellarops.ContractDeploymentVersion,
	"Sets the DON ed25519 signer configuration on the CRE forwarder",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetConfigInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.SetConfig(b.GetContext(), in.DonID, in.ConfigVersion, in.F, in.Signers); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ClearConfigInput removes the DON signer set for a (don_id, config_version) pair.
type ClearConfigInput struct {
	ContractID    string `json:"contract_id"`
	DonID         uint32 `json:"don_id"`
	ConfigVersion uint32 `json:"config_version"`
}

// ClearConfig calls forwarder `clear_config` (owner-gated).
var ClearConfig = cldfops.NewOperation(
	"cre-forwarder:clear-config",
	stellarops.ContractDeploymentVersion,
	"Clears a DON signer configuration on the CRE forwarder",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ClearConfigInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.ClearConfig(b.GetContext(), in.DonID, in.ConfigVersion); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AddForwarderInput registers an authorized transmitter address.
type AddForwarderInput struct {
	ContractID string `json:"contract_id"`
	Forwarder  string `json:"forwarder"`
}

// AddForwarder calls forwarder `add_forwarder` (owner-gated).
var AddForwarder = cldfops.NewOperation(
	"cre-forwarder:add-forwarder",
	stellarops.ContractDeploymentVersion,
	"Adds an authorized transmitter to the CRE forwarder registry",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AddForwarderInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.AddForwarder(b.GetContext(), in.Forwarder); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// RemoveForwarderInput deregisters an authorized transmitter address.
type RemoveForwarderInput struct {
	ContractID string `json:"contract_id"`
	Forwarder  string `json:"forwarder"`
}

// RemoveForwarder calls forwarder `remove_forwarder` (owner-gated).
var RemoveForwarder = cldfops.NewOperation(
	"cre-forwarder:remove-forwarder",
	stellarops.ContractDeploymentVersion,
	"Removes an authorized transmitter from the CRE forwarder registry",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in RemoveForwarderInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.RemoveForwarder(b.GetContext(), in.Forwarder); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// TransferOwnershipInput starts two-step forwarder ownership transfer.
type TransferOwnershipInput struct {
	ContractID string `json:"contract_id"`
	NewOwner   string `json:"new_owner"`
}

// TransferOwnership calls `transfer_ownership` on the CRE forwarder.
var TransferOwnership = cldfops.NewOperation(
	"cre-forwarder:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers CRE forwarder ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.TransferOwnership(b.GetContext(), in.NewOwner); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// AcceptOwnershipInput completes two-step forwarder ownership transfer for the caller.
type AcceptOwnershipInput struct {
	ContractID string `json:"contract_id"`
}

// AcceptOwnership calls `accept_ownership` on the CRE forwarder.
var AcceptOwnership = cldfops.NewOperation(
	"cre-forwarder:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts CRE forwarder ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := crebindings.NewForwarderClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
