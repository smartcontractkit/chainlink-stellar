package helpers

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"
)

type stellarV2Fixture struct {
	EncodingVersion uint32 `json:"encoding_version"`
	Vectors         []struct {
		NetworkIDHex          string `json:"network_id_hex"`
		MultisigContractIDHex string `json:"multisig_contract_id_hex"`
		TargetContractIDHex   string `json:"target_contract_id_hex"`
		Metadata              struct {
			PreOpCount           uint64 `json:"pre_op_count"`
			PostOpCount          uint64 `json:"post_op_count"`
			OverridePreviousRoot bool   `json:"override_previous_root"`
			ConfigVersion        uint64 `json:"config_version"`
			PreimageHex          string `json:"preimage_hex"`
			LeafHex              string `json:"leaf_hex"`
		} `json:"metadata"`
		Operation struct {
			Nonce       uint64 `json:"nonce"`
			Function    string `json:"function"`
			ArgsXDRHex  string `json:"args_xdr_hex"`
			PreimageHex string `json:"preimage_hex"`
			LeafHex     string `json:"leaf_hex"`
		} `json:"operation"`
		RootSignature struct {
			MerkleRootHex       string `json:"merkle_root_hex"`
			ValidUntil          uint32 `json:"valid_until"`
			SetRootInnerHashHex string `json:"set_root_inner_hash_hex"`
			EIP191SignedHashHex string `json:"eip191_signed_hash_hex"`
		} `json:"root_signature"`
		Timelock struct {
			PredecessorHex       string `json:"predecessor_hex"`
			SaltHex              string `json:"salt_hex"`
			CallPreimageHex      string `json:"call_preimage_hex"`
			CallHashHex          string `json:"call_hash_hex"`
			OperationPreimageHex string `json:"operation_preimage_hex"`
			OperationIDHex       string `json:"operation_id_hex"`
		} `json:"timelock"`
	} `json:"vectors"`
}

func TestStellarV2GoldenVector(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/mcms/testdata/stellar_golden_vectors.json")
	require.NoError(t, err)
	var fixture stellarV2Fixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Len(t, fixture.Vectors, 1)
	v := fixture.Vectors[0]

	decode32 := func(value string) [32]byte {
		decoded, decodeErr := hex.DecodeString(value)
		require.NoError(t, decodeErr)
		require.Len(t, decoded, 32)
		var out [32]byte
		copy(out[:], decoded)
		return out
	}
	toContract := func(value string) string {
		id := decode32(value)
		address, encodeErr := strkey.Encode(strkey.VersionByteContract, id[:])
		require.NoError(t, encodeErr)
		return address
	}
	argsXDR, err := hex.DecodeString(v.Operation.ArgsXDRHex)
	require.NoError(t, err)

	metadata := mcmsbindings.StellarRootMetadata{
		ConfigVersion:        v.Metadata.ConfigVersion,
		EncodingVersion:      fixture.EncodingVersion,
		Multisig:             toContract(v.MultisigContractIDHex),
		NetworkId:            decode32(v.NetworkIDHex),
		OverridePreviousRoot: v.Metadata.OverridePreviousRoot,
		PostOpCount:          v.Metadata.PostOpCount,
		PreOpCount:           v.Metadata.PreOpCount,
	}
	operation := mcmsbindings.StellarOp{
		ArgsXdr:         argsXDR,
		EncodingVersion: fixture.EncodingVersion,
		Function:        v.Operation.Function,
		Multisig:        metadata.Multisig,
		NetworkId:       metadata.NetworkId,
		Nonce:           v.Operation.Nonce,
		Target:          toContract(v.TargetContractIDHex),
	}

	metadataPreimage, err := EncodeRootMetadata(metadata)
	require.NoError(t, err)
	require.Equal(t, v.Metadata.PreimageHex, hex.EncodeToString(metadataPreimage))
	metadataLeaf, err := HashRootMetadata(metadata)
	require.NoError(t, err)
	require.Equal(t, v.Metadata.LeafHex, hex.EncodeToString(metadataLeaf[:]))

	operationPreimage, err := EncodeStellarOp(operation)
	require.NoError(t, err)
	require.Equal(t, v.Operation.PreimageHex, hex.EncodeToString(operationPreimage))
	operationLeaf, err := HashStellarOp(operation)
	require.NoError(t, err)
	require.Equal(t, v.Operation.LeafHex, hex.EncodeToString(operationLeaf[:]))

	root := MerkleRootTwoLeaves(metadataLeaf, operationLeaf)
	require.Equal(t, v.RootSignature.MerkleRootHex, hex.EncodeToString(root[:]))
	inner := HashSetRootInner(root, v.RootSignature.ValidUntil)
	require.Equal(t, v.RootSignature.SetRootInnerHashHex, hex.EncodeToString(inner[:]))
	signed := EthSignedMessageHash32(inner)
	require.Equal(t, v.RootSignature.EIP191SignedHashHex, hex.EncodeToString(signed[:]))

	call := timelockbindings.Call{
		ArgsXdr:  argsXDR,
		Function: v.Operation.Function,
		Target:   operation.Target,
	}
	callPreimage, err := EncodeTimelockCall(call)
	require.NoError(t, err)
	require.Equal(t, v.Timelock.CallPreimageHex, hex.EncodeToString(callPreimage))
	callHash, err := HashTimelockCall(call)
	require.NoError(t, err)
	require.Equal(t, v.Timelock.CallHashHex, hex.EncodeToString(callHash[:]))

	operationID, err := TimelockOperationID(
		timelockbindings.Calls{Inner: []timelockbindings.Call{call}},
		decode32(v.Timelock.PredecessorHex),
		decode32(v.Timelock.SaltHex),
	)
	require.NoError(t, err)
	require.Equal(t, v.Timelock.OperationIDHex, hex.EncodeToString(operationID[:]))
}
