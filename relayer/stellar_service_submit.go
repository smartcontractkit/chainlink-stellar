package relayer

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"

	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
)

// SubmitTransaction invokes a Soroban contract via the TXM pipeline.
// It converts the high-level domain request to an InvokeHostFunction operation,
// enqueues it through the TXM (which handles simulation, sequence management,
// signing, fee bumping, and on-chain confirmation), and returns the result.
func (s *stellarService) SubmitTransaction(ctx context.Context, req stellartypes.SubmitTransactionRequest) (*stellartypes.SubmitTransactionResponse, error) {
	if req.ContractID == "" {
		return nil, fmt.Errorf("SubmitTransaction: contract_id is required")
	}
	if req.Function == "" {
		return nil, fmt.Errorf("SubmitTransaction: function is required")
	}

	xdrArgs, err := domainScValsToXDR(req.Args)
	if err != nil {
		return nil, fmt.Errorf("SubmitTransaction: convert args: %w", err)
	}

	op, err := txm.BuildInvokeContractOperation(req.ContractID, req.Function, xdrArgs, req.FromAddress)
	if err != nil {
		return nil, fmt.Errorf("SubmitTransaction: build operation: %w", err)
	}

	result, err := s.txMgr.EnqueueAndWait(ctx, txm.TxRequest{
		ID:                 req.IdempotencyKey,
		FromAddress:        req.FromAddress,
		Operations:         []txnbuild.Operation{op},
		LedgerBoundsOffset: req.LedgerBoundsOffset,
	})
	if err != nil {
		return nil, fmt.Errorf("SubmitTransaction: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("SubmitTransaction: nil result returned by TXM")
	}

	reply := &stellartypes.SubmitTransactionResponse{
		TxHash:           result.Hash,
		TxIdempotencyKey: result.ID,
		ResultXDR:        result.ResultXDR,
		ResultMetaXDR:    result.ResultMetaXDR,
	}

	switch result.Status {
	case commontypes.Finalized:
		reply.TxStatus = stellartypes.TxSuccess
	case commontypes.Failed:
		reply.TxStatus = stellartypes.TxFailed
		if result.Error != nil {
			return reply, result.Error
		}
	default:
		reply.TxStatus = stellartypes.TxFatal
		if result.Error != nil {
			return reply, result.Error
		}
		return reply, fmt.Errorf("SubmitTransaction: unexpected terminal status %v", result.Status)
	}

	return reply, nil
}

// domainScValsToXDR converts a slice of domain ScVal to xdr.ScVal.
func domainScValsToXDR(vals []stellartypes.ScVal) ([]xdr.ScVal, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	out := make([]xdr.ScVal, len(vals))
	for i, v := range vals {
		x, err := domainScValToXDR(v, 0)
		if err != nil {
			return nil, fmt.Errorf("arg[%d]: %w", i, err)
		}
		out[i] = x
	}
	return out, nil
}

const maxScValDepth = 64

func domainScValToXDR(v stellartypes.ScVal, depth int) (xdr.ScVal, error) {
	if depth > maxScValDepth {
		return xdr.ScVal{}, fmt.Errorf("ScVal nesting exceeds maximum depth of %d", maxScValDepth)
	}
	switch v.Type {
	case stellartypes.ScValTypeBool:
		if v.Bool == nil {
			return xdr.ScVal{}, fmt.Errorf("scvBool: nil")
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: v.Bool}, nil

	case stellartypes.ScValTypeVoid:
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil

	case stellartypes.ScValTypeError:
		if v.Error == nil {
			return xdr.ScVal{}, fmt.Errorf("scvError: nil")
		}
		xe, err := domainScErrorToXDR(v.Error)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvError, Error: &xe}, nil

	case stellartypes.ScValTypeU32:
		if v.U32 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvU32: nil")
		}
		u := xdr.Uint32(*v.U32)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil

	case stellartypes.ScValTypeI32:
		if v.I32 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvI32: nil")
		}
		i := xdr.Int32(*v.I32)
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &i}, nil

	case stellartypes.ScValTypeU64:
		if v.U64 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvU64: nil")
		}
		u := xdr.Uint64(*v.U64)
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}, nil

	case stellartypes.ScValTypeI64:
		if v.I64 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvI64: nil")
		}
		i := xdr.Int64(*v.I64)
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &i}, nil

	case stellartypes.ScValTypeTimepoint:
		if v.Timepoint == nil {
			return xdr.ScVal{}, fmt.Errorf("scvTimepoint: nil")
		}
		tp := xdr.TimePoint(*v.Timepoint)
		return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &tp}, nil

	case stellartypes.ScValTypeDuration:
		if v.Duration == nil {
			return xdr.ScVal{}, fmt.Errorf("scvDuration: nil")
		}
		d := xdr.Duration(*v.Duration)
		return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &d}, nil

	case stellartypes.ScValTypeU128:
		if v.U128 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvU128: nil")
		}
		parts := xdr.UInt128Parts{Hi: xdr.Uint64(v.U128.Hi), Lo: xdr.Uint64(v.U128.Lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &parts}, nil

	case stellartypes.ScValTypeI128:
		if v.I128 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvI128: nil")
		}
		parts := xdr.Int128Parts{Hi: xdr.Int64(v.I128.Hi), Lo: xdr.Uint64(v.I128.Lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &parts}, nil

	case stellartypes.ScValTypeU256:
		if v.U256 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvU256: nil")
		}
		parts := xdr.UInt256Parts{
			HiHi: xdr.Uint64(v.U256.HiHi), HiLo: xdr.Uint64(v.U256.HiLo),
			LoHi: xdr.Uint64(v.U256.LoHi), LoLo: xdr.Uint64(v.U256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &parts}, nil

	case stellartypes.ScValTypeI256:
		if v.I256 == nil {
			return xdr.ScVal{}, fmt.Errorf("scvI256: nil")
		}
		parts := xdr.Int256Parts{
			HiHi: xdr.Int64(v.I256.HiHi), HiLo: xdr.Uint64(v.I256.HiLo),
			LoHi: xdr.Uint64(v.I256.LoHi), LoLo: xdr.Uint64(v.I256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &parts}, nil

	case stellartypes.ScValTypeBytes:
		b := xdr.ScBytes(v.Bytes)
		return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}, nil

	case stellartypes.ScValTypeString:
		if v.String == nil {
			return xdr.ScVal{}, fmt.Errorf("scvString: nil")
		}
		s := xdr.ScString(*v.String)
		return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &s}, nil

	case stellartypes.ScValTypeSymbol:
		if v.Symbol == nil {
			return xdr.ScVal{}, fmt.Errorf("scvSymbol: nil")
		}
		sym := xdr.ScSymbol(*v.Symbol)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}, nil

	case stellartypes.ScValTypeVec:
		if v.Vec == nil {
			return xdr.ScVal{}, fmt.Errorf("scvVec: nil")
		}
		elems := make([]xdr.ScVal, len(v.Vec.Values))
		for i, elem := range v.Vec.Values {
			if elem == nil {
				return xdr.ScVal{}, fmt.Errorf("vec[%d]: nil element", i)
			}
			xe, err := domainScValToXDR(*elem, depth+1)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("vec[%d]: %w", i, err)
			}
			elems[i] = xe
		}
		vec := xdr.ScVec(elems)
		vecPtr := &vec
		return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}, nil

	case stellartypes.ScValTypeMap:
		if v.Map == nil {
			return xdr.ScVal{}, fmt.Errorf("scvMap: nil")
		}
		entries := make(xdr.ScMap, len(v.Map.Entries))
		for i, entry := range v.Map.Entries {
			if entry.Key == nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d].key: nil", i)
			}
			if entry.Val == nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d].val: nil", i)
			}
			xk, err := domainScValToXDR(*entry.Key, depth+1)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d].key: %w", i, err)
			}
			xv, err := domainScValToXDR(*entry.Val, depth+1)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map[%d].val: %w", i, err)
			}
			entries[i] = xdr.ScMapEntry{Key: xk, Val: xv}
		}
		scm := entries
		scmPtr := &scm
		return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &scmPtr}, nil

	case stellartypes.ScValTypeAddress:
		if v.Address == nil {
			return xdr.ScVal{}, fmt.Errorf("scvAddress: nil")
		}
		xa, err := domainScAddressToXDR(v.Address)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &xa}, nil

	case stellartypes.ScValTypeLedgerKeyContractInstance:
		return xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance}, nil

	case stellartypes.ScValTypeNonceKey:
		if v.NonceKey == nil {
			return xdr.ScVal{}, fmt.Errorf("scvNonceKey: nil")
		}
		nk := xdr.ScNonceKey{Nonce: xdr.Int64(v.NonceKey.Nonce)}
		return xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyNonce, NonceKey: &nk}, nil

	default:
		return xdr.ScVal{}, fmt.Errorf("unsupported ScVal type: %d", v.Type)
	}
}

