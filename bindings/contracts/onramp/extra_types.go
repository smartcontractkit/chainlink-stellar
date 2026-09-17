// Package onramp extra_types.go contains hand-maintained types that the
// stellar-bindings-generator does NOT emit (they are not declared in the onramp
// interface or events source the generator reads), but that consumer code
// (ccv/common, tests/ccv/chain, tests/integration) depends on.
//
// This file is deliberately separate from the generated types.go / client.go so
// that `make generate-bindings` (which rewrites only those two files) does not
// wipe these definitions. If the onramp interface ever exposes these types
// natively, remove the corresponding definitions here to avoid duplicates.
package onramp

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// GenericExtraArgsV3 represents the GenericExtraArgsV3 struct from the contract.
// It is encoded to XDR by ccv/common.EncodeExtraArgsV3 and parsed on-chain via
// GenericExtraArgsV3::from_xdr. The onramp interface represents extra_args as
// opaque bytes, so this typed struct is maintained here.
type GenericExtraArgsV3 struct {
	BlockConfirmations uint32
	CcvArgs            [][]byte
	Ccvs               []string
	Executor           string
	ExecutorArgs       []byte
	GasLimit           uint32
	TokenArgs          []byte
	TokenReceiver      []byte
}

// ToScVal converts GenericExtraArgsV3 to an xdr.ScVal for contract calls.
func (s GenericExtraArgsV3) ToScVal() (xdr.ScVal, error) {
	return scval.BuildStructScVal(map[string]xdr.ScVal{
		"block_confirmations": scval.Uint32ToScVal(s.BlockConfirmations),
		"ccv_args":            scval.BytesSliceToScVal(s.CcvArgs),
		"ccvs":                scval.AddressSliceToScVal(s.Ccvs),
		"executor":            scval.AddressToScVal(s.Executor),
		"executor_args":       scval.BytesToScVal(s.ExecutorArgs),
		"gas_limit":           scval.Uint32ToScVal(s.GasLimit),
		"token_args":          scval.BytesToScVal(s.TokenArgs),
		"token_receiver":      scval.BytesToScVal(s.TokenReceiver),
	})
}

// GenericExtraArgsV3FromScVal parses an xdr.ScVal into GenericExtraArgsV3.
func GenericExtraArgsV3FromScVal(val xdr.ScVal) (*GenericExtraArgsV3, error) {
	scMap, ok := val.GetMap()
	if !ok || scMap == nil {
		return nil, fmt.Errorf("not a map type")
	}

	result := &GenericExtraArgsV3{}
	for _, entry := range *scMap {
		key, ok := entry.Key.GetSym()
		if !ok {
			continue
		}

		switch string(key) {
		case "block_confirmations":
			v, ok := entry.Val.GetU32()
			if !ok {
				return nil, fmt.Errorf("block_confirmations is not u32")
			}
			result.BlockConfirmations = uint32(v)
		case "ccv_args":
			vec, ok := entry.Val.GetVec()
			if !ok || vec == nil {
				return nil, fmt.Errorf("ccv_args is not a vec")
			}
			result.CcvArgs = make([][]byte, len(*vec))
			for i, item := range *vec {
				v, ok := item.GetBytes()
				if !ok {
					return nil, fmt.Errorf("vec item is not bytes")
				}
				result.CcvArgs[i] = []byte(v)
			}
		case "ccvs":
			vec, ok := entry.Val.GetVec()
			if !ok || vec == nil {
				return nil, fmt.Errorf("ccvs is not a vec")
			}
			result.Ccvs = make([]string, len(*vec))
			for i, item := range *vec {
				v, err := scval.AddressFromScVal(item)
				if err != nil {
					return nil, err
				}
				result.Ccvs[i] = v
			}
		case "executor":
			v, err := scval.AddressFromScVal(entry.Val)
			if err != nil {
				return nil, fmt.Errorf("executor: %w", err)
			}
			result.Executor = v
		case "executor_args":
			v, ok := entry.Val.GetBytes()
			if !ok {
				return nil, fmt.Errorf("executor_args is not bytes")
			}
			result.ExecutorArgs = []byte(v)
		case "gas_limit":
			v, ok := entry.Val.GetU32()
			if !ok {
				return nil, fmt.Errorf("gas_limit is not u32")
			}
			result.GasLimit = uint32(v)
		case "token_args":
			v, ok := entry.Val.GetBytes()
			if !ok {
				return nil, fmt.Errorf("token_args is not bytes")
			}
			result.TokenArgs = []byte(v)
		case "token_receiver":
			v, ok := entry.Val.GetBytes()
			if !ok {
				return nil, fmt.Errorf("token_receiver is not bytes")
			}
			result.TokenReceiver = []byte(v)
		}
	}

	return result, nil
}

// Receipt represents the Receipt struct from the contract. fee_token_amount is
// *big.Int for lossless i128 handling.
type Receipt struct {
	DestBytesOverhead uint32
	DestGasLimit      uint32
	ExtraArgs         []byte
	FeeTokenAmount    *big.Int
	Issuer            string
}

// ToScVal converts Receipt to an xdr.ScVal for contract calls.
func (s Receipt) ToScVal() (xdr.ScVal, error) {
	return scval.BuildStructScVal(map[string]xdr.ScVal{
		"dest_bytes_overhead": scval.Uint32ToScVal(s.DestBytesOverhead),
		"dest_gas_limit":      scval.Uint32ToScVal(s.DestGasLimit),
		"extra_args":          scval.BytesToScVal(s.ExtraArgs),
		"fee_token_amount":    scval.I128ToScVal(s.FeeTokenAmount),
		"issuer":              scval.AddressToScVal(s.Issuer),
	})
}

