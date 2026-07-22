// Package scval provides shared helper functions for converting between
// Go types and Stellar Soroban xdr.ScVal values. These helpers are used
// by all generated contract bindings and hand-written extras.
package scval

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Uint64ToScVal converts a uint64 to an xdr.ScVal.
func Uint64ToScVal(v uint64) xdr.ScVal {
	xdrU64 := xdr.Uint64(v)
	return xdr.ScVal{
		Type: xdr.ScValTypeScvU64,
		U64:  &xdrU64,
	}
}

// Uint32ToScVal converts a uint32 to an xdr.ScVal.
func Uint32ToScVal(v uint32) xdr.ScVal {
	xdrU32 := xdr.Uint32(v)
	return xdr.ScVal{
		Type: xdr.ScValTypeScvU32,
		U32:  &xdrU32,
	}
}

// BoolToScVal converts a bool to an xdr.ScVal.
func BoolToScVal(v bool) xdr.ScVal {
	return xdr.ScVal{
		Type: xdr.ScValTypeScvBool,
		B:    &v,
	}
}

// I128ToScVal converts an int64 to an xdr.ScVal representing i128.
func I128ToScVal(v int64) xdr.ScVal {
	var hi int64
	if v < 0 {
		hi = -1 // Sign extend for negative numbers
	}
	lo := uint64(v)
	parts := xdr.Int128Parts{
		Hi: xdr.Int64(hi),
		Lo: xdr.Uint64(lo),
	}
	return xdr.ScVal{
		Type: xdr.ScValTypeScvI128,
		I128: &parts,
	}
}

