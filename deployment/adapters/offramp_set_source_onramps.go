package adapters

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/xdr"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	offrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

var (
	_ ccvadapters.OffRampSourceOnRampSetter = (*StellarChainFamilyAdapter)(nil)
	_ ccvadapters.OffRampSourceOnRampReader = (*StellarChainFamilyAdapter)(nil)
)

// GetOffRampSourceOnRamps implements ccvadapters.OffRampSourceOnRampReader: the
// onramps currently whitelisted for sourceChainSelector on the local chain's
// OffRamp, as the source chain writes them into its messages (for a stellar
// source that is the 40-byte Address::to_xdr envelope). An unconfigured lane
// returns an empty whitelist, not an error.
func (a *StellarChainFamilyAdapter) GetOffRampSourceOnRamps(
	e cldf.Environment,
	localChainSelector uint64,
	sourceChainSelector uint64,
) ([][]byte, error) {
	offRampID, deps, err := a.offRampReadDeps(e, localChainSelector)
	if err != nil {
		return nil, err
	}
	return getOffRampSourceOnRamps(e.OperationsBundle, deps, offRampID, sourceChainSelector)
}

// getOffRampSourceOnRamps reads the OffRamp source chain config and returns its
// onramp whitelist; unconfigured lanes read as empty.
func getOffRampSourceOnRamps(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	offRampID string,
	sourceChainSelector uint64,
) ([][]byte, error) {
	report, err := cldf_ops.ExecuteOperation(b, offrampops.GetSourceChainConfig, deps, offrampops.GetSourceChainConfigInput{
		ContractID:          offRampID,
		SourceChainSelector: sourceChainSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("get source chain config for %d on OffRamp %s: %w",
			sourceChainSelector, offRampID, err)
	}
	if report.Output == nil {
		return nil, nil
	}
	onRamps := make([][]byte, 0, len(report.Output.OnRamps))
	onRamps = append(onRamps, report.Output.OnRamps...)
	return onRamps, nil
}

// SetOffRampSourceOnRamps implements ccvadapters.OffRampSourceOnRampSetter:
// replaces the source-chain onramp whitelist on the local chain's OffRamp.
// Deployer-owned OffRamps are reconfigured directly; a timelock-owned OffRamp
// gets one MCMS proposal transaction (the invoking timelock satisfies the
// contract's owner require_auth). Already-matching whitelists skip.
func (a *StellarChainFamilyAdapter) SetOffRampSourceOnRamps(
	e cldf.Environment,
	update ccvadapters.OffRampSetSourceOnRampsEntry,
) (*mcmstypes.BatchOperation, bool, error) {
	ch, ok := e.BlockChains.StellarChains()[update.LocalChainSelector]
	if !ok {
		return nil, false, fmt.Errorf("stellar chain %d not found in environment", update.LocalChainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(e.Logger))
	if err != nil {
		return nil, false, fmt.Errorf("stellar deployer from chain %d: %w", update.LocalChainSelector, err)
	}
	offRampID, err := a.offRampContractID(e.DataStore, update.LocalChainSelector)
	if err != nil {
		return nil, false, err
	}
	refs, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return nil, false, fmt.Errorf("fetch datastore addresses: %w", err)
	}
	return setOffRampSourceOnRamps(
		e.OperationsBundle,
		stellardeps.FromDeployer(dep),
		dep.SignerAddress(),
		refs,
		a.readOwnerFn(),
		offRampID,
		update,
	)
}

// setOffRampSourceOnRamps applies one OffRamp source-onramp whitelist update:
// parse the operator-supplied bytes as given, read the current config (failing
// fast when the lane is not configured — the preserved Router/IsEnabled/CCVs
// would otherwise come from a zero-valued config), skip when the whitelist
// already matches unordered, then route by owner — direct exec for the
// deployer, one MCMS proposal transaction for the owning timelock.
func setOffRampSourceOnRamps(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	deployerAddr string,
	refs []datastore.AddressRef,
	readOwner func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error),
	offRampID string,
	update ccvadapters.OffRampSetSourceOnRampsEntry,
) (*mcmstypes.BatchOperation, bool, error) {
	desiredOnRamps, err := parseOffRampSourceOnRampAddresses(update.OnRamps)
	if err != nil {
		return nil, false, err
	}

	currentReport, err := cldf_ops.ExecuteOperation(b, offrampops.GetSourceChainConfig, deps, offrampops.GetSourceChainConfigInput{
		ContractID:          offRampID,
		SourceChainSelector: update.SourceChainSelector,
	})
	if err != nil {
		return nil, false, fmt.Errorf("get source chain config for %d on OffRamp %s: %w",
			update.SourceChainSelector, offRampID, err)
	}
	current := currentReport.Output
	if current == nil {
		return nil, false, fmt.Errorf(
			"source chain %d is not configured on OffRamp %s (chain %d); configure the lane first (LaneExpansion or configure_chains_for_lanes)",
			update.SourceChainSelector, offRampID, update.LocalChainSelector)
	}

	if unorderedOnRampBytesEqual(current.OnRamps, desiredOnRamps) {
		b.Logger.Infow("OffRamp source onramp whitelist already matches desired state, skipping",
			"localChain", update.LocalChainSelector,
			"sourceChain", update.SourceChainSelector,
			"offRamp", offRampID,
			"onRampCount", len(desiredOnRamps),
		)
		return nil, true, nil
	}

	desired := offrampbindings.SourceChainConfigArgs{
		Router:              current.Router,
		SourceChainSelector: update.SourceChainSelector,
		IsEnabled:           current.IsEnabled,
		OnRamps:             desiredOnRamps,
		DefaultCcvs:         current.DefaultCcvs,
		LaneMandatedCcvs:    current.LaneMandatedCcvs,
	}

	owner, err := readOwner(b.GetContext(), deps, datastore.AddressRef{
		ChainSelector: update.LocalChainSelector,
		Type:          datastore.ContractType(offrampops.ContractType),
		Version:       stellarops.ContractDeploymentVersion,
		Address:       offRampID,
	})
	if err != nil {
		return nil, false, fmt.Errorf("read owner of OffRamp %s: %w", offRampID, err)
	}

	if owner == deployerAddr {
		if _, err := cldf_ops.ExecuteOperation(b, offrampops.ApplySourceChainCfgUpdates, deps, offrampops.ApplySourceChainCfgUpdatesInput{
			ContractID: offRampID,
			Updates:    []offrampbindings.SourceChainConfigArgs{desired},
		}); err != nil {
			return nil, false, fmt.Errorf("apply source chain config on OffRamp %s: %w", offRampID, err)
		}
		op, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
			ChainSelector: update.LocalChainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{ContractType: offrampops.ContractType},
				To:                offRampID,
			},
			ExecInfo: &evmcontract.ExecInfo{Hash: "stellar-offramp-set-source-onramps-direct-exec"},
		}})
		if err != nil {
			return nil, false, fmt.Errorf("build batch operation for chain %d: %w", update.LocalChainSelector, err)
		}
		return &op, false, nil
	}

	for _, qual := range []string{cciputils.CLLQualifier, cciputils.RMNTimelockQualifier, cciputils.UltraFastCurseMCMSQualifier} {
		tl, ok := mcmsutil.FindExistingStellarTimelock(refs, update.LocalChainSelector, qual)
		if !ok || tl != owner {
			continue
		}
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("apply_source_chain_cfg_updates", []xdr.ScVal{
			scval.StructSliceToScVal([]offrampbindings.SourceChainConfigArgs{desired}),
		})
		if err != nil {
			return nil, false, err
		}
		op, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
			ChainSelector: update.LocalChainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{ContractType: offrampops.ContractType},
				To:                offRampID,
				Data:              data,
				AdditionalFields:  mcmsutil.StellarMCMSTxAdditionalFields(),
			},
		}})
		if err != nil {
			return nil, false, fmt.Errorf("build batch operation for chain %d: %w", update.LocalChainSelector, err)
		}
		return &op, false, nil
	}

	return nil, false, fmt.Errorf(
		"OffRamp %s (chain %d) owner %q is neither the deployer %q nor a deployed timelock (CLL/RMN/UltraFastCurse); cannot set source onramps",
		offRampID, update.LocalChainSelector, owner, deployerAddr)
}

