package relayer

import (
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

func convertScValFromDomain(v stellartypes.ScVal) (xdr.ScVal, error) {
	switch v.Type {
	case stellartypes.ScValTypeBool:
		if v.Bool == nil {
			return xdr.ScVal{}, errNilScValField("Bool")
		}
		b := *v.Bool
		return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &b}, nil

	case stellartypes.ScValTypeVoid:
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil

	case stellartypes.ScValTypeU32:
		if v.U32 == nil {
			return xdr.ScVal{}, errNilScValField("U32")
		}
		u := xdr.Uint32(*v.U32)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u}, nil

	case stellartypes.ScValTypeI32:
		if v.I32 == nil {
			return xdr.ScVal{}, errNilScValField("I32")
		}
		i := xdr.Int32(*v.I32)
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &i}, nil

	case stellartypes.ScValTypeU64:
		if v.U64 == nil {
			return xdr.ScVal{}, errNilScValField("U64")
		}
		u := xdr.Uint64(*v.U64)
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &u}, nil

	case stellartypes.ScValTypeI64:
		if v.I64 == nil {
			return xdr.ScVal{}, errNilScValField("I64")
		}
		i := xdr.Int64(*v.I64)
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &i}, nil

	case stellartypes.ScValTypeTimepoint:
		if v.Timepoint == nil {
			return xdr.ScVal{}, errNilScValField("Timepoint")
		}
		t := xdr.TimePoint(*v.Timepoint)
		return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &t}, nil

	case stellartypes.ScValTypeDuration:
		if v.Duration == nil {
			return xdr.ScVal{}, errNilScValField("Duration")
		}
		d := xdr.Duration(*v.Duration)
		return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &d}, nil

	case stellartypes.ScValTypeU128:
		if v.U128 == nil {
			return xdr.ScVal{}, errNilScValField("U128")
		}
		p := xdr.UInt128Parts{Hi: xdr.Uint64(v.U128.Hi), Lo: xdr.Uint64(v.U128.Lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &p}, nil

	case stellartypes.ScValTypeI128:
		if v.I128 == nil {
			return xdr.ScVal{}, errNilScValField("I128")
		}
		p := xdr.Int128Parts{Hi: xdr.Int64(v.I128.Hi), Lo: xdr.Uint64(v.I128.Lo)}
		return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &p}, nil

	case stellartypes.ScValTypeU256:
		if v.U256 == nil {
			return xdr.ScVal{}, errNilScValField("U256")
		}
		p := xdr.UInt256Parts{
			HiHi: xdr.Uint64(v.U256.HiHi),
			HiLo: xdr.Uint64(v.U256.HiLo),
			LoHi: xdr.Uint64(v.U256.LoHi),
			LoLo: xdr.Uint64(v.U256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &p}, nil

	case stellartypes.ScValTypeI256:
		if v.I256 == nil {
			return xdr.ScVal{}, errNilScValField("I256")
		}
		p := xdr.Int256Parts{
			HiHi: xdr.Int64(v.I256.HiHi),
			HiLo: xdr.Uint64(v.I256.HiLo),
			LoHi: xdr.Uint64(v.I256.LoHi),
			LoLo: xdr.Uint64(v.I256.LoLo),
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &p}, nil

	case stellartypes.ScValTypeBytes:
		b := xdr.ScBytes(v.Bytes)
		return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}, nil

	case stellartypes.ScValTypeString:
		if v.String == nil {
			return xdr.ScVal{}, errNilScValField("String")
		}
		s := xdr.ScString(*v.String)
		return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &s}, nil

	case stellartypes.ScValTypeSymbol:
		if v.Symbol == nil {
			return xdr.ScVal{}, errNilScValField("Symbol")
		}
		s := xdr.ScSymbol(*v.Symbol)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &s}, nil

	case stellartypes.ScValTypeVec:
		if v.Vec == nil {
			return xdr.ScVal{}, errNilScValField("Vec")
		}
		items := make([]xdr.ScVal, len(v.Vec.Values))
		for i, e := range v.Vec.Values {
			if e == nil {
				return xdr.ScVal{}, fmt.Errorf("vec element %d is nil", i)
			}
			c, err := convertScValFromDomain(*e)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("vec element %d: %w", i, err)
			}
			items[i] = c
		}
		vec := xdr.ScVec(items)
		vecPtr := &vec
		return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}, nil

	case stellartypes.ScValTypeMap:
		if v.Map == nil {
			return xdr.ScVal{}, errNilScValField("Map")
		}
		entries := make([]xdr.ScMapEntry, len(v.Map.Entries))
		for i, e := range v.Map.Entries {
			if e.Key == nil || e.Val == nil {
				return xdr.ScVal{}, fmt.Errorf("map entry %d has nil key or val", i)
			}
			k, err := convertScValFromDomain(*e.Key)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map entry %d key: %w", i, err)
			}
			val, err := convertScValFromDomain(*e.Val)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("map entry %d val: %w", i, err)
			}
			entries[i] = xdr.ScMapEntry{Key: k, Val: val}
		}
		m := xdr.ScMap(entries)
		mapPtr := &m
		return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mapPtr}, nil

	case stellartypes.ScValTypeAddress:
		if v.Address == nil {
			return xdr.ScVal{}, errNilScValField("Address")
		}
		addr, err := convertAddressToDomain(*v.Address)
		if err != nil {
			return xdr.ScVal{}, err
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}, nil

	default:
		return xdr.ScVal{}, fmt.Errorf("unsupported ScVal type %d as contract argument", v.Type)
	}
}

