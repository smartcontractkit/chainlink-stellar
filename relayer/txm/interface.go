package txm

import (
	"context"
	"math/big"

	"github.com/smartcontractkit/chainlink-common/pkg/services"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

// TxManager defines the interface for the Stellar Transaction Manager.
type TxManager interface {
	services.Service

	Enqueue(ctx context.Context, req TxRequest) (string, error)
	EnqueueAndWait(ctx context.Context, req TxRequest) (*TxResult, error)
	Simulate(ctx context.Context, req TxRequest) (protocolrpc.SimulateTransactionResponse, error)

	GetStatus(transactionID string) (commontypes.TransactionStatus, error)
	GetTransactionResult(transactionID string) (*TxResult, error)
	GetTransactionFee(transactionID string) (*big.Int, error)
	InflightCount() (int, int)
}
