package deployment

import (
	"context"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// n6Run1Deployer is the deployer account of the NONEVM-4294 N6 run-1 full deploy
// on Stellar testnet (staging_testnet_stellar), taken from the run's op reports.
const n6Run1Deployer = "GBP4UTCFSXUZ3QMM5EL6I62YQBM367TDSHXOPWDOFP2DQPJU7FPH72LQ"

// n6Run1OnRampID is the OnRamp contract that run created with salt
// GenerateDeterministicSalt(n6Run1Deployer, "onramp").
const n6Run1OnRampID = "CC445W4LX2SOGZZPLCQ3AA2W2KQNDG2RHKPX2RRI6367F2TRAOKA5BZH"

const testnetPassphrase = "Test SDF Network ; September 2015"

func TestComputeContractID_MatchesDeploySaltDerivation(t *testing.T) {
	t.Parallel()
	// The salt the full deploy derives for OnRamp must predict that run's
	// on-chain contract ID on testnet.
	salt := GenerateDeterministicSalt(n6Run1Deployer, "onramp")
	id, err := ComputeContractID(testnetPassphrase, n6Run1Deployer, salt)
	require.NoError(t, err)
	require.Equal(t, n6Run1OnRampID, id)
}

func TestComputeContractID_DistinctPerLabelAndNetwork(t *testing.T) {
	t.Parallel()
	a, err := ComputeContractID(testnetPassphrase, n6Run1Deployer, GenerateDeterministicSalt(n6Run1Deployer, "onramp"))
	require.NoError(t, err)
	b, err := ComputeContractID(testnetPassphrase, n6Run1Deployer, GenerateDeterministicSalt(n6Run1Deployer, "offramp"))
	require.NoError(t, err)
	require.NotEqual(t, a, b)

	c, err := ComputeContractID("Standalone Network ; February 2017", n6Run1Deployer, GenerateDeterministicSalt(n6Run1Deployer, "onramp"))
	require.NoError(t, err)
	require.NotEqual(t, a, c)
}

func TestComputeContractID_RejectsNonAccountAddress(t *testing.T) {
	t.Parallel()
	_, err := ComputeContractID(testnetPassphrase, "CC445W4LX2SOGZZPLCQ3AA2W2KQNDG2RHKPX2RRI6367F2TRAOKA5BZH", [32]byte{})
	require.ErrorContains(t, err, "decode deployer address")
}

// instanceEntryResponse builds a GetLedgerEntries response holding the instance
// entry of contractID running wasmHash.
func instanceEntryResponse(t *testing.T, contractID string, wasmHash xdr.Hash) protocolrpc.GetLedgerEntriesResponse {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	require.NoError(t, err)
	var id xdr.ContractId
	copy(id[:], raw)
	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id},
			Key:      xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Val: xdr.ScVal{
				Type: xdr.ScValTypeScvContractInstance,
				Instance: &xdr.ScContractInstance{
					Executable: xdr.ContractExecutable{
						Type:     xdr.ContractExecutableTypeContractExecutableWasm,
						WasmHash: &wasmHash,
					},
				},
			},
		},
	}
	entryXDR, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)
	return protocolrpc.GetLedgerEntriesResponse{
		Entries: []protocolrpc.LedgerEntryResult{{DataXDR: entryXDR}},
	}
}

func TestContractInstanceState(t *testing.T) {
	t.Parallel()
	contractID := n6Run1OnRampID
	wasmHash := xdr.Hash{0xAB, 0xCD}

	t.Run("absent reports exists false", func(t *testing.T) {
		t.Parallel()
		d := newTestDeployer(t, &mockRPC{
			GetLedgerEntriesFn: func(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
				return protocolrpc.GetLedgerEntriesResponse{}, nil
			},
		})
		exists, _, err := d.ContractInstanceState(context.Background(), contractID)
		require.NoError(t, err)
		require.False(t, exists)
	})

	t.Run("present returns wasm hash", func(t *testing.T) {
		t.Parallel()
		d := newTestDeployer(t, &mockRPC{
			GetLedgerEntriesFn: func(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
				return instanceEntryResponse(t, contractID, wasmHash), nil
			},
		})
		exists, hash, err := d.ContractInstanceState(context.Background(), contractID)
		require.NoError(t, err)
		require.True(t, exists)
		require.Equal(t, wasmHash, hash)
	})

	t.Run("non wasm instance errors", func(t *testing.T) {
		t.Parallel()
		d := newTestDeployer(t, &mockRPC{
			GetLedgerEntriesFn: func(_ context.Context, _ protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
				return protocolrpc.GetLedgerEntriesResponse{
					Entries: []protocolrpc.LedgerEntryResult{{DataXDR: nonInstanceEntryXDR(t, contractID)}},
				}, nil
			},
		})
		exists, _, err := d.ContractInstanceState(context.Background(), contractID)
		require.ErrorContains(t, err, "is not a WASM contract instance")
		require.False(t, exists, "exists is only meaningful when err is nil")
	})

	t.Run("invalid contract id errors", func(t *testing.T) {
		t.Parallel()
		d := newTestDeployer(t, &mockRPC{})
		_, _, err := d.ContractInstanceState(context.Background(), "not-a-strkey")
		require.ErrorContains(t, err, "invalid contract id")
	})
}

// nonInstanceEntryXDR builds a contract-data entry whose val is a void instance
// key entry without a contract instance, i.e. not a WASM instance.
func nonInstanceEntryXDR(t *testing.T, contractID string) string {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	require.NoError(t, err)
	var id xdr.ContractId
	copy(id[:], raw)
	entry := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract: xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id},
			Key:      xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Val:      xdr.ScVal{Type: xdr.ScValTypeScvVoid},
		},
	}
	entryXDR, err := xdr.MarshalBase64(entry)
	require.NoError(t, err)
	return entryXDR
}
