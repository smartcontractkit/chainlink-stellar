package helpers

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"

	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// Domain separators — must match contracts/mcms/src/constants.rs and docs/mcms-stellar-plan.md.
var (
	domainMetaStellar = [32]byte{
		0xde, 0x51, 0xf2, 0xd6, 0x7b, 0xb4, 0x89, 0x5d, 0x0d, 0xd1, 0xf3, 0x6a, 0xdb, 0x04, 0x42, 0x27,
		0xaa, 0x7b, 0x76, 0x4d, 0x4e, 0x52, 0x4d, 0x6b, 0x0d, 0x70, 0x04, 0x72, 0x27, 0x28, 0xfd, 0xa0,
	}
	domainOpStellar = [32]byte{
		0x12, 0xcd, 0xc8, 0x8e, 0x33, 0xb5, 0x9a, 0x3a, 0x5a, 0x9f, 0xe3, 0x07, 0x2e, 0x0b, 0xab, 0x63,
		0xee, 0x3d, 0xb8, 0x88, 0xaf, 0x2c, 0xdb, 0x10, 0xbc, 0x93, 0x34, 0x56, 0x88, 0x05, 0x8d, 0x16,
	}
)

// Anvil0SKHex is Anvil account #0 secret (must match contracts/mcms/src/test.rs ANVIL_SK_0).
const Anvil0SKHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