func convertGetEventsRequestsFromDomain(req stellartypes.GetEventsRequest) (protocol.GetEventsRequest, error) {
	if req.StartLedger != 0 && req.EndLedger != 0 && req.StartLedger > req.EndLedger {
		return protocol.GetEventsRequest{}, fmt.Errorf(
			"invalid ledger range: startLedger (%d) must be <= endLedger (%d)",
			req.StartLedger,
			req.EndLedger,
		)
	}

	filters := make([]protocol.EventFilter, len(req.Filters))
	for i, f := range req.Filters {
		filter, err := convertEventFilterFromDomain(f)
		if err != nil {
			return protocol.GetEventsRequest{}, fmt.Errorf("filters[%d]: %w", i, err)
		}
		filters[i] = filter
	}

	out := protocol.GetEventsRequest{
		StartLedger: req.StartLedger,
		EndLedger:   req.EndLedger,
		Filters:     filters,
		Format:      protocol.FormatBase64,
	}

	if req.Pagination != nil {
		pagination := &protocol.PaginationOptions{
			Limit: uint(req.Pagination.Limit),
		}

		if req.Pagination.Cursor != "" {
			cursor, err := protocol.ParseCursor(req.Pagination.Cursor)
			if err != nil {
				return protocol.GetEventsRequest{}, fmt.Errorf("pagination.cursor: %w", err)
			}
			pagination.Cursor = &cursor
		}

		out.Pagination = pagination
	}

	return out, nil
}

func convertEventFilterFromDomain(f stellartypes.EventFilter) (protocol.EventFilter, error) {
	eventTypes := protocol.EventTypeSet(nil)
	if len(f.EventTypes) > 0 {
		eventTypes = make(protocol.EventTypeSet, len(f.EventTypes))
		for i, eventType := range f.EventTypes {
			var et string
			switch eventType {
			case stellartypes.EventTypeSystem:
				et = protocol.EventTypeSystem
			case stellartypes.EventTypeContract:
				et = protocol.EventTypeContract
			default:
				return protocol.EventFilter{}, fmt.Errorf("eventTypes[%d]: unsupported event type: %v", i, eventType)
			}
			eventTypes[et] = nil
		}
	}

	topics := make([]protocol.TopicFilter, len(f.Topics))
	for i, topic := range f.Topics {
		protocolTopic, err := convertTopicFilterFromDomain(topic)
		if err != nil {
			return protocol.EventFilter{}, fmt.Errorf("topics[%d]: %w", i, err)
		}
		topics[i] = protocolTopic
	}

	return protocol.EventFilter{
		EventType:   eventTypes,
		ContractIDs: append([]string(nil), f.ContractIDs...),
		Topics:      topics,
	}, nil
}