// readOwnerFn returns the owner-read seam, defaulting to ownership.ContractOwner.
func (a *StellarChainFamilyAdapter) readOwnerFn() func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
	if a.readOwner != nil {
		return a.readOwner
	}
	return ownership.ContractOwner
}

// offRampReadDeps resolves the OffRamp strkey and the operation deps for a
// read on the local chain's OffRamp.
func (a *StellarChainFamilyAdapter) offRampReadDeps(e cldf.Environment, localChainSelector uint64) (string, stellardeps.StellarDeps, error) {
	ch, ok := e.BlockChains.StellarChains()[localChainSelector]
	if !ok {
		return "", stellardeps.StellarDeps{}, fmt.Errorf("stellar chain %d not found in environment", localChainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return "", stellardeps.StellarDeps{}, fmt.Errorf("stellar deployer from chain %d: %w", localChainSelector, err)
	}
	offRampID, err := a.offRampContractID(e.DataStore, localChainSelector)
	if err != nil {
		return "", stellardeps.StellarDeps{}, err
	}
	return offRampID, stellardeps.FromDeployer(dep), nil
}

// offRampContractID resolves the local chain's OffRamp strkey from the datastore.
func (a *StellarChainFamilyAdapter) offRampContractID(ds datastore.DataStore, chainSelector uint64) (string, error) {
	offRampBytes, err := a.GetOffRampAddress(ds, chainSelector)
	if err != nil {
		return "", fmt.Errorf("resolve OffRamp on chain %d: %w", chainSelector, err)
	}
	offRampID, err := scval.BytesToContractStrkey(offRampBytes)
	if err != nil {
		return "", fmt.Errorf("offramp address to strkey for chain %d: %w", chainSelector, err)
	}
	return offRampID, nil
}

// parseOffRampSourceOnRampAddresses decodes the operator-supplied onramps into
// the bytes stored on the OffRamp. The source chain may be of any family, so
// the bytes are taken exactly as given: the OffRamp matches an incoming
// message by hashing the onramp bytes it carries, and only the source family
// knows how it encodes them (for a stellar source that is the 40-byte
// Address::to_xdr envelope, never the raw 32-byte contract ID).
func parseOffRampSourceOnRampAddresses(addrs []string) ([][]byte, error) {
	out := make([][]byte, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for i, addr := range addrs {
		raw, err := decodeOffRampOnRampHex(addr)
		if err != nil {
			return nil, fmt.Errorf("onRamps[%d]: %w", i, err)
		}
		key := string(raw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, raw)
	}
	if len(out) == 0 {
		return nil, errors.New("no valid onRamp addresses after parsing")
	}
	return out, nil
}

// decodeOffRampOnRampHex decodes a 0x-prefixed hex string into the raw onramp
// bytes. The only bound is the wire format's: MessageV1 length-prefixes the
// onramp address with a uint8, so it cannot exceed 255 bytes.
func decodeOffRampOnRampHex(addr string) ([]byte, error) {
	raw, err := hexutil.Decode(strings.TrimSpace(addr))
	if err != nil {
		return nil, fmt.Errorf("invalid hex address %q: %w", addr, err)
	}
	if len(raw) == 0 || len(raw) > math.MaxUint8 {
		return nil, fmt.Errorf("address %q must be 1-%d bytes, got %d", addr, math.MaxUint8, len(raw))
	}
	return raw, nil
}

// unorderedOnRampBytesEqual compares two onramp whitelists ignoring order,
// mirroring EVM's UnorderedSliceEqual for the skip check.
func unorderedOnRampBytesEqual(a, b [][]byte) bool {
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