// BytesToScVal converts a byte slice to an xdr.ScVal.
func BytesToScVal(b []byte) xdr.ScVal {
	bytes := xdr.ScBytes(b)
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes2ToScVal converts a [2]byte to an xdr.ScVal.
func Bytes2ToScVal(b [2]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes4ToScVal converts a [4]byte to an xdr.ScVal (for BytesN<4>).
func Bytes4ToScVal(b [4]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes16ToScVal converts a [16]byte to an xdr.ScVal.
func Bytes16ToScVal(b [16]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes32ToScVal converts a [32]byte to an xdr.ScVal.
func Bytes32ToScVal(b [32]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes64ToScVal converts a [64]byte to an xdr.ScVal.
func Bytes64ToScVal(b [64]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// AddressToScVal converts a Stellar address string (G... or C...) to an xdr.ScVal.
func AddressToScVal(addr string) xdr.ScVal {
	scAddr := ParseAddress(addr)
	return xdr.ScVal{
		Type:    xdr.ScValTypeScvAddress,
		Address: scAddr,
	}
}

// VoidScVal returns an ScVal representing Soroban's void/None value.
func VoidScVal() xdr.ScVal {
	return xdr.ScVal{
		Type: xdr.ScValTypeScvVoid,
	}
}

// OptionalAddressToScVal converts a *string to an ScVal.
// nil maps to ScvVoid (Soroban Option::None), non-nil maps to ScvAddress (Option::Some).
func OptionalAddressToScVal(addr *string) xdr.ScVal {
	if addr == nil {
		return VoidScVal()
	}
	return AddressToScVal(*addr)
}

// OptionalAddressFromScVal extracts a *string from an ScVal.
// ScvVoid returns nil (Option::None), ScvAddress returns a pointer to the address string (Option::Some).
func OptionalAddressFromScVal(val xdr.ScVal) (*string, error) {
	if val.Type == xdr.ScValTypeScvVoid {
		return nil, nil
	}
	addr, err := AddressFromScVal(val)
	if err != nil {
		return nil, err
	}
	return &addr, nil
}

// VecToScVal converts a slice of xdr.ScVal into a vector xdr.ScVal.
func VecToScVal(items []xdr.ScVal) xdr.ScVal {
	scVec := xdr.ScVec(items)
	vecPtr := &scVec
	return xdr.ScVal{
		Type: xdr.ScValTypeScvVec,
		Vec:  &vecPtr,
	}
}

// SymbolToScVal converts a string to an xdr.ScVal symbol.
func SymbolToScVal(sym string) xdr.ScVal {
	scSym := xdr.ScSymbol(sym)
	return xdr.ScVal{
		Type: xdr.ScValTypeScvSymbol,
		Sym:  &scSym,
	}
}

// SymbolFromScVal extracts a string from an xdr.ScVal containing a symbol.
func SymbolFromScVal(val xdr.ScVal) (string, error) {
	sym, ok := val.GetSym()
	if !ok {
		return "", fmt.Errorf("not a symbol type: %v", val.Type)
	}
	return string(sym), nil
}

// StringToScVal converts a Go string to an xdr.ScVal representing Soroban String.
func StringToScVal(s string) xdr.ScVal {
	scStr := xdr.ScString(s)
	return xdr.ScVal{
		Type: xdr.ScValTypeScvString,
		Str:  &scStr,
	}
}

// StringFromScVal extracts a Go string from an xdr.ScVal containing Soroban String.
func StringFromScVal(val xdr.ScVal) (string, error) {
	s, ok := val.GetStr()
	if !ok {
		return "", fmt.Errorf("not a string type: %v", val.Type)
	}
	return string(s), nil
}

// BuildStructScVal builds a struct xdr.ScVal from a map of field names to values.
// Soroban requires ScMap keys to be sorted lexicographically.
func BuildStructScVal(fields map[string]xdr.ScVal) (xdr.ScVal, error) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]xdr.ScMapEntry, 0, len(fields))
	for _, k := range keys {
		v := fields[k]
		sym := xdr.ScSymbol(k)
		entries = append(entries, xdr.ScMapEntry{
			Key: xdr.ScVal{
				Type: xdr.ScValTypeScvSymbol,
				Sym:  &sym,
			},
			Val: v,
		})
	}
	scMap := xdr.ScMap(entries)
	mapPtr := &scMap
	return xdr.ScVal{
		Type: xdr.ScValTypeScvMap,
		Map:  &mapPtr,
	}, nil
}

// ParseAddress parses a Stellar address string (G... or C...) into an xdr.ScAddress.
func ParseAddress(addr string) *xdr.ScAddress {
	if len(addr) == 0 {
		return nil
	}

	if addr[0] == 'C' {
		decoded, err := strkey.Decode(strkey.VersionByteContract, addr)
		if err != nil {
			return nil
		}
		return BuildContractScAddress(decoded)
	}

	if addr[0] == 'G' {
		decoded, err := strkey.Decode(strkey.VersionByteAccountID, addr)
		if err != nil {
			return nil
		}
		var pubKey xdr.Uint256
		copy(pubKey[:], decoded)
		accountID := xdr.AccountId{
			Type:    xdr.PublicKeyTypePublicKeyTypeEd25519,
			Ed25519: &pubKey,
		}
		return &xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		}
	}

	return nil
}

// BuildContractScAddress creates an ScAddress for a contract from raw bytes.
func BuildContractScAddress(contractIDBytes []byte) *xdr.ScAddress {
	if len(contractIDBytes) != 32 {
		return nil
	}
	xdrBytes := make([]byte, 0, 36)
	xdrBytes = append(xdrBytes, 0, 0, 0, 1)
	xdrBytes = append(xdrBytes, contractIDBytes...)

	var addr xdr.ScAddress
	if err := addr.UnmarshalBinary(xdrBytes); err != nil {
		return nil
	}
	return &addr
}

// AddressFromScVal extracts a strkey address from an xdr.ScVal.
func AddressFromScVal(val xdr.ScVal) (string, error) {
	addr, ok := val.GetAddress()
	if !ok {
		return "", fmt.Errorf("not an address type: %v", val.Type)
	}

	switch addr.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		accountID := addr.MustAccountId()
		pubKey := accountID.Ed25519
		if pubKey == nil {
			return "", fmt.Errorf("account ID has no Ed25519 key")
		}
		return strkey.Encode(strkey.VersionByteAccountID, (*pubKey)[:])
	case xdr.ScAddressTypeScAddressTypeContract:
		contractID := addr.MustContractId()
		return strkey.Encode(strkey.VersionByteContract, contractID[:])
	default:
		return "", fmt.Errorf("unsupported address type: %s", addr.Type)
	}
}

// Uint64FromScVal extracts a uint64 from an xdr.ScVal.
func Uint64FromScVal(val xdr.ScVal) (uint64, error) {
	u64, ok := val.GetU64()
	if !ok {
		return 0, fmt.Errorf("not a u64 type: %v", val.Type)
	}
	return uint64(u64), nil
}

// Uint32FromScVal extracts a uint32 from an xdr.ScVal.
func Uint32FromScVal(val xdr.ScVal) (uint32, error) {
	u32, ok := val.GetU32()
	if !ok {
		return 0, fmt.Errorf("not a u32 type: %v", val.Type)
	}
	return uint32(u32), nil
}

// BytesFromScVal extracts a byte slice from an xdr.ScVal containing Bytes.
func BytesFromScVal(val xdr.ScVal) ([]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return nil, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	return []byte(bytes), nil
}

// Bytes2FromScVal extracts a [2]byte from an xdr.ScVal containing BytesN<2>.
func Bytes2FromScVal(val xdr.ScVal) ([2]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [2]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 2 {
		return [2]byte{}, fmt.Errorf("expected 2 bytes, got %d", len(bytes))
	}
	var result [2]byte
	copy(result[:], bytes)
	return result, nil
}

// Bytes4FromScVal extracts a [4]byte from an xdr.ScVal containing BytesN<4>.
func Bytes4FromScVal(val xdr.ScVal) ([4]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [4]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 4 {
		return [4]byte{}, fmt.Errorf("expected 4 bytes, got %d", len(bytes))
	}
	var result [4]byte
	copy(result[:], bytes)
	return result, nil
}

// Bytes16FromScVal extracts a [16]byte from an xdr.ScVal containing BytesN<16>.
func Bytes16FromScVal(val xdr.ScVal) ([16]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [16]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 16 {
		return [16]byte{}, fmt.Errorf("expected 16 bytes, got %d", len(bytes))
	}
	var result [16]byte
	copy(result[:], bytes)
	return result, nil
}

// Bytes10ToScVal converts a [10]byte to an xdr.ScVal.
func Bytes10ToScVal(b [10]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes10FromScVal extracts a [10]byte from an xdr.ScVal containing BytesN<10>.
func Bytes10FromScVal(val xdr.ScVal) ([10]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [10]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 10 {
		return [10]byte{}, fmt.Errorf("expected 10 bytes, got %d", len(bytes))
	}
	var result [10]byte
	copy(result[:], bytes)
	return result, nil
}

// Bytes20ToScVal converts a [20]byte to an xdr.ScVal.
func Bytes20ToScVal(b [20]byte) xdr.ScVal {
	bytes := xdr.ScBytes(b[:])
	return xdr.ScVal{
		Type:  xdr.ScValTypeScvBytes,
		Bytes: &bytes,
	}
}

// Bytes20FromScVal extracts a [20]byte from an xdr.ScVal containing BytesN<20>.
func Bytes20FromScVal(val xdr.ScVal) ([20]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [20]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 20 {
		return [20]byte{}, fmt.Errorf("expected 20 bytes, got %d", len(bytes))
	}
	var result [20]byte
	copy(result[:], bytes)
	return result, nil
}

// Bytes32FromScVal extracts a [32]byte from an xdr.ScVal containing BytesN<32>.
func Bytes32FromScVal(val xdr.ScVal) ([32]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [32]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 32 {
		return [32]byte{}, fmt.Errorf("expected 32 bytes, got %d", len(bytes))
	}
	var result [32]byte
	copy(result[:], bytes)
	return result, nil
}

// Bytes64FromScVal extracts a [64]byte from an xdr.ScVal containing BytesN<64>.
func Bytes64FromScVal(val xdr.ScVal) ([64]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return [64]byte{}, fmt.Errorf("not a bytes type: %v", val.Type)
	}
	if len(bytes) != 64 {
		return [64]byte{}, fmt.Errorf("expected 64 bytes, got %d", len(bytes))
	}
	var result [64]byte
	copy(result[:], bytes)
	return result, nil
}

// I128FromScVal extracts an int64 from an xdr.ScVal containing i128.
// Note: This truncates to int64 for simplicity.
func I128FromScVal(val xdr.ScVal) (int64, error) {
	i128, ok := val.GetI128()
	if !ok {
		return 0, fmt.Errorf("not an i128 type: %v", val.Type)
	}
	return int64(i128.Lo), nil
}

// U128 represents a Soroban u128 value. It wraps xdr.UInt128Parts and implements ToScVal
// for use in generated struct serialization.
type U128 xdr.UInt128Parts

// ToScVal converts U128 to an xdr.ScVal for contract calls.
func (u U128) ToScVal() (xdr.ScVal, error) {
	parts := xdr.UInt128Parts(u)
	return U128ToScVal(parts), nil
}

// U128ToScVal converts xdr.UInt128Parts to an xdr.ScVal representing u128.
func U128ToScVal(parts xdr.UInt128Parts) xdr.ScVal {
	return xdr.ScVal{
		Type: xdr.ScValTypeScvU128,
		U128: &parts,
	}
}

// U128FromScVal extracts U128 from an xdr.ScVal containing u128.
func U128FromScVal(val xdr.ScVal) (U128, error) {
	u128, ok := val.GetU128()
	if !ok {
		return U128{}, fmt.Errorf("not a u128 type: %v", val.Type)
	}
	return U128(u128), nil
}

// I256 represents a Soroban i256 value. It wraps xdr.Int256Parts and implements ToScVal
// for use in generated struct serialization.
type I256 xdr.Int256Parts

// ToScVal converts I256 to an xdr.ScVal for contract calls.
func (i I256) ToScVal() (xdr.ScVal, error) {
	parts := xdr.Int256Parts(i)
	return I256PartsToScVal(parts), nil
}

// I256PartsToScVal converts xdr.Int256Parts to an xdr.ScVal representing i256.
func I256PartsToScVal(parts xdr.Int256Parts) xdr.ScVal {
	return xdr.ScVal{
		Type: xdr.ScValTypeScvI256,
		I256: &parts,
	}
}

// I256FromScVal extracts I256 from an xdr.ScVal containing i256.
func I256FromScVal(val xdr.ScVal) (I256, error) {
	i256, ok := val.GetI256()
	if !ok {
		return I256{}, fmt.Errorf("not an i256 type: %v", val.Type)
	}
	return I256(i256), nil
}

// I256FromBigInt converts v (two's-complement range [-2^255, 2^255)) to I256.
func I256FromBigInt(v *big.Int) (I256, error) {
	if v == nil {
		return I256{}, fmt.Errorf("nil big.Int")
	}
	limit := new(big.Int).Lsh(big.NewInt(1), 255)
	if v.Cmp(limit) >= 0 || v.Cmp(new(big.Int).Neg(limit)) < 0 {
		return I256{}, fmt.Errorf("value %s out of i256 range", v)
	}
	twos := new(big.Int).Set(v)
	if twos.Sign() < 0 {
		twos.Add(twos, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	var be [32]byte
	twos.FillBytes(be[:])
	limb := func(o int) uint64 { return binary.BigEndian.Uint64(be[o : o+8]) }
	return I256{
		HiHi: xdr.Int64(limb(0)),
		HiLo: xdr.Uint64(limb(8)),
		LoHi: xdr.Uint64(limb(16)),
		LoLo: xdr.Uint64(limb(24)),
	}, nil
}

// BigInt converts I256 to a big.Int.
func (i I256) BigInt() *big.Int {
	v := new(big.Int).SetInt64(int64(i.HiHi))
	for _, limb := range []uint64{uint64(i.HiLo), uint64(i.LoHi), uint64(i.LoLo)} {
		v.Lsh(v, 64)
		v.Or(v, new(big.Int).SetUint64(limb))
	}
	return v
}

// MustToScVal panics if ToScVal returns an error.
func MustToScVal(val xdr.ScVal, err error) xdr.ScVal {
	if err != nil {
		panic(err)
	}
	return val
}

// AddressSliceToScVal converts a slice of address strings to an xdr.ScVal vector.
func AddressSliceToScVal(items []string) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = AddressToScVal(item)
	}
	return VecToScVal(scVals)
}

// StructSliceToScVal converts a slice of structs with ToScVal to an xdr.ScVal vector.
func StructSliceToScVal[T interface{ ToScVal() (xdr.ScVal, error) }](items []T) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i := range items {
		val, err := items[i].ToScVal()
		if err != nil {
			panic(err)
		}
		scVals[i] = val
	}
	return VecToScVal(scVals)
}