func convertTopicFilterFromDomain(topic stellartypes.TopicFilter) (protocol.TopicFilter, error) {
	if len(topic.Segments) == 0 {
		return nil, fmt.Errorf("topic filter must have at least one segment")
	}

	segments := make(protocol.TopicFilter, len(topic.Segments))

	for i, segment := range topic.Segments {
		switch {
		case segment.Wildcard != nil && segment.Value != nil:
			return nil, fmt.Errorf("segments[%d]: topic segment cannot set both wildcard and value", i)

		case segment.Wildcard == nil && segment.Value == nil:
			return nil, fmt.Errorf("segments[%d]: topic segment must set either wildcard or value", i)

		case segment.Wildcard != nil:
			if *segment.Wildcard != protocol.WildCardExactOne &&
				*segment.Wildcard != protocol.WildCardZeroOrMore {
				return nil, fmt.Errorf("segments[%d]: wildcard must be '*' or '**'", i)
			}

			segments[i] = protocol.SegmentFilter{
				Wildcard: segment.Wildcard,
			}

		case segment.Value != nil:
			value, err := convertScValFromDomain(*segment.Value)
			if err != nil {
				return nil, fmt.Errorf("segments[%d]: value: %w", i, err)
			}

			segments[i] = protocol.SegmentFilter{
				ScVal: &value,
			}
		}
	}

	return segments, nil
}

func decodeScValBase64ToDomain(encoded string) (stellartypes.ScVal, error) {
	var value xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(encoded, &value); err != nil {
		return stellartypes.ScVal{}, err
	}
	return convertScValToDomain(value)
}

