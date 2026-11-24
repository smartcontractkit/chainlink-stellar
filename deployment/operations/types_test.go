package operations

import (
	"encoding/json"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

const roundTripAddress = "GCMPTNQYROFGUE7ZP6SIVK3YPYEBBIYK73FFV26QEGP2GOP3SELOEZGG"

func TestDeployInput_ConstructorArgsJSONRoundTrip(t *testing.T) {
	t.Parallel()
	// #given one arg of each shape a constructor takes: address, address vector,
	// byte array, struct (ScMap) and an empty optional address
	structVal := xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: func() **xdr.ScMap {
		m := &xdr.ScMap{{Key: scval.SymbolToScVal("max"), Val: scval.Uint32ToScVal(7)}}
		return &m
	}()}
	in := DeployInput{
		WasmPath: "x.wasm",
		Salt:     [32]byte{1},
		ConstructorArgs: ConstructorArgs{
			scval.AddressToScVal(roundTripAddress),
			scval.AddressSliceToScVal([]string{roundTripAddress, roundTripAddress}),
			scval.Bytes32ToScVal([32]byte{0xAB}),
			structVal,
			scval.OptionalAddressToScVal(nil),
		},
	}

	// #when
	data, err := json.Marshal(in)
	require.NoError(t, err)
	var out DeployInput
	require.NoError(t, json.Unmarshal(data, &out))

	// #then every arg survives byte for byte
	require.Len(t, out.ConstructorArgs, len(in.ConstructorArgs))
	for i := range in.ConstructorArgs {
		want, err := in.ConstructorArgs[i].MarshalBinary()
		require.NoError(t, err)
		got, err := out.ConstructorArgs[i].MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, want, got, "arg %d", i)
	}
}

func TestDeployInput_NoConstructorArgsOmitted(t *testing.T) {
	t.Parallel()
	// #given a stateless deploy, #when serialized, #then the field is absent so
	// its report input matches the pre-constructor shape
	data, err := json.Marshal(DeployInput{WasmPath: "x.wasm"})
	require.NoError(t, err)
	require.NotContains(t, string(data), "constructor_args")
}
