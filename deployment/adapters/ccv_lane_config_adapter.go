package adapters

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// defaultQualifier is the committee-verifier / executor qualifier resolved when
// the lane input omits one, matching the ccip topology changeset's
// resolveDefaultCCVs behavior.
const defaultQualifier = "default"

// StellarCCVLaneConfigAdapter implements ccvdeploymentadapters.LaneConfigAdapter
// for Stellar chains. It wires a single chain into one or more lanes by reusing
// the existing StellarConfigureChainForLanes sequence: per remote chain it
// configures the local OnRamp, OffRamp, FeeQuoter, Executor and Router, plus
// the committee verifiers' signature quorums and allowed-finality config.
//
// Committee-verifier signature quorums are set only when the caller supplies
// InboundSigners on the RemoteLaneConfig; when omitted they are left untouched,
// owned by the incremental committee changesets. Remote chain ramps are
// supplied pre-resolved on the RemoteLaneConfig (the lane changeset resolves
// them via the remote chain's own adapter). The underlying sequence is
// idempotent.
//
// One deliberate divergence from EVM's ccv LaneConfigAdapter: the remote-side
// defaults (FeeQuoter dest chain config, ChainFamilySelector,
// AddressBytesLength, RemoteChainDefaults) come from the remote chain
// family's adapter, matching the ccip topology changeset — the dest chain
// config describes the remote, so its family selector and address length are
// the remote family's. EVM's ccv adapter approximates them with its local
// family's defaults instead.
type StellarCCVLaneConfigAdapter struct {
	// remoteChainFamily is a seam over the remote chain family's adapter
	// lookup; nil uses the ccip ChainFamily registry. For unit tests.
	remoteChainFamily func(family string) (ccvadapters.ChainFamily, bool)
}

var _ ccvdeploymentadapters.LaneConfigAdapter = (*StellarCCVLaneConfigAdapter)(nil)

// laneChainFamily and laneCVContracts are stateless reusable resolvers and
// defaults providers from the stellar chain-family adapter layer.
var (
	laneChainFamily = &StellarChainFamilyAdapter{}
	laneCVContracts = &StellarCommitteeVerifierContractAdapter{}
)

var stellarConfigureLane = cldf_ops.NewSequence(
	"stellar-configure-lane",
	stellarops.ContractDeploymentVersion,
	"Resolves topology-free LaneConfigInput into the Stellar ConfigureChainForLanes input and executes it",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input ccvdeploymentadapters.LaneConfigInput) (ccvdeploymentadapters.LaneConfigOutput, error) {
		if _, ok := chains.StellarChains()[input.ChainSelector]; !ok {
			return ccvdeploymentadapters.LaneConfigOutput{},
				fmt.Errorf("stellar chain not found for selector %d", input.ChainSelector)
		}

		cfInput, err := (&StellarCCVLaneConfigAdapter{}).toConfigureChainForLanesInput(input)
		if err != nil {
			return ccvdeploymentadapters.LaneConfigOutput{},
				fmt.Errorf("failed to build stellar lane config input for chain %d: %w", input.ChainSelector, err)
		}

		report, err := cldf_ops.ExecuteSequence(b, laneChainFamily.ConfigureChainForLanes(), chains, cfInput)
		if err != nil {
			return ccvdeploymentadapters.LaneConfigOutput{},
				fmt.Errorf("stellar ConfigureChainForLanes failed for chain %d: %w", input.ChainSelector, err)
		}

		// The underlying sequence executes EOA-only, so its output carries no
		// batch ops; the marker keeps the direct-exec shape the shared
		// changesets expect and is dropped by their OutputBuilder. The Router
		// is the lane wiring's culminating contract, so it is the marker's
		// target.
		batchOps := report.Output.BatchOps
		if len(batchOps) == 0 {
			routerID, err := scval.BytesToContractStrkey(cfInput.Router)
			if err != nil {
				return ccvdeploymentadapters.LaneConfigOutput{}, fmt.Errorf("router address: %w", err)
			}
			batchOp, err := laneConfigDirectExecBatchOp(input.ChainSelector, routerID)
			if err != nil {
				return ccvdeploymentadapters.LaneConfigOutput{}, err
			}
			batchOps = []mcmstypes.BatchOperation{batchOp}
		}
		return ccvdeploymentadapters.LaneConfigOutput{
			Addresses: report.Output.Addresses,
			BatchOps:  batchOps,
		}, nil
	},
)