// ChainNetworkIDFromHex parses a 32-byte Stellar chain network id from hex (e.g. chain-selectors ChainID).
func ChainNetworkIDFromHex(chainIDHex string) ([32]byte, error) {
	var out [32]byte
	s := strings.TrimPrefix(strings.TrimPrefix(chainIDHex, "0x"), "0X")
	if len(s) != 64 {
		return out, fmt.Errorf("expected 64 hex chars for chain network id, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

// mcmsEncodingVersion matches contracts/mcms/src/constants.rs ENCODING_VERSION.
const mcmsEncodingVersion uint32 = 1

// mcmsUint40Limit matches contracts/mcms/src/encoding.rs UINT40_LIMIT.
const mcmsUint40Limit uint64 = 1 << 40

func appendU32BE(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func appendU64BE(buf *bytes.Buffer, v uint64) error {
	if v >= mcmsUint40Limit {
		return fmt.Errorf("value overflows uint40: %d", v)
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	buf.Write(b[:])
	return nil
}

// HashRootMetadata returns keccak256(encode_root_metadata(m)) per contracts/mcms/src/encoding.rs.
func HashRootMetadata(m mcmsbindings.StellarRootMetadata) ([32]byte, error) {
	if m.EncodingVersion != mcmsEncodingVersion {
		return [32]byte{}, fmt.Errorf("unsupported encoding version %d, want %d", m.EncodingVersion, mcmsEncodingVersion)
	}
	multisig, err := ContractIDToBytes32(m.Multisig)
	if err != nil {
		return [32]byte{}, fmt.Errorf("decode multisig contract id: %w", err)
	}
	var buf bytes.Buffer
	buf.Write(domainMetaStellar[:])
	appendU32BE(&buf, m.EncodingVersion)
	buf.Write(m.NetworkId[:])
	buf.Write(multisig[:])
	if err := appendU64BE(&buf, m.PreOpCount); err != nil {
		return [32]byte{}, err
	}
	if err := appendU64BE(&buf, m.PostOpCount); err != nil {
		return [32]byte{}, err
	}
	if m.OverridePreviousRoot {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}
	var cv [8]byte
	binary.BigEndian.PutUint64(cv[:], m.ConfigVersion)
	buf.Write(cv[:])
	h := crypto.Keccak256(buf.Bytes())
	var digest [32]byte
	copy(digest[:], h)
	return digest, nil
}

// HashStellarOp returns keccak256(encode_stellar_op(op)) per contracts/mcms/src/encoding.rs.
// The function field is encoded as the full ScVal::Symbol XDR (SCV_SYMBOL discriminant plus
// length-prefixed, 4-byte-padded bytes), matching Symbol::to_xdr in encode_stellar_op. Omitting
// the ScVal wrapper shortens the length-prefixed segment below and yields a different merkle leaf
// than the contract computes, so set_root succeeds but execute fails proof verification.
func HashStellarOp(op mcmsbindings.StellarOp) ([32]byte, error) {
	if op.EncodingVersion != mcmsEncodingVersion {
		return [32]byte{}, fmt.Errorf("unsupported encoding version %d, want %d", op.EncodingVersion, mcmsEncodingVersion)
	}
	multisig, err := ContractIDToBytes32(op.Multisig)
	if err != nil {
		return [32]byte{}, fmt.Errorf("decode multisig contract id: %w", err)
	}
	target, err := ContractIDToBytes32(op.Target)
	if err != nil {
		return [32]byte{}, fmt.Errorf("decode target contract id: %w", err)
	}
	fnXDR, err := scval.SymbolToScVal(op.Function).MarshalBinary()
	if err != nil {
		return [32]byte{}, fmt.Errorf("marshal function symbol %q: %w", op.Function, err)
	}
	var buf bytes.Buffer
	buf.Write(domainOpStellar[:])
	appendU32BE(&buf, op.EncodingVersion)
	buf.Write(op.NetworkId[:])
	buf.Write(multisig[:])
	if err := appendU64BE(&buf, op.Nonce); err != nil {
		return [32]byte{}, err
	}
	buf.Write(target[:])
	appendU32BE(&buf, uint32(len(fnXDR)))
	buf.Write(fnXDR)
	appendU32BE(&buf, uint32(len(op.ArgsXdr)))
	buf.Write(op.ArgsXdr)
	h := crypto.Keccak256(buf.Bytes())
	var digest [32]byte
	copy(digest[:], h)
	return digest, nil
}

// HashSetRootInner returns keccak256(abi.encode(bytes32 root, uint32 validUntil)).
func HashSetRootInner(root [32]byte, validUntil uint32) [32]byte {
	var buf bytes.Buffer
	buf.Write(root[:])
	var vu [32]byte
	vb := make([]byte, 4)
	binary.BigEndian.PutUint32(vb, validUntil)
	copy(vu[28:], vb)
	buf.Write(vu[:])
	h := crypto.Keccak256(buf.Bytes())
	var out [32]byte
	copy(out[:], h)
	return out
}

// EthSignedMessageHash32 returns keccak256(EIP-191 prefix || digest) for a 32-byte payload (contracts/mcms EIP-191 path).
func EthSignedMessageHash32(digest [32]byte) [32]byte {
	const prefix = "\x19Ethereum Signed Message:\n32"
	h := crypto.Keccak256(append([]byte(prefix), digest[:]...))
	var out [32]byte
	copy(out[:], h)
	return out
}

func efficientHashPair(a, b [32]byte) [32]byte {
	var left, right [32]byte
	if bytes.Compare(a[:], b[:]) <= 0 {
		left, right = a, b
	} else {
		left, right = b, a
	}
	var combined [64]byte
	copy(combined[:32], left[:])
	copy(combined[32:], right[:])
	h := crypto.Keccak256(combined[:])
	var out [32]byte
	copy(out[:], h)
	return out
}

// MerkleRootTwoLeaves builds the root from exactly two leaf digests (matches mcms merkle_root_native for n=2).
func MerkleRootTwoLeaves(l0, l1 [32]byte) [32]byte {
	return efficientHashPair(l0, l1)
}

// MerkleProofTwoLeaves returns the sibling path for a two-leaf tree (compute_proof_for_leaf in mcms tests).
func MerkleProofTwoLeaves(leaves [2][32]byte, leafIndex int) [][32]byte {
	if leafIndex != 0 && leafIndex != 1 {
		panic("leafIndex must be 0 or 1")
	}
	sib := 1 - leafIndex
	return [][32]byte{leaves[sib]}
}

// PaddedEthAddress returns the 32-byte left-padded address form used in MCMS signer config (match Rust padded_eth_address).
func PaddedEthAddress(pub *ecdsa.PublicKey) [32]byte {
	addr := crypto.PubkeyToAddress(*pub)
	var out [32]byte
	copy(out[12:], addr[:])
	return out
}

// SignaturesForSetRoot signs the EIP-191 digest over hash_set_root_inner with the Anvil #0 test key (1-of-1).
func SignaturesForSetRoot(pk *ecdsa.PrivateKey, root [32]byte, validUntil uint32) (mcmsbindings.SignatureVec, error) {
	inner := HashSetRootInner(root, validUntil)
	msgHash := EthSignedMessageHash32(inner)
	sig65, err := crypto.Sign(msgHash[:], pk)
	if err != nil {
		return mcmsbindings.SignatureVec{}, err
	}
	var r, s [32]byte
	copy(r[:], sig65[:32])
	copy(s[:], sig65[32:64])
	v := uint32(27 + sig65[64])
	return mcmsbindings.SignatureVec{
		Inner: []mcmsbindings.Signature{
			{V: v, R: r, S: s},
		},
	}, nil
}
