package sequences

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/stellar/go-stellar-sdk/strkey"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	executorbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/executor"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	executorops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/executor"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	offrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// ErrBaseExecutionGasCostLowered guards the OnRamp BaseExecutionGasCost: the
// family default is one flat value, so a lane re-run would otherwise reset a
// destination whose gas cost was raised by hand and break execution there.
var ErrBaseExecutionGasCostLowered = errors.New("baseExecutionGasCost would be lowered")

// ErrZeroAddressNotAllowed rejects zero-valued source OnRamp entries: the
// OffRamp matches an incoming message by hashing the onramp bytes it carries,
// so a zero entry can never match a real source and would leave the lane
// enabled but unusable. The Soroban OffRamp would accept it silently, so the
// guard lives here (EVM ZeroAddressNotAllowed ABI parity).
var ErrZeroAddressNotAllowed = errors.New("zero address not allowed")

// StellarConfigureChainForLanes is the Stellar leg of the shared
// ConfigureChainsForLanesFromTopology changeset. Per remote chain it wires the
// full lane, EVM-sequence style: OffRamp source chain config, OnRamp dest
// chain config, Executor dest chain config, FeeQuoter dest chain config, and
// Router ramp entries. Every write is guarded by a read of the current
// on-chain state, so re-runs are idempotent: zero/empty/nil input values keep
// the current on-chain value, and a remote whose desired state already
// matches emits no write at all. Writes are ordered infra-first
// (OffRamp → OnRamp → Executor → FeeQuoter) with the Router write last, so
// the Router never routes to a contract that is not yet configured.
//
// Guards, EVM-parity wording where one exists:
//   - AllowOnrampOverride=false refuses to replace a Router onRamp entry that
//     already points at a different OnRamp (use a migration to swap OnRamps).
//   - AllowLoweringBaseExecutionGasCost=false refuses to lower the OnRamp
//     BaseExecutionGasCost below its current on-chain value (ErrBaseExecutionGasCostLowered).
//   - A remote whose SkipExecutorConfig is set gets the no-execution sentinel
//     (stellarutil.NoExecutionExecutorStrkey, = Rust NO_EXECUTION_STRKEY) as
//     the OnRamp DefaultExecutor and no Executor write at all — EVM
//     NoExecutionAddress parity.
//   - FeeQuoter configs already enabled on chain are left alone unless the
//     input's OverrideExistingConfig is set, protecting deploy-time pricing.
//   - Source OnRamp entries that are empty or all-zero are rejected
//     (ErrZeroAddressNotAllowed) instead of being written verbatim.
//
// Deliberately NOT done here:
//   - FeeQuoter gas/token price updates (UpdatePrices): deploy seeds them and
//     the offchain price updater owns them afterwards (Solana precedent).
//   - CommitteeVerifier remote chain configs/allowlists/resolver routing:
//     coalesced defaults here would clobber deploy-time values. The committee
//     verifier writes this sequence does make are the signature quorums
//     (applyCommitteeVerifierSignatureQuorums) and the verifier-global
//     allowed-finality config (applyCommitteeVerifierAllowedFinality, EVM
//     ConfigureChainForLanes parity).
//   - RampRegistry sync: not reachable from ConfigureChainForLanesInput; would
//     need a FamilyExtras change in chainlink-ccip (noted as a follow-up).
//
// Committee verifier signature quorums run at lane-configuration time, not
// deploy time, on purpose: since the chainlink-ccv 2026-06-26 signing-key-sync
// cutover, topology enrichment skips families whose bootstrappers push signing
// keys to JD on connect, so signer addresses are not available to
// RunStellarCCIPFullDeploy. The changeset resolves them from JD (falling back
// to topology signer addresses) and passes them in via
// CommitteeVerifierRemoteChainConfig.SignatureConfig.
//
// Everything executes as plain EOA transactions; Soroban has no MCMS, so the
// output carries no BatchOps.
var StellarConfigureChainForLanes = cldf_ops.NewSequence(
	"stellar-configure-chain-for-lanes",
	SequenceVersion,
	"Applies full lane wiring (OffRamp, OnRamp, Executor, FeeQuoter, Router) and committee verifier signature quorums on Stellar for each remote lane",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input ccvadapters.ConfigureChainForLanesInput) (seq_core.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[input.ChainSelector]
		if !ok {
			return seq_core.OnChainOutput{}, fmt.Errorf("stellar chain not found for selector %d", input.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("stellar deployer from chain: %w", err)
		}
		return runStellarConfigureChainForLanes(b, stellardeps.FromDeployer(dep), input)
	},
)

