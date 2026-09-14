package common

import (
	"testing"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeExtraArgsV3_nonEmpty(t *testing.T) {
	t.Parallel()
	vvr := stellarutil.MustGenerateMockContractID("deployer", "vvr-extra")
	ex := stellarutil.MustGenerateMockContractID("deployer", "ex-extra")
	v3 := onrampbindings.GenericExtraArgsV3{
		BlockConfirmations: 2,
		Ccvs:               []string{vvr},
		CcvArgs:            [][]byte{{0x01, 0x02}},
		Executor:           ex,
		GasLimit:           99,
		ExecutorArgs:       []byte{0x03},
		TokenArgs:          []byte{0x04},
	}
	out, err := EncodeExtraArgsV3(v3)
	require.NoError(t, err)
	assert.NotEmpty(t, out)
}

func TestEncodeExtraArgsV3_roundTripGenericExtraArgsV3FromScVal(t *testing.T) {
	t.Parallel()
	vvr := stellarutil.MustGenerateMockContractID("deployer", "vvr-rt")
	ex := stellarutil.MustGenerateMockContractID("deployer", "ex-rt")
	v3 := onrampbindings.GenericExtraArgsV3{
		BlockConfirmations: 3,
		Ccvs:               []string{vvr},
		CcvArgs:            [][]byte{{0x10}},
		Executor:           ex,
		GasLimit:           42,
		ExecutorArgs:       []byte{0x11},
		TokenArgs:          []byte{0x12},
	}
	raw, err := EncodeExtraArgsV3(v3)
	require.NoError(t, err)
	var scVal xdr.ScVal
	require.NoError(t, scVal.UnmarshalBinary(raw))
	got, err := onrampbindings.GenericExtraArgsV3FromScVal(scVal)
	require.NoError(t, err)
	assert.Equal(t, v3.BlockConfirmations, got.BlockConfirmations)
	assert.Equal(t, v3.Ccvs, got.Ccvs)
	assert.Equal(t, v3.Executor, got.Executor)
	assert.Equal(t, v3.GasLimit, got.GasLimit)
	assert.Equal(t, v3.ExecutorArgs, got.ExecutorArgs)
	assert.Equal(t, v3.TokenArgs, got.TokenArgs)
	assert.Equal(t, v3.CcvArgs, got.CcvArgs)
}
