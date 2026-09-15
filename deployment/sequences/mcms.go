package sequences

import (
	"fmt"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	timelockops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/timelock"
)

func stellarDeployerFromChain(ch cldfstellar.Chain) (*stellardeployment.Deployer, error) {
	return stellardeployment.NewDeployerFromChain(ch)
}

func timelockMinDelay(in deploy.MCMSDeploymentConfigPerChainWithAddress) (uint64, error) {
	if in.TimelockMinDelay == nil {
		return 0, nil
	}
	if !in.TimelockMinDelay.IsUint64() {
		return 0, fmt.Errorf("timelockMinDelay must fit uint64")
	}
	return in.TimelockMinDelay.Uint64(), nil
}

// DeployStellarMCMS deploys three role-specific Soroban MCMS instances (proposer, canceller,
// bypasser) plus one self-administered RBACTimelock. Each MCMS gets an independent signer config,
// its own deterministic salt/address, an immutable instance label, and is owned by the timelock
// from initialization (no deployer ownership ever exists). Timelock roles follow the matrix:
// proposer MCMS holds PROPOSER and CANCELLER, canceller MCMS holds CANCELLER
// bypasser MCMS holds BYPASSER; execution is permissionless. Reruns are idempotent:
// an already-deployed role instance is left untouched (config changes go through governance).
var DeployStellarMCMS = cldfops.NewSequence(
	"stellar-deploy-mcms",
	deploy.MCMSVersion,
	"Deploy three role-specific Soroban MCMS instances and a self-administered RBACTimelock",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.MCMSDeploymentConfigPerChainWithAddress) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		qual := mcmsutil.QualifierStr(in.Qualifier)

		dep, err := stellarDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)

		roleConfig := map[mcmsutil.MCMSRole]mcmstypes.Config{
			mcmsutil.RoleProposer:  in.Proposer,
			mcmsutil.RoleCanceller: in.Canceller,
			mcmsutil.RoleBypasser:  in.Bypasser,
		}

		// Resolve or deploy each role instance. `fresh` marks the ones that still need initialize.
		addrs := map[mcmsutil.MCMSRole]string{}
		fresh := map[mcmsutil.MCMSRole]bool{}
		var mcmsWasm string
		for _, role := range mcmsutil.AllMCMSRoles {
			existing, found, err := mcmsutil.FindExistingStellarMCMSByRole(in.ExistingAddresses, in.ChainSelector, qual, role)
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			if found {
				addrs[role] = existing
				continue
			}
			if mcmsWasm == "" {
				if mcmsWasm, err = mcmsutil.ResolveMCMSWasmPath(); err != nil {
					return seqcore.OnChainOutput{}, err
				}
			}
			salt := mcmsutil.MCMSRoleDeploySalt(in.ChainSelector, qual, role)
			depOut, err := cldfops.ExecuteOperation(b, mcmsops.Deploy, deps, stellarops.DeployInput{WasmPath: mcmsWasm, Salt: salt})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("mcms deploy (%s): %w", role, err)
			}
			addrs[role] = depOut.Output.ContractID
			fresh[role] = true
		}

		// Resolve or deploy the timelock.
		tlID, haveTL := mcmsutil.FindExistingStellarTimelock(in.ExistingAddresses, in.ChainSelector, qual)
		if !haveTL {
			tlWasm, err := mcmsutil.ResolveTimelockWasmPath()
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			tlSalt := mcmsutil.TimelockDeploySalt(in.ChainSelector, qual)
			tlOut, err := cldfops.ExecuteOperation(b, timelockops.Deploy, deps, stellarops.DeployInput{WasmPath: tlWasm, Salt: tlSalt})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("timelock deploy: %w", err)
			}
			tlID = tlOut.Output.ContractID

			minDelay, err := timelockMinDelay(in)
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			// proposer also holds CANCELLER, same as EVM; execution is permissionless.
			_, err = cldfops.ExecuteOperation(b, timelockops.Initialize, deps, timelockops.InitializeInput{
				ContractID: tlID,
				MinDelay:   minDelay,
				Proposers:  []string{addrs[mcmsutil.RoleProposer]},
				Cancellers: []string{addrs[mcmsutil.RoleProposer], addrs[mcmsutil.RoleCanceller]},
				Bypassers:  []string{addrs[mcmsutil.RoleBypasser]},
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("timelock initialize: %w", err)
			}
		}

		// Initialize freshly deployed instances: owner is the timelock, so no deployer ownership
		// ever exists and config changes must go through governance thereafter.
		chainNetID := mcmsutil.ChainNetworkID(ch.NetworkPassphrase)
		for _, role := range mcmsutil.AllMCMSRoles {
			if !fresh[role] {
				continue
			}
			cfg := roleConfig[role]
			signerAddrs, signerGroups, gq, gp, _, err := mcmsutil.ConfigToStellarSetConfig(&cfg, true)
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("mcms config (%s): %w", role, err)
			}
			_, err = cldfops.ExecuteOperation(b, mcmsops.Initialize, deps, mcmsops.InitializeInput{
				ContractID:      addrs[role],
				Owner:           tlID,
				ChainNetworkID:  chainNetID,
				SignerAddresses: signerAddrs,
				SignerGroups:    signerGroups,
				GroupQuorums:    gq,
				GroupParents:    gp,
				InstanceLabel:   role.InstanceLabel(),
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("mcms initialize (%s): %w", role, err)
			}
		}

		refs := make([]datastore.AddressRef, 0, len(mcmsutil.AllMCMSRoles)+1)
		for _, role := range mcmsutil.AllMCMSRoles {
			ref, err := mcmsutil.StellarMCMSRoleDatastoreRef(in.ChainSelector, qual, role, addrs[role])
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			refs = append(refs, ref)
		}
		refs = append(refs, mcmsutil.StellarTimelockDatastoreRef(in.ChainSelector, qual, tlID))
		return seqcore.OnChainOutput{Addresses: refs}, nil
	},
)