// runStellarConfigureChainForLanes is the sequence body, extracted so tests
// can drive it with a recording invoker instead of a live chain.
func runStellarConfigureChainForLanes(b cldf_ops.Bundle, deps stellardeps.StellarDeps, input ccvadapters.ConfigureChainForLanesInput) (seq_core.OnChainOutput, error) {
	// Local contract IDs arrive as raw 32-byte contract IDs (the adapter's
	// toStellarAddressBytes); the operations take strkey C… IDs.
	routerID, err := scval.BytesToContractStrkey(input.Router)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("router address: %w", err)
	}
	onRampID, err := scval.BytesToContractStrkey(input.OnRamp)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("onramp address: %w", err)
	}
	offRampID, err := scval.BytesToContractStrkey(input.OffRamp)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("offramp address: %w", err)
	}
	feeQuoterID, err := scval.BytesToContractStrkey(input.FeeQuoter)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("fee quoter address: %w", err)
	}

	remoteSelectors := make([]uint64, 0, len(input.RemoteChains))
	for remoteSelector := range input.RemoteChains {
		remoteSelectors = append(remoteSelectors, remoteSelector)
	}
	sort.Slice(remoteSelectors, func(i, j int) bool { return remoteSelectors[i] < remoteSelectors[j] })

	// ── Phase 0: Router onRamp guard ─────────────────────────────────────────
	// Reading the current onRamp entry per remote both enforces the guard and
	// feeds the Router adds below. Replacing an onramp mapping (e.g. test router
	// to prod router promotion) is a migration, not a lane re-run.
	currentOnRampByRemote := make(map[uint64]*string, len(remoteSelectors))
	for _, remoteSelector := range remoteSelectors {
		current, err := execStellarCCIPOp(b, deps, routerops.GetOnramp, routerops.GetOnrampInput{
			ContractID:        routerID,
			DestChainSelector: remoteSelector,
		})
		if err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf(
				"failed to read onramp for dest %d from Router(%s): %w", remoteSelector, routerID, err)
		}
		if current != nil && *current != onRampID && !input.AllowOnrampOverride {
			return seq_core.OnChainOutput{}, fmt.Errorf(
				"router %s already has onramp %s for dest chain %d; "+
					"refusing to overwrite with %s (AllowOnrampOverride is false) -- "+
					"use the migration changeset to update router mappings",
				routerID, *current, remoteSelector, onRampID)
		}
		currentOnRampByRemote[remoteSelector] = current
	}

	// ── Phase 1: collect desired state per remote chain ──────────────────────
	// The "maybe" helpers read on-chain state and only append when a diff
	// exists (idempotency), so no-op remotes emit no transactions.
	offRampArgs := make([]offrampbindings.SourceChainConfigArgs, 0, len(remoteSelectors))
	onRampArgs := make([]onrampbindings.DestChainConfigArgs, 0, len(remoteSelectors))
	fqArgs := make([]fqbindings.DestChainConfigArgs, 0, len(remoteSelectors))
	onRampAdds := make([]routerbindings.OnRampEntry, 0, len(remoteSelectors))
	offRampAdds := make([]routerbindings.OffRampEntry, 0, len(remoteSelectors))
	executorArgsByContract := make(map[string][]executorbindings.RemoteChainConfigArgs)

	for _, remoteSelector := range remoteSelectors {
		remoteConfig := input.RemoteChains[remoteSelector]

		// OffRamp: tells the local OffRamp which source chains to accept messages from.
		offRampArgs, err = maybeOffRampSourceChainConfigArg(b, deps, input, remoteSelector, remoteConfig, routerID, offRampID, offRampArgs)
		if err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("remote chain %d: %w", remoteSelector, err)
		}

		// OnRamp: tells the local OnRamp how to send messages to this remote chain.
		onRampArgs, err = maybeOnRampDestChainConfigArg(b, deps, input, remoteSelector, remoteConfig, routerID, onRampID, onRampArgs)
		if err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("remote chain %d: %w", remoteSelector, err)
		}

		// Executor: no proxy indirection on Stellar, so the resolved contract ID
		// is the contract to configure; dest chains group per executor ID.
		// SkipExecutorConfig means the destination executes manually — nothing
		// to read or write here, and the OnRamp gets the no-execution sentinel.
		if !remoteConfig.SkipExecutorConfig {
			executorID, err := localContractStrkey(remoteConfig.DefaultExecutor, "default executor")
			if err != nil {
				return seq_core.OnChainOutput{}, fmt.Errorf("remote chain %d: %w", remoteSelector, err)
			}
			desired := executorbindings.RemoteChainConfig{
				Enabled:     remoteConfig.ExecutorDestChainConfig.Enabled,
				UsdCentsFee: uint32(remoteConfig.ExecutorDestChainConfig.USDCentsFee),
			}
			current, err := execStellarCCIPOp(b, deps, executorops.GetDestChainConfig, executorops.GetDestChainConfigInput{
				ContractID:        executorID,
				DestChainSelector: remoteSelector,
			})
			if err != nil {
				return seq_core.OnChainOutput{}, fmt.Errorf(
					"failed to get dest chain config for selector %d from Executor(%s): %w", remoteSelector, executorID, err)
			}
			if current == nil || *current != desired {
				executorArgsByContract[executorID] = append(executorArgsByContract[executorID], executorbindings.RemoteChainConfigArgs{
					DestChainSelector: remoteSelector,
					Config:            desired,
				})
			}
		}

		// FeeQuoter dest chain config: when OverrideExistingConfig is false, an
		// already-enabled config is left alone to protect deploy-time parameters.
		fqArgs, err = maybeFeeQuoterDestChainConfigArg(b, deps, feeQuoterID, remoteSelector, remoteConfig, fqArgs)
		if err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("remote chain %d: %w", remoteSelector, err)
		}

		// Router onRamp: only add if the router doesn't already point to our
		// OnRamp for this destination (cached from Phase 0).
		if current := currentOnRampByRemote[remoteSelector]; current == nil || *current != onRampID {
			onRampAdds = append(onRampAdds, routerbindings.OnRampEntry{
				DestChainSelector: remoteSelector,
				Onramp:            onRampID,
			})
		}
		// Router offRamp: always collected — duplicates are filtered in bulk
		// below via one read of the whole offramp table.
		offRampAdds = append(offRampAdds, routerbindings.OffRampEntry{
			SourceChainSelector: remoteSelector,
			Offramp:             offRampID,
		})
	}

	// ── Phase 2: bulk-filter already-configured router entries ────────────────
	existingOffRamps, err := execStellarCCIPOp(b, deps, routerops.GetOfframps, routerops.GetOfframpsInput{
		ContractID: routerID,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("failed to read offramp table from Router(%s): %w", routerID, err)
	}
	existingOffRampBySource := make(map[uint64]string, len(existingOffRamps))
	for _, entry := range existingOffRamps {
		existingOffRampBySource[entry.SourceChainSelector] = entry.Offramp
	}
	filteredOffRampAdds := make([]routerbindings.OffRampEntry, 0, len(offRampAdds))
	for _, add := range offRampAdds {
		if existingOffRampBySource[add.SourceChainSelector] == add.Offramp {
			continue
		}
		filteredOffRampAdds = append(filteredOffRampAdds, add)
	}
	offRampAdds = filteredOffRampAdds

	// ── Phase 3: apply writes, Router last ────────────────────────────────────
	if len(offRampArgs) > 0 {
		if _, err := execStellarCCIPOp(b, deps, offrampops.ApplySourceChainCfgUpdates, offrampops.ApplySourceChainCfgUpdatesInput{
			ContractID: offRampID,
			Updates:    offRampArgs,
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("failed to apply source chain config updates to OffRamp(%s): %w", offRampID, err)
		}
	}

	if len(onRampArgs) > 0 {
		if _, err := execStellarCCIPOp(b, deps, onrampops.ApplyDestChainConfigUpdates, onrampops.ApplyDestChainConfigUpdatesInput{
			ContractID: onRampID,
			Updates:    onRampArgs,
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("failed to apply dest chain config updates to OnRamp(%s): %w", onRampID, err)
		}
	}

	executorContracts := make([]string, 0, len(executorArgsByContract))
	for executorID := range executorArgsByContract {
		executorContracts = append(executorContracts, executorID)
	}
	sort.Strings(executorContracts)
	for _, executorID := range executorContracts {
		if _, err := execStellarCCIPOp(b, deps, executorops.ApplyDestChainUpdates, executorops.ApplyDestChainUpdatesInput{
			ContractID: executorID,
			ToRemove:   []uint64{},
			ToAdd:      executorArgsByContract[executorID],
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("failed to apply dest chain updates to Executor(%s): %w", executorID, err)
		}
	}

	if len(fqArgs) > 0 {
		if _, err := execStellarCCIPOp(b, deps, fqops.ApplyDestChainConfigs, fqops.ApplyDestChainConfigsInput{
			ContractID: feeQuoterID,
			Configs:    fqArgs,
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("failed to apply dest chain configs to FeeQuoter(%s): %w", feeQuoterID, err)
		}
	}

	// Committee verifier signature quorums (blind writes, per the JD-fallback
	// comment on StellarConfigureChainForLanes).
	if err := applyCommitteeVerifierSignatureQuorums(b, deps, input); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	// Committee verifier allowed-finality config (EVM ConfigureChainForLanes
	// parity: the shared changeset populates each family's default via
	// ChainFamilyAdapter.GetDefaultFinalityConfig; without this write the
	// verifier stays at the deployed wait-for-finality-only default and
	// rejects depth/safe-flag `get_fee` requests with InvalidRequestedFinality).
	if err := applyCommitteeVerifierAllowedFinality(b, deps, input); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	if len(onRampAdds) > 0 || len(offRampAdds) > 0 {
		if _, err := execStellarCCIPOp(b, deps, routerops.ApplyRampUpdates, routerops.ApplyRampUpdatesInput{
			ContractID:     routerID,
			OnRampUpdates:  onRampAdds,
			OffRampRemoves: []routerbindings.OffRampEntry{},
			OffRampAdds:    offRampAdds,
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("failed to apply ramp updates to Router(%s): %w", routerID, err)
		}
	}

	// EOA writes only — Soroban has no MCMS proposals to emit.
	return seq_core.OnChainOutput{}, nil
}

// applyCommitteeVerifierSignatureQuorums applies per-remote signature quorum
// configs on the chain's committee verifiers. Each remote's current quorum is
// read first and only written when it differs (EVM parity with
// configureCommitteeVerifierAsDest); an empty SignatureConfig (no signers and
// zero threshold) means the caller is not managing the quorum through this
// sequence, so the remote is skipped rather than clobbered.
func applyCommitteeVerifierSignatureQuorums(b cldf_ops.Bundle, deps stellardeps.StellarDeps, input ccvadapters.ConfigureChainForLanesInput) error {
	for _, cvCfg := range input.CommitteeVerifiers {
		contractID, err := stellarCommitteeVerifierContractID(cvCfg.CommitteeVerifier)
		if err != nil {
			return fmt.Errorf("chain %d: %w", input.ChainSelector, err)
		}

		remoteSelectors := make([]uint64, 0, len(cvCfg.RemoteChains))
		for remoteSelector := range cvCfg.RemoteChains {
			remoteSelectors = append(remoteSelectors, remoteSelector)
		}
		sort.Slice(remoteSelectors, func(i, j int) bool { return remoteSelectors[i] < remoteSelectors[j] })

		quorumConfigs := make([]cvbindings.SignatureQuorumConfig, 0, len(remoteSelectors))
		for _, remoteSelector := range remoteSelectors {
			sigCfg := cvCfg.RemoteChains[remoteSelector].SignatureConfig
			if len(sigCfg.Signers) == 0 && sigCfg.Threshold == 0 {
				continue
			}
			if len(sigCfg.Signers) == 0 {
				return fmt.Errorf(
					"chain %d: no committee signers resolved for remote chain %d (expected topology signer addresses or JD signing keys)",
					input.ChainSelector, remoteSelector)
			}
			signers := make([][32]byte, 0, len(sigCfg.Signers))
			for _, s := range sigCfg.Signers {
				if !common.IsHexAddress(s) {
					return fmt.Errorf("invalid signer address %q for source chain %d", s, remoteSelector)
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
			desired := cvbindings.SignatureQuorumConfig{
				SourceChainSelector: remoteSelector,
				Threshold:           uint32(sigCfg.Threshold),
				Signers:             signers,
			}

			current, err := execStellarCCIPOp(b, deps, cvops.GetSignatureConfig, cvops.GetSignatureConfigInput{
				ContractID:          contractID,
				SourceChainSelector: remoteSelector,
			})
			if err != nil {
				return fmt.Errorf("get signature quorum config on chain %d remote %d: %w", input.ChainSelector, remoteSelector, err)
			}
			if current != nil &&
				current.Threshold == desired.Threshold &&
				unorderedSignersEqual(current.Signers, desired.Signers) {
				continue
			}
			quorumConfigs = append(quorumConfigs, desired)
		}

		if len(quorumConfigs) == 0 {
			continue
		}
		if _, err := execStellarCCIPOp(b, deps, cvops.ApplySignatureConfigs, cvops.ApplySignatureConfigsInput{
			ContractID:             contractID,
			RemoveSelectors:        []uint64{},
			SignatureQuorumConfigs: quorumConfigs,
		}); err != nil {
			return fmt.Errorf("apply signature quorum configs on chain %d: %w", input.ChainSelector, err)
		}
	}
	return nil
}

// applyCommitteeVerifierAllowedFinality sets the verifier-global allowed-finality
// config on each committee verifier (EVM `configure_chain_for_lanes.go` parity:
// read current, write only when different, skip a zero input config so a caller
// not managing finality through this sequence never clobbers an on-chain value).
func applyCommitteeVerifierAllowedFinality(b cldf_ops.Bundle, deps stellardeps.StellarDeps, input ccvadapters.ConfigureChainForLanesInput) error {
	for _, cvCfg := range input.CommitteeVerifiers {
		if cvCfg.AllowedFinalityConfig.IsZero() {
			continue
		}
		contractID, err := stellarCommitteeVerifierContractID(cvCfg.CommitteeVerifier)
		if err != nil {
			return fmt.Errorf("chain %d: %w", input.ChainSelector, err)
		}

		desiredRaw := cvCfg.AllowedFinalityConfig.Raw()
		desired := binary.BigEndian.Uint32(desiredRaw[:])
		current, err := execStellarCCIPOp(b, deps, cvops.GetAllowedFinalityConfig, cvops.GetAllowedFinalityConfigInput{
			ContractID: contractID,
		})
		if err != nil {
			return fmt.Errorf("get allowed finality config on chain %d: %w", input.ChainSelector, err)
		}
		if current == desired {
			continue
		}
		if _, err := execStellarCCIPOp(b, deps, cvops.SetAllowedFinalityConfig, cvops.SetAllowedFinalityConfigInput{
			ContractID:            contractID,
			AllowedFinalityConfig: desired,
		}); err != nil {
			return fmt.Errorf("set allowed finality config on chain %d: %w", input.ChainSelector, err)
		}
	}
	return nil
}

// unorderedSignersEqual compares two signer sets ignoring order, mirroring
// EVM's UnorderedSliceEqual for signature quorum diffs.
func unorderedSignersEqual(a, b [][32]byte) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[[32]byte]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// maybeOffRampSourceChainConfigArg reads the current OffRamp source chain
// config and appends to args only when the desired state differs. Zero/empty
// fields in the input are treated as "keep current" so callers only need to
// specify fields they want to change.
func maybeOffRampSourceChainConfigArg(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	input ccvadapters.ConfigureChainForLanesInput,
	remoteSelector uint64,
	remoteConfig ccvadapters.RemoteChainConfig[[]byte, string],
	routerID, offRampID string,
	args []offrampbindings.SourceChainConfigArgs,
) ([]offrampbindings.SourceChainConfigArgs, error) {
	// Source onramps are opaque here: the OffRamp matches an incoming message by
	// hashing the bytes it carries, and how those bytes are encoded is the source
	// family's business. They are stored verbatim; only zero entries are rejected.
	onRamps, err := nonZeroSourceOnRamps(remoteSelector, remoteConfig.OnRamps)
	if err != nil {
		return nil, err
	}
	defaultCCVs, err := localContractStrkeys(remoteConfig.DefaultInboundCCVs, "default inbound CCV")
	if err != nil {
		return nil, err
	}
	laneMandatedCCVs, err := localContractStrkeys(remoteConfig.LaneMandatedInboundCCVs, "lane-mandated inbound CCV")
	if err != nil {
		return nil, err
	}

	current, err := execStellarCCIPOp(b, deps, offrampops.GetSourceChainConfig, offrampops.GetSourceChainConfigInput{
		ContractID:          offRampID,
		SourceChainSelector: remoteSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get source chain config for selector %d from OffRamp(%s): %w", remoteSelector, offRampID, err)
	}
	var cur offrampbindings.SourceChainConfig
	if current != nil {
		cur = *current
	}

	isEnabled := cur.IsEnabled
	if remoteConfig.AllowTrafficFrom != nil {
		isEnabled = *remoteConfig.AllowTrafficFrom
	}

	desired := offrampbindings.SourceChainConfigArgs{
		Router:              routerID,
		SourceChainSelector: remoteSelector,
		IsEnabled:           isEnabled,
		OnRamps:             onRamps,
		DefaultCcvs:         defaultCCVs,
		LaneMandatedCcvs:    laneMandatedCCVs,
	}
	if len(desired.OnRamps) == 0 {
		desired.OnRamps = cur.OnRamps
	}
	if len(desired.DefaultCcvs) == 0 {
		desired.DefaultCcvs = cur.DefaultCcvs
	}
	if len(desired.LaneMandatedCcvs) == 0 {
		desired.LaneMandatedCcvs = cur.LaneMandatedCcvs
	}

	if cur.IsEnabled != desired.IsEnabled ||
		cur.Router != desired.Router ||
		!unorderedBytesSliceEqual(cur.OnRamps, desired.OnRamps) ||
		!unorderedStringSliceEqual(cur.DefaultCcvs, desired.DefaultCcvs) ||
		!unorderedStringSliceEqual(cur.LaneMandatedCcvs, desired.LaneMandatedCcvs) {
		args = append(args, desired)
	}
	return args, nil
}

// maybeOnRampDestChainConfigArg reads the current OnRamp dest chain config and
// appends to args only when the desired state differs. Same
// zero-means-keep-current semantics as the OffRamp helper.
func maybeOnRampDestChainConfigArg(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	input ccvadapters.ConfigureChainForLanesInput,
	remoteSelector uint64,
	remoteConfig ccvadapters.RemoteChainConfig[[]byte, string],
	routerID, onRampID string,
	args []onrampbindings.DestChainConfigArgs,
) ([]onrampbindings.DestChainConfigArgs, error) {
	defaultOutboundCCVs, err := localContractStrkeys(remoteConfig.DefaultOutboundCCVs, "default outbound CCV")
	if err != nil {
		return nil, err
	}
	laneMandatedOutboundCCVs, err := localContractStrkeys(remoteConfig.LaneMandatedOutboundCCVs, "lane-mandated outbound CCV")
	if err != nil {
		return nil, err
	}

	// When the destination executes messages manually (SkipExecutorConfig), the
	// executor reference is not resolved at all (EVM parity: ZeroAddress without
	// a lookup) — the sentinel tells the OnRamp this lane has no executor, and a
	// missing executor ref must not block a manual-execution lane.
	var defaultExecutor string
	if remoteConfig.SkipExecutorConfig {
		defaultExecutor = stellarutil.NoExecutionExecutorStrkey
	} else {
		defaultExecutor, err = localContractStrkey(remoteConfig.DefaultExecutor, "default executor")
		if err != nil {
			return nil, err
		}
	}

	desired := onrampbindings.DestChainConfigArgs{
		Router:                    routerID,
		DestChainSelector:         remoteSelector,
		AddressBytesLength:        uint32(remoteConfig.AddressBytesLength),
		BaseExecutionGasCost:      remoteConfig.BaseExecutionGasCost,
		MessageNetworkFeeUsdCents: uint32(remoteConfig.MessageNetworkFeeUSDCents),
		TokenNetworkFeeUsdCents:   uint32(remoteConfig.TokenNetworkFeeUSDCents),
		// ExecutionFeeUsdCents mirrors the executor's USDCentsFee so the OnRamp
		// quotes the same execution fee the Executor charges (stellar-specific
		// field; EVM carries this via the executor config itself).
		ExecutionFeeUsdCents: uint32(remoteConfig.ExecutorDestChainConfig.USDCentsFee),
		DefaultCcvs:          defaultOutboundCCVs,
		LaneMandatedCcvs:     laneMandatedOutboundCCVs,
		DefaultExecutor:      defaultExecutor,
		OffRamp:              remoteConfig.OffRamp,
	}

	current, err := execStellarCCIPOp(b, deps, onrampops.GetDestChainConfig, onrampops.GetDestChainConfigInput{
		ContractID:        onRampID,
		DestChainSelector: remoteSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get dest chain config for selector %d from OnRamp(%s): %w", remoteSelector, onRampID, err)
	}
	var cur onrampbindings.DestChainConfig
	if current != nil {
		cur = *current
	}

	if remoteConfig.TokenReceiverAllowed == nil {
		desired.TokenReceiverAllowed = cur.TokenReceiverAllowed
	} else {
		desired.TokenReceiverAllowed = *remoteConfig.TokenReceiverAllowed
	}
	if desired.MessageNetworkFeeUsdCents == 0 {
		desired.MessageNetworkFeeUsdCents = cur.MessageNetworkFeeUsdCents
	}
	if desired.TokenNetworkFeeUsdCents == 0 {
		desired.TokenNetworkFeeUsdCents = cur.TokenNetworkFeeUsdCents
	}
	if desired.ExecutionFeeUsdCents == 0 {
		desired.ExecutionFeeUsdCents = cur.ExecutionFeeUsdCents
	}
	if desired.BaseExecutionGasCost == 0 {
		desired.BaseExecutionGasCost = cur.BaseExecutionGasCost
	}
	// The family default is one flat value, so a lane re-run would otherwise
	// reset a destination whose gas cost was raised by hand and break execution
	// there.
	if desired.BaseExecutionGasCost < cur.BaseExecutionGasCost && !input.AllowLoweringBaseExecutionGasCost {
		return nil, fmt.Errorf(
			"OnRamp(%s) dest chain %d has baseExecutionGasCost %d, refusing to lower it to %d: %w",
			onRampID, remoteSelector, cur.BaseExecutionGasCost, desired.BaseExecutionGasCost,
			ErrBaseExecutionGasCostLowered,
		)
	}
	if desired.AddressBytesLength == 0 {
		desired.AddressBytesLength = cur.AddressBytesLength
	}
	if len(desired.DefaultCcvs) == 0 {
		desired.DefaultCcvs = cur.DefaultCcvs
	}
	if len(desired.LaneMandatedCcvs) == 0 {
		desired.LaneMandatedCcvs = cur.LaneMandatedCcvs
	}

	if onRampDestChainConfigDiffers(cur, desired) {
		args = append(args, desired)
	}
	return args, nil
}

// onRampDestChainConfigDiffers compares a read DestChainConfig against
// desired args, ignoring read-only fields (MessageNumber).
func onRampDestChainConfigDiffers(cur onrampbindings.DestChainConfig, desired onrampbindings.DestChainConfigArgs) bool {
	return cur.Router != desired.Router ||
		cur.AddressBytesLength != desired.AddressBytesLength ||
		cur.BaseExecutionGasCost != desired.BaseExecutionGasCost ||
		cur.MessageNetworkFeeUsdCents != desired.MessageNetworkFeeUsdCents ||
		cur.TokenNetworkFeeUsdCents != desired.TokenNetworkFeeUsdCents ||
		cur.ExecutionFeeUsdCents != desired.ExecutionFeeUsdCents ||
		cur.DefaultExecutor != desired.DefaultExecutor ||
		cur.TokenReceiverAllowed != desired.TokenReceiverAllowed ||
		!bytes.Equal(cur.OffRamp, desired.OffRamp) ||
		!unorderedStringSliceEqual(cur.DefaultCcvs, desired.DefaultCcvs) ||
		!unorderedStringSliceEqual(cur.LaneMandatedCcvs, desired.LaneMandatedCcvs)
}

// maybeFeeQuoterDestChainConfigArg compares the desired FeeQuoter dest chain
// config against the current on-chain state and appends to args only when
// they differ. Nil override fields are filled from on-chain state so partial
// updates are safe. When OverrideExistingConfig is false and an enabled config
// is already on chain, the remote is skipped to protect deploy-time pricing.
func maybeFeeQuoterDestChainConfigArg(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	feeQuoterID string,
	remoteSelector uint64,
	remoteConfig ccvadapters.RemoteChainConfig[[]byte, string],
	args []fqbindings.DestChainConfigArgs,
) ([]fqbindings.DestChainConfigArgs, error) {
	current, err := execStellarCCIPOp(b, deps, fqops.GetDestChainConfig, fqops.GetDestChainConfigInput{
		ContractID:        feeQuoterID,
		DestChainSelector: remoteSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get dest chain config for remote chain selector %d from FeeQuoter(%s): %w", remoteSelector, feeQuoterID, err)
	}
	if !remoteConfig.FeeQuoterDestChainConfig.OverrideExistingConfig && current != nil && current.IsEnabled {
		return args, nil
	}
	var cur fqbindings.DestChainConfig
	if current != nil {
		cur = *current
	}

	desired := fillFeeQuoterOverridesFromCurrent(remoteConfig.FeeQuoterDestChainConfig, cur)
	if feeQuoterDestChainConfigEqualTo(cur, desired) {
		return args, nil
	}
	return append(args, fqbindings.DestChainConfigArgs{
		DestChainSelector: remoteSelector,
		Config: fqbindings.DestChainConfig{
			IsEnabled:             *desired.IsEnabled,
			MaxDataBytes:          *desired.MaxDataBytes,
			MaxPerMsgGasLimit:     *desired.MaxPerMsgGasLimit,
			DestGasOverhead:       *desired.DestGasOverhead,
			DestGasPerPayloadByte: uint32(*desired.DestGasPerPayloadByteBase),
			DefaultTokenFeeUsd:    uint32(*desired.DefaultTokenFeeUSDCents),
			DefaultTokenDestGas:   *desired.DefaultTokenDestGasOverhead,
			DefaultTxGasLimit:     *desired.DefaultTxGasLimit,
			NetworkFeeUsdCents:    uint32(*desired.NetworkFeeUSDCents),
			LinkPremiumPercent:    uint32(*desired.LinkFeeMultiplierPercent),
		},
	}), nil
}

// fillFeeQuoterOverridesFromCurrent fills nil override fields from the current
// on-chain values so a partial update never zeroes an unset field.
// ChainFamilySelector has no Soroban FeeQuoter binding field, and
// USDPerUnitGas is deliberately unmapped (prices are owned by the offchain
// updater; see the sequence doc comment).
func fillFeeQuoterOverridesFromCurrent(
	desired ccvadapters.FeeQuoterDestChainConfigOverrides,
	cur fqbindings.DestChainConfig,
) ccvadapters.FeeQuoterDestChainConfigOverrides {
	if desired.IsEnabled == nil {
		v := cur.IsEnabled
		desired.IsEnabled = &v
	}
	if desired.MaxDataBytes == nil {
		v := cur.MaxDataBytes
		desired.MaxDataBytes = &v
	}
	if desired.MaxPerMsgGasLimit == nil {
		v := cur.MaxPerMsgGasLimit
		desired.MaxPerMsgGasLimit = &v
	}
	if desired.DestGasOverhead == nil {
		v := cur.DestGasOverhead
		desired.DestGasOverhead = &v
	}
	if desired.DestGasPerPayloadByteBase == nil {
		v := uint8(cur.DestGasPerPayloadByte)
		desired.DestGasPerPayloadByteBase = &v
	}
	if desired.DefaultTokenFeeUSDCents == nil {
		v := uint16(cur.DefaultTokenFeeUsd)
		desired.DefaultTokenFeeUSDCents = &v
	}
	if desired.DefaultTokenDestGasOverhead == nil {
		v := cur.DefaultTokenDestGas
		desired.DefaultTokenDestGasOverhead = &v
	}
	if desired.DefaultTxGasLimit == nil {
		v := cur.DefaultTxGasLimit
		desired.DefaultTxGasLimit = &v
	}
	if desired.NetworkFeeUSDCents == nil {
		v := uint16(cur.NetworkFeeUsdCents)
		desired.NetworkFeeUSDCents = &v
	}
	if desired.LinkFeeMultiplierPercent == nil {
		v := uint8(cur.LinkPremiumPercent)
		desired.LinkFeeMultiplierPercent = &v
	}
	return desired
}

func feeQuoterDestChainConfigEqualTo(cur fqbindings.DestChainConfig, desired ccvadapters.FeeQuoterDestChainConfigOverrides) bool {
	return cur.IsEnabled == *desired.IsEnabled &&
		cur.MaxDataBytes == *desired.MaxDataBytes &&
		cur.MaxPerMsgGasLimit == *desired.MaxPerMsgGasLimit &&
		cur.DestGasOverhead == *desired.DestGasOverhead &&
		cur.DestGasPerPayloadByte == uint32(*desired.DestGasPerPayloadByteBase) &&
		cur.DefaultTokenFeeUsd == uint32(*desired.DefaultTokenFeeUSDCents) &&
		cur.DefaultTokenDestGas == *desired.DefaultTokenDestGasOverhead &&
		cur.DefaultTxGasLimit == *desired.DefaultTxGasLimit &&
		cur.NetworkFeeUsdCents == uint32(*desired.NetworkFeeUSDCents) &&
		cur.LinkPremiumPercent == uint32(*desired.LinkFeeMultiplierPercent)
}

// nonZeroSourceOnRamps passes the source chain's onramp addresses through,
// rejecting empty and all-zero entries (ErrZeroAddressNotAllowed). Length and
// layout are deliberately not checked, since they belong to whichever family
// the source chain is.
func nonZeroSourceOnRamps(sourceChainSelector uint64, onRamps [][]byte) ([][]byte, error) {
	for i, onRamp := range onRamps {
		if len(onRamp) == 0 {
			return nil, fmt.Errorf("onRamps[%d] for source chain %d is empty: %w", i, sourceChainSelector, ErrZeroAddressNotAllowed)
		}
		allZero := true
		for _, b := range onRamp {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return nil, fmt.Errorf("onRamps[%d] for source chain %d is the zero address: %w", i, sourceChainSelector, ErrZeroAddressNotAllowed)
		}
	}
	return onRamps, nil
}

// localContractStrkey accepts a local contract reference in either 0x-hex
// (how datastore AddressRefs carry it) or strkey (C…) form, and returns the
// strkey the operations take. The changeset resolves local contracts through
// the adapter; both forms are tolerated so a resolver returning either one
// cannot break the other callers' paths.
func localContractStrkey(s, what string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty %s contract reference", what)
	}
	if strings.HasPrefix(s, "0x") {
		raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
		if err != nil {
			return "", fmt.Errorf("convert %s reference %q: %w", what, s, err)
		}
		if len(raw) != 32 {
			return "", fmt.Errorf("%s reference %q: expected 32-byte contract ID, got %d bytes", what, s, len(raw))
		}
		return scval.BytesToContractStrkey(raw)
	}
	raw, err := strkey.Decode(strkey.VersionByteContract, s)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("%s reference %q is neither 0x-hex nor a contract strkey", what, s)
	}
	return s, nil
}

func localContractStrkeys(refs []string, what string) ([]string, error) {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		id, err := localContractStrkey(ref, what)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func unorderedStringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, x := range a {
		counts[x]++
	}
	for _, y := range b {
		counts[y]--
		if counts[y] < 0 {
			return false
		}
	}
	return true
}

func unorderedBytesSliceEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, x := range a {
		counts[string(x)]++
	}
	for _, y := range b {
		counts[string(y)]--
		if counts[string(y)] < 0 {
			return false
		}
	}
	return true
}

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
