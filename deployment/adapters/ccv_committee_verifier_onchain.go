package adapters

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	ccvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
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

	// The Soroban committee_verifier stores signature configs under
	// QuorumConfigKey::SourceChainSelector with no enumeration entrypoint (there is no
	// get_all_signature_configs), so the only way to read them back is to probe. Every chain
	// in the environment other than this one is the same bounded universe the ccv changesets
	// iterate over, so a config for a chain absent from the environment is invisible here.
	candidates := candidateSourceSelectors(env, chainSelector)

	states := make([]*ccvdeploymentadapters.CommitteeState, 0, len(refs))
	for _, ref := range refs {
		contractID, err := scval.HexToContractStrkey(ref.Address)
		if err != nil {
			return nil, fmt.Errorf("convert address %s to Stellar contract ID: %w", ref.Address, err)
		}

		client := ccvbindings.NewCommitteeVerifierClient(deployer, contractID)

		sigConfigs := make([]ccvdeploymentadapters.SignatureConfig, 0, len(candidates))
		for _, src := range candidates {
			cfg, err := client.GetSignatureConfig(ctx, src)
			if err != nil {
				if isSourceSignersNotConfigured(err) {
					continue
				}
				return nil, fmt.Errorf(
					"get signature config for source %d from %s on chain %d: %w",
					src, ref.Address, chainSelector, err)
			}
			if cfg == nil {
				continue
			}
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

	contractID, err := scval.HexToContractStrkey(refs[0].Address)
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
		// The contract requires strictly ascending signer order (CCIPError 67/66).
		sort.Slice(signers, func(i, j int) bool { return bytes.Compare(signers[i][:], signers[j][:]) < 0 })
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

// candidateSourceSelectors returns every chain selector in the environment except self.
// It is the probe set for ScanCommitteeStates; see the comment at its call site.
func candidateSourceSelectors(env deployment.Environment, self uint64) []uint64 {
	all := env.BlockChains.ListChainSelectors()
	out := make([]uint64, 0, len(all))
	for _, sel := range all {
		if sel != self {
			out = append(out, sel)
		}
	}
	return out
}

// isSourceSignersNotConfigured reports whether err is the Soroban contract's
// CCIPError::SourceSignersNotConfigured (code 19, contracts/common/error/src/lib.rs), which
// get_signature_config returns for a source chain that simply has no config yet.
//
// This must stay narrow. ScanCommitteeStates feeds ccv's CommitteeInputFromState, which treats
// an empty scan as "not deployed — skip"; swallowing an RPC error here would report committee
// drift as agreement.
func isSourceSignersNotConfigured(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), sourceSignersNotConfiguredCode)
}

// sourceSignersNotConfiguredCode is how the Soroban host renders contract error 19 in the
// simulation diagnostics the RPC client surfaces.
const sourceSignersNotConfiguredCode = "Error(Contract, #19)"

// SetAllowedFinalityConfig is a no-op for Stellar.
//
// The Soroban committee_verifier has no allowed-finality gate — set_allowed_finality_config
// exists only on the token pools (contracts/common/pool), and the verifier stores no finality
// state. Stellar ledgers are final on close, so every requested finality level is already
// accepted and the caller's request is already satisfied; recording that as a no-op is
// accurate, and returning an error instead would block mixed-family
// SetAllowedFinalityConfig runs for no on-chain benefit.
//
// If the contract ever gains set_allowed_finality_config, wire it here.
func (a *StellarCCVCommitteeVerifierOnchainAdapter) SetAllowedFinalityConfig(
	ctx context.Context,
	env deployment.Environment,
	chainSelector uint64,
	qualifier string,
	waitForFinality bool,
	waitForSafe bool,
	blockDepth uint16,
) error {
	env.Logger.Infow(
		"Stellar committee verifier has no allowed-finality config; treating request as satisfied",
		"chainSelector", chainSelector,
		"qualifier", qualifier,
		"waitForFinality", waitForFinality,
		"waitForSafe", waitForSafe,
		"blockDepth", blockDepth,
	)
	return nil
}

// ApplyAllowlistUpdates updates the per-destination-chain sender allowlist on the Soroban
// committee_verifier.
//
// Senders are Stellar strkeys (C… contract or G… account), not EVM hex: AllowListUpdate
// serializes them via scval.AddressSliceToScVal. Validate up front rather than letting a hex
// string through — HexToContractStrkey would happily mint a well-formed but wrong contract ID.
func (a *StellarCCVCommitteeVerifierOnchainAdapter) ApplyAllowlistUpdates(
	ctx context.Context,
	env deployment.Environment,
	chainSelector uint64,
	qualifier string,
	destChainSelector uint64,
	allowlistEnabled bool,
	addedSenders []string,
	removedSenders []string,
) error {
	if err := validateStellarSenders(addedSenders, "added"); err != nil {
		return err
	}
	if err := validateStellarSenders(removedSenders, "removed"); err != nil {
		return err
	}

	client, err := committeeVerifierClient(env, chainSelector, qualifier)
	if err != nil {
		return err
	}

	if err := client.ApplyAllowlistUpdates(ctx, []ccvbindings.AllowListUpdate{{
		DestChainSelector:         destChainSelector,
		AllowlistEnabled:          allowlistEnabled,
		AddedAllowlistedSenders:   addedSenders,
		RemovedAllowlistedSenders: removedSenders,
	}}); err != nil {
		return fmt.Errorf("apply_allowlist_updates on chain %d: %w", chainSelector, err)
	}
	return nil
}

// validateStellarSenders rejects anything that is not a Stellar contract or account strkey.
func validateStellarSenders(senders []string, label string) error {
	for _, s := range senders {
		if _, err := strkey.Decode(strkey.VersionByteContract, s); err == nil {
			continue
		}
		if _, err := strkey.Decode(strkey.VersionByteAccountID, s); err == nil {
			continue
		}
		return fmt.Errorf("invalid %s allowlist sender %q: expected a Stellar C… or G… address", label, s)
	}
	return nil
}

// committeeVerifierClient resolves the single CommitteeVerifier for (chain, qualifier) and
// returns a client bound to it.
func committeeVerifierClient(
	env deployment.Environment,
	chainSelector uint64,
	qualifier string,
) (*ccvbindings.CommitteeVerifierClient, error) {
	refs := env.DataStore.Addresses().Filter(
		datastore.AddressRefByType(datastore.ContractType(committee_verifier.ContractType)),
		datastore.AddressRefByChainSelector(chainSelector),
		datastore.AddressRefByQualifier(qualifier),
	)
	if len(refs) == 0 {
		return nil, fmt.Errorf("no CommitteeVerifier found for chain %d qualifier %q", chainSelector, qualifier)
	}
	if len(refs) > 1 {
		return nil, fmt.Errorf("multiple CommitteeVerifiers found for chain %d qualifier %q", chainSelector, qualifier)
	}

	chain, ok := env.BlockChains.StellarChains()[chainSelector]
	if !ok {
		return nil, fmt.Errorf("Stellar chain %d not found in environment", chainSelector)
	}
	if chain.Signer == nil {
		return nil, fmt.Errorf("Stellar chain %d has no signer configured", chainSelector)
	}

	contractID, err := scval.HexToContractStrkey(refs[0].Address)
	if err != nil {
		return nil, fmt.Errorf("convert address %s to Stellar contract ID: %w", refs[0].Address, err)
	}

	deployer := stellardeployment.NewDeployerWithSigner(
		chain.Client, chain.NetworkPassphrase, stellardeployment.NewSDKSigner(chain.Signer))
	return ccvbindings.NewCommitteeVerifierClient(deployer, contractID), nil
}