// StringSliceToScVal converts a slice of strings to an xdr.ScVal vector.
func BytesSliceToScVal(items [][]byte) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = BytesToScVal(item)
	}
	return VecToScVal(scVals)
}

// Bytes32SliceToScVal converts a slice of [32]byte to an xdr.ScVal vector (for Vec<BytesN<32>>).
func Bytes32SliceToScVal(items [][32]byte) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = Bytes32ToScVal(item)
	}
	return VecToScVal(scVals)
}

func AddressBytes32SliceToScVal(items [][32]byte) xdr.ScVal {
	return Bytes32SliceToScVal(items)
}

// Bytes4SliceToScVal converts a slice of [4]byte to an xdr.ScVal vector (for Vec<BytesN<4>>).
func Bytes4SliceToScVal(items [][4]byte) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = Bytes4ToScVal(item)
	}
	return VecToScVal(scVals)
}

// Bytes16SliceToScVal converts a slice of [16]byte to an xdr.ScVal vector (for Vec<BytesN<16>>).
func Bytes16SliceToScVal(items [][16]byte) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = Bytes16ToScVal(item)
	}
	return VecToScVal(scVals)
}

func Uint64SliceToScVal(items []uint64) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = Uint64ToScVal(item)
	}
	return VecToScVal(scVals)
}

