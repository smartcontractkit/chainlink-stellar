// This file holds the PostConnect wiring for the shared-pipeline stellar
// pools (burn-mint and plain lock-release): each pool's remote updates
// toward its paired EVM counterpart. The pairs themselves (one BnM token +
// one pool per combo) are deployed by the shared TokenExpansion pipeline via
// the StellarTokenAdapter sequences (see GetTokenExpansionConfigs); the EVM
// side of every pair is wired by the shared ConfigureTokensForTransfers
// pipeline, which runs before PostConnect.
package ccvchain

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	chainsel "github.com/smartcontractkit/chain-selectors"
	evmonramp "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/onramp"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/token_pool"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchainprod "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// sharedPairAllowedFinality is the allowed-finality config applied to every
// shared-pipeline pool on the lane (both sides): {WaitForSafe, BlockDepth 1}
// admits safe-tag requests and block-depth requests ≥ 1, mirroring the
// AllowedFinalityConfig the stellar TokenTransferConfig declares. Pools
// default to wait-for-finality only, which rejects the fast-finality
// requests the tcapi combo finality produces (block-depth 1).
var sharedPairAllowedFinality = finality.Config{WaitForSafe: true, BlockDepth: 1}

// sharedPairAllowedFinalityU32 is the stellar bindings' uint32 form of
// sharedPairAllowedFinality (same on-wire bytes4 encoding).
var sharedPairAllowedFinalityU32 = func() uint32 {
	raw := sharedPairAllowedFinality.Raw()
	return binary.BigEndian.Uint32(raw[:])
}()

