package adapters

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/strkey"

	ccvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	dsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
)

type StellarAggregatorConfigAdapter struct{}

var _ ccvadapters.AggregatorConfigAdapter = (*StellarAggregatorConfigAdapter)(nil)

func (a *StellarAggregatorConfigAdapter) ScanCommitteeStates(ctx context.Context, env deployment.Environment, chainSelector uint64) ([]*ccvadapters.CommitteeState, error) {
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

	// get_signature_config is a read-only Soroban simulation, but we use the
	// CLDF chain signer (an Ed25519 key already provisioned by the deployment
	// environment) instead of generating an ephemeral keypair. This avoids
	// creating a fresh unfunded Stellar account on every changeset run and
	// keeps the deployer account stable across calls.
	deployer := stellardeployment.NewDeployerWithSigner(chain.Client, chain.NetworkPassphrase, stellardeployment.NewSDKSigner(chain.Signer))

	states := make([]*ccvadapters.CommitteeState, 0, len(refs))
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

		sigConfigs := make([]ccvadapters.SignatureConfig, 0, len(configs))
		for _, cfg := range configs {
			signers := make([]string, 0, len(cfg.Signers))
			for _, signer := range cfg.Signers {
				// Match EVMAggregatorConfigAdapter: on-chain values are left-padded
				// 20-byte EVM addresses (see contracts/common/verifier ETH_ADDRESS_OFFSET).
				signers = append(signers, common.BytesToAddress(signer[12:32]).Hex())
			}
			sigConfigs = append(sigConfigs, ccvadapters.SignatureConfig{
				SourceChainSelector: cfg.SourceChainSelector,
				Signers:             signers,
				Threshold:           uint8(cfg.Threshold),
			})
		}

		states = append(states, &ccvadapters.CommitteeState{
			Qualifier:        ref.Qualifier,
			ChainSelector:    chainSelector,
			Address:          ref.Address,
			SignatureConfigs: sigConfigs,
		})
	}

	return states, nil
}

// readSignatureConfigs returns the signature quorum config of every source chain in the
// environment that the committee verifier has configured. The contract only exposes a
// per-source getter, so sources outside the environment are not visible here.
func readSignatureConfigs(ctx context.Context, env deployment.Environment, chainSelector uint64, client *ccvbindings.CommitteeVerifierClient) ([]ccvbindings.SignatureQuorumConfig, error) {
	var configs []ccvbindings.SignatureQuorumConfig
	for _, source := range env.BlockChains.ListChainSelectors(cldf_chain.WithChainSelectorsExclusion([]uint64{chainSelector})) {
		cfg, err := client.GetSignatureConfig(ctx, source)
		if err != nil {
			if isContractError(err, ccvbindings.CCIPErrorSourceSignersNotConfigured) {
				continue
			}
			return nil, fmt.Errorf("source %d: %w", source, err)
		}
		configs = append(configs, *cfg)
	}
	return configs, nil
}

// isContractError reports whether a simulation error carries the given CCIPError code.
func isContractError(err error, code int) bool {
	return strings.Contains(err.Error(), fmt.Sprintf("Error(Contract, #%d)", code))
}

func hexToStellarContractID(hexAddr string) (string, error) {
	addr := strings.TrimPrefix(hexAddr, "0x")
	b, err := hex.DecodeString(addr)
	if err != nil {
		return "", fmt.Errorf("decode hex address %q: %w", hexAddr, err)
	}
	return strkey.Encode(strkey.VersionByteContract, b)
}

func (a *StellarAggregatorConfigAdapter) ResolveVerifierAddress(ds datastore.DataStore, chainSelector uint64, qualifier string) (string, error) {
	return dsutils.FindAndFormatFirstRef(ds, chainSelector,
		func(r datastore.AddressRef) (string, error) { return r.Address, nil },
		datastore.AddressRef{
			Type:      datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
			Qualifier: qualifier,
		},
		datastore.AddressRef{
			Type:      datastore.ContractType(committee_verifier.ContractType),
			Qualifier: qualifier,
		},
	)
}
