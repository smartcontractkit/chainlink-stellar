package destinationreader

import (
	"fmt"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// ExecutionStateChangedEvent represents the ExecutionStateChangedEvent event.
// Topics: [offramp_1_7_ExecStateChanged]
type ExecutionStateChangedEvent struct {
	SourceChainSelector uint64
	SequenceNumber      uint64
	MessageId           [32]byte
	State               offrampbindings.MessageExecutionState
	ReturnData          []byte
	// Event metadata
	Ledger uint32
	TxHash string
}

// ExecutionStateChangedEventTopic is the event topic identifier.
const ExecutionStateChangedEventTopic = "offramp_1_7_ExecStateChanged"

// ParseExecutionStateChangedEvent decodes an ExecutionStateChangedEvent from an
// RPC event info entry.
func ParseExecutionStateChangedEvent(e protocolrpc.EventInfo) (*ExecutionStateChangedEvent, error) {
	var eventVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(e.ValueXDR, &eventVal); err != nil {
		return nil, fmt.Errorf("failed to decode event: %w", err)
	}

	scMap, ok := eventVal.GetMap()
	if !ok || scMap == nil {
		return nil, fmt.Errorf("event is not a map")
	}

	result := &ExecutionStateChangedEvent{
		Ledger: uint32(e.Ledger),
		TxHash: e.TransactionHash,
	}

	for _, entry := range *scMap {
		key, ok := entry.Key.GetSym()
		if !ok {
			continue
		}

		switch string(key) {
		case "source_chain_selector":
			v, err := scval.Uint64FromScVal(entry.Val)
			if err == nil {
				result.SourceChainSelector = v
			}
		case "sequence_number":
			v, err := scval.Uint64FromScVal(entry.Val)
			if err == nil {
				result.SequenceNumber = v
			}
		case "message_id":
			v, err := scval.Bytes32FromScVal(entry.Val)
			if err == nil {
				result.MessageId = v
			}
		case "state":
			v, err := offrampbindings.MessageExecutionStateFromScVal(entry.Val)
			if err == nil {
				result.State = v
			}
		case "return_data":
			v, ok := entry.Val.GetBytes()
			if ok {
				result.ReturnData = []byte(v)
			}
		}
	}

	return result, nil
}