func convertScValToDomain(value xdr.ScVal) (stellartypes.ScVal, error) {
	switch value.Type {
	case xdr.ScValTypeScvBool:
		if value.B == nil {
			return stellartypes.ScVal{}, fmt.Errorf("bool value is nil")
		}
		v := *value.B
		return stellartypes.ScVal{Type: stellartypes.ScValTypeBool, Bool: &v}, nil

	case xdr.ScValTypeScvVoid:
		return stellartypes.ScVal{Type: stellartypes.ScValTypeVoid, Void: &stellartypes.Void{}}, nil

	case xdr.ScValTypeScvError:
		if value.Error == nil {
			return stellartypes.ScVal{}, fmt.Errorf("error value is nil")
		}
		converted, err := convertScErrorToDomain(*value.Error)
		if err != nil {
			return stellartypes.ScVal{}, err
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeError, Error: converted}, nil

	case xdr.ScValTypeScvU32:
		if value.U32 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("u32 value is nil")
		}
		v := uint32(*value.U32)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeU32, U32: &v}, nil

	case xdr.ScValTypeScvI32:
		if value.I32 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("i32 value is nil")
		}
		v := int32(*value.I32)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeI32, I32: &v}, nil

	case xdr.ScValTypeScvU64:
		if value.U64 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("u64 value is nil")
		}
		v := uint64(*value.U64)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeU64, U64: &v}, nil

	case xdr.ScValTypeScvI64:
		if value.I64 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("i64 value is nil")
		}
		v := int64(*value.I64)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeI64, I64: &v}, nil

	case xdr.ScValTypeScvTimepoint:
		if value.Timepoint == nil {
			return stellartypes.ScVal{}, fmt.Errorf("timepoint value is nil")
		}
		v := uint64(*value.Timepoint)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeTimepoint, Timepoint: &v}, nil

	case xdr.ScValTypeScvDuration:
		if value.Duration == nil {
			return stellartypes.ScVal{}, fmt.Errorf("duration value is nil")
		}
		v := uint64(*value.Duration)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeDuration, Duration: &v}, nil

	case xdr.ScValTypeScvU128:
		if value.U128 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("u128 value is nil")
		}
		v := &stellartypes.UInt128Parts{
			Hi: uint64(value.U128.Hi),
			Lo: uint64(value.U128.Lo),
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeU128, U128: v}, nil

	case xdr.ScValTypeScvI128:
		if value.I128 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("i128 value is nil")
		}
		v := &stellartypes.Int128Parts{
			Hi: int64(value.I128.Hi),
			Lo: uint64(value.I128.Lo),
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeI128, I128: v}, nil

	case xdr.ScValTypeScvU256:
		if value.U256 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("u256 value is nil")
		}
		v := &stellartypes.UInt256Parts{
			HiHi: uint64(value.U256.HiHi),
			HiLo: uint64(value.U256.HiLo),
			LoHi: uint64(value.U256.LoHi),
			LoLo: uint64(value.U256.LoLo),
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeU256, U256: v}, nil

	case xdr.ScValTypeScvI256:
		if value.I256 == nil {
			return stellartypes.ScVal{}, fmt.Errorf("i256 value is nil")
		}
		v := &stellartypes.Int256Parts{
			HiHi: int64(value.I256.HiHi),
			HiLo: uint64(value.I256.HiLo),
			LoHi: uint64(value.I256.LoHi),
			LoLo: uint64(value.I256.LoLo),
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeI256, I256: v}, nil

	case xdr.ScValTypeScvBytes:
		if value.Bytes == nil {
			return stellartypes.ScVal{}, fmt.Errorf("bytes value is nil")
		}
		return stellartypes.ScVal{
			Type:  stellartypes.ScValTypeBytes,
			Bytes: append([]byte(nil), []byte(*value.Bytes)...),
		}, nil

	case xdr.ScValTypeScvString:
		if value.Str == nil {
			return stellartypes.ScVal{}, fmt.Errorf("string value is nil")
		}
		v := string(*value.Str)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeString, String: &v}, nil

	case xdr.ScValTypeScvSymbol:
		if value.Sym == nil {
			return stellartypes.ScVal{}, fmt.Errorf("symbol value is nil")
		}
		v := string(*value.Sym)
		return stellartypes.ScVal{Type: stellartypes.ScValTypeSymbol, Symbol: &v}, nil

	case xdr.ScValTypeScvVec:
		if value.Vec == nil {
			return stellartypes.ScVal{}, fmt.Errorf("vec value is nil")
		}
		vec := &stellartypes.ScVec{}
		if *value.Vec != nil {
			values := make([]*stellartypes.ScVal, 0, len(**value.Vec))
			for i, item := range **value.Vec {
				converted, err := convertScValToDomain(item)
				if err != nil {
					return stellartypes.ScVal{}, fmt.Errorf("vec[%d]: %w", i, err)
				}
				values = append(values, &converted)
			}
			vec.Values = values
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeVec, Vec: vec}, nil

	case xdr.ScValTypeScvMap:
		if value.Map == nil {
			return stellartypes.ScVal{}, fmt.Errorf("map value is nil")
		}
		m := &stellartypes.ScMap{}
		if *value.Map != nil {
			entries, err := convertScMapToDomain(**value.Map)
			if err != nil {
				return stellartypes.ScVal{}, err
			}
			m.Entries = entries
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeMap, Map: m}, nil

	case xdr.ScValTypeScvAddress:
		if value.Address == nil {
			return stellartypes.ScVal{}, fmt.Errorf("address value is nil")
		}
		converted, err := convertScAddressToDomain(*value.Address)
		if err != nil {
			return stellartypes.ScVal{}, err
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeAddress, Address: converted}, nil

	case xdr.ScValTypeScvContractInstance:
		if value.Instance == nil {
			return stellartypes.ScVal{}, fmt.Errorf("contract instance value is nil")
		}
		converted, err := convertScContractInstanceToDomain(*value.Instance)
		if err != nil {
			return stellartypes.ScVal{}, err
		}
		return stellartypes.ScVal{Type: stellartypes.ScValTypeContractInstance, ContractInstance: converted}, nil

	case xdr.ScValTypeScvLedgerKeyContractInstance:
		return stellartypes.ScVal{
			Type:                      stellartypes.ScValTypeLedgerKeyContractInstance,
			LedgerKeyContractInstance: &stellartypes.Void{},
		}, nil

	case xdr.ScValTypeScvLedgerKeyNonce:
		if value.NonceKey == nil {
			return stellartypes.ScVal{}, fmt.Errorf("nonce key value is nil")
		}
		return stellartypes.ScVal{
			Type:     stellartypes.ScValTypeNonceKey,
			NonceKey: &stellartypes.ScNonceKey{Nonce: int64(value.NonceKey.Nonce)},
		}, nil

	default:
		return stellartypes.ScVal{}, fmt.Errorf("unsupported scval type: %v", value.Type)
	}
}

