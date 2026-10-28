package relayer

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"
)

func TestConvertScAddressToDomain(t *testing.T) {
	t.Parallel()

	var contractID xdr.ContractId
	for i := range contractID {
		contractID[i] = byte(i)
	}
	var key xdr.Uint256
	key[0] = 1

	t.Run("contract", func(t *testing.T) {
		t.Parallel()
		got, err := convertScAddressToDomain(xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID})
		require.NoError(t, err)
		require.Equal(t, &stellartypes.ScAddress{Type: stellartypes.ScAddressTypeContractID, ContractID: contractID[:]}, got)
	})

	t.Run("muxed account", func(t *testing.T) {
		t.Parallel()
		got, err := convertScAddressToDomain(xdr.ScAddress{
			Type:         xdr.ScAddressTypeScAddressTypeMuxedAccount,
			MuxedAccount: &xdr.MuxedEd25519Account{Id: 9, Ed25519: key},
		})
		require.NoError(t, err)
		require.Equal(t, &stellartypes.ScAddress{
			Type:         stellartypes.ScAddressTypeMuxedAccount,
			MuxedAccount: &stellartypes.MuxedEd25519Account{ID: 9, Ed25519: key[:]},
		}, got)
	})

	t.Run("muxed contract is rejected and named by its W-address", func(t *testing.T) {
		t.Parallel()
		contractAddr, err := strkey.Encode(strkey.VersionByteContract, contractID[:])
		require.NoError(t, err)
		var want strkey.MuxedContract
		want.SetID(7)
		require.NoError(t, want.SetContractID(contractAddr))
		wantAddr, err := want.Address()
		require.NoError(t, err)

		_, err = convertScAddressToDomain(xdr.ScAddress{
			Type:          xdr.ScAddressTypeScAddressTypeMuxedContract,
			MuxedContract: &xdr.MuxedContract{Id: 7, ContractId: contractID},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), wantAddr)
		require.Contains(t, err.Error(), "not supported")
	})

	t.Run("nil muxed contract", func(t *testing.T) {
		t.Parallel()
		_, err := convertScAddressToDomain(xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeMuxedContract})
		require.Error(t, err)
		require.Contains(t, err.Error(), "muxed contract is nil")
	})

	t.Run("muxed contract inside an address ScVal", func(t *testing.T) {
		t.Parallel()
		addr := xdr.ScAddress{
			Type:          xdr.ScAddressTypeScAddressTypeMuxedContract,
			MuxedContract: &xdr.MuxedContract{Id: 7, ContractId: contractID},
		}
		_, err := convertScValToDomain(xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr})
		require.Error(t, err)
		require.Contains(t, err.Error(), "muxed contract address")
	})

	t.Run("unknown type", func(t *testing.T) {
		t.Parallel()
		_, err := convertScAddressToDomain(xdr.ScAddress{Type: xdr.ScAddressType(99)})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported sc address type")
	})
}