func Uint32SliceToScVal(items []uint32) xdr.ScVal {
	scVals := make([]xdr.ScVal, len(items))
	for i, item := range items {
		scVals[i] = Uint32ToScVal(item)
	}
	return VecToScVal(scVals)
}

// RawBytesFromAddressScVal extracts the raw 32-byte key (contract ID or
// ed25519 pubkey) from an xdr.ScVal address.
func RawBytesFromAddressScVal(val xdr.ScVal) ([]byte, error) {
	addr, ok := val.GetAddress()
	if !ok {
		return nil, fmt.Errorf("not an address (type=%s)", val.Type)
	}
	switch addr.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		accountID := addr.MustAccountId()
		pubKey := accountID.Ed25519
		if pubKey == nil {
			return nil, fmt.Errorf("account ID has no Ed25519 key")
		}
		raw := make([]byte, 32)
		copy(raw, (*pubKey)[:])
		return raw, nil
	case xdr.ScAddressTypeScAddressTypeContract:
		contractID := addr.MustContractId()
		raw := make([]byte, 32)
		copy(raw, contractID[:])
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported address type: %s", addr.Type)
	}
}

// BytesVecFromScVal decodes a Vec<Bytes> from an xdr.ScVal vector.
func BytesVecFromScVal(val xdr.ScVal) ([][]byte, error) {
	vec, ok := val.GetVec()
	if !ok || vec == nil {
		return nil, fmt.Errorf("not a vec")
	}

	result := make([][]byte, 0, len(*vec))
	for _, item := range *vec {
		b, ok := item.GetBytes()
		if !ok {
			return nil, fmt.Errorf("vec item is not bytes (type=%s)", item.Type)
		}
		result = append(result, []byte(b))
	}
	return result, nil
}

