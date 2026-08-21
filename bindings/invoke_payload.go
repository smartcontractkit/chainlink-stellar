package bindings

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/xdr"
)

var sorobanInvokeSymbol = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

// SorobanInvokeEncodingVersion is the current native MCMS invoke wire format.
const SorobanInvokeEncodingVersion uint32 = 1

// EncodeSorobanInvokePayload encodes the current MCMS invoke payload.
func EncodeSorobanInvokePayload(function string, args []xdr.ScVal) ([]byte, error) {
	if !sorobanInvokeSymbol.MatchString(function) {
		return nil, fmt.Errorf("invalid Soroban symbol %q", function)
	}
	items := make(xdr.ScVec, 0, len(args)+1)
	items = append(items, scval.SymbolToScVal(function))
	items = append(items, args...)
	p := &items
	return (xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &p}).MarshalBinary()
}

// DecodeSorobanInvokePayload decodes the current MCMS invoke payload.
func DecodeSorobanInvokePayload(data []byte) (string, []xdr.ScVal, error) {
	var value xdr.ScVal
	if err := value.UnmarshalBinary(data); err != nil {
		return "", nil, fmt.Errorf("decode Soroban invoke payload: %w", err)
	}
	vec, ok := value.GetVec()
	if !ok || vec == nil || len(*vec) == 0 {
		return "", nil, errors.New("Soroban invoke payload must be a non-empty vector")
	}
	function, err := scval.SymbolFromScVal((*vec)[0])
	if err != nil {
		return "", nil, fmt.Errorf("invalid invoke function symbol: %w", err)
	}
	if !sorobanInvokeSymbol.MatchString(function) {
		return "", nil, fmt.Errorf("invalid invoke function symbol %q", function)
	}
	return function, append([]xdr.ScVal(nil), (*vec)[1:]...), nil
}
