package fakes

import (
	"errors"
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar/scval"
)

// maxScValDepth bounds recursion when converting nested vec/map arguments.
const maxScValDepth = 64

// scValsToXDR converts workflow-supplied contract arguments to Soroban XDR.
func scValsToXDR(vals []*scval.ScVal) ([]xdr.ScVal, error) {
	out := make([]xdr.ScVal, 0, len(vals))
	for i, v := range vals {
		x, err := scValToXDRAt(v, 0)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out = append(out, x)
	}
	return out, nil
}

// scValToXDRAt converts a proto ScVal to its XDR form. Only the variants that
// can appear as contract-call arguments are supported; ledger-only variants
// (contract instances, nonce keys, errors) are rejected.
func scValToXDRAt(v *scval.ScVal, depth int) (xdr.ScVal, error) {
	if depth > maxScValDepth {
		return xdr.ScVal{}, fmt.Errorf("scVal nesting exceeds maximum depth of %d", maxScValDepth)
	}
	if v == nil {
		return xdr.ScVal{}, errors.New("scVal is nil")
	}

	switch val := v.Value.(type) {
	case *scval.ScVal_B:
		b := val.B
		return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &b}, nil
	case *scval.ScVal_VoidVal:
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
	case *scval.ScVal_U32:
		u := xdr.Uint32(val.U32)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil
	case *scval.ScVal_I32:
		i := xdr.Int32(val.I32)
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &i}, nil
	case *scval.ScVal_U64:
		u := xdr.Uint64(val.U64)
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}, nil
	case *scval.ScVal_I64:
		i := xdr.Int64(val.I64)
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &i}, nil
	case *scval.ScVal_Timepoint:
		t := xdr.TimePoint(val.Timepoint)
		return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &t}, nil
	case *scval.ScVal_Duration:
		d := xdr.Duration(val.Duration)
		return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &d}, nil
	case *scval.ScVal_U128:
		if val.U128 == nil {
			return xdr.ScVal{}, errors.New("scvU128: nil")
		}
		p := xdr.UInt128Parts{Hi: xdr.Uint64(val.U128.Hi), Lo: xdr.Uint64(val.U128.Lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &p}, nil
	case *scval.ScVal_I128:
		if val.I128 == nil {
			return xdr.ScVal{}, errors.New("scvI128: nil")
		}
		p := xdr.Int128Parts{Hi: xdr.Int64(val.I128.Hi), Lo: xdr.Uint64(val.I128.Lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &p}, nil
	case *scval.ScVal_U256:
		if val.U256 == nil {
			return xdr.ScVal{}, errors.New("scvU256: nil")
		}
		p := xdr.UInt256Parts{
			HiHi: xdr.Uint64(val.U256.HiHi),
			HiLo: xdr.Uint64(val.U256.HiLo),
			LoHi: xdr.Uint64(val.U256.LoHi),
			LoLo: xdr.Uint64(val.U256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &p}, nil
	case *scval.ScVal_I256:
		if val.I256 == nil {
			return xdr.ScVal{}, errors.New("scvI256: nil")
		}
		p := xdr.Int256Parts{
			HiHi: xdr.Int64(val.I256.HiHi),
			HiLo: xdr.Uint64(val.I256.HiLo),
			LoHi: xdr.Uint64(val.I256.LoHi),
			LoLo: xdr.Uint64(val.I256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &p}, nil
	case *scval.ScVal_BytesVal:
		b := xdr.ScBytes(append([]byte(nil), val.BytesVal...))
		return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}, nil
	case *scval.ScVal_Str:
		s := xdr.ScString(val.Str)
		return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &s}, nil
	case *scval.ScVal_Sym:
		s := xdr.ScSymbol(val.Sym)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &s}, nil
	case *scval.ScVal_Vec:
		var items []*scval.ScVal
		if val.Vec != nil {
			items = val.Vec.Values
		}
		vec := make(xdr.ScVec, 0, len(items))
		for i, item := range items {
			x, err := scValToXDRAt(item, depth+1)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("vec[%d]: %w", i, err)
			}
			vec = append(vec, x)
		}
		vp := &vec
		return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vp}, nil
	case *scval.ScVal_Map:
		var entries []*scval.ScMapEntry
		if val.Map != nil {
			entries = val.Map.Entries
		}
		m := make(xdr.ScMap, 0, len(entries))
		for i, e := range entries {
			if e == nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d]: entry is nil", i)
			}
			k, err := scValToXDRAt(e.Key, depth+1)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d] key: %w", i, err)
			}
			mv, err := scValToXDRAt(e.Val, depth+1)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d] val: %w", i, err)
			}
			m = append(m, xdr.ScMapEntry{Key: k, Val: mv})
		}
		mp := &m
		return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mp}, nil
	case *scval.ScVal_Address:
		addr, err := scAddressToXDR(val.Address)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}, nil
	default:
		return xdr.ScVal{}, fmt.Errorf("unsupported scVal type %T for contract arguments", v.Value)
	}
}