// configureSharedBnmPools applies every shared-pipeline stellar pool's
// (burn-mint or lock-release) remote updates. The pipeline derives each
// pool's qualifier as
// "TEST (A, B)::A" (A = this pool's own description, B = the counterpart's),
// so the counterpart pool ref resolves deterministically by swapping the
// instance suffix — no resolver heuristics.
func (c *Chain) configureSharedBnmPools(env *deployment.Environment, stellarSelector uint64, remoteSelectors []uint64) error {
	allRefs, err := env.DataStore.Addresses().Fetch()
	if err != nil {
		return fmt.Errorf("fetch datastore addresses: %w", err)
	}

	// Index candidate counterpart pool/token rows on the EVM remotes by
	// (chain, type, version, qualifier).
	type poolKey struct {
		chain     uint64
		typ       string
		version   string
		qualifier string
	}
	poolsBy := make(map[poolKey]datastore.AddressRef)
	for _, ref := range allRefs {
		family, err := chainsel.GetSelectorFamily(ref.ChainSelector)
		if err != nil || family != chainsel.FamilyEVM {
			continue
		}
		if string(ref.Type) != stellarccip.DevenvCatalogBurnMintTokenPoolType &&
			string(ref.Type) != stellarccip.DevenvCatalogLockReleaseTokenPoolType {
			continue
		}
		if ref.Version == nil {
			continue
		}
		poolsBy[poolKey{ref.ChainSelector, string(ref.Type), ref.Version.String(), ref.Qualifier}] = ref
	}

	evmRemoteSelectors := make([]uint64, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		family, err := chainsel.GetSelectorFamily(rs)
		if err == nil && family == chainsel.FamilyEVM {
			evmRemoteSelectors = append(evmRemoteSelectors, rs)
		}
	}
	if len(evmRemoteSelectors) == 0 {
		return nil
	}

	// EVM counterpart pools (2.0.0+) needing the shared allowed-finality
	// config; keyed by (chain selector, pool address). Pool addresses are
	// unique only within a chain — the same deployer/nonce produces
	// identical addresses across EVM dev chains — so keying by address alone
	// would drop all but one chain's pool and leave the rest rejecting
	// fast-finality transfers.
	type allowedPoolKey struct {
		chain uint64
		addr  string
	}
	evmAllowedPools := make(map[allowedPoolKey]struct{})

	for _, ref := range allRefs {
		if ref.ChainSelector != stellarSelector || ref.Version == nil {
			continue
		}
		// Shared pipeline stellar pools: burn-mint and (plain) lock-release.
		// Both implement the shared pool entrypoints the wiring below uses
		// (ApplyChainUpdates, SetAllowedFinalityConfig); a lock-release pool's
		// counterpart is always an EVM burn-mint pool (pairing rules forbid
		// LR↔LR), which the EVM counterpart index already covers.
		switch string(ref.Type) {
		case stellarccip.DevenvCatalogBurnMintTokenPoolType, stellarccip.DevenvCatalogLockReleaseTokenPoolType:
		default:
			continue
		}
		poolStrkey, err := scval.HexToContractStrkey(ref.Address)
		if err != nil {
			return fmt.Errorf("convert shared pool address %s: %w", ref.Address, err)
		}
		counterpartType, counterpartVersion, counterpartQualifier, err := sharedPairCounterpart(ref.Qualifier)
		if err != nil {
			return fmt.Errorf("parse shared pair qualifier %q: %w", ref.Qualifier, err)
		}

		var updates []tokenpoolbindings.ChainUpdate
		for _, rs := range evmRemoteSelectors {
			counterpart, ok := poolsBy[poolKey{rs, counterpartType, counterpartVersion, counterpartQualifier}]
			if !ok {
				c.logger.Warn().
					Uint64("evmChain", rs).
					Str("counterpart", counterpartQualifier).
					Msg("No counterpart pool found for a shared stellar pool")
				continue
			}
			// 2.0.0+ counterpart pools gate lockOrBurn on allowed finality the
			// same way the stellar pool does; remember them for the shared
			// allowed-finality application after the stellar loop.
			if counterpart.Version.Major() >= 2 {
				evmAllowedPools[allowedPoolKey{rs, counterpart.Address}] = struct{}{}
			}
			counterpartToken, ok := evmTokenForPoolQualifier(allRefs, rs, counterpart.Qualifier)
			if !ok {
				c.logger.Warn().
					Uint64("evmChain", rs).
					Str("counterpart", counterpart.Qualifier).
					Msg("No counterpart token found for a shared stellar pool")
				continue
			}
			remotePoolBytes, err := stellarccip.AddressBytesHex(counterpart, rs)
			if err != nil {
				return fmt.Errorf("encode counterpart pool address for %d: %w", rs, err)
			}
			remoteTokenBytes, err := stellarccip.AddressBytesHex(counterpartToken, rs)
			if err != nil {
				return fmt.Errorf("encode counterpart token address for %d: %w", rs, err)
			}
			// The inbound token_transfer.source_pool_address is abi-encoded by
			// the EVM OnRamp and the pool's is_remote_source_pool check compares
			// it byte-exactly, so store the same 32-byte abi-encoded form.
			remotePoolBytes = common.LeftPadBytes(remotePoolBytes, 32)
			updates = append(updates, tokenpoolbindings.ChainUpdate{
				RemoteChainSelector:       rs,
				RemotePoolAddresses:       [][]byte{remotePoolBytes},
				RemoteTokenAddress:        remoteTokenBytes,
				OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
				InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
			})
		}
		if len(updates) == 0 {
			continue
		}
		poolClient := tokenpoolbindings.NewTokenPoolClient(c.deployer, poolStrkey)
		if err := poolClient.ApplyChainUpdates(context.Background(), updates, nil); err != nil {
			return fmt.Errorf("apply chain updates for shared pool %q: %w", ref.Qualifier, err)
		}
		// Fast-finality admission (stellar side): the pool defaults
		// to wait-for-finality only, and lock_or_burn gates FTF requests on the
		// allowed config (finality codec ensure_requested_finality_allowed),
		// so combo-finality cases (block-depth-1 requests) would be rejected
		// without this. The pipeline would apply the AllowedFinalityConfig
		// declared in the TokenTransferConfig, but only through the pool-config
		// changeset (TokenFeeAdapter), which the devenv never runs — PostConnect
		// applies it directly. Owner-gated EOA write; idempotent.
		if err := poolClient.SetAllowedFinalityConfig(context.Background(), sharedPairAllowedFinalityU32); err != nil {
			return fmt.Errorf("set allowed finality for shared pool %q: %w", ref.Qualifier, err)
		}
	}

	// Fast-finality admission (EVM side): the 2.0.0 counterpart pools gate
	// lockOrBurn on allowed finality the same way (default 0 rejects FTF
	// requests), so the EVM-source combo-finality cases need the same config
	// on the EVM pools.
	for k := range evmAllowedPools {
		if err := c.setEVMPoolAllowedFinality(env, k.chain, k.addr); err != nil {
			return err
		}
	}
	return nil
}

