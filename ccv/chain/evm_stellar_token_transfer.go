// This file holds the production-usable, CTF-free logic for selecting the EVM
// token/pool pair used in EVM-to-Stellar token transfers. The devenv-coupled
// *Chain methods that drive these transfers live in the separate tests module
// (tests/ccv/chain), so this package stays free of the e2e testing framework.
package ccvchain

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

const evmBurnMintTokenPoolType = "BurnMintTokenPool"

// RemoteTokenContractTypes lists token contract types used by EVM devenv
// deployments. Order mirrors deployment likelihood. It is shared with the
// devenv chain implementation in the tests module, which uses it for the
// non-EVM remote token fallback.
var RemoteTokenContractTypes = []string{
	"BurnMintERC20WithDripToken",
	"BurnMintERC20WithDrip",
	"BurnMintERC20Token",
}

// ResolveEVMTokenPoolForStellar selects the EVM token/pool pair used for
// EVM-to-Stellar token transfers. Prefer BurnMint -> LockRelease combinations
// because the Stellar test pool is lock-release, then fall back to any BurnMint
// pool with a matching test token.
func ResolveEVMTokenPoolForStellar(refs []datastore.AddressRef, chainSelector uint64) (pool, token datastore.AddressRef, found bool) {
	pools := make([]datastore.AddressRef, 0)
	for _, ref := range refs {
		if ref.ChainSelector == chainSelector && string(ref.Type) == evmBurnMintTokenPoolType {
			pools = append(pools, ref)
		}
	}
	sort.SliceStable(pools, func(i, j int) bool {
		return compareEVMStellarPoolRefs(pools[i], pools[j]) < 0
	})

	for _, candidate := range pools {
		candidateToken, ok := findEVMTokenForPool(refs, chainSelector, candidate.Qualifier)
		if ok {
			return candidate, candidateToken, true
		}
	}
	return datastore.AddressRef{}, datastore.AddressRef{}, false
}

func compareEVMStellarPoolRefs(a, b datastore.AddressRef) int {
	if pa, pb := evmStellarPoolPreference(a), evmStellarPoolPreference(b); pa != pb {
		if pa < pb {
			return -1
		}
		return 1
	}
	if av, bv := versionString(a.Version), versionString(b.Version); av != bv {
		if av < bv {
			return -1
		}
		return 1
	}
	if a.Qualifier != b.Qualifier {
		if a.Qualifier < b.Qualifier {
			return -1
		}
		return 1
	}
	if a.Address < b.Address {
		return -1
	}
	if a.Address > b.Address {
		return 1
	}
	return 0
}

func evmStellarPoolPreference(ref datastore.AddressRef) int {
	q := ref.Qualifier
	switch {
	case strings.Contains(q, "BurnMintTokenPool") && strings.Contains(q, "to LockReleaseTokenPool"):
		return 0
	case strings.HasPrefix(q, stellarccip.DevenvTestTokenPoolQualifier):
		return 1
	default:
		return 2
	}
}

func versionString(v *semver.Version) string {
	if v == nil {
		return ""
	}
	return v.String()
}

func findEVMTokenForPool(refs []datastore.AddressRef, chainSelector uint64, qualifier string) (datastore.AddressRef, bool) {
	for _, tokenType := range RemoteTokenContractTypes {
		for _, ref := range refs {
			if ref.ChainSelector == chainSelector && string(ref.Type) == tokenType && ref.Qualifier == qualifier {
				return ref, true
			}
		}
	}
	return datastore.AddressRef{}, false
}
