package ccvclient

import (
	"context"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

// RPCClient is the unified Stellar RPC interface used by TXM, SourceReader,
// DestinationReader, Deployer, and other components. It is the superset of
// all Soroban RPC methods used across the chainlink-stellar repository.
// *rpcclient.Client satisfies this interface.
type RPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
	GetLatestLedger(ctx context.Context) (protocolrpc.GetLatestLedgerResponse, error)
	GetLedgers(ctx context.Context, req protocolrpc.GetLedgersRequest) (protocolrpc.GetLedgersResponse, error)
}

var _ RPCClient = (*rpcclient.Client)(nil)

// Client wraps an RPCClient with optional convenience helpers.
type Client struct {
	RPC RPCClient
}

// NewClient creates a new Client wrapping a concrete *rpcclient.Client.
func NewClient(rpcClient *rpcclient.Client) *Client {
	return &Client{RPC: rpcClient}
}

// NewClientFromInterface creates a new Client from any RPCClient implementation.
func NewClientFromInterface(rpc RPCClient) *Client {
	return &Client{RPC: rpc}
}