func convertScMapToDomain(m xdr.ScMap) ([]stellartypes.ScMapEntry, error) {
	entries := make([]stellartypes.ScMapEntry, len(m))
	for i, entry := range m {
		key, err := convertScValToDomain(entry.Key)
		if err != nil {
			return nil, fmt.Errorf("map[%d].key: %w", i, err)
		}
		val, err := convertScValToDomain(entry.Val)
		if err != nil {
			return nil, fmt.Errorf("map[%d].val: %w", i, err)
		}
		entries[i] = stellartypes.ScMapEntry{
			Key: &key,
			Val: &val,
		}
	}
	return entries, nil
}

func convertScAddressToDomain(value xdr.ScAddress) (*stellartypes.ScAddress, error) {
	switch value.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		if value.AccountId == nil {
			return nil, fmt.Errorf("account id is nil")
		}
		ed25519, ok := value.AccountId.GetEd25519()
		if !ok {
			return nil, fmt.Errorf("account id ed25519 arm is not set")
		}
		return &stellartypes.ScAddress{
			Type:      stellartypes.ScAddressTypeAccountID,
			AccountID: append([]byte(nil), ed25519[:]...),
		}, nil

	case xdr.ScAddressTypeScAddressTypeContract:
		if value.ContractId == nil {
			return nil, fmt.Errorf("contract id is nil")
		}
		hash := xdr.Hash(*value.ContractId)
		return &stellartypes.ScAddress{
			Type:       stellartypes.ScAddressTypeContractID,
			ContractID: append([]byte(nil), hash[:]...),
		}, nil

	case xdr.ScAddressTypeScAddressTypeMuxedAccount:
		if value.MuxedAccount == nil {
			return nil, fmt.Errorf("muxed account is nil")
		}
		return &stellartypes.ScAddress{
			Type: stellartypes.ScAddressTypeMuxedAccount,
			MuxedAccount: &stellartypes.MuxedEd25519Account{
				ID:      uint64(value.MuxedAccount.Id),
				Ed25519: append([]byte(nil), value.MuxedAccount.Ed25519[:]...),
			},
		}, nil

	case xdr.ScAddressTypeScAddressTypeClaimableBalance:
		if value.ClaimableBalanceId == nil {
			return nil, fmt.Errorf("claimable balance id is nil")
		}
		v0, ok := value.ClaimableBalanceId.GetV0()
		if !ok {
			return nil, fmt.Errorf("claimable balance v0 arm is not set")
		}
		return &stellartypes.ScAddress{
			Type: stellartypes.ScAddressTypeClaimableBalanceID,
			ClaimableBalance: &stellartypes.ClaimableBalanceID{
				V0: append([]byte(nil), v0[:]...),
			},
		}, nil

	case xdr.ScAddressTypeScAddressTypeLiquidityPool:
		if value.LiquidityPoolId == nil {
			return nil, fmt.Errorf("liquidity pool id is nil")
		}
		hash := xdr.Hash(*value.LiquidityPoolId)
		return &stellartypes.ScAddress{
			Type:            stellartypes.ScAddressTypeLiquidityPoolID,
			LiquidityPoolID: append([]byte(nil), hash[:]...),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported sc address type: %v", value.Type)
	}
}