// setEVMPoolAllowedFinality applies the shared allowed-finality config to an
// EVM burn-mint pool via the EVM token-pool op (2.0.0+ pools only — 1.6.1
// pools predate the finality codec). Owner-gated EOA write; idempotent.
func (c *Chain) setEVMPoolAllowedFinality(env *deployment.Environment, evmSelector uint64, poolAddress string) error {
	evmChain, ok := env.BlockChains.EVMChains()[evmSelector]
	if !ok {
		return fmt.Errorf("EVM chain %d not found in environment", evmSelector)
	}
	if _, err := cldf_ops.ExecuteOperation(env.OperationsBundle, evmtokenpool.SetAllowedFinalityConfig, evmChain,
		evmcontract.FunctionInput[[4]byte]{
			Address:       common.HexToAddress(poolAddress),
			ChainSelector: evmSelector,
			Args:          sharedPairAllowedFinality.Raw(),
		}); err != nil {
		return fmt.Errorf("set allowed finality on EVM pool %s: %w", poolAddress, err)
	}
	return nil
}

// sharedPairCounterpart parses a shared-pipeline pool instance qualifier
// "TEST (A, B)::A" into the counterpart pool's type, version, and instance
// qualifier ("TEST (A, B)::B"). For a symmetric pair (A == B) the counterpart
// is the same description.
func sharedPairCounterpart(qualifier string) (typ, version, counterpartQualifier string, err error) {
	idx := strings.LastIndex(qualifier, "::")
	if idx < 0 || !strings.HasPrefix(qualifier, "TEST (") {
		return "", "", "", fmt.Errorf("not a shared-pipeline pair qualifier")
	}
	pairPart, ownDesc := qualifier[:idx], qualifier[idx+2:]
	inner := strings.TrimSuffix(strings.TrimPrefix(pairPart, "TEST ("), ")")
	descs := strings.SplitN(inner, ", ", 2)
	if len(descs) != 2 {
		return "", "", "", fmt.Errorf("pair part %q does not hold two descriptions", pairPart)
	}
	counterpartDesc := descs[1]
	if counterpartDesc == ownDesc {
		counterpartDesc = descs[0]
	}
	fields := strings.Fields(counterpartDesc)
	if len(fields) < 2 {
		return "", "", "", fmt.Errorf("counterpart description %q is not a pool description", counterpartDesc)
	}
	return fields[0], fields[1], pairPart + "::" + counterpartDesc, nil
}

// evmTokenForPoolQualifier finds the token row keyed by the pool qualifier
// (mirrors ccvchainprod.findEVMTokenForPool: same qualifier, token types by
// preference).
func evmTokenForPoolQualifier(refs []datastore.AddressRef, chainSelector uint64, qualifier string) (datastore.AddressRef, bool) {
	for _, tokenType := range ccvchainprod.RemoteTokenContractTypes {
		for _, ref := range refs {
			if ref.ChainSelector == chainSelector && string(ref.Type) == tokenType && ref.Qualifier == qualifier {
				return ref, true
			}
		}
	}
	return datastore.AddressRef{}, false
}