func (a *StellarCCVLaneConfigAdapter) ConfigureLane() *cldf_ops.Sequence[ccvdeploymentadapters.LaneConfigInput, ccvdeploymentadapters.LaneConfigOutput, cldf_chain.BlockChains] {
	return stellarConfigureLane
}

// GetOnRampAddress resolves the stellar OnRamp as the 40-byte Address::to_xdr
// envelope it writes into the messages it sends — the exact bytes a remote
// OffRamp whitelists by hashing (delegates to StellarChainFamilyAdapter).
func (a *StellarCCVLaneConfigAdapter) GetOnRampAddress(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return laneChainFamily.GetOnRampAddress(ds, chainSelector)
}

// GetOffRampAddress resolves the stellar OffRamp as the raw 32-byte contract
// ID — destination-side addresses travel unpadded (delegates to
// StellarChainFamilyAdapter).
func (a *StellarCCVLaneConfigAdapter) GetOffRampAddress(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return laneChainFamily.GetOffRampAddress(ds, chainSelector)
}

// laneConfigDirectExecBatchOp builds the ExecInfo-marked empty batch op that
// marks a direct-exec (deployer-key) lane configuration: the writes went out
// as EOA transactions and the shared OutputBuilder drops the empty op.
func laneConfigDirectExecBatchOp(chainSelector uint64, routerID string) (mcmstypes.BatchOperation, error) {
	op, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
		ChainSelector: chainSelector,
		Tx: mcmstypes.Transaction{
			OperationMetadata: mcmstypes.OperationMetadata{ContractType: routerops.ContractType},
			To:                routerID,
		},
		ExecInfo: &evmcontract.ExecInfo{Hash: "stellar-configure-lane-direct-exec"},
	}})
	if err != nil {
		return mcmstypes.BatchOperation{}, fmt.Errorf("build direct-exec batch operation for chain %d: %w", chainSelector, err)
	}
	return op, nil
}

// toConfigureChainForLanesInput resolves the local chain's contract addresses
// and per-remote lane settings from the topology-free input into the
// ConfigureChainForLanesInput the stellar sequence expects. FamilyExtras is
// not interpreted, mirroring EVM's ccv adapter (no stellar key convention
// exists; FamilyExtras rides the ccip topology changeset instead).
func (a *StellarCCVLaneConfigAdapter) toConfigureChainForLanesInput(
	input ccvdeploymentadapters.LaneConfigInput,
) (ccvadapters.ConfigureChainForLanesInput, error) {
	local := input.ChainSelector
	ds := laneLocalDataStore(input.ExistingAddresses)

	router, err := resolveLaneRouter(ds, local, input.UseTestRouter)
	if err != nil {
		return ccvadapters.ConfigureChainForLanesInput{}, err
	}
	onRamp, err := laneChainFamily.GetOnRampAddress(ds, local)
	if err != nil {
		return ccvadapters.ConfigureChainForLanesInput{}, fmt.Errorf("resolve local OnRamp: %w", err)
	}
	offRamp, err := laneChainFamily.GetOffRampAddress(ds, local)
	if err != nil {
		return ccvadapters.ConfigureChainForLanesInput{}, fmt.Errorf("resolve local OffRamp: %w", err)
	}
	feeQuoter, err := laneChainFamily.GetFQAddress(ds, local)
	if err != nil {
		return ccvadapters.ConfigureChainForLanesInput{}, fmt.Errorf("resolve local FeeQuoter: %w", err)
	}

	remoteChains := make(map[uint64]ccvadapters.RemoteChainConfig[[]byte, string], len(input.RemoteChains))
	// committeeVerifiers accumulates one config per referenced CCV qualifier so
	// the sequence can apply quorums and allowed-finality for each.
	committeeVerifiers := make(map[string]*ccvadapters.CommitteeVerifierConfig[datastore.AddressRef])

	for remoteSel, rlc := range input.RemoteChains {
		remoteCfg, err := a.resolveRemoteLaneConfig(ds, local, remoteSel, rlc)
		if err != nil {
			return ccvadapters.ConfigureChainForLanesInput{}, fmt.Errorf("remote chain %d: %w", remoteSel, err)
		}
		remoteChains[remoteSel] = remoteCfg

		// Optional signature quorum for inbound traffic from this remote, set
		// as part of lane expansion when the caller provides InboundSigners.
		if err := validateInboundSignatureQuorum(rlc.InboundSigners, rlc.InboundThreshold); err != nil {
			return ccvadapters.ConfigureChainForLanesInput{}, fmt.Errorf("remote chain %d: %w", remoteSel, err)
		}

		// Every committee verifier referenced inbound or outbound for this
		// remote must be configured; qualifiers default to "default" when
		// omitted (see defaultedCCVQualifiers).
		for _, qualifier := range dedupeQualifiers(defaultedCCVQualifiers(rlc.InboundCCVQualifiers), defaultedCCVQualifiers(rlc.OutboundCCVQualifiers)) {
			if err := addCommitteeVerifierRemote(committeeVerifiers, ds, local, remoteSel, qualifier, rlc.InboundSigners, rlc.InboundThreshold); err != nil {
				return ccvadapters.ConfigureChainForLanesInput{}, fmt.Errorf("remote chain %d: %w", remoteSel, err)
			}
		}
	}

	return ccvadapters.ConfigureChainForLanesInput{
		ChainSelector: local,
		// AllowOnrampOverride stays false: replacing an existing Router OnRamp
		// mapping is a migration, not a lane reconfiguration (EVM parity).
		AllowOnrampOverride: false,
		Router:              router,
		OnRamp:              onRamp,
		FeeQuoter:           feeQuoter,
		OffRamp:             offRamp,
		CommitteeVerifiers:  committeeVerifierConfigs(committeeVerifiers),
		RemoteChains:        remoteChains,
	}, nil
}

