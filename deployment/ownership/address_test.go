package ownership

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

func TestNormalizeContractAddress(t *testing.T) {
	t.Parallel()

	strkeyAddr := stellarutil.MustGenerateMockContractID("deployer", "normalize-address-test")
	hexForm, err := stellarutil.StrkeyToHex(strkeyAddr)
	require.NoError(t, err)

	t.Run("strkey passes through unchanged", func(t *testing.T) {
		t.Parallel()
		got, err := NormalizeContractAddress(strkeyAddr)
		require.NoError(t, err)
		require.Equal(t, strkeyAddr, got)
	})

	t.Run("recorded hex form converts back to the strkey", func(t *testing.T) {
		t.Parallel()
		got, err := NormalizeContractAddress(hexForm)
		require.NoError(t, err)
		require.Equal(t, strkeyAddr, got)
	})

	t.Run("hex without the 0x prefix also converts", func(t *testing.T) {
		t.Parallel()
		got, err := NormalizeContractAddress(hexForm[2:])
		require.NoError(t, err)
		require.Equal(t, strkeyAddr, got)
	})

	t.Run("garbage is rejected, not guessed", func(t *testing.T) {
		t.Parallel()
		_, err := NormalizeContractAddress("not-a-contract-address")
		require.Error(t, err)
		require.Contains(t, err.Error(), "neither a contract strkey nor the hex form")
	})

	t.Run("empty is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NormalizeContractAddress("")
		require.Error(t, err)
		require.Contains(t, err.Error(), "neither a contract strkey nor the hex form")
	})

	t.Run("wrong-length hex is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NormalizeContractAddress(hexForm[:len(hexForm)-1]) // 63 nibbles
		require.Error(t, err)
		require.Contains(t, err.Error(), "neither a contract strkey nor the hex form")
	})

	t.Run("account strkeys are rejected as contract addresses", func(t *testing.T) {
		t.Parallel()
		_, err := NormalizeContractAddress("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
		require.Error(t, err)
		require.Contains(t, err.Error(), "neither a contract strkey nor the hex form")
	})

	t.Run("contract-shaped strkey with a bad checksum is rejected", func(t *testing.T) {
		t.Parallel()
		malformed := "C" + strings.Repeat("A", 55) // right shape, wrong checksum
		_, err := NormalizeContractAddress(malformed)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not a valid contract strkey")
	})

	t.Run("mutated valid strkey is rejected", func(t *testing.T) {
		t.Parallel()
		mutated := strkeyAddr[:len(strkeyAddr)-1] + "A" // flip the last base32 char
		if mutated == strkeyAddr {
			t.Skip("mutation collided with the original")
		}
		_, err := NormalizeContractAddress(mutated)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not a valid contract strkey")
	})
}
