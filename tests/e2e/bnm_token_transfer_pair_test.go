package e2e_tests

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
)

// bnmPairQualifier is the shared-pipeline symmetric BM2.0.0[] pair: the
// TokenExpansion pipeline deploys both legs under this instance qualifier
// (the pool description is family-agnostic, so the qualifier string is
// identical on the stellar and EVM chains).
const bnmPairQualifier = "TEST (BurnMintTokenPool 2.0.0 [], BurnMintTokenPool 2.0.0 [])::BurnMintTokenPool 2.0.0 []"

// bnmCombination builds the symmetric BM↔BM combo for the shared-pipeline
// BnM pair via TokenCombinationsFromPoolPairs: both legs use the ccv catalog
// type BurnMintTokenPool 2.0.0 under the pipeline-derived instance qualifier
// (the stellar leg resolves against the row the pipeline's TokenExpansion
// deploys through the StellarTokenAdapter). CCVQualifiers stays empty: the
// message carries no explicit CCVs (the lane default committee verifies;
// 4 receipt issuers / 1 verifier result).
func bnmCombination(t *testing.T, lib ccv.Lib, localSel, remoteSel uint64) devenvcommon.TokenCombination {
	ds, err := lib.DataStore()
	require.NoError(t, err)
	combos, err := devenvcommon.TokenCombinationsFromPoolPairs(ds, []devenvcommon.TokenPoolPair{{
		LocalSelector:  strconv.FormatUint(localSel, 10),
		RemoteSelector: strconv.FormatUint(remoteSel, 10),
		LocalPool:      devenvcommon.TokenPoolRefCfg{Type: devenvcommon.BurnMintTokenPoolType, Version: "2.0.0", Qualifier: bnmPairQualifier},
		RemotePool:     devenvcommon.TokenPoolRefCfg{Type: devenvcommon.BurnMintTokenPoolType, Version: "2.0.0", Qualifier: bnmPairQualifier},
	}})
	require.NoError(t, err)
	require.Len(t, combos, 1)
	return combos[0]
}
