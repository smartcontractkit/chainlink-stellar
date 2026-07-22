package scval

import (
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestRawBytesFromAddressScVal_Contract(t *testing.T) {
	contractID := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	addr := BuildContractScAddress(contractID[:])
	if addr == nil {
		t.Fatal("BuildContractScAddress returned nil")
	}
	val := xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: addr}

	raw, err := RawBytesFromAddressScVal(val)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(raw))
	}
	for i := range contractID {
		if raw[i] != contractID[i] {
			t.Fatalf("byte %d mismatch: expected %d, got %d", i, contractID[i], raw[i])
		}
	}
}

func TestRawBytesFromAddressScVal_Account(t *testing.T) {
	pubKeyBytes := [32]byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160,
		170, 180, 190, 200, 210, 220, 230, 240, 250, 1, 2, 3, 4, 5, 6, 7}
	pubKey := xdr.Uint256(pubKeyBytes)
	accountID := xdr.AccountId{
		Type:    xdr.PublicKeyTypePublicKeyTypeEd25519,
		Ed25519: &pubKey,
	}
	val := xdr.ScVal{
		Type: xdr.ScValTypeScvAddress,
		Address: &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		},
	}

	raw, err := RawBytesFromAddressScVal(val)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(raw))
	}
	for i := range pubKeyBytes {
		if raw[i] != pubKeyBytes[i] {
			t.Fatalf("byte %d mismatch: expected %d, got %d", i, pubKeyBytes[i], raw[i])
		}
	}
}

func TestRawBytesFromAddressScVal_Invalid(t *testing.T) {
	val := xdr.ScVal{Type: xdr.ScValTypeScvU64}
	_, err := RawBytesFromAddressScVal(val)
	if err == nil {
		t.Fatal("expected error for non-address ScVal")
	}
}

func TestBytesVecFromScVal_Empty(t *testing.T) {
	emptyVec := xdr.ScVec{}
	vecPtr := &emptyVec
	val := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}

	result, err := BytesVecFromScVal(val)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty slice, got %d items", len(result))
	}
}

func TestBytesVecFromScVal_Single(t *testing.T) {
	item := xdr.ScBytes([]byte{0xAA, 0xBB})
	vec := xdr.ScVec{xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &item}}
	vecPtr := &vec
	val := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}

	result, err := BytesVecFromScVal(val)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result))
	}
	if result[0][0] != 0xAA || result[0][1] != 0xBB {
		t.Fatalf("unexpected bytes: %x", result[0])
	}
}

func TestBytesVecFromScVal_Multiple(t *testing.T) {
	b1 := xdr.ScBytes([]byte{1})
	b2 := xdr.ScBytes([]byte{2, 3})
	b3 := xdr.ScBytes([]byte{4, 5, 6})
	vec := xdr.ScVec{
		xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b1},
		xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b2},
		xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b3},
	}
	vecPtr := &vec
	val := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}

	result, err := BytesVecFromScVal(val)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
}

func TestBytesVecFromScVal_NonVec(t *testing.T) {
	val := xdr.ScVal{Type: xdr.ScValTypeScvU32}
	_, err := BytesVecFromScVal(val)
	if err == nil {
		t.Fatal("expected error for non-vec ScVal")
	}
}

func TestSymbolToScValPtr(t *testing.T) {
	ptr := SymbolToScValPtr("transfer")
	if ptr == nil {
		t.Fatal("expected non-nil pointer")
	}
	if ptr.Type != xdr.ScValTypeScvSymbol {
		t.Fatalf("expected symbol type, got %v", ptr.Type)
	}
	sym, ok := ptr.GetSym()
	if !ok {
		t.Fatal("failed to get symbol")
	}
	if string(sym) != "transfer" {
		t.Fatalf("expected 'transfer', got %q", string(sym))
	}
}

func TestHexToContractStrkey_Valid(t *testing.T) {
	contractID := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	hexStr := "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	result, err := HexToContractStrkey(hexStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify by decoding back
	decoded, err := strkey.Decode(strkey.VersionByteContract, result)
	if err != nil {
		t.Fatalf("failed to decode strkey: %v", err)
	}
	for i := range contractID {
		if decoded[i] != contractID[i] {
			t.Fatalf("byte %d mismatch: expected %d, got %d", i, contractID[i], decoded[i])
		}
	}
}

func TestHexToContractStrkey_MissingPrefix(t *testing.T) {
	// Without 0x prefix still works (TrimPrefix is a no-op for non-matching prefix)
	hexStr := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	_, err := HexToContractStrkey(hexStr)
	if err != nil {
		t.Fatalf("unexpected error for hex without 0x prefix: %v", err)
	}
}

func TestHexToContractStrkey_InvalidHex(t *testing.T) {
	_, err := HexToContractStrkey("0xZZZZ")
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestBytesNSmallHelpers(t *testing.T) {
	b2 := [2]byte{0xAA, 0xBB}
	v := Bytes2ToScVal(b2)
	got2, err := Bytes2FromScVal(v)
	require.NoError(t, err)
	require.Equal(t, b2, got2)

	b10 := [10]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	got10, err := Bytes10FromScVal(Bytes10ToScVal(b10))
	require.NoError(t, err)
	require.Equal(t, b10, got10)

	b20 := [20]byte{0xDE, 0xAD}
	got20, err := Bytes20FromScVal(Bytes20ToScVal(b20))
	require.NoError(t, err)
	require.Equal(t, b20, got20)

	// wrong length must error
	_, err = Bytes2FromScVal(Bytes10ToScVal(b10))
	require.Error(t, err)
}

func TestI256RoundTrip(t *testing.T) {
	cases := []*big.Int{
		big.NewInt(0),
		big.NewInt(1),
		big.NewInt(-1),
		new(big.Int).Lsh(big.NewInt(1), 200), // large positive
		new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 200)),                // large negative
		new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)), // max i256
		new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 255)),                // min i256
	}
	for _, c := range cases {
		v, err := I256ToScVal(c)
		require.NoError(t, err, c.String())
		got, err := I256FromScVal(v)
		require.NoError(t, err, c.String())
		require.Zero(t, c.Cmp(got), "round-trip mismatch for %s: got %s", c, got)
	}
	// out of range must error
	_, err := I256ToScVal(new(big.Int).Lsh(big.NewInt(1), 255)) // 2^255 > max i256
	require.Error(t, err)
	// negative out of range must also error: -(2^255 + 1) is one below min i256 (-(2^255))
	minI256 := new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 255))
	belowMin := new(big.Int).Sub(minI256, big.NewInt(1))
	_, err = I256ToScVal(belowMin)
	require.Error(t, err)
	// nil treated as zero
	v, err := I256ToScVal(nil)
	require.NoError(t, err)
	got, err := I256FromScVal(v)
	require.NoError(t, err)
	require.Zero(t, got.Sign())
}
