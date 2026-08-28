package deployment

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestBuildCreateContractHostFunction(t *testing.T) {
	var pubKey xdr.Uint256
	pubKey[0] = 0x42
	var wasmHash xdr.Hash
	wasmHash[0] = 0x99
	salt := [32]byte{7}

	// no args → legacy V1 CreateContract
	hfV1 := buildCreateContractHostFunction(pubKey, wasmHash, salt, nil)
	require.Equal(t, xdr.HostFunctionTypeHostFunctionTypeCreateContract, hfV1.Type)
	require.NotNil(t, hfV1.CreateContract)

	// args → CreateContractV2 carrying ConstructorArgs
	args := []xdr.ScVal{{Type: xdr.ScValTypeScvU32, U32: func() *xdr.Uint32 { v := xdr.Uint32(5); return &v }()}}
	hfV2 := buildCreateContractHostFunction(pubKey, wasmHash, salt, args)
	require.Equal(t, xdr.HostFunctionTypeHostFunctionTypeCreateContractV2, hfV2.Type)
	require.NotNil(t, hfV2.CreateContractV2)
	require.Len(t, hfV2.CreateContractV2.ConstructorArgs, 1)
	require.Equal(t, xdr.Uint256(salt), hfV2.CreateContractV2.ContractIdPreimage.FromAddress.Salt)
	require.Equal(t, pubKey, *hfV2.CreateContractV2.ContractIdPreimage.FromAddress.Address.AccountId.Ed25519)
	require.Equal(t, wasmHash, *hfV2.CreateContractV2.Executable.WasmHash)
}
