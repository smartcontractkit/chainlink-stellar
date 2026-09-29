package ccip

import (
	"strings"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddressBytesLength_stellar(t *testing.T) {
	t.Parallel()
	l, err := AddressBytesLength(chainsel.STELLAR_LOCALNET.Selector)
	require.NoError(t, err)
	assert.Equal(t, uint32(StellarAddressByteLen), l)
}

func TestAddressBytesLength_unknownSelector(t *testing.T) {
	t.Parallel()
	_, err := AddressBytesLength(uint64(0))
	require.Error(t, err)
}

func TestAddressBytesHex_wrongLengthForStellar(t *testing.T) {
	t.Parallel()
	ref := datastore.AddressRef{Address: "0x00"}
	_, err := AddressBytesHex(ref, chainsel.STELLAR_LOCALNET.Selector)
	require.Error(t, err)
}

func TestCanonicalSourceOnRampBytes_stellarPassesThrough(t *testing.T) {
	t.Parallel()
	sid := stellarutil.MustGenerateMockContractID("lane-canon", "onr")
	hexAddr, err := stellarutil.StrkeyToHex(sid)
	require.NoError(t, err)
	raw, err := CanonicalSourceOnRampBytes(datastore.AddressRef{Address: hexAddr}, chainsel.STELLAR_LOCALNET.Selector)
	require.NoError(t, err)
	assert.Len(t, raw, int(StellarAddressByteLen))
}

func TestCanonicalSourceOnRampBytes_evmPadsLeft(t *testing.T) {
	t.Parallel()
	sel := chainsel.ETHEREUM_MAINNET.Selector
	// 20-byte EVM address as hex
	hexAddr := "0x" + strings.Repeat("ab", 20)
	raw, err := CanonicalSourceOnRampBytes(datastore.AddressRef{Address: hexAddr}, sel)
	require.NoError(t, err)
	assert.Len(t, raw, 32)
	assert.Equal(t, byte(0), raw[0])
	assert.Equal(t, byte(0xab), raw[31])
}
