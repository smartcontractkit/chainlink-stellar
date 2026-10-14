package committee_verifier

import (
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ContractType labels Committee Verifier (CCVS).
const ContractType = "CommitteeVerifier"

// Deploy uploads ccvs_committee_verifier.wasm.
var Deploy = stellarops.NewDeployOperation("committee-verifier:deploy", "Deploys the Committee Verifier Soroban contract from WASM")

// InitializeInput configures Committee Verifier dynamic config and storage.
type InitializeInput struct {
	ContractID       string                   `json:"contract_id"`
	Owner            string                   `json:"owner"`
	DynamicConfig    cvbindings.DynamicConfig `json:"dynamic_config"`
	StorageLocations [][]byte                 `json:"storage_locations"`
	RmnProxy         string                   `json:"rmn_proxy"`
	VersionTag       [4]byte                  `json:"version_tag"`
}

// Initialize calls Committee Verifier `initialize`.
var Initialize = cldfops.NewOperation(
	"committee-verifier:initialize",
	stellarops.ContractDeploymentVersion,
	"Initializes Committee Verifier with owner, dynamic config, and RMN proxy",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in InitializeInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		if err := c.Initialize(b.GetContext(), in.Owner, in.DynamicConfig, in.StorageLocations, in.RmnProxy, in.VersionTag); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplyRemoteChainCfgUpdatesInput applies per-remote-chain fee and router settings on Committee Verifier.
type ApplyRemoteChainCfgUpdatesInput struct {
	ContractID string                         `json:"contract_id"`
	Configs    []cvbindings.RemoteChainConfig `json:"configs"`
}

// ApplyRemoteChainCfgUpdates calls Committee Verifier `apply_remote_chain_cfg_updates`.
var ApplyRemoteChainCfgUpdates = cldfops.NewOperation(
	"committee-verifier:apply-remote-chain-cfg-updates",
	stellarops.ContractDeploymentVersion,
	"Applies Committee Verifier remote chain configuration updates",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplyRemoteChainCfgUpdatesInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		if err := c.ApplyRemoteChainCfgUpdates(b.GetContext(), in.Configs); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// ApplySignatureConfigsInput updates signature quorum configs on Committee Verifier.
type ApplySignatureConfigsInput struct {
	ContractID             string                             `json:"contract_id"`
	RemoveSelectors        []uint64                           `json:"remove_selectors"`
	SignatureQuorumConfigs []cvbindings.SignatureQuorumConfig `json:"signature_quorum_configs"`
}

// ApplySignatureConfigs calls Committee Verifier `apply_signature_configs`.
var ApplySignatureConfigs = cldfops.NewOperation(
	"committee-verifier:apply-signature-configs",
	stellarops.ContractDeploymentVersion,
	"Applies Committee Verifier signature quorum configurations",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in ApplySignatureConfigsInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		if err := c.ApplySignatureConfigs(b.GetContext(), in.RemoveSelectors, in.SignatureQuorumConfigs); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// GetSignatureConfigInput identifies a Committee Verifier signature quorum read.
type GetSignatureConfigInput struct {
	ContractID          string `json:"contract_id"`
	SourceChainSelector uint64 `json:"source_chain_selector"`
}

// GetSignatureConfig reads Committee Verifier `get_signature_config` for one
// source chain. It returns (nil, nil) when the source chain has no signature
// quorum yet — the contract errors with SourceSignersNotConfigured (#19) for
// unconfigured sources, which callers treat as "not configured, apply fresh
// values" (EVM read-before-write parity with committee_verifier.GetSignatureConfig).
var GetSignatureConfig = cldfops.NewOperation(
	"committee-verifier:get-signature-config",
	stellarops.ContractDeploymentVersion,
	"Reads the Committee Verifier signature quorum config for one source chain",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetSignatureConfigInput) (*cvbindings.SignatureQuorumConfig, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		out, err := c.GetSignatureConfig(b.GetContext(), in.SourceChainSelector)
		if err != nil {
			if stellarops.IsContractErrorCode(err, stellarops.SourceSignersNotConfiguredCode) {
				return nil, nil
			}
			return nil, err
		}
		return out, nil
	},
)

// GetAllowedFinalityConfigInput identifies a Committee Verifier allowed-finality read.
type GetAllowedFinalityConfigInput struct {
	ContractID string `json:"contract_id"`
}

// GetAllowedFinalityConfig reads Committee Verifier `get_allowed_finality_config`
// (EVM `BaseVerifier.getAllowedFinalityConfig`).
var GetAllowedFinalityConfig = cldfops.NewOperation(
	"committee-verifier:get-allowed-finality-config",
	stellarops.ContractDeploymentVersion,
	"Reads the Committee Verifier allowed finality config",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in GetAllowedFinalityConfigInput) (uint32, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		return c.GetAllowedFinalityConfig(b.GetContext())
	},
)

// SetAllowedFinalityConfigInput sets the verifier-global allowed finality.
type SetAllowedFinalityConfigInput struct {
	ContractID            string `json:"contract_id"`
	AllowedFinalityConfig uint32 `json:"allowed_finality_config"`
}

// SetAllowedFinalityConfig calls Committee Verifier `set_allowed_finality_config`
// (owner-gated, M-9 / INV-FIN-CCV-1/2; EVM `BaseVerifier.setAllowedFinalityConfig`).
var SetAllowedFinalityConfig = cldfops.NewOperation(
	"committee-verifier:set-allowed-finality-config",
	stellarops.ContractDeploymentVersion,
	"Sets the Committee Verifier allowed finality config",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in SetAllowedFinalityConfigInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		if err := c.SetAllowedFinalityConfig(b.GetContext(), in.AllowedFinalityConfig); err != nil {
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

// WithdrawFeeTokens calls Committee Verifier `withdraw_fee_tokens`.
var WithdrawFeeTokens = cldfops.NewOperation(
	"committee-verifier:withdraw-fee-tokens",
	stellarops.ContractDeploymentVersion,
	"Withdraws listed fee token balances to the Committee Verifier fee aggregator",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in WithdrawFeeTokensInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
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

// TransferOwnership calls `transfer_ownership` on Committee Verifier.
var TransferOwnership = cldfops.NewOperation(
	"committee-verifier:transfer-ownership",
	stellarops.ContractDeploymentVersion,
	"Transfers Committee Verifier ownership to a pending new owner",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in TransferOwnershipInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
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

// AcceptOwnership calls `accept_ownership` on Committee Verifier.
var AcceptOwnership = cldfops.NewOperation(
	"committee-verifier:accept-ownership",
	stellarops.ContractDeploymentVersion,
	"Accepts Committee Verifier ownership after transfer_ownership",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in AcceptOwnershipInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		if err := c.AcceptOwnership(b.GetContext()); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)

// UpgradeInput upgrades the Committee Verifier contract in place to a new Wasm hash.
type UpgradeInput struct {
	ContractID  string   `json:"contract_id"`
	NewWasmHash [32]byte `json:"new_wasm_hash"`
}

// Upgrade calls `upgrade` on Committee Verifier (owner-gated in-place self-upgrade).
var Upgrade = cldfops.NewOperation(
	"committee-verifier:upgrade",
	stellarops.ContractDeploymentVersion,
	"Upgrades the Committee Verifier Soroban contract in place to a new WASM hash",
	func(b cldfops.Bundle, d stellardeps.StellarDeps, in UpgradeInput) (stellarops.Void, error) {
		c := cvbindings.NewCommitteeVerifierClient(d.Invoker, in.ContractID)
		if err := c.Upgrade(b.GetContext(), in.NewWasmHash); err != nil {
			return stellarops.Void{}, err
		}
		return stellarops.Void{}, nil
	},
)
