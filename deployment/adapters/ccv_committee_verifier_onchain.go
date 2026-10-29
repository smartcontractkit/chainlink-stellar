package adapters

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	ccvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
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

	// No enumeration entrypoint exists for signature configs, so probe every
	// other chain in the environment; a config for an absent chain is invisible.
	candidates := candidateSourceSelectors(env, chainSelector)

	states := make([]*ccvdeploymentadapters.CommitteeState, 0, len(refs))
	for _, ref := range refs {
		contractID, err := ownership.NormalizeContractAddress(ref.Address)
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

// committeeVerifierWrite is the env-free input of the write cores: chain I/O,
// the deployer authorizer, and the target verifier ref as a contract strkey.
type committeeVerifierWrite struct {
	deps      stellardeps.StellarDeps
	deployer  string
	ref       datastore.AddressRef
	chainRefs []datastore.AddressRef
	readOwner func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error)
}

// mustPropose routes by owner: deployer means direct exec, any deployed
// RBACTimelock means a proposal. Custom MCMS qualifiers exist, so all refs count.
// The caller's qualifier is invisible here, so a different executing timelock
// fails on-chain with an auth error, as on EVM.
func (w committeeVerifierWrite) mustPropose(ctx context.Context) (bool, error) {
	owner, err := w.readOwner(ctx, w.deps, w.ref)
	if err != nil {
		return false, fmt.Errorf("read owner of committee verifier %s: %w", w.ref.Address, err)
	}
	if owner == w.deployer {
		return false, nil
	}
	for _, ref := range w.chainRefs {
		if ref.Type != datastore.ContractType(cciputils.RBACTimelock) || !ref.Version.Equal(deploy.MCMSVersion) {
			continue
		}
		addr, err := ownership.NormalizeContractAddress(ref.Address)
		if err != nil {
			return false, fmt.Errorf("normalize RBACTimelock %s on chain %d: %w", ref.Address, ref.ChainSelector, err)
		}
		if addr == owner {
			return true, nil
		}
	}
	return false, fmt.Errorf(
		"committee verifier %s (chain %d) owner %q is neither the deployer %q nor a deployed RBACTimelock; no operation emitted",
		w.ref.Address, w.ref.ChainSelector, owner, w.deployer)
}

// directClient binds the verifier client used for direct execution.
func (w committeeVerifierWrite) directClient() *ccvbindings.CommitteeVerifierClient {
	return ccvbindings.NewCommitteeVerifierClient(w.deps.Invoker, w.ref.Address)
}

// proposalOp returns fn and its args as a one-transaction MCMS batch operation.
func (w committeeVerifierWrite) proposalOp(fn string, args []xdr.ScVal) ([]mcmstypes.BatchOperation, error) {
	data, err := mcmsutil.EncodeSorobanMCMSInvokePayload(fn, args)
	if err != nil {
		return nil, fmt.Errorf("encode %s payload: %w", fn, err)
	}
	return []mcmstypes.BatchOperation{mcmsutil.MCMSInvokeTx(w.ref, data)}, nil
}

func applySignatureConfigsCore(
	ctx context.Context,
	w committeeVerifierWrite,
	change ccvdeploymentadapters.SignatureConfigChange,
) ([]mcmstypes.BatchOperation, error) {
	configs, err := signatureQuorumConfigs(change.NewConfigs)
	if err != nil {
		return nil, err
	}

	propose, err := w.mustPropose(ctx)
	if err != nil {
		return nil, err
	}
	if !propose {
		if err := w.directClient().ApplySignatureConfigs(ctx, change.RemovedSourceChainSelectors, configs); err != nil {
			return nil, fmt.Errorf("apply_signature_configs on chain %d: %w", w.ref.ChainSelector, err)
		}
		return nil, nil
	}

	return w.proposalOp("apply_signature_configs", []xdr.ScVal{
		scval.Uint64SliceToScVal(change.RemovedSourceChainSelectors),
		scval.StructSliceToScVal(configs),
	})
}

