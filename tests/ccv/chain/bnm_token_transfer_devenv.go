// This file holds the PostConnect wiring for the shared-pipeline stellar
// burn-mint pools: each pool's remote updates toward its paired EVM
// counterpart. The pairs themselves (one BnM token + burn-mint pool per
// combo) are deployed by the shared TokenExpansion pipeline via the
// StellarTokenAdapter sequences (see GetTokenExpansionConfigs); the EVM side
// of every pair is wired by the shared ConfigureTokensForTransfers pipeline,
// which runs before PostConnect.
package ccvchain

import (
	"context"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchainprod "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// configureSharedBnmPools applies every shared-pipeline stellar burn-mint
// pool's remote updates. The pipeline derives each pool's qualifier as
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
			string(ref.Type) != "LockReleaseTokenPool" {
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

	for _, ref := range allRefs {
		if ref.ChainSelector != stellarSelector ||
			string(ref.Type) != stellarccip.DevenvCatalogBurnMintTokenPoolType ||
			ref.Version == nil {
			continue
		}
		poolStrkey, err := scval.HexToContractStrkey(ref.Address)
		if err != nil {
			return fmt.Errorf("convert burn-mint pool address %s: %w", ref.Address, err)
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
					Msg("No counterpart pool found for a shared stellar burn-mint pool")
				continue
			}
			counterpartToken, ok := evmTokenForPoolQualifier(allRefs, rs, counterpart.Qualifier)
			if !ok {
				c.logger.Warn().
					Uint64("evmChain", rs).
					Str("counterpart", counterpart.Qualifier).
					Msg("No counterpart token found for a shared stellar burn-mint pool")
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
			return fmt.Errorf("apply chain updates for shared burn-mint pool %q: %w", ref.Qualifier, err)
		}
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