// SymbolToScValPtr returns a pointer to a symbol ScVal.
func SymbolToScValPtr(sym string) *xdr.ScVal {
	scSym := xdr.ScSymbol(sym)
	return &xdr.ScVal{
		Type: xdr.ScValTypeScvSymbol,
		Sym:  &scSym,
	}
}

// HexToContractStrkey converts a 0x-prefixed hex string (32-byte contract ID)
// to a Stellar contract strkey (C...).
func HexToContractStrkey(hexAddr string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(hexAddr, "0x"))
	if err != nil {
		return "", fmt.Errorf("decode hex address %q: %w", hexAddr, err)
	}
	return strkey.Encode(strkey.VersionByteContract, raw)
}

// AddressVecFromScVal extracts raw 32-byte addresses from a Vec<Address> ScVal.
func AddressVecFromScVal(val xdr.ScVal) ([][]byte, error) {
	vec, ok := val.GetVec()
	if !ok || vec == nil {
		return nil, fmt.Errorf("not a vec type: %v", val.Type)
	}

	addresses := make([][]byte, 0, len(*vec))
	for i, item := range *vec {
		raw, err := RawBytesFromAddressScVal(item)
		if err != nil {
			return nil, fmt.Errorf("failed to decode address at index %d: %w", i, err)
		}
		addresses = append(addresses, raw)
	}
	return addresses, nil
}
