package adapters

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	ccvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// StellarCCVCommitteeVerifierOnchainAdapter implements
// chainlink-ccv/deployment/adapters.CommitteeVerifierOnchainAdapter for Stellar
// (Soroban committee_verifier), wiring GenerateAggregatorConfig and threshold
// changesets to the same on-chain reads/writes as the EVM adapter.
type StellarCCVCommitteeVerifierOnchainAdapter struct{}

var _ ccvdeploymentadapters.CommitteeVerifierOnchainAdapter = (*StellarCCVCommitteeVerifierOnchainAdapter)(nil)

func (a *StellarCCVCommitteeVerifierOnchainAdapter) ScanCommitteeStates(
	ctx context.Context,
	env deployment.Environment,
	chainSelector uint64,
) ([]*ccvdeploymentadapters.CommitteeState, error) {
	refs := env.DataStore.Addresses().Filter(
		datastore.AddressRefByType(datastore.ContractType(committee_verifier.ContractType)),
		datastore.AddressRefByChainSelector(chainSelector),
	)

	if len(refs) == 0 {
		return nil, nil
	}

	stellarChains := env.BlockChains.StellarChains()
	chain, ok := stellarChains[chainSelector]
	if !ok {
		return nil, fmt.Errorf("Stellar chain %d not found in environment", chainSelector)
	}
	if chain.Signer == nil {
		return nil, fmt.Errorf("Stellar chain %d has no signer configured", chainSelector)
	}

	deployer := stellardeployment.NewDeployerWithSigner(
		chain.Client, chain.NetworkPassphrase, stellardeployment.NewSDKSigner(chain.Signer))

	states := make([]*ccvdeploymentadapters.CommitteeState, 0, len(refs))
	for _, ref := range refs {
		contractID, err := hexToStellarContractID(ref.Address)
		if err != nil {
			return nil, fmt.Errorf("convert address %s to Stellar contract ID: %w", ref.Address, err)
		}

		client := ccvbindings.NewCommitteeVerifierClient(deployer, contractID)
		configs, err := readSignatureConfigs(ctx, env, chainSelector, client)
		if err != nil {
			return nil, fmt.Errorf("get signature configs from %s on chain %d: %w", ref.Address, chainSelector, err)
		}

		sigConfigs := make([]ccvdeploymentadapters.SignatureConfig, 0, len(configs))
		for _, cfg := range configs {
			signers := make([]string, 0, len(cfg.Signers))
			for _, signer := range cfg.Signers {
				signers = append(signers, common.BytesToAddress(signer[12:32]).Hex())
			}
			sigConfigs = append(sigConfigs, ccvdeploymentadapters.SignatureConfig{
				SourceChainSelector: cfg.SourceChainSelector,
				Signers:             signers,
				Threshold:           uint8(cfg.Threshold),
			})
		}

		states = append(states, &ccvdeploymentadapters.CommitteeState{
			Qualifier:        ref.Qualifier,
			ChainSelector:    chainSelector,
			Address:          ref.Address,
			SignatureConfigs: sigConfigs,
		})
	}

	return states, nil
}

func (a *StellarCCVCommitteeVerifierOnchainAdapter) ApplySignatureConfigs(
	ctx context.Context,
	env deployment.Environment,
	destChainSelector uint64,
	qualifier string,
	change ccvdeploymentadapters.SignatureConfigChange,
) error {
	refs := env.DataStore.Addresses().Filter(
		datastore.AddressRefByType(datastore.ContractType(committee_verifier.ContractType)),
		datastore.AddressRefByChainSelector(destChainSelector),
		datastore.AddressRefByQualifier(qualifier),
	)
	if len(refs) == 0 {
		return fmt.Errorf("no CommitteeVerifier found for chain %d qualifier %q", destChainSelector, qualifier)
	}
	if len(refs) > 1 {
		return fmt.Errorf("multiple CommitteeVerifiers found for chain %d qualifier %q", destChainSelector, qualifier)
	}

	stellarChains := env.BlockChains.StellarChains()
	chain, ok := stellarChains[destChainSelector]
	if !ok {
		return fmt.Errorf("Stellar chain %d not found in environment", destChainSelector)
	}
	if chain.Signer == nil {
		return fmt.Errorf("Stellar chain %d has no signer configured", destChainSelector)
	}

	contractID, err := hexToStellarContractID(refs[0].Address)
	if err != nil {
		return fmt.Errorf("convert address %s to Stellar contract ID: %w", refs[0].Address, err)
	}

	deployer := stellardeployment.NewDeployerWithSigner(
		chain.Client, chain.NetworkPassphrase, stellardeployment.NewSDKSigner(chain.Signer))
	client := ccvbindings.NewCommitteeVerifierClient(deployer, contractID)

	signatureConfigs := make([]ccvbindings.SignatureQuorumConfig, 0, len(change.NewConfigs))
	for _, c := range change.NewConfigs {
		signers := make([][32]byte, 0, len(c.Signers))
		for _, s := range c.Signers {
			if !common.IsHexAddress(s) {
				return fmt.Errorf("invalid signer address %q for source chain %d", s, c.SourceChainSelector)
			}
			var padded [32]byte
			addr := common.HexToAddress(s)
			copy(padded[12:], addr[:])
			signers = append(signers, padded)
		}
		signatureConfigs = append(signatureConfigs, ccvbindings.SignatureQuorumConfig{
			SourceChainSelector: c.SourceChainSelector,
			Threshold:           uint32(c.Threshold),
			Signers:             signers,
		})
	}

	if err := client.ApplySignatureConfigs(ctx, change.RemovedSourceChainSelectors, signatureConfigs); err != nil {
		return fmt.Errorf("apply_signature_configs on chain %d: %w", destChainSelector, err)
	}

	return nil
}
