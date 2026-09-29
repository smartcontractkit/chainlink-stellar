package bindings

import (
	"context"
	"errors"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Invoker provides methods to invoke and simulate Soroban contracts.
// This is a common interface that can be implemented by any Stellar
// contract invocation mechanism (e.g., Deployer, RPC client wrapper).
// It is shared across all generated contract bindings.
type Invoker interface {
	InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error)
	SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error)
	GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error)
}

// ErrExternalArchivedBlocked is surfaced by the Stellar TXM when an invocation
// transaction touched archived storage entries owned by external (non-Chainlink-
// core) contracts that the relayer refused to rehydrate (see relayer/txm restore
// allowlist). The contract transmitter bridges this to the chain-agnostic
// executor.ErrExternalArchivedBlocked sentinel so the executor stops retrying and
// defers to manual re-execution. It is wrapped into the TxResult error chain by
// the TXM and propagated (via %w) through the generated binding clients.
var ErrExternalArchivedBlocked = errors.New("external archived entries blocked execution")

