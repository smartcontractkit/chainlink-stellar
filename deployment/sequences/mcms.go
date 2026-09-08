package sequences

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

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

// validateStellarTimelockAdmin rejects a configured TimelockAdmin: the Stellar RBACTimelock
// grants ADMIN_ROLE only to itself, so there is no external admin to assign.
func validateStellarTimelockAdmin(in deploy.MCMSDeploymentConfigPerChainWithAddress) error {
	if in.TimelockAdmin != (common.Address{}) {
		return fmt.Errorf("timelockAdmin must be the zero address for Stellar RBACTimelock deploy (the timelock is its own admin); non-zero EVM addresses are not supported")
	}
	return nil
}

// DeployStellarMCMS deploys a single Soroban MCMS instance and applies the merged signer config.
var DeployStellarMCMS = cldfops.NewSequence(
	"stellar-deploy-mcms",
	deploy.MCMSVersion,
	"Deploy single Soroban MCMS, set config, then deploy and initialize RBACTimelock (MCMS as proposer/bypasser/canceller)",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.MCMSDeploymentConfigPerChainWithAddress) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
		}
		qual := mcmsutil.QualifierStr(in.Qualifier)
		merged, err := mcmsutil.MergeTripleMCMSConfig(in.Proposer, in.Bypasser, in.Canceller)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		signerAddrs, signerGroups, gq, gp, _, err := mcmsutil.ConfigToStellarSetConfig(merged, true)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}

		dep, err := stellarDeployerFromChain(ch)
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)

		contractID, _ := mcmsutil.FindExistingStellarMCMS(in.ExistingAddresses, in.ChainSelector, qual)
		if contractID == "" {
			wasmPath, err := mcmsutil.ResolveMCMSWasmPath()
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			salt := mcmsutil.MCMSDeploySalt(in.ChainSelector, qual)
			depOut, err := cldfops.ExecuteOperation(b, mcmsops.Deploy, deps, stellarops.DeployInput{WasmPath: wasmPath, Salt: salt})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("mcms deploy: %w", err)
			}
			contractID = depOut.Output.ContractID
			_, err = cldfops.ExecuteOperation(b, mcmsops.Initialize, deps, mcmsops.InitializeInput{
				ContractID:     contractID,
				Owner:          ch.Signer.Address(),
				ChainNetworkID: mcmsutil.ChainNetworkID(ch.NetworkPassphrase),
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("mcms initialize: %w", err)
			}
		}

		_, err = cldfops.ExecuteOperation(b, mcmsops.SetConfig, deps, mcmsops.SetConfigInput{
			ContractID:      contractID,
			SignerAddresses: signerAddrs,
			SignerGroups:    signerGroups,
			GroupQuorums:    gq,
			GroupParents:    gp,
			ClearRoot:       true,
		})
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("mcms set_config: %w", err)
		}

		mcmsRefs := mcmsutil.StellarMCMSDatastoreRefs(in.ChainSelector, qual, contractID)
		mergedRefs := append(append([]datastore.AddressRef{}, in.ExistingAddresses...), mcmsRefs...)
		tlID, haveTL := mcmsutil.FindExistingStellarTimelock(mergedRefs, in.ChainSelector, qual)
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
			if err := validateStellarTimelockAdmin(in); err != nil {
				return seqcore.OnChainOutput{}, err
			}
			var minDelay uint64
			if in.TimelockMinDelay != nil {
				if !in.TimelockMinDelay.IsUint64() {
					return seqcore.OnChainOutput{}, fmt.Errorf("timelockMinDelay must fit uint64")
				}
				minDelay = in.TimelockMinDelay.Uint64()
			}
			roleHolders := []string{contractID}
			_, err = cldfops.ExecuteOperation(b, timelockops.Initialize, deps, timelockops.InitializeInput{
				ContractID: tlID,
				MinDelay:   minDelay,
				Proposers:  roleHolders,
				Cancellers: roleHolders,
				Bypassers:  roleHolders,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("timelock initialize: %w", err)
			}
		}

		out := append(mcmsRefs, mcmsutil.StellarTimelockDatastoreRef(in.ChainSelector, qual, tlID))
		return seqcore.OnChainOutput{Addresses: out}, nil
	},
)

// FinalizeStellarDeployMCMS is a no-op (initialize runs in DeployStellarMCMS).
var FinalizeStellarDeployMCMS = cldfops.NewSequence(
	"stellar-finalize-deploy-mcms",
	deploy.MCMSVersion,
	"No-op finalize for Stellar MCMS (initialize is synchronous with deploy)",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in deploy.MCMSDeploymentConfigPerChainWithAddress) (seqcore.OnChainOutput, error) {
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