func setAllowedFinalityConfigCore(
	ctx context.Context,
	w committeeVerifierWrite,
	waitForSafe bool,
	blockDepth uint16,
) ([]mcmstypes.BatchOperation, error) {
	var allowedFinality uint32
	if waitForSafe {
		allowedFinality |= finalitySafeFlagBit
	}
	allowedFinality |= uint32(blockDepth)

	propose, err := w.mustPropose(ctx)
	if err != nil {
		return nil, err
	}
	if !propose {
		if err := w.directClient().SetAllowedFinalityConfig(ctx, allowedFinality); err != nil {
			return nil, fmt.Errorf("set_allowed_finality_config on chain %d: %w", w.ref.ChainSelector, err)
		}
		return nil, nil
	}

	return w.proposalOp("set_allowed_finality_config", []xdr.ScVal{scval.Uint32ToScVal(allowedFinality)})
}

func applyAllowlistUpdatesCore(
	ctx context.Context,
	w committeeVerifierWrite,
	destChainSelector uint64,
	allowlistEnabled bool,
	addedSenders []string,
	removedSenders []string,
) ([]mcmstypes.BatchOperation, error) {
	updates := []ccvbindings.AllowListUpdate{{
		DestChainSelector:         destChainSelector,
		AllowlistEnabled:          allowlistEnabled,
		AddedAllowlistedSenders:   addedSenders,
		RemovedAllowlistedSenders: removedSenders,
	}}

	propose, err := w.mustPropose(ctx)
	if err != nil {
		return nil, err
	}
	if !propose {
		if err := w.directClient().ApplyAllowlistUpdates(ctx, updates); err != nil {
			return nil, fmt.Errorf("apply_allowlist_updates on chain %d: %w", w.ref.ChainSelector, err)
		}
		return nil, nil
	}

	return w.proposalOp("apply_allowlist_updates", []xdr.ScVal{scval.StructSliceToScVal(updates)})
}

// committeeVerifierWriteTarget resolves the single CommitteeVerifier for
// (chain, qualifier) and gathers the chain's refs for timelock routing.
func committeeVerifierWriteTarget(
	env deployment.Environment,
	chainSelector uint64,
	qualifier string,
) (committeeVerifierWrite, error) {
	refs := env.DataStore.Addresses().Filter(
		datastore.AddressRefByType(datastore.ContractType(committee_verifier.ContractType)),
		datastore.AddressRefByChainSelector(chainSelector),
		datastore.AddressRefByQualifier(qualifier),
	)
	if len(refs) == 0 {
		return committeeVerifierWrite{}, fmt.Errorf("no CommitteeVerifier found for chain %d qualifier %q", chainSelector, qualifier)
	}
	if len(refs) > 1 {
		return committeeVerifierWrite{}, fmt.Errorf("multiple CommitteeVerifiers found for chain %d qualifier %q", chainSelector, qualifier)
	}

	chain, ok := env.BlockChains.StellarChains()[chainSelector]
	if !ok {
		return committeeVerifierWrite{}, fmt.Errorf("Stellar chain %d not found in environment", chainSelector)
	}
	if chain.Signer == nil {
		return committeeVerifierWrite{}, fmt.Errorf("Stellar chain %d has no signer configured", chainSelector)
	}

	ref := refs[0]
	contractID, err := ownership.NormalizeContractAddress(ref.Address)
	if err != nil {
		return committeeVerifierWrite{}, fmt.Errorf("convert address %s to Stellar contract ID: %w", ref.Address, err)
	}
	ref.Address = contractID

	deployer := stellardeployment.NewDeployerWithSigner(
		chain.Client, chain.NetworkPassphrase, stellardeployment.NewSDKSigner(chain.Signer))

	return committeeVerifierWrite{
		deps:      stellardeps.FromDeployer(deployer),
		deployer:  deployer.SignerAddress(),
		ref:       ref,
		chainRefs: env.DataStore.Addresses().Filter(datastore.AddressRefByChainSelector(chainSelector)),
		readOwner: ownership.ContractOwner,
	}, nil
}

