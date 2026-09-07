package mcmsutil

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// EncodeSorobanMCMSInvokePayload builds the MCMS transaction-data payload for Stellar
// proposals: XDR for ScVal::Vec([Symbol(fn), ...args]). This is the wire format consumed by
// the MCMS proposal transformer marked by AdditionalFields {"version":1,"family":"stellar"},
// which splits it into StellarOp.function / StellarOp.args_xdr before submission.
func EncodeSorobanMCMSInvokePayload(functionName string, argScVals []xdr.ScVal) ([]byte, error) {
	vec := make(xdr.ScVec, 0, 1+len(argScVals))
	vec = append(vec, scval.SymbolToScVal(functionName))
	vec = append(vec, argScVals...)
	inner := vec
	p := &inner
	sc := xdr.ScVal{
		Type: xdr.ScValTypeScvVec,
		Vec:  &p,
	}
	b, err := sc.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal soroban invoke payload %q: %w", functionName, err)
	}
	return b, nil
}

// EncodeSorobanInvokeArgs encodes Soroban invocation arguments as XDR `Vec<Val>` for
// timelock `Call.args_xdr` and mcms `StellarOp.args_xdr`, matching
// contracts/common/helpers/src/soroban_invoke.rs (decode_invoke_args). The function name is
// carried separately by the `function` field on those structs and must not be included here.
func EncodeSorobanInvokeArgs(argScVals []xdr.ScVal) ([]byte, error) {
	vec := xdr.ScVec(argScVals)
	b, err := vec.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal soroban invoke args: %w", err)
	}
	return b, nil
}