// ReceiptFromScVal parses an xdr.ScVal into Receipt.
func ReceiptFromScVal(val xdr.ScVal) (*Receipt, error) {
	scMap, ok := val.GetMap()
	if !ok || scMap == nil {
		return nil, fmt.Errorf("not a map type")
	}

	result := &Receipt{}
	for _, entry := range *scMap {
		key, ok := entry.Key.GetSym()
		if !ok {
			continue
		}

		switch string(key) {
		case "dest_bytes_overhead":
			v, ok := entry.Val.GetU32()
			if !ok {
				return nil, fmt.Errorf("dest_bytes_overhead is not u32")
			}
			result.DestBytesOverhead = uint32(v)
		case "dest_gas_limit":
			v, ok := entry.Val.GetU32()
			if !ok {
				return nil, fmt.Errorf("dest_gas_limit is not u32")
			}
			result.DestGasLimit = uint32(v)
		case "extra_args":
			v, ok := entry.Val.GetBytes()
			if !ok {
				return nil, fmt.Errorf("extra_args is not bytes")
			}
			result.ExtraArgs = []byte(v)
		case "fee_token_amount":
			v, err := scval.I128FromScVal(entry.Val)
			if err != nil {
				return nil, fmt.Errorf("fee_token_amount: %w", err)
			}
			result.FeeTokenAmount = v
		case "issuer":
			v, err := scval.AddressFromScVal(entry.Val)
			if err != nil {
				return nil, fmt.Errorf("issuer: %w", err)
			}
			result.Issuer = v
		}
	}

	return result, nil
}

// CCIPMessageSentEvent represents the CCIPMessageSentEvent event.
// Topics: [onramp_1_7_CCIPMessageSent]. token_amount_before_fees is *big.Int
// for lossless i128 handling.
type CCIPMessageSentEvent struct {
	DestChainSelector     uint64
	SequenceNumber        uint64
	Sender                string
	MessageId             [32]byte
	FeeToken              string
	TokenAmountBeforeFees *big.Int
	EncodedMessage        []byte
	Receipts              []Receipt
	VerifierBlobs         [][]byte
	// Event metadata
	Ledger uint32
	TxHash string
}

// CCIPMessageSentEventTopic is the event topic identifier.
const CCIPMessageSentEventTopic = "onramp_1_7_CCIPMessageSent"

// WaitForCCIPMessageSentEvent waits for a CCIPMessageSentEvent event.
func (c *OnRampClient) WaitForCCIPMessageSentEvent(ctx context.Context, startLedger uint32, timeout time.Duration, filter func(*CCIPMessageSentEvent) bool) (*CCIPMessageSentEvent, error) {
	startTime := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if time.Since(startTime) > timeout {
				return nil, fmt.Errorf("timeout waiting for event")
			}

			events, err := c.invoker.GetEvents(ctx, c.contractID, startLedger, []string{CCIPMessageSentEventTopic})
			if err != nil {
				continue
			}

			for _, e := range events {
				parsed, err := ParseCCIPMessageSentEvent(e)
				if err != nil {
					continue
				}
				if filter == nil || filter(parsed) {
					return parsed, nil
				}
			}
		}
	}
}

// ParseCCIPMessageSentEvent parses a CCIPMessageSentEvent from an EventInfo.
func ParseCCIPMessageSentEvent(e protocolrpc.EventInfo) (*CCIPMessageSentEvent, error) {
	var eventVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(e.ValueXDR, &eventVal); err != nil {
		return nil, fmt.Errorf("failed to decode event: %w", err)
	}

	scMap, ok := eventVal.GetMap()
	if !ok || scMap == nil {
		return nil, fmt.Errorf("event is not a map")
	}

	result := &CCIPMessageSentEvent{
		Ledger: uint32(e.Ledger),
		TxHash: e.TransactionHash,
	}

	for _, entry := range *scMap {
		key, ok := entry.Key.GetSym()
		if !ok {
			continue
		}

		switch string(key) {
		case "dest_chain_selector":
			v, err := scval.Uint64FromScVal(entry.Val)
			if err == nil {
				result.DestChainSelector = v
			}
		case "sequence_number":
			v, err := scval.Uint64FromScVal(entry.Val)
			if err == nil {
				result.SequenceNumber = v
			}
		case "sender":
			v, err := scval.AddressFromScVal(entry.Val)
			if err == nil {
				result.Sender = v
			}
		case "message_id":
			v, err := scval.Bytes32FromScVal(entry.Val)
			if err == nil {
				result.MessageId = v
			}
		case "fee_token":
			v, err := scval.AddressFromScVal(entry.Val)
			if err == nil {
				result.FeeToken = v
			}
		case "token_amount_before_fees":
			v, err := scval.I128FromScVal(entry.Val)
			if err == nil {
				result.TokenAmountBeforeFees = v
			}
		case "encoded_message":
			v, ok := entry.Val.GetBytes()
			if ok {
				result.EncodedMessage = []byte(v)
			}
		case "receipts":
			vec, ok := entry.Val.GetVec()
			if ok && vec != nil {
				parsed := make([]Receipt, 0, len(*vec))
				for _, item := range *vec {
					v, err := ReceiptFromScVal(item)
					if err == nil {
						parsed = append(parsed, *v)
					}
				}
				result.Receipts = parsed
			}
		case "verifier_blobs":
			vec, ok := entry.Val.GetVec()
			if ok && vec != nil {
				parsed := make([][]byte, len(*vec))
				for i, item := range *vec {
					v, ok := item.GetBytes()
					if ok {
						parsed[i] = []byte(v)
					}
				}
				result.VerifierBlobs = parsed
			}
		}
	}

	return result, nil
}