// resolveRemoteLaneConfig builds the stellar per-remote lane config, layering
// caller overrides from RemoteLaneConfig on top of the remote chain family
// adapter's defaults (see the adapter doc comment for the divergence from
// EVM's ccv adapter) and resolving the executor and committee-verifier
// (resolver) addresses by qualifier.
func (a *StellarCCVLaneConfigAdapter) resolveRemoteLaneConfig(
	ds datastore.DataStore,
	local, remote uint64,
	rlc ccvdeploymentadapters.RemoteLaneConfig,
) (ccvadapters.RemoteChainConfig[[]byte, string], error) {
	executorQualifier := rlc.ExecutorQualifier
	if executorQualifier == "" {
		executorQualifier = defaultQualifier
	}
	executorAddr, err := laneChainFamily.ResolveExecutor(ds, local, executorQualifier)
	if err != nil {
		return ccvadapters.RemoteChainConfig[[]byte, string]{}, fmt.Errorf("resolve executor (qualifier %q): %w", executorQualifier, err)
	}

	inboundCCVs, err := resolveResolverAddresses(ds, local, defaultedCCVQualifiers(rlc.InboundCCVQualifiers))
	if err != nil {
		return ccvadapters.RemoteChainConfig[[]byte, string]{}, fmt.Errorf("resolve inbound CCVs: %w", err)
	}
	outboundCCVs, err := resolveResolverAddresses(ds, local, defaultedCCVQualifiers(rlc.OutboundCCVQualifiers))
	if err != nil {
		return ccvadapters.RemoteChainConfig[[]byte, string]{}, fmt.Errorf("resolve outbound CCVs: %w", err)
	}

	remoteFamily, err := chainsel.GetSelectorFamily(remote)
	if err != nil {
		return ccvadapters.RemoteChainConfig[[]byte, string]{}, fmt.Errorf("get chain family for remote chain %d: %w", remote, err)
	}
	remoteAdapter, ok := a.remoteChainFamilyAdapter(remoteFamily)
	if !ok {
		return ccvadapters.RemoteChainConfig[[]byte, string]{}, fmt.Errorf("no chain family adapter registered for remote chain family %q", remoteFamily)
	}

	fqConfig := remoteAdapter.GetDefaultFeeQuoterDestChainConfig(local, remote, remoteAdapter.GetChainFamilySelector())
	// The remote family's selector is authoritative regardless of defaults.
	fqConfig.ChainFamilySelector = remoteAdapter.GetChainFamilySelector()

	defaults := remoteAdapter.GetDefaultRemoteChainConfig(local, remote)
	allowTrafficFrom := cciputils.Coalesce(rlc.AllowTrafficFrom, defaults.AllowTrafficFrom)
	tokenReceiverAllowed := cciputils.Coalesce(rlc.TokenReceiverAllowed, defaults.TokenReceiverAllowed)

	return ccvadapters.RemoteChainConfig[[]byte, string]{
		AllowTrafficFrom:         &allowTrafficFrom,
		OnRamps:                  rlc.RemoteOnRamps,
		OffRamp:                  rlc.RemoteOffRamp,
		DefaultExecutor:          executorAddr,
		DefaultInboundCCVs:       inboundCCVs,
		DefaultOutboundCCVs:      outboundCCVs,
		FeeQuoterDestChainConfig: fqConfig,
		ExecutorDestChainConfig:  defaults.ExecutorDestChainConfig,
		// The remote adapter flags destinations that execute manually so the
		// sequence writes its no-execution sentinel and skips the Executor.
		SkipExecutorConfig:        defaults.SkipExecutorConfig,
		AddressBytesLength:        remoteAdapter.GetAddressBytesLength(),
		BaseExecutionGasCost:      cciputils.Coalesce(rlc.BaseExecutionGasCost, defaults.BaseExecutionGasCost),
		TokenReceiverAllowed:      &tokenReceiverAllowed,
		MessageNetworkFeeUSDCents: cciputils.Coalesce(rlc.MessageNetworkFeeUSDCents, defaults.MessageNetworkFeeUSDCents),
		TokenNetworkFeeUSDCents:   cciputils.Coalesce(rlc.TokenNetworkFeeUSDCents, defaults.TokenNetworkFeeUSDCents),
	}, nil
}