// enableTokenReceivers flips token_receiver_allowed on both lane OnRamps
// (both families default it false and the shared pipeline never sets it),
// read-modify-write per remote, so separate-token-receiver sends are
// admitted on the lane.
func (c *Chain) enableTokenReceivers(env *deployment.Environment, stellarSelector uint64, remoteSelectors []uint64) error {
	ctx := context.Background()
	evmRemoteSelectors := make([]uint64, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		if family, err := chainsel.GetSelectorFamily(rs); err == nil && family == chainsel.FamilyEVM {
			evmRemoteSelectors = append(evmRemoteSelectors, rs)
		}
	}
	if len(evmRemoteSelectors) == 0 {
		return nil
	}

	// Stellar OnRamp: read-modify-write per EVM remote.
	if c.onRampContractID != "" {
		onrampClient := onrampbindings.NewOnRampClient(c.deployer, c.onRampContractID)
		var updates []onrampbindings.DestChainConfigArgs
		for _, rs := range evmRemoteSelectors {
			cur, err := onrampClient.GetDestChainConfig(ctx, rs)
			if err != nil {
				return fmt.Errorf("get stellar onramp dest config for %d: %w", rs, err)
			}
			if cur == nil || cur.TokenReceiverAllowed {
				continue
			}
			updates = append(updates, onrampbindings.DestChainConfigArgs{
				AddressBytesLength:        cur.AddressBytesLength,
				BaseExecutionGasCost:      cur.BaseExecutionGasCost,
				DefaultCcvs:               cur.DefaultCcvs,
				DefaultExecutor:           cur.DefaultExecutor,
				DestChainSelector:         rs,
				ExecutionFeeUsdCents:      cur.ExecutionFeeUsdCents,
				LaneMandatedCcvs:          cur.LaneMandatedCcvs,
				MessageNetworkFeeUsdCents: cur.MessageNetworkFeeUsdCents,
				OffRamp:                   cur.OffRamp,
				Router:                    cur.Router,
				TokenNetworkFeeUsdCents:   cur.TokenNetworkFeeUsdCents,
				TokenReceiverAllowed:      true,
			})
		}
		if len(updates) > 0 {
			if err := onrampClient.ApplyDestChainConfigUpdates(ctx, updates); err != nil {
				return fmt.Errorf("enable token receivers on stellar onramp: %w", err)
			}
			c.logger.Info().Int("remotes", len(updates)).Msg("Enabled token receivers on stellar onramp")
		}
	}

	// EVM OnRamps: read-modify-write the stellar remote via the ops.
	allRefs, err := env.DataStore.Addresses().Fetch()
	if err != nil {
		return fmt.Errorf("fetch datastore addresses: %w", err)
	}
	for _, rs := range evmRemoteSelectors {
		evmChain, ok := env.BlockChains.EVMChains()[rs]
		if !ok {
			continue
		}
		for _, ref := range allRefs {
			if ref.ChainSelector != rs || string(ref.Type) != "OnRamp" ||
				ref.Version == nil || ref.Version.String() != "2.0.0" {
				continue
			}
			addr := common.HexToAddress(ref.Address)
			read, err := cldf_ops.ExecuteOperation(env.OperationsBundle, evmonramp.GetDestChainConfig, evmChain,
				evmcontract.FunctionInput[uint64]{Address: addr, ChainSelector: rs, Args: stellarSelector})
			if err != nil {
				return fmt.Errorf("read EVM onramp %s dest config for stellar: %w", ref.Address, err)
			}
			cur := read.Output
			if cur.TokenReceiverAllowed {
				continue
			}
			args := evmonramp.DestChainConfigArgs{
				DestChainSelector:         stellarSelector,
				Router:                    cur.Router,
				AddressBytesLength:        cur.AddressBytesLength,
				TokenReceiverAllowed:      true,
				MessageNetworkFeeUSDCents: cur.MessageNetworkFeeUSDCents,
				TokenNetworkFeeUSDCents:   cur.TokenNetworkFeeUSDCents,
				BaseExecutionGasCost:      cur.BaseExecutionGasCost,
				DefaultCCVs:               cur.DefaultCCVs,
				LaneMandatedCCVs:          cur.LaneMandatedCCVs,
				DefaultExecutor:           cur.DefaultExecutor,
				OffRamp:                   cur.OffRamp,
			}
			if _, err := cldf_ops.ExecuteOperation(env.OperationsBundle, evmonramp.ApplyDestChainConfigUpdates, evmChain,
				evmcontract.FunctionInput[[]evmonramp.DestChainConfigArgs]{
					Address: addr, ChainSelector: rs, Args: []evmonramp.DestChainConfigArgs{args},
				}); err != nil {
				return fmt.Errorf("enable token receivers on EVM onramp %s: %w", ref.Address, err)
			}
		}
	}
	return nil
}
