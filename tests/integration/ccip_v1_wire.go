//go:build integration

package integration

import (
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Soroban / contracts/common/message: MESSAGE_V1_VERSION.
const ccipMessageV1Version byte = 1

// ccipV1Wire holds fields for canonical CcipMessageV1 encoding (ToBytes layout).
type ccipV1Wire struct {
	SourceChainSelector uint64
	DestChainSelector   uint64
	SequenceNumber      uint64
	ExecutionGasLimit   uint32
	CcipReceiveGasLimit uint32
	Finality            uint32
	CcvExecutorHash     [32]byte
	OnRampAddress       []byte
	OffRampAddress      []byte
	Sender              []byte
	Receiver            []byte
	DestBlob            []byte
	TokenTransfer       []byte
	Data                []byte
}

func encodeCcipMessageV1(m ccipV1Wire) ([]byte, error) {
	if len(m.OnRampAddress) > 255 || len(m.OffRampAddress) > 255 || len(m.Sender) > 255 || len(m.Receiver) > 255 {
		return nil, fmt.Errorf("1-byte length field overflow for address field")
	}
	if len(m.DestBlob) > 65535 || len(m.TokenTransfer) > 65535 || len(m.Data) > 65535 {
		return nil, fmt.Errorf("2-byte length field overflow for blob field")
	}
	var b []byte
	b = append(b, ccipMessageV1Version)
	b = binary.BigEndian.AppendUint64(b, m.SourceChainSelector)
	b = binary.BigEndian.AppendUint64(b, m.DestChainSelector)
	b = binary.BigEndian.AppendUint64(b, m.SequenceNumber)
	b = binary.BigEndian.AppendUint32(b, m.ExecutionGasLimit)
	b = binary.BigEndian.AppendUint32(b, m.CcipReceiveGasLimit)
	b = binary.BigEndian.AppendUint32(b, m.Finality)
	b = append(b, m.CcvExecutorHash[:]...)
	b = append(b, byte(len(m.OnRampAddress)))
	b = append(b, m.OnRampAddress...)
	b = append(b, byte(len(m.OffRampAddress)))
	b = append(b, m.OffRampAddress...)
	b = append(b, byte(len(m.Sender)))
	b = append(b, m.Sender...)
	b = append(b, byte(len(m.Receiver)))
	b = append(b, m.Receiver...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(m.DestBlob)))
	b = append(b, m.DestBlob...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(m.TokenTransfer)))
	b = append(b, m.TokenTransfer...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(m.Data)))
	b = append(b, m.Data...)
	return b, nil
}

func keccak256MessageID(encoded []byte) [32]byte {
	h := crypto.Keccak256(encoded)
	var out [32]byte
	copy(out[:], h)
	return out
}

// contractAddressScValSuffix32 returns the last 32 bytes of Soroban ScVal XDR for a contract
// strkey (C...), matching CcipMessageV1::address_raw_bytes / OffRamp execute's offramp check.
func contractAddressScValSuffix32(contractStrkey string) ([]byte, error) {
	v := scval.AddressToScVal(contractStrkey)
	buf, err := v.MarshalBinary()
	if err != nil {
		return nil, err
	}
	if len(buf) < 32 {
		return nil, fmt.Errorf("ScVal XDR shorter than 32 bytes: %d", len(buf))
	}
	return buf[len(buf)-32:], nil
}

// sorobanAddressRaw32 matches contracts/common/message CcipMessageV1::address_raw_bytes (last 32 bytes of Address ScVal XDR).
func sorobanAddressRaw32(addrStrkey string) ([]byte, error) {
	v := scval.AddressToScVal(addrStrkey)
	buf, err := v.MarshalBinary()
	if err != nil {
		return nil, err
	}
	if len(buf) < 32 {
		return nil, fmt.Errorf("address ScVal XDR shorter than 32 bytes: %d", len(buf))
	}
	return buf[len(buf)-32:], nil
}

// sorobanScValXDR returns full XDR for a ScVal (matches Soroban Address::to_xdr / Bytes::to_xdr on-chain).
func sorobanScValXDR(v xdr.ScVal) ([]byte, error) {
	return v.MarshalBinary()
}

