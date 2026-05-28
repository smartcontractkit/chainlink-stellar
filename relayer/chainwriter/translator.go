package chainwriter

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"

	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"
)

func toXDRScVal(v stellartypes.ScVal) (xdr.ScVal, error) {
	switch v.Type {
	case stellartypes.ScValTypeBool:
		if v.Bool == nil {
			return xdr.ScVal{}, fmt.Errorf("missing bool value")
		}
		b := xdr.ScValTypeScvBool
		val := *v.Bool
		return xdr.ScVal{Type: b, B: &val}, nil
	case stellartypes.ScValTypeVoid:
		b := xdr.ScValTypeScvVoid
		return xdr.ScVal{Type: b}, nil
	case stellartypes.ScValTypeError:
		if v.Error == nil {
			return xdr.ScVal{}, fmt.Errorf("missing error value")
		}
		b := xdr.ScValTypeScvError
		scErr := xdr.ScError{
			Type: xdr.ScErrorType(v.Error.Type),
		}
		if v.Error.ContractCode != nil {
			code := xdr.Uint32(*v.Error.ContractCode)
			scErr.ContractCode = &code
		} else if v.Error.Code != nil {
			code := xdr.ScErrorCode(*v.Error.Code)
			scErr.Code = &code
		}
		return xdr.ScVal{Type: b, Error: &scErr}, nil
	case stellartypes.ScValTypeU32:
		if v.U32 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing u32 value")
		}
		val := xdr.Uint32(*v.U32)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &val}, nil
	case stellartypes.ScValTypeI32:
		if v.I32 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing i32 value")
		}
		val := xdr.Int32(*v.I32)
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &val}, nil
	case stellartypes.ScValTypeU64:
		if v.U64 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing u64 value")
		}
		val := xdr.Uint64(*v.U64)
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &val}, nil
	case stellartypes.ScValTypeI64:
		if v.I64 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing i64 value")
		}
		val := xdr.Int64(*v.I64)
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &val}, nil
	case stellartypes.ScValTypeTimepoint:
		if v.Timepoint == nil {
			return xdr.ScVal{}, fmt.Errorf("missing timepoint value")
		}
		val := xdr.TimePoint(*v.Timepoint)
		return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &val}, nil
	case stellartypes.ScValTypeDuration:
		if v.Duration == nil {
			return xdr.ScVal{}, fmt.Errorf("missing duration value")
		}
		val := xdr.Duration(*v.Duration)
		return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &val}, nil
	case stellartypes.ScValTypeU128:
		if v.U128 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing u128 value")
		}
		val := xdr.UInt128Parts{
			Hi: xdr.Uint64(v.U128.Hi),
			Lo: xdr.Uint64(v.U128.Lo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &val}, nil
	case stellartypes.ScValTypeI128:
		if v.I128 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing i128 value")
		}
		val := xdr.Int128Parts{
			Hi: xdr.Int64(v.I128.Hi),
			Lo: xdr.Uint64(v.I128.Lo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &val}, nil
	case stellartypes.ScValTypeU256:
		if v.U256 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing u256 value")
		}
		val := xdr.UInt256Parts{
			HiHi: xdr.Uint64(v.U256.HiHi),
			HiLo: xdr.Uint64(v.U256.HiLo),
			LoHi: xdr.Uint64(v.U256.LoHi),
			LoLo: xdr.Uint64(v.U256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &val}, nil
	case stellartypes.ScValTypeI256:
		if v.I256 == nil {
			return xdr.ScVal{}, fmt.Errorf("missing i256 value")
		}
		val := xdr.Int256Parts{
			HiHi: xdr.Int64(v.I256.HiHi),
			HiLo: xdr.Uint64(v.I256.HiLo),
			LoHi: xdr.Uint64(v.I256.LoHi),
			LoLo: xdr.Uint64(v.I256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &val}, nil
	case stellartypes.ScValTypeBytes:
		val := xdr.ScBytes(v.Bytes)
		return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &val}, nil
	case stellartypes.ScValTypeString:
		if v.String == nil {
			return xdr.ScVal{}, fmt.Errorf("missing string value")
		}
		val := xdr.ScString(*v.String)
		return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &val}, nil
	case stellartypes.ScValTypeSymbol:
		if v.Symbol == nil {
			return xdr.ScVal{}, fmt.Errorf("missing symbol value")
		}
		val := xdr.ScSymbol(*v.Symbol)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &val}, nil
	case stellartypes.ScValTypeVec:
		if v.Vec == nil {
			return xdr.ScVal{}, fmt.Errorf("missing vec value")
		}
		var vec xdr.ScVec
		for _, item := range v.Vec.Values {
			xdrItem, err := toXDRScVal(*item)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("error converting vec item: %w", err)
			}
			vec = append(vec, xdrItem)
		}
		var scvVec *xdr.ScVec
		if len(vec) > 0 {
			scvVec = &vec
		} else {
			emptyVec := xdr.ScVec{}
			scvVec = &emptyVec
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &scvVec}, nil
	case stellartypes.ScValTypeMap:
		if v.Map == nil {
			return xdr.ScVal{}, fmt.Errorf("missing map value")
		}
		var scMap xdr.ScMap
		for _, entry := range v.Map.Entries {
			key, err := toXDRScVal(*entry.Key)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("error converting map key: %w", err)
			}
			val, err := toXDRScVal(*entry.Val)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("error converting map value: %w", err)
			}
			scMap = append(scMap, xdr.ScMapEntry{Key: key, Val: val})
		}
		var scvMap *xdr.ScMap
		if len(scMap) > 0 {
			scvMap = &scMap
		} else {
			emptyMap := xdr.ScMap{}
			scvMap = &emptyMap
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &scvMap}, nil
	case stellartypes.ScValTypeAddress:
		if v.Address == nil {
			return xdr.ScVal{}, fmt.Errorf("missing address value")
		}
		addr, err := toXDRScAddress(*v.Address)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}, nil
	case stellartypes.ScValTypeContractInstance:
		// Not implementing unless needed, as it is complex and rare for contract arguments
		return xdr.ScVal{}, fmt.Errorf("ContractInstance conversion not implemented")
	case stellartypes.ScValTypeLedgerKeyContractInstance:
		return xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance}, nil
	case stellartypes.ScValTypeNonceKey:
		if v.NonceKey == nil {
			return xdr.ScVal{}, fmt.Errorf("missing nonce key")
		}
		nk := xdr.ScNonceKey{Nonce: xdr.Int64(v.NonceKey.Nonce)}
		return xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyNonce, NonceKey: &nk}, nil
	default:
		return xdr.ScVal{}, fmt.Errorf("unknown ScValType: %v", v.Type)
	}
}

func toXDRScAddress(v stellartypes.ScAddress) (xdr.ScAddress, error) {
	switch v.Type {
	case stellartypes.ScAddressTypeAccountID:
		if len(v.AccountID) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("AccountID must be 32 bytes")
		}
		var pubKey xdr.Uint256
		copy(pubKey[:], v.AccountID)
		accountID := xdr.AccountId{
			Type:    xdr.PublicKeyTypePublicKeyTypeEd25519,
			Ed25519: &pubKey,
		}
		return xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &accountID,
		}, nil
	case stellartypes.ScAddressTypeContractID:
		if len(v.ContractID) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("ContractID must be 32 bytes")
		}
		var hash xdr.Hash
		copy(hash[:], v.ContractID)
		cid := xdr.ContractId(hash)
		return xdr.ScAddress{
			Type:       xdr.ScAddressTypeScAddressTypeContract,
			ContractId: &cid,
		}, nil
	default:
		return xdr.ScAddress{}, fmt.Errorf("unsupported ScAddress type for now: %v", v.Type)
	}
}