// remoteChainFamilyAdapter resolves the remote chain family's adapter through
// the seam when set, otherwise the ccip ChainFamily registry.
func (a *StellarCCVLaneConfigAdapter) remoteChainFamilyAdapter(family string) (ccvadapters.ChainFamily, bool) {
	if a.remoteChainFamily != nil {
		return a.remoteChainFamily(family)
	}
	return ccvadapters.GetChainFamilyRegistry().GetChainFamily(family)
}

// addCommitteeVerifierRemote ensures committeeVerifiers has an entry for
// qualifier and records remote as one of the chains it serves, using
// chain-family defaults and an empty SignatureConfig unless the caller
// supplied signers (signatures are otherwise owned by the committee
// changesets).
func addCommitteeVerifierRemote(
	committeeVerifiers map[string]*ccvadapters.CommitteeVerifierConfig[datastore.AddressRef],
	ds datastore.DataStore,
	local, remote uint64,
	qualifier string,
	signers []string,
	threshold uint8,
) error {
	cfg, ok := committeeVerifiers[qualifier]
	if !ok {
		refs, err := laneCVContracts.ResolveCommitteeVerifierContracts(ds, local, qualifier)
		if err != nil {
			return fmt.Errorf("resolve committee verifier (qualifier %q): %w", qualifier, err)
		}
		cfg = &ccvadapters.CommitteeVerifierConfig[datastore.AddressRef]{
			CommitteeVerifier:     refs,
			RemoteChains:          make(map[uint64]ccvadapters.CommitteeVerifierRemoteChainConfig),
			AllowedFinalityConfig: laneChainFamily.GetDefaultFinalityConfig(),
		}
		committeeVerifiers[qualifier] = cfg
	}

	cvDefaults := laneChainFamily.GetDefaultCommitteeVerifierRemoteChainConfig()
	remoteCfg := ccvadapters.CommitteeVerifierRemoteChainConfig{
		AllowlistEnabled:   cvDefaults.AllowlistEnabled,
		FeeUSDCents:        cvDefaults.FeeUSDCents,
		GasForVerification: cvDefaults.GasForVerification,
		PayloadSizeBytes:   cvDefaults.PayloadSizeBytes,
	}
	if len(signers) > 0 {
		remoteCfg.SignatureConfig = ccvadapters.CommitteeVerifierSignatureQuorumConfig{
			Signers:   signers,
			Threshold: threshold,
		}
	}
	cfg.RemoteChains[remote] = remoteCfg
	return nil
}