func domainScErrorToXDR(e *stellartypes.ScError) (xdr.ScError, error) {
	xe := xdr.ScError{Type: xdr.ScErrorType(e.Type)}
	switch e.Type {
	case stellartypes.ScErrorTypeContract:
		if e.ContractCode == nil {
			return xdr.ScError{}, fmt.Errorf("scError contract code: nil")
		}
		xe.Code = (*xdr.ScErrorCode)(nil)
		xe.ContractCode = (*xdr.Uint32)(e.ContractCode)
	default:
		if e.Code == nil {
			return xdr.ScError{}, fmt.Errorf("scError code: nil")
		}
		c := xdr.ScErrorCode(*e.Code)
		xe.Code = &c
	}
	return xe, nil
}

func domainScAddressToXDR(a *stellartypes.ScAddress) (xdr.ScAddress, error) {
	switch a.Type {
	case stellartypes.ScAddressTypeAccountID:
		key, err := xdr.NewPublicKey(xdr.PublicKeyTypePublicKeyTypeEd25519, xdr.Uint256(xdr.Uint256(toUint256(a.AccountID))))
		if err != nil {
			return xdr.ScAddress{}, fmt.Errorf("account pubkey: %w", err)
		}
		acc := xdr.AccountId(key)
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &acc}, nil

	case stellartypes.ScAddressTypeContractID:
		if len(a.ContractID) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("contract id must be 32 bytes, got %d", len(a.ContractID))
		}
		var contractID xdr.ContractId
		copy(contractID[:], a.ContractID)
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID}, nil

	default:
		return xdr.ScAddress{}, fmt.Errorf("unsupported ScAddress type: %d", a.Type)
	}
}

// toUint256 pads or truncates a byte slice to exactly 32 bytes (big-endian).
func toUint256(b []byte) [32]byte {
	var out [32]byte
	if len(b) >= 32 {
		copy(out[:], b[len(b)-32:])
	} else {
		copy(out[32-len(b):], b)
	}
	return out
}

