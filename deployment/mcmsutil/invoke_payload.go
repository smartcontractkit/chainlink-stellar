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

// EncodeSorobanInvokeArgs encodes Soroban invocation arguments as the canonical XDR for
// `ScVal::Vec([...args])` for timelock `Call.args_xdr` and mcms `StellarOp.args_xdr`, matching
// contracts/common/helpers/src/soroban_invoke.rs (decode_invoke_args). The function name is
// carried separately by the `function` field on those structs and must not be included here.
//
// The encoding is the SCV_VEC discriminant (0x10) + the XDR optional "present" flag + a u32
// element count + the elements, so nil/empty args encode to the 12-byte canonical empty vector
// 000000100000000100000000 (the normative `args_xdr_hex` in
// contracts/mcms/testdata/stellar_golden_vectors.json). decode_invoke_args runs
// soroban_sdk Vec::<Val>::from_xdr and then re-encodes and byte-compares, so the ScVal wrapper is
// mandatory: a bare xdr.ScVec marshal omits it and traps the Soroban host inside
// deserialize_from_bytes, which is unrecoverable on chain rather than a mappable contract error.
//
// Build the value via scval.VecToScVal: its `&scVec` indirection is what makes the "present" flag
// true. A hand-rolled xdr.ScVal{Type: ScvVec} with a nil inner pointer encodes as
// `ScVal::Vec(None)`, which soroban rejects.
func EncodeSorobanInvokeArgs(argScVals []xdr.ScVal) ([]byte, error) {
	b, err := scval.VecToScVal(argScVals).MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("marshal soroban invoke args: %w", err)
	}
	return b, nil
}