func scAddressToXDR(a *scval.ScAddress) (xdr.ScAddress, error) {
	if a == nil {
		return xdr.ScAddress{}, errors.New("scvAddress: nil")
	}
	switch addr := a.Address.(type) {
	case *scval.ScAddress_AccountId:
		key, err := uint256(addr.AccountId, "account_id")
		if err != nil {
			return xdr.ScAddress{}, err
		}
		accountID := xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &key}
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &accountID}, nil
	case *scval.ScAddress_ContractId:
		h, err := hash(addr.ContractId, "contract_id")
		if err != nil {
			return xdr.ScAddress{}, err
		}
		contractID := xdr.ContractId(h)
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID}, nil
	case *scval.ScAddress_MuxedAccount:
		if addr.MuxedAccount == nil {
			return xdr.ScAddress{}, errors.New("muxed_account: nil")
		}
		key, err := uint256(addr.MuxedAccount.Ed25519, "muxed_account.ed25519")
		if err != nil {
			return xdr.ScAddress{}, err
		}
		muxed := xdr.MuxedEd25519Account{Id: xdr.Uint64(addr.MuxedAccount.Id), Ed25519: key}
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeMuxedAccount, MuxedAccount: &muxed}, nil
	default:
		return xdr.ScAddress{}, fmt.Errorf("unsupported scAddress type %T for contract arguments", a.Address)
	}
}

func uint256(b []byte, field string) (xdr.Uint256, error) {
	var out xdr.Uint256
	if len(b) != len(out) {
		return out, fmt.Errorf("%s must be %d bytes, got %d", field, len(out), len(b))
	}
	copy(out[:], b)
	return out, nil
}

func hash(b []byte, field string) (xdr.Hash, error) {
	var out xdr.Hash
	if len(b) != len(out) {
		return out, fmt.Errorf("%s must be %d bytes, got %d", field, len(out), len(b))
	}
	copy(out[:], b)
	return out, nil
}

// contractAddress decodes a C… StrKey into an XDR contract address.
func contractAddress(contractID string) (xdr.ScAddress, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("invalid Stellar contract address %q: %w", contractID, err)
	}
	h, err := hash(raw, "contract address")
	if err != nil {
		return xdr.ScAddress{}, err
	}
	id := xdr.ContractId(h)
	return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id}, nil
}

// accountAddress decodes a G… StrKey into an XDR account address.
func accountAddress(accountID string) (xdr.ScAddress, error) {
	raw, err := strkey.Decode(strkey.VersionByteAccountID, accountID)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("invalid Stellar account %q: %w", accountID, err)
	}
	key, err := uint256(raw, "account")
	if err != nil {
		return xdr.ScAddress{}, err
	}
	id := xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &key}
	return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &id}, nil
}