// computeCcvAndExecutorHash matches contracts/common/message::CcipMessageV1::compute_ccv_and_executor_hash:
// keccak256( [len_executor_u8] || raw(ccv_0) || ... || raw(executor) ).
func computeCcvAndExecutorHash(ccvs []string, executorStrkey string) ([32]byte, error) {
	execRaw, err := sorobanAddressRaw32(executorStrkey)
	if err != nil {
		return [32]byte{}, fmt.Errorf("executor raw: %w", err)
	}
	var b []byte
	b = append(b, byte(len(execRaw)))
	for _, c := range ccvs {
		raw, err := sorobanAddressRaw32(c)
		if err != nil {
			return [32]byte{}, fmt.Errorf("ccv raw %s: %w", c, err)
		}
		b = append(b, raw...)
	}
	b = append(b, execRaw...)
	h := crypto.Keccak256(b)
	var out [32]byte
	copy(out[:], h)
	return out, nil
}

const ccipTokenTransferV1Version byte = 1

// EncodeCcipTokenTransferV1 matches contracts/common/message::CcipTokenTransferV1::to_bytes (OnRamp lock path).
func EncodeCcipTokenTransferV1(
	amount int64,
	sourcePoolContractStrkey, sourceTokenContractStrkey string,
	destTokenAddress, tokenReceiver, extraData []byte,
) ([]byte, error) {
	if amount < 0 {
		return nil, fmt.Errorf("negative token amount")
	}
	if len(destTokenAddress) > 255 || len(tokenReceiver) > 255 || len(extraData) > 65535 {
		return nil, fmt.Errorf("token transfer field length overflow")
	}
	poolXDR, err := sorobanScValXDR(scval.AddressToScVal(sourcePoolContractStrkey))
	if err != nil {
		return nil, fmt.Errorf("pool scval xdr: %w", err)
	}
	tokenXDR, err := sorobanScValXDR(scval.AddressToScVal(sourceTokenContractStrkey))
	if err != nil {
		return nil, fmt.Errorf("token scval xdr: %w", err)
	}
	if len(poolXDR) > 255 || len(tokenXDR) > 255 {
		return nil, fmt.Errorf("scval xdr longer than 255 bytes")
	}
	var amountBE [32]byte
	binary.BigEndian.PutUint64(amountBE[16:24], 0)
	binary.BigEndian.PutUint64(amountBE[24:], uint64(amount))

	var b []byte
	b = append(b, ccipTokenTransferV1Version)
	b = append(b, amountBE[:]...)
	b = append(b, byte(len(poolXDR)))
	b = append(b, poolXDR...)
	b = append(b, byte(len(tokenXDR)))
	b = append(b, tokenXDR...)
	b = append(b, byte(len(destTokenAddress)))
	b = append(b, destTokenAddress...)
	b = append(b, byte(len(tokenReceiver)))
	b = append(b, tokenReceiver...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(extraData)))
	b = append(b, extraData...)
	return b, nil
}

// EncodeCcipTokenTransferV1Inbound builds CcipTokenTransferV1::to_bytes for inbound OffRamp execute:
// source pool/token are raw bytes (e.g. 20-byte remote addresses). Dest token and receiver must be Stellar
// **contract** strkeys (C…): we encode the raw 32-byte contract identifiers (same as many cross-chain payloads;
// OffRamp::address_from_token_bytes uses the last 32 bytes, so len==32 is ideal).
func EncodeCcipTokenTransferV1Inbound(
	amount int64,
	sourcePoolRaw, sourceTokenRaw []byte,
	destTokenStrkey, receiverContractStrkey string,
	extraData []byte,
) ([]byte, error) {
	if amount < 0 {
		return nil, fmt.Errorf("negative token amount")
	}
	if len(sourcePoolRaw) > 255 || len(sourceTokenRaw) > 255 {
		return nil, fmt.Errorf("source pool/token length overflow")
	}
	if len(extraData) > 65535 {
		return nil, fmt.Errorf("extra_data length overflow")
	}
	destRaw, err := strkey.Decode(strkey.VersionByteContract, destTokenStrkey)
	if err != nil {
		return nil, fmt.Errorf("dest token strkey: %w", err)
	}
	if len(destRaw) != 32 {
		return nil, fmt.Errorf("dest token raw id len %d, want 32", len(destRaw))
	}
	recvRaw, err := strkey.Decode(strkey.VersionByteContract, receiverContractStrkey)
	if err != nil {
		return nil, fmt.Errorf("receiver strkey: %w", err)
	}
	if len(recvRaw) != 32 {
		return nil, fmt.Errorf("receiver raw id len %d, want 32", len(recvRaw))
	}
	var amountBE [32]byte
	binary.BigEndian.PutUint64(amountBE[16:24], 0)
	binary.BigEndian.PutUint64(amountBE[24:], uint64(amount))

	var b []byte
	b = append(b, ccipTokenTransferV1Version)
	b = append(b, amountBE[:]...)
	b = append(b, byte(len(sourcePoolRaw)))
	b = append(b, sourcePoolRaw...)
	b = append(b, byte(len(sourceTokenRaw)))
	b = append(b, sourceTokenRaw...)
	b = append(b, byte(len(destRaw)))
	b = append(b, destRaw...)
	b = append(b, byte(len(recvRaw)))
	b = append(b, recvRaw...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(extraData)))
	b = append(b, extraData...)
	return b, nil
}

