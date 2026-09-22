package mcmsutil

import (
	"encoding/hex"
	"testing"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestDecodeSorobanMCMSInvokePayload_RoundTrip(t *testing.T) {
	t.Parallel()
	subjects := [][16]byte{{0xFD, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFD}}
	caller, err := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))
	require.NoError(t, err)
	data, err := EncodeSorobanMCMSInvokePayload("curse", []xdr.ScVal{
		scval.AddressToScVal(caller),
		scval.Bytes16SliceToScVal(subjects),
	})
	require.NoError(t, err)
	_ = err

	fn, args, err := DecodeSorobanMCMSInvokePayload(data)
	require.NoError(t, err)
	require.Equal(t, "curse", fn)
	require.Len(t, args, 2)

	decodedCaller, err := scval.AddressFromScVal(args[0])
	require.NoError(t, err)
	require.Equal(t, caller, decodedCaller)

	subjectVec, ok := args[1].GetVec()
	require.True(t, ok, "second arg must be a vec of Bytes16")
	require.NotNil(t, subjectVec)
	require.Len(t, *subjectVec, len(subjects))
	for i, item := range *subjectVec {
		decoded, err := scval.Bytes16FromScVal(item)
		require.NoError(t, err)
		require.Equal(t, subjects[i], decoded)
	}
}

func TestDecodeSorobanMCMSInvokePayload_EmptyArgsGoldenVector(t *testing.T) {
	t.Parallel()
	// The canonical empty-args encoding from EncodeSorobanInvokeArgs:
	// SCV_VEC discriminant + present flag + zero element count.
	golden, err := hex.DecodeString("000000100000000100000000")
	require.NoError(t, err)
	argsData, err := EncodeSorobanInvokeArgs(nil)
	require.NoError(t, err)
	require.Equal(t, golden, argsData)

	// The payload vec always carries the function symbol, so a no-arg payload
	// decodes to the function name and an empty arg slice.
	data, err := EncodeSorobanMCMSInvokePayload("accept_ownership", nil)
	require.NoError(t, err)
	fn, args, err := DecodeSorobanMCMSInvokePayload(data)
	require.NoError(t, err)
	require.Equal(t, "accept_ownership", fn)
	require.Empty(t, args)
}

func TestDecodeSorobanMCMSInvokePayload_RejectsMalformed(t *testing.T) {
	t.Parallel()
	t.Run("not xdr", func(t *testing.T) {
		t.Parallel()
		_, _, err := DecodeSorobanMCMSInvokePayload([]byte{0xFF, 0xFF})
		require.Error(t, err)
	})
	t.Run("not a vec", func(t *testing.T) {
		t.Parallel()
		data, err := scval.SymbolToScVal("curse").MarshalBinary()
		require.NoError(t, err)
		_, _, err = DecodeSorobanMCMSInvokePayload(data)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not a populated vec")
	})
	t.Run("empty vec", func(t *testing.T) {
		t.Parallel()
		data, err := scval.VecToScVal(nil).MarshalBinary()
		require.NoError(t, err)
		_, _, err = DecodeSorobanMCMSInvokePayload(data)
		require.Error(t, err)
		require.Contains(t, err.Error(), "vec is empty")
	})
	t.Run("first element not a symbol", func(t *testing.T) {
		t.Parallel()
		data, err := scval.VecToScVal([]xdr.ScVal{scval.Uint64ToScVal(1)}).MarshalBinary()
		require.NoError(t, err)
		_, _, err = DecodeSorobanMCMSInvokePayload(data)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not a symbol")
	})
}