func convertContractExecutableToDomain(value xdr.ContractExecutable) (*stellartypes.ContractExecutable, error) {
	switch value.Type {
	case xdr.ContractExecutableTypeContractExecutableWasm:
		if value.WasmHash == nil {
			return nil, fmt.Errorf("wasm hash is nil")
		}
		return &stellartypes.ContractExecutable{
			Type:     stellartypes.ContractExecutableTypeWasmHash,
			WasmHash: append([]byte(nil), value.WasmHash[:]...),
		}, nil

	case xdr.ContractExecutableTypeContractExecutableStellarAsset:
		return &stellartypes.ContractExecutable{
			Type:         stellartypes.ContractExecutableTypeStellarAsset,
			StellarAsset: true,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported contract executable type: %v", value.Type)
	}
}

func convertScContractInstanceToDomain(value xdr.ScContractInstance) (*stellartypes.ScContractInstance, error) {
	executable, err := convertContractExecutableToDomain(value.Executable)
	if err != nil {
		return nil, fmt.Errorf("executable: %w", err)
	}

	out := &stellartypes.ScContractInstance{
		Executable: executable,
	}

	if value.Storage != nil {
		storage, err := convertScMapToDomain(*value.Storage)
		if err != nil {
			return nil, fmt.Errorf("storage: %w", err)
		}
		out.Storage = storage
	}

	return out, nil
}

func convertScErrorTypeToDomain(value xdr.ScErrorType) (stellartypes.ScErrorType, error) {
	switch value {
	case xdr.ScErrorTypeSceContract:
		return stellartypes.ScErrorTypeContract, nil
	case xdr.ScErrorTypeSceWasmVm:
		return stellartypes.ScErrorTypeWasmVM, nil
	case xdr.ScErrorTypeSceContext:
		return stellartypes.ScErrorTypeContext, nil
	case xdr.ScErrorTypeSceStorage:
		return stellartypes.ScErrorTypeStorage, nil
	case xdr.ScErrorTypeSceObject:
		return stellartypes.ScErrorTypeObject, nil
	case xdr.ScErrorTypeSceCrypto:
		return stellartypes.ScErrorTypeCrypto, nil
	case xdr.ScErrorTypeSceEvents:
		return stellartypes.ScErrorTypeEvents, nil
	case xdr.ScErrorTypeSceBudget:
		return stellartypes.ScErrorTypeBudget, nil
	case xdr.ScErrorTypeSceValue:
		return stellartypes.ScErrorTypeValue, nil
	case xdr.ScErrorTypeSceAuth:
		return stellartypes.ScErrorTypeAuth, nil
	default:
		return 0, fmt.Errorf("unsupported sc error type: %v", value)
	}
}

func convertScErrorToDomain(value xdr.ScError) (*stellartypes.ScError, error) {
	errorType, err := convertScErrorTypeToDomain(value.Type)
	if err != nil {
		return nil, err
	}

	out := &stellartypes.ScError{Type: errorType}
	if value.Type == xdr.ScErrorTypeSceContract {
		if value.ContractCode == nil {
			return nil, fmt.Errorf("contract error code is nil")
		}
		code := uint32(*value.ContractCode)
		out.ContractCode = &code
		return out, nil
	}

	if value.Code == nil {
		return nil, fmt.Errorf("error code is nil")
	}
	code := stellartypes.ScErrorCode(*value.Code)
	out.Code = &code
	return out, nil
}

func convertScValsToDomain(vals []stellartypes.ScVal) ([]xdr.ScVal, error) {
	out := make([]xdr.ScVal, len(vals))
	for i, v := range vals {
		x, err := convertScValFromDomain(v)
		if err != nil {
			return nil, fmt.Errorf("arg[%d]: %w", i, err)
		}
		out[i] = x
	}
	return out, nil
}

func convertAddressToDomain(a stellartypes.ScAddress) (xdr.ScAddress, error) {
	switch a.Type {
	case stellartypes.ScAddressTypeAccountID:
		if len(a.AccountID) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("account id must be 32 bytes, got %d", len(a.AccountID))
		}
		gAddr, err := strkey.Encode(strkey.VersionByteAccountID, a.AccountID)
		if err != nil {
			return xdr.ScAddress{}, fmt.Errorf("encode account id: %w", err)
		}
		accountID, err := xdr.AddressToAccountId(gAddr)
		if err != nil {
			return xdr.ScAddress{}, fmt.Errorf("build account address: %w", err)
		}
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &accountID}, nil

	case stellartypes.ScAddressTypeContractID:
		if len(a.ContractID) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("contract id must be 32 bytes, got %d", len(a.ContractID))
		}
		addr := scval.BuildContractScAddress(a.ContractID)
		if addr == nil {
			return xdr.ScAddress{}, fmt.Errorf("failed to build contract address")
		}
		return *addr, nil

	default:
		return xdr.ScAddress{}, fmt.Errorf("unsupported ScAddress type %d as contract argument", a.Type)
	}
}

