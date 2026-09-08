package sequences

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// StellarConfigureChainForLanes is the Stellar leg of the shared
// ConfigureChainsForLanesFromTopology changeset. Its only on-chain effect today is applying
// committee verifier signature quorum configs per remote chain.
//
// This runs at lane-configuration time, not deploy time, on purpose: since the
// chainlink-ccv 2026-06-26 signing-key-sync cutover, topology enrichment skips families whose
// bootstrappers push signing keys to JD on connect, so signer addresses are not available to
// RunStellarCCIPFullDeploy. The changeset resolves them from JD (falling back to topology
// signer addresses) and passes them in via CommitteeVerifierRemoteChainConfig.SignatureConfig.
//
// Router/FeeQuoter/OnRamp/OffRamp lane wiring for Stellar is handled at deploy time by
// RunStellarCCIPFullDeploy and in ccvchain Chain.PostConnect, not here.
var StellarConfigureChainForLanes = cldf_ops.NewSequence(
	"stellar-configure-chain-for-lanes",
	SequenceVersion,
	"Applies committee verifier signature quorum configs on Stellar for each remote lane",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input ccvadapters.ConfigureChainForLanesInput) (seq_core.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[input.ChainSelector]
		if !ok {
			return seq_core.OnChainOutput{}, fmt.Errorf("stellar chain not found for selector %d", input.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("stellar deployer from chain: %w", err)
		}
		deps := stellardeps.FromDeployer(dep)

		for _, cvCfg := range input.CommitteeVerifiers {
			contractID, err := stellarCommitteeVerifierContractID(cvCfg.CommitteeVerifier)
			if err != nil {
				return seq_core.OnChainOutput{}, fmt.Errorf("chain %d: %w", input.ChainSelector, err)
			}

			remoteSelectors := make([]uint64, 0, len(cvCfg.RemoteChains))
			for remoteSelector := range cvCfg.RemoteChains {
				remoteSelectors = append(remoteSelectors, remoteSelector)
			}
			sort.Slice(remoteSelectors, func(i, j int) bool { return remoteSelectors[i] < remoteSelectors[j] })

			quorumConfigs := make([]cvbindings.SignatureQuorumConfig, 0, len(remoteSelectors))
			for _, remoteSelector := range remoteSelectors {
				sigCfg := cvCfg.RemoteChains[remoteSelector].SignatureConfig
				if len(sigCfg.Signers) == 0 {
					return seq_core.OnChainOutput{}, fmt.Errorf(
						"chain %d: no committee signers resolved for remote chain %d (expected topology signer addresses or JD signing keys)",
						input.ChainSelector, remoteSelector)
				}
				signers := make([][32]byte, 0, len(sigCfg.Signers))
				for _, s := range sigCfg.Signers {
					if !common.IsHexAddress(s) {
						return seq_core.OnChainOutput{}, fmt.Errorf("invalid signer address %q for source chain %d", s, remoteSelector)
					}
					// Stellar's committee_verifier stores 20-byte ETH-style signer identities
					// left-padded to 32 bytes.
					var padded [32]byte
					addr := common.HexToAddress(s)
					copy(padded[12:], addr[:])
					signers = append(signers, padded)
				}
				// The contract requires strictly ascending signer order (CCIPError 67/66).
				sort.Slice(signers, func(i, j int) bool { return bytes.Compare(signers[i][:], signers[j][:]) < 0 })
				quorumConfigs = append(quorumConfigs, cvbindings.SignatureQuorumConfig{
					SourceChainSelector: remoteSelector,
					Threshold:           uint32(sigCfg.Threshold),
					Signers:             signers,
				})
			}

			if _, err := execStellarCCIPOp(b, deps, cvops.ApplySignatureConfigs, cvops.ApplySignatureConfigsInput{
				ContractID:             contractID,
				RemoveSelectors:        []uint64{},
				SignatureQuorumConfigs: quorumConfigs,
			}); err != nil {
				return seq_core.OnChainOutput{}, fmt.Errorf("apply signature quorum configs on chain %d: %w", input.ChainSelector, err)
			}
		}

		return seq_core.OnChainOutput{}, nil
	},
)

// stellarCommitteeVerifierContractID extracts the CommitteeVerifier contract ref (skipping the
// versioned verifier resolver ref) and converts its 0x-hex datastore address to a strkey.
func stellarCommitteeVerifierContractID(refs []datastore.AddressRef) (string, error) {
	for _, ref := range refs {
		if ref.Type != datastore.ContractType(committee_verifier.ContractType) {
			continue
		}
		contractID, err := scval.HexToContractStrkey(ref.Address)
		if err != nil {
			return "", fmt.Errorf("convert committee verifier address %s to strkey: %w", ref.Address, err)
		}
		return contractID, nil
	}
	return "", fmt.Errorf("no CommitteeVerifier contract ref in %d resolved refs", len(refs))
}
