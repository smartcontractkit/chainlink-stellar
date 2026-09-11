package ccvchain

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEVMSelector = uint64(3379446385462418246)

func TestResolveEVMTokenPoolForStellar(t *testing.T) {
	lockReleaseQualifier := "TEST (BurnMintTokenPool 1.6.1 [] to LockReleaseTokenPool 2.0.0 [])"
	burnMintQualifier := "TEST (BurnMintTokenPool 1.6.1 [] to BurnMintTokenPool 1.6.1 [])"

	refs := []datastore.AddressRef{
		{
			Address:       "0x0000000000000000000000000000000000000001",
			ChainSelector: testEVMSelector,
			Type:          datastore.ContractType("BurnMintTokenPool"),
			Version:       semver.MustParse("1.6.1"),
			Qualifier:     burnMintQualifier,
		},
		{
			Address:       "0x0000000000000000000000000000000000000002",
			ChainSelector: testEVMSelector,
			Type:          datastore.ContractType("BurnMintERC20WithDripToken"),
			Version:       semver.MustParse("1.0.0"),
			Qualifier:     burnMintQualifier,
		},
		{
			Address:       "0x0000000000000000000000000000000000000003",
			ChainSelector: testEVMSelector,
			Type:          datastore.ContractType("BurnMintTokenPool"),
			Version:       semver.MustParse("1.6.1"),
			Qualifier:     lockReleaseQualifier,
		},
		{
			Address:       "0x0000000000000000000000000000000000000004",
			ChainSelector: testEVMSelector,
			Type:          datastore.ContractType("BurnMintERC20WithDripToken"),
			Version:       semver.MustParse("1.0.0"),
			Qualifier:     lockReleaseQualifier,
		},
	}

	pool, token, found := ResolveEVMTokenPoolForStellar(refs, testEVMSelector)
	require.True(t, found)
	assert.Equal(t, "0x0000000000000000000000000000000000000003", pool.Address)
	assert.Equal(t, "0x0000000000000000000000000000000000000004", token.Address)
}