// FinalizeStellarDeployMCMS verifies the post-deployment authority invariants and fails the
// deployment if any residual deployer authority remains or the timelock does not own each MCMS.
var FinalizeStellarDeployMCMS = cldfops.NewSequence(
	"stellar-finalize-deploy-mcms",
	deploy.MCMSVersion,
	"Verify no residual deployer authority and timelock ownership of each MCMS after deploy",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.MCMSDeploymentConfigPerChainWithAddress) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		dep, err := stellarDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		if err := VerifyStellarMCMSGovernance(b.GetContext(), stellardeps.FromDeployer(dep), in.ExistingAddresses, in.ChainSelector, mcmsutil.QualifierStr(in.Qualifier), dep.SignerAddress()); err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("mcms deployment finalize: %w", err)
		}
		return seqcore.OnChainOutput{}, nil
	},
)

// GrantAdminRoleToTimelockStellar is a no-op (no Stellar CallProxy; timelock executors can be added in a follow-up).
var GrantAdminRoleToTimelockStellar = cldfops.NewSequence(
	"stellar-grant-admin-role-to-timelock",
	deploy.MCMSVersion,
	"No-op: Stellar has no CallProxy executor grant step (EVM parity stub)",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.GrantAdminRoleToTimelockConfigPerChainWithSelector) (seqcore.OnChainOutput, error) {
		return seqcore.OnChainOutput{}, nil
	},
)

// UpdateStellarMCMSConfig applies set_config on each listed MCM contract address.
var UpdateStellarMCMSConfig = cldfops.NewSequence(
	"stellar-update-mcms-config",
	deploy.MCMSVersion,
	"Updates signer config on Stellar MCMS (single instance)",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.UpdateMCMSConfigInputPerChainWithSelector) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		dep, err := stellarDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)
		signerAddrs, signerGroups, gq, gp, _, err := mcmsutil.ConfigToStellarSetConfig(&in.MCMConfig, true)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		for _, ref := range in.MCMContracts {
			if ref.Address == "" {
				continue
			}
			_, err := cldfops.ExecuteOperation(b, mcmsops.SetConfig, deps, mcmsops.SetConfigInput{
				ContractID:      ref.Address,
				SignerAddresses: signerAddrs,
				SignerGroups:    signerGroups,
				GroupQuorums:    gq,
				GroupParents:    gp,
				ClearRoot:       true,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("set_config on %s: %w", ref.Address, err)
			}
		}
		return seqcore.OnChainOutput{}, nil
	},
)