// signatureQuorumConfigs pads signers to 32 bytes and sorts strictly ascending,
// which the contract requires (CCIPError 67/66).
func signatureQuorumConfigs(configs []ccvdeploymentadapters.SignatureConfig) ([]ccvbindings.SignatureQuorumConfig, error) {
	out := make([]ccvbindings.SignatureQuorumConfig, 0, len(configs))
	for _, c := range configs {
		signers := make([][32]byte, 0, len(c.Signers))
		for _, s := range c.Signers {
			if !common.IsHexAddress(s) {
				return nil, fmt.Errorf("invalid signer address %q for source chain %d", s, c.SourceChainSelector)
			}
			var padded [32]byte
			copy(padded[12:], common.HexToAddress(s).Bytes())
			signers = append(signers, padded)
		}
		sort.Slice(signers, func(i, j int) bool { return bytes.Compare(signers[i][:], signers[j][:]) < 0 })
		out = append(out, ccvbindings.SignatureQuorumConfig{
			SourceChainSelector: c.SourceChainSelector,
			Threshold:           uint32(c.Threshold),
			Signers:             signers,
		})
	}
	return out, nil
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

// isSourceSignersNotConfigured matches the contract's SourceSignersNotConfigured
// (code 19). It must stay narrow: swallowing an RPC error here would report
// committee drift as agreement in ScanCommitteeStates.
func isSourceSignersNotConfigured(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), sourceSignersNotConfiguredCode)
}

// sourceSignersNotConfiguredCode is how the Soroban host renders contract error 19 in the
// simulation diagnostics the RPC client surfaces.
const sourceSignersNotConfiguredCode = "Error(Contract, #19)"

// SetAllowedFinalityConfig sets the verifier-global allowed-finality config
// (EVM BaseVerifier.setAllowedFinalityConfig parity). The safe flag is bit 16 and
// the block depth the lower 16 bits; waitForFinality contributes no bits.
func (a *StellarCCVCommitteeVerifierOnchainAdapter) SetAllowedFinalityConfig(
	ctx context.Context,
	env deployment.Environment,
	chainSelector uint64,
	qualifier string,
	waitForFinality bool,
	waitForSafe bool,
	blockDepth uint16,
) ([]mcmstypes.BatchOperation, error) {
	_ = waitForFinality // contributes no bits; see the comment above.
	w, err := committeeVerifierWriteTarget(env, chainSelector, qualifier)
	if err != nil {
		return nil, err
	}
	return setAllowedFinalityConfigCore(ctx, w, waitForSafe, blockDepth)
}

// finalitySafeFlagBit is the FinalityCodec.sol wait-for-safe flag (bit 16), the only
// flag bit the raw allowed-finality encoding carries today.
const finalitySafeFlagBit uint32 = 1 << 16

// ApplyAllowlistUpdates updates the per-destination-chain sender allowlist.
// Senders are Stellar strkeys; a hex string would mint a well-formed but wrong
// contract ID, so validate before building the update.
func (a *StellarCCVCommitteeVerifierOnchainAdapter) ApplyAllowlistUpdates(
	ctx context.Context,
	env deployment.Environment,
	chainSelector uint64,
	qualifier string,
	destChainSelector uint64,
	allowlistEnabled bool,
	addedSenders []string,
	removedSenders []string,
) ([]mcmstypes.BatchOperation, error) {
	if err := validateStellarSenders(addedSenders, "added"); err != nil {
		return nil, err
	}
	if err := validateStellarSenders(removedSenders, "removed"); err != nil {
		return nil, err
	}

	w, err := committeeVerifierWriteTarget(env, chainSelector, qualifier)
	if err != nil {
		return nil, err
	}
	return applyAllowlistUpdatesCore(ctx, w, destChainSelector, allowlistEnabled, addedSenders, removedSenders)
}

// ApplySignatureConfigs applies the given change to the CommitteeVerifier at
// destChainSelector for the given committee qualifier.
func (a *StellarCCVCommitteeVerifierOnchainAdapter) ApplySignatureConfigs(
	ctx context.Context,
	env deployment.Environment,
	destChainSelector uint64,
	qualifier string,
	change ccvdeploymentadapters.SignatureConfigChange,
) ([]mcmstypes.BatchOperation, error) {
	w, err := committeeVerifierWriteTarget(env, destChainSelector, qualifier)
	if err != nil {
		return nil, err
	}
	return applySignatureConfigsCore(ctx, w, change)
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