// validateInboundSignatureQuorum rejects a malformed inbound signature quorum
// before it reaches the committee verifier: signer addresses must be valid
// 20-byte ETH-style hex (what the stellar committee_verifier stores,
// left-padded to 32 bytes on chain), and when signers are supplied the
// threshold must be in [1, len(signers)]. An empty signer set leaves the
// quorum untouched, so the threshold is ignored. The sequence pads and
// strictly-ascending-sorts the signers itself; this only validates.
func validateInboundSignatureQuorum(signers []string, threshold uint8) error {
	for _, s := range signers {
		if !common.IsHexAddress(s) {
			return fmt.Errorf("InboundSigners: %q is not a valid hex address", s)
		}
	}
	if len(signers) > 0 {
		if threshold == 0 {
			return errors.New("InboundThreshold must be greater than 0 when InboundSigners is set")
		}
		if int(threshold) > len(signers) {
			return fmt.Errorf("InboundThreshold %d exceeds the number of InboundSigners (%d)", threshold, len(signers))
		}
	}
	return nil
}

// resolveResolverAddresses maps committee-verifier qualifiers to their verifier
// resolver addresses — the address referenced as a CCV on the ramps —
// preserving order. Duplicate qualifiers collapse to a single address.
func resolveResolverAddresses(ds datastore.DataStore, chainSelector uint64, qualifiers []string) ([]string, error) {
	out := make([]string, 0, len(qualifiers))
	seen := make(map[string]struct{}, len(qualifiers))
	for _, qualifier := range qualifiers {
		if _, dup := seen[qualifier]; dup {
			continue
		}
		seen[qualifier] = struct{}{}
		refs, err := laneCVContracts.GetCommitteeVerifierResolver(ds, chainSelector, qualifier)
		if err != nil {
			return nil, fmt.Errorf("resolver for qualifier %q: %w", qualifier, err)
		}
		for _, ref := range refs {
			out = append(out, ref.Address)
		}
	}
	return out, nil
}

// resolveLaneRouter resolves the Router for the chain. Stellar has no separate
// TestRouter, so the test and production router resolve to the same address
// and a test→prod promotion re-run is idempotent.
func resolveLaneRouter(ds datastore.DataStore, chainSelector uint64, useTestRouter bool) ([]byte, error) {
	if useTestRouter {
		addr, err := laneChainFamily.GetTestRouter(ds, chainSelector)
		if err != nil {
			return nil, fmt.Errorf("resolve local TestRouter: %w", err)
		}
		return addr, nil
	}
	addr, err := laneChainFamily.GetRouterAddress(ds, chainSelector)
	if err != nil {
		return nil, fmt.Errorf("resolve local Router: %w", err)
	}
	return addr, nil
}

// laneLocalDataStore reconstructs a sealed datastore from the local chain's
// ExistingAddresses so the stellar chain-family resolvers (which take a
// DataStore) can be reused as-is.
func laneLocalDataStore(refs []datastore.AddressRef) datastore.DataStore {
	ms := datastore.NewMemoryDataStore()
	for _, ref := range refs {
		_ = ms.Addresses().Add(ref)
	}
	return ms.Seal()
}

// defaultedCCVQualifiers returns the given committee-verifier qualifiers, or
// the single "default" qualifier when none are supplied, so operators can omit
// CCV qualifiers entirely and have the default committee verifier
// auto-resolved.
func defaultedCCVQualifiers(qualifiers []string) []string {
	if len(qualifiers) == 0 {
		return []string{defaultQualifier}
	}
	return qualifiers
}

// dedupeQualifiers returns the union of the given qualifier lists in stable
// (sorted) order, dropping empties and duplicates.
func dedupeQualifiers(lists ...[]string) []string {
	seen := make(map[string]struct{})
	for _, list := range lists {
		for _, q := range list {
			if q == "" {
				continue
			}
			seen[q] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for q := range seen {
		out = append(out, q)
	}
	sort.Strings(out)
	return out
}

// committeeVerifierConfigs flattens the per-qualifier map into the slice the
// sequence consumes, ordered by qualifier for determinism.
func committeeVerifierConfigs(byQualifier map[string]*ccvadapters.CommitteeVerifierConfig[datastore.AddressRef]) []ccvadapters.CommitteeVerifierConfig[datastore.AddressRef] {
	qualifiers := make([]string, 0, len(byQualifier))
	for q := range byQualifier {
		qualifiers = append(qualifiers, q)
	}
	sort.Strings(qualifiers)
	out := make([]ccvadapters.CommitteeVerifierConfig[datastore.AddressRef], 0, len(byQualifier))
	for _, q := range qualifiers {
		out = append(out, *byQualifier[q])
	}
	return out
}