func convertGetEventsResponseToDomain(resp protocol.GetEventsResponse) (stellartypes.GetEventsResponse, error) {
	events := make([]stellartypes.EventInfo, len(resp.Events))
	for i, event := range resp.Events {
		converted, err := convertEventInfoToDomain(event)
		if err != nil {
			return stellartypes.GetEventsResponse{}, fmt.Errorf("events[%d]: %w", i, err)
		}
		events[i] = converted
	}

	return stellartypes.GetEventsResponse{
		Events:                events,
		Cursor:                resp.Cursor,
		LatestLedger:          resp.LatestLedger,
		OldestLedger:          resp.OldestLedger,
		LatestLedgerCloseTime: resp.LatestLedgerCloseTime,
		OldestLedgerCloseTime: resp.OldestLedgerCloseTime,
	}, nil
}

func convertEventInfoToDomain(event protocol.EventInfo) (stellartypes.EventInfo, error) {
	if event.Ledger < 0 {
		return stellartypes.EventInfo{}, fmt.Errorf("ledger must be non-negative: %d", event.Ledger)
	}

	var eventType stellartypes.EventType
	switch event.EventType {
	case protocol.EventTypeSystem:
		eventType = stellartypes.EventTypeSystem
	case protocol.EventTypeContract:
		eventType = stellartypes.EventTypeContract
	default:
		return stellartypes.EventInfo{}, fmt.Errorf("unsupported event type: %s", event.EventType)
	}

	topics := make([]stellartypes.ScVal, len(event.TopicXDR))
	for i, topicXDR := range event.TopicXDR {
		topic, err := decodeScValBase64ToDomain(topicXDR)
		if err != nil {
			return stellartypes.EventInfo{}, fmt.Errorf("topics[%d]: %w", i, err)
		}
		topics[i] = topic
	}

	if event.ValueXDR == "" {
		return stellartypes.EventInfo{}, fmt.Errorf("value is required")
	}
	value, err := decodeScValBase64ToDomain(event.ValueXDR)
	if err != nil {
		return stellartypes.EventInfo{}, fmt.Errorf("value: %w", err)
	}

	return stellartypes.EventInfo{
		EventType:        eventType,
		Ledger:           uint32(event.Ledger),
		LedgerClosedAt:   event.LedgerClosedAt,
		ContractID:       event.ContractID,
		ID:               event.ID,
		OperationIndex:   event.OpIndex,
		TransactionIndex: event.TxIndex,
		TransactionHash:  event.TransactionHash,
		Topics:           topics,
		Value:            value,
	}, nil
}

func validateSimulateAuthMode(mode stellartypes.SimulateAuthMode) (string, error) {
	switch mode {
	case "":
		return string(stellartypes.SimulateAuthModeRecord), nil
	case stellartypes.SimulateAuthModeEnforce,
		stellartypes.SimulateAuthModeRecord,
		stellartypes.SimulateAuthModeRecordAllowNonroot:
		return string(mode), nil
	default:
		return "", fmt.Errorf("unsupported auth mode: %s", mode)
	}
}

func errNilScValField(name string) error {
	return fmt.Errorf("scVal of declared type has nil %s field", name)
}