// StellarOnrampMessageIDInput is the off-chain mirror of CcipMessageV1 built in OnRamp::forward_from_router
// before keccak256 (contracts/common/message + contracts/onramp/src/lib.rs).
type StellarOnrampMessageIDInput struct {
	SourceChainSelector uint64
	DestChainSelector   uint64
	SequenceNumber      uint64
	GasLimit            uint32
	BlockConfirmations  uint32 // maps to CcipMessageV1.finality (from GenericExtraArgsV3.block_confirmations)
	Ccvs                []string
	Executor            string
	OnRampContractID    string
	OffRampRawBytes     []byte // raw Bytes from dest config; wrapped as ScVal::Bytes on-chain
	SenderStrkey        string
	Receiver            []byte
	Data                []byte
	// TokenTransfer is CcipTokenTransferV1::to_bytes output, or nil/empty if no tokens.
	TokenTransfer []byte
}

// PredictStellarOnrampMessageID returns keccak256(encode(CcipMessageV1)) matching OnRamp message_id / Router event.
func PredictStellarOnrampMessageID(in StellarOnrampMessageIDInput) ([32]byte, error) {
	if len(in.Receiver) > 255 || len(in.OffRampRawBytes) > 255 {
		return [32]byte{}, fmt.Errorf("receiver or off_ramp length overflow")
	}
	ccvHash, err := computeCcvAndExecutorHash(in.Ccvs, in.Executor)
	if err != nil {
		return [32]byte{}, err
	}
	onXDR, err := sorobanScValXDR(scval.AddressToScVal(in.OnRampContractID))
	if err != nil {
		return [32]byte{}, fmt.Errorf("onramp scval: %w", err)
	}
	offXDR, err := sorobanScValXDR(scval.BytesToScVal(in.OffRampRawBytes))
	if err != nil {
		return [32]byte{}, fmt.Errorf("off_ramp scval: %w", err)
	}
	senderXDR, err := sorobanScValXDR(scval.AddressToScVal(in.SenderStrkey))
	if err != nil {
		return [32]byte{}, fmt.Errorf("sender scval: %w", err)
	}
	if len(onXDR) > 255 || len(offXDR) > 255 || len(senderXDR) > 255 {
		return [32]byte{}, fmt.Errorf("onramp/offramp/sender XDR length overflow")
	}
	tt := in.TokenTransfer
	if tt == nil {
		tt = []byte{}
	}
	encoded, err := encodeCcipMessageV1(ccipV1Wire{
		SourceChainSelector: in.SourceChainSelector,
		DestChainSelector:   in.DestChainSelector,
		SequenceNumber:      in.SequenceNumber,
		ExecutionGasLimit:   in.GasLimit,
		CcipReceiveGasLimit: 0,
		Finality:            in.BlockConfirmations,
		CcvExecutorHash:     ccvHash,
		OnRampAddress:       onXDR,
		OffRampAddress:      offXDR,
		Sender:              senderXDR,
		Receiver:            in.Receiver,
		DestBlob:            nil,
		TokenTransfer:       tt,
		Data:                in.Data,
	})
	if err != nil {
		return [32]byte{}, err
	}
	return keccak256MessageID(encoded), nil
}
