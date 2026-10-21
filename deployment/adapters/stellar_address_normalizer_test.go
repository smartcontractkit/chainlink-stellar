package adapters

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

func TestStellarAddressNormalizer_CanonicalizesBothForms(t *testing.T) {
	t.Parallel()
	n := StellarAddressNormalizer{}

	contractID := stellarutil.MustGenerateMockContractID("deployer", "normalizer-test")
	want, err := stellarutil.StrkeyToHex(contractID)
	require.NoError(t, err)

	fromStrkey, err := n.NormalizeAddress(contractID)
	require.NoError(t, err)
	require.Equal(t, want, fromStrkey)

	// An uppercase hex form of the same ID canonicalizes to the same lowercase
	// datastore form, so refs from different sources compare equal.
	mixed := "0x" + strings.ToUpper(strings.TrimPrefix(want, "0x"))
	fromHex, err := n.NormalizeAddress(mixed)
	require.NoError(t, err)
	require.Equal(t, want, fromHex)

	// An account strkey (a token's SAC address) normalizes like a contract ID.
	kp, err := keypair.Random()
	require.NoError(t, err)
	accountID := kp.Address()
	accountHex, err := stellarutil.StrkeyToHex(accountID)
	require.NoError(t, err)
	got, err := n.NormalizeAddress(accountID)
	require.NoError(t, err)
	require.Equal(t, accountHex, got)
}

func TestStellarAddressNormalizer_BytesToString(t *testing.T) {
	t.Parallel()
	n := StellarAddressNormalizer{}
	contractID := stellarutil.MustGenerateMockContractID("deployer", "normalizer-test")
	hexForm, err := stellarutil.StrkeyToHex(contractID)
	require.NoError(t, err)

	raw, err := n.StringToBytes(hexForm)
	require.NoError(t, err)
	require.Len(t, raw, 32)

	got, err := n.BytesToString(raw)
	require.NoError(t, err)
	require.Equal(t, hexForm, got)

	for _, bad := range [][]byte{nil, {}, make([]byte, 20), make([]byte, 33)} {
		_, err := n.BytesToString(bad)
		require.Error(t, err, "length %d", len(bad))
	}
}

func TestStellarAddressNormalizer_StringToBytes(t *testing.T) {
	t.Parallel()
	n := StellarAddressNormalizer{}
	contractID := stellarutil.MustGenerateMockContractID("deployer", "normalizer-test")
	hexForm, err := stellarutil.StrkeyToHex(contractID)
	require.NoError(t, err)

	fromStrkey, err := n.StringToBytes(contractID)
	require.NoError(t, err)
	fromHex, err := n.StringToBytes(hexForm)
	require.NoError(t, err)
	require.Equal(t, fromHex, fromStrkey)

	// A 20-byte hex address is an EVM form, not a stellar one: error rather
	// than produce bytes no stellar ref ever carries.
	_, err = n.StringToBytes("0x" + strings.Repeat("ab", 20))
	require.ErrorContains(t, err, "want 32")

	_, err = n.StringToBytes("nope")
	require.ErrorContains(t, err, "neither a contract/account strkey nor 0x-hex")
	_, err = n.StringToBytes("")
	require.Error(t, err)
}

// TestStellarAddressNormalizer_ZeroAddress pins the zero-address path callers
// like RemoveRemotePools probe with IsZeroAddress: an all-zero 32-byte address
// round-trips, and a real contract ID never reads as zero.
func TestStellarAddressNormalizer_ZeroAddress(t *testing.T) {
	t.Parallel()
	n := StellarAddressNormalizer{}
	zero := "0x" + strings.Repeat("00", 32)
	raw, err := n.StringToBytes(zero)
	require.NoError(t, err)
	require.Empty(t, bytes0Trimmed(raw))
	got, err := n.BytesToString(raw)
	require.NoError(t, err)
	require.Equal(t, zero, got)

	contractID := stellarutil.MustGenerateMockContractID("deployer", "normalizer-test")
	hexForm, err := stellarutil.StrkeyToHex(contractID)
	require.NoError(t, err)
	raw, err = n.StringToBytes(hexForm)
	require.NoError(t, err)
	require.NotEmpty(t, bytes0Trimmed(raw))
}

// bytes0Trimmed trims zero bytes, mirroring how IsZeroAddress inspects them.
func bytes0Trimmed(b []byte) []byte {
	var out []byte
	for _, x := range b {
		if x != 0 {
			out = append(out, x)
		}
	}
	return out
}

// TestStellarAddressNormalizer_HexutilEncodingIsCanonical pins that the
// normalizer emits exactly the encoding the datastore record helpers store,
// which is what makes normalized refs hit datastore lookups.
func TestStellarAddressNormalizer_HexutilEncodingIsCanonical(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("deployer", "normalizer-test")
	hexForm, err := stellarutil.StrkeyToHex(contractID)
	require.NoError(t, err)
	raw, err := hexutil.Decode(hexForm)
	require.NoError(t, err)

	got, err := StellarAddressNormalizer{}.BytesToString(raw)
	require.NoError(t, err)
	require.Equal(t, hexForm, got)
}
