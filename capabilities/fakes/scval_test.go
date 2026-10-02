package fakes

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar/scval"
)

func TestScValsToXDR_Scalars(t *testing.T) {
	t.Parallel()
	in := []*scval.ScVal{
		{Value: &scval.ScVal_B{B: true}},
		{Value: &scval.ScVal_VoidVal{VoidVal: &scval.Void{}}},
		{Value: &scval.ScVal_U32{U32: 1}},
		{Value: &scval.ScVal_I32{I32: -1}},
		{Value: &scval.ScVal_U64{U64: 2}},
		{Value: &scval.ScVal_I64{I64: -2}},
		{Value: &scval.ScVal_Timepoint{Timepoint: 3}},
		{Value: &scval.ScVal_Duration{Duration: 4}},
		{Value: &scval.ScVal_U128{U128: &scval.UInt128Parts{Hi: 1, Lo: 2}}},
		{Value: &scval.ScVal_I128{I128: &scval.Int128Parts{Hi: -1, Lo: 2}}},
		{Value: &scval.ScVal_U256{U256: &scval.UInt256Parts{HiHi: 1, HiLo: 2, LoHi: 3, LoLo: 4}}},
		{Value: &scval.ScVal_I256{I256: &scval.Int256Parts{HiHi: -1, HiLo: 2, LoHi: 3, LoLo: 4}}},
		{Value: &scval.ScVal_BytesVal{BytesVal: []byte{9}}},
		{Value: &scval.ScVal_Str{Str: "s"}},
		{Value: &scval.ScVal_Sym{Sym: "sym"}},
	}
	out, err := scValsToXDR(in)
	require.NoError(t, err)
	require.Len(t, out, len(in))

	assert.True(t, *out[0].B)
	assert.Equal(t, xdr.ScValTypeScvVoid, out[1].Type)
	assert.Equal(t, xdr.Uint32(1), *out[2].U32)
	assert.Equal(t, xdr.Int32(-1), *out[3].I32)
	assert.Equal(t, xdr.Uint64(2), *out[4].U64)
	assert.Equal(t, xdr.Int64(-2), *out[5].I64)
	assert.Equal(t, xdr.TimePoint(3), *out[6].Timepoint)
	assert.Equal(t, xdr.Duration(4), *out[7].Duration)
	assert.Equal(t, xdr.UInt128Parts{Hi: 1, Lo: 2}, *out[8].U128)
	assert.Equal(t, xdr.Int128Parts{Hi: -1, Lo: 2}, *out[9].I128)
	assert.Equal(t, xdr.UInt256Parts{HiHi: 1, HiLo: 2, LoHi: 3, LoLo: 4}, *out[10].U256)
	assert.Equal(t, xdr.Int256Parts{HiHi: -1, HiLo: 2, LoHi: 3, LoLo: 4}, *out[11].I256)
	assert.Equal(t, xdr.ScBytes{9}, *out[12].Bytes)
	assert.Equal(t, xdr.ScString("s"), *out[13].Str)
	assert.Equal(t, xdr.ScSymbol("sym"), *out[14].Sym)

	// Every converted value must be valid, encodable XDR.
	for i, v := range out {
		_, err := v.MarshalBinary()
		require.NoError(t, err, "arg %d", i)
	}
}

func TestScValsToXDR_Nested(t *testing.T) {
	t.Parallel()
	in := []*scval.ScVal{{Value: &scval.ScVal_Map{Map: &scval.ScMap{Entries: []*scval.ScMapEntry{{
		Key: &scval.ScVal{Value: &scval.ScVal_Sym{Sym: "k"}},
		Val: &scval.ScVal{Value: &scval.ScVal_Vec{Vec: &scval.ScVec{Values: []*scval.ScVal{{Value: &scval.ScVal_U32{U32: 7}}}}}},
	}}}}}}
	out, err := scValsToXDR(in)
	require.NoError(t, err)
	m := **out[0].Map
	require.Len(t, m, 1)
	assert.Equal(t, xdr.ScSymbol("k"), *m[0].Key.Sym)
	vec := **m[0].Val.Vec
	assert.Equal(t, xdr.Uint32(7), *vec[0].U32)
}

func TestScValsToXDR_Addresses(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	key[0] = 1
	in := []*scval.ScVal{
		{Value: &scval.ScVal_Address{Address: &scval.ScAddress{Address: &scval.ScAddress_AccountId{AccountId: key}}}},
		{Value: &scval.ScVal_Address{Address: &scval.ScAddress{Address: &scval.ScAddress_ContractId{ContractId: key}}}},
		{Value: &scval.ScVal_Address{Address: &scval.ScAddress{Address: &scval.ScAddress_MuxedAccount{MuxedAccount: &scval.MuxedEd25519Account{Id: 5, Ed25519: key}}}}},
	}
	out, err := scValsToXDR(in)
	require.NoError(t, err)
	assert.Equal(t, xdr.ScAddressTypeScAddressTypeAccount, out[0].Address.Type)
	assert.Equal(t, byte(1), out[0].Address.AccountId.Ed25519[0])
	assert.Equal(t, xdr.ScAddressTypeScAddressTypeContract, out[1].Address.Type)
	assert.Equal(t, byte(1), out[1].Address.ContractId[0])
	assert.Equal(t, xdr.ScAddressTypeScAddressTypeMuxedAccount, out[2].Address.Type)
	assert.Equal(t, xdr.Uint64(5), out[2].Address.MuxedAccount.Id)
}

func TestScValsToXDR_Errors(t *testing.T) {
	t.Parallel()
	deep := &scval.ScVal{Value: &scval.ScVal_U32{U32: 1}}
	for i := 0; i <= maxScValDepth+1; i++ {
		deep = &scval.ScVal{Value: &scval.ScVal_Vec{Vec: &scval.ScVec{Values: []*scval.ScVal{deep}}}}
	}
	for name, tc := range map[string]struct {
		in   *scval.ScVal
		want string
	}{
		"nil":             {nil, "nil"},
		"short account":   {&scval.ScVal{Value: &scval.ScVal_Address{Address: &scval.ScAddress{Address: &scval.ScAddress_AccountId{AccountId: []byte{1}}}}}, "32 bytes"},
		"unsupported":     {&scval.ScVal{Value: &scval.ScVal_NonceKey{NonceKey: &scval.ScNonceKey{}}}, "unsupported"},
		"too deep":        {deep, "depth"},
		"nil map entry":   {&scval.ScVal{Value: &scval.ScVal_Map{Map: &scval.ScMap{Entries: []*scval.ScMapEntry{nil}}}}, "nil"},
		"nil u128 parts":  {&scval.ScVal{Value: &scval.ScVal_U128{}}, "nil"},
		"nil address val": {&scval.ScVal{Value: &scval.ScVal_Address{}}, "nil"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := scValsToXDR([]*scval.ScVal{tc.in})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestContractAddress(t *testing.T) {
	t.Parallel()
	_, err := contractAddress(testForwarder)
	require.NoError(t, err)
	_, err = contractAddress("GAAZI4TCR3TY5OJHCTJC2A4QSY6CJWJH5IAJTGKIN2ER7LBNVKOCCWN7")
	require.Error(t, err, "account StrKeys are not contract addresses")
}
