package ccvchain

import (
	"context"
	"fmt"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"

	destinationreader "github.com/smartcontractkit/chainlink-stellar/ccv/destination_reader"
)

// CCIPMessageSentEvent represents the CCIPMessageSentEvent event.
// Topics: [onramp_1_7_CCIPMessageSent]
type CCIPMessageSentEvent struct {
	DestChainSelector     uint64
	SequenceNumber        uint64
	Sender                string
	MessageId             [32]byte
	FeeToken              string
	TokenAmountBeforeFees int64
	EncodedMessage        []byte
	Receipts              []onrampbindings.Receipt
	VerifierBlobs         [][]byte
	// Event metadata
	Ledger uint32
	TxHash string
}

// CCIPMessageSentEventTopic is the event topic identifier.
const CCIPMessageSentEventTopic = "onramp_1_7_CCIPMessageSent"

// ParseCCIPMessageSentEvent decodes a CCIPMessageSentEvent from an RPC event
// info entry.
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
				parsed := make([]onrampbindings.Receipt, 0, len(*vec))
				for _, item := range *vec {
					v, err := onrampbindings.ReceiptFromScVal(item)
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

// waitForSorobanEvent polls a contract for events matching topic from
// startLedger to the latest ledger, returning the first parsed event accepted
// by filter (nil filter accepts any).
func waitForSorobanEvent[T any](
	ctx context.Context,
	rpcClient *rpcclient.Client,
	contractID, topic string,
	startLedger uint32,
	timeout time.Duration,
	parse func(protocolrpc.EventInfo) (*T, error),
	filter func(*T) bool,
) (*T, error) {
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

			events, err := getContractEvents(ctx, rpcClient, contractID, topic, startLedger)
			if err != nil {
				continue
			}
			for _, e := range events {
				parsed, err := parse(e)
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

// waitForCCIPMessageSentEvent waits for a CCIPMessageSentEvent from the chain's
// OnRamp. It replaces the OnRampClient helper removed in newer bindings versions.
func (c *Chain) waitForCCIPMessageSentEvent(ctx context.Context, startLedger uint32, timeout time.Duration, filter func(*CCIPMessageSentEvent) bool) (*CCIPMessageSentEvent, error) {
	return waitForSorobanEvent(ctx, c.rpcClient, c.onRampContractID, CCIPMessageSentEventTopic, startLedger, timeout, ParseCCIPMessageSentEvent, filter)
}

// waitForExecutionStateChangedEvent waits for an ExecutionStateChangedEvent from
// the chain's OffRamp. It replaces the OffRampClient helper removed in newer
// bindings versions.
func (c *Chain) waitForExecutionStateChangedEvent(ctx context.Context, startLedger uint32, timeout time.Duration, filter func(*destinationreader.ExecutionStateChangedEvent) bool) (*destinationreader.ExecutionStateChangedEvent, error) {
	return waitForSorobanEvent(ctx, c.rpcClient, c.offRampContractID, destinationreader.ExecutionStateChangedEventTopic, startLedger, timeout, destinationreader.ParseExecutionStateChangedEvent, filter)
}

// getContractEvents fetches contract events for a single topic from startLedger
// to the latest closed ledger.
func getContractEvents(
	ctx context.Context,
	rpcClient *rpcclient.Client,
	contractID, topic string,
	startLedger uint32,
) ([]protocolrpc.EventInfo, error) {
	topicScVal := scval.SymbolToScValPtr(topic)
	zeroOrMore := protocolrpc.WildCardZeroOrMore

	resp, err := rpcClient.GetEvents(ctx, protocolrpc.GetEventsRequest{
		StartLedger: startLedger,
		Filters: []protocolrpc.EventFilter{
			{
				EventType:   protocolrpc.EventTypeSet{protocolrpc.EventTypeContract: nil},
				ContractIDs: []string{contractID},
				Topics: []protocolrpc.TopicFilter{
					{
						{ScVal: topicScVal},
						{Wildcard: &zeroOrMore},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return resp.Events, nil
}
