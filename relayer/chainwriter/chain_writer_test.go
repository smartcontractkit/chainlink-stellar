
package chainwriter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"

	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
	"github.com/stellar/go-stellar-sdk/strkey"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
)

type mockTxManager struct {
	txm.TxManager // embedded to satisfy interface implicitly
	enqueueFunc  func(ctx context.Context, req txm.TxRequest) (string, error)
	simulateFunc func(ctx context.Context, req txm.TxRequest) (protocolrpc.SimulateTransactionResponse, error)
	getStatusFunc func(transactionID string) (commontypes.TransactionStatus, error)
}

func (m *mockTxManager) Enqueue(ctx context.Context, req txm.TxRequest) (string, error) {
	if m.enqueueFunc != nil {
		return m.enqueueFunc(ctx, req)
	}
	return "mock-tx-id", nil
}

func (m *mockTxManager) Simulate(ctx context.Context, req txm.TxRequest) (protocolrpc.SimulateTransactionResponse, error) {
	if m.simulateFunc != nil {
		return m.simulateFunc(ctx, req)
	}
	return protocolrpc.SimulateTransactionResponse{}, nil
}

func (m *mockTxManager) GetStatus(transactionID string) (commontypes.TransactionStatus, error) {
	if m.getStatusFunc != nil {
		return m.getStatusFunc(transactionID)
	}
	return commontypes.Unknown, nil
}

type mockKeystore struct {
	core.Keystore
	accounts []string
	err      error
}

func (m *mockKeystore) Accounts(ctx context.Context) ([]string, error) {
	return m.accounts, m.err
}

func TestChainWriter_SubmitTransaction(t *testing.T) {
	lggr := logger.Test(t)
	ks := &mockKeystore{accounts: []string{"GA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA"}}
	
	validConfig := ChainWriterConfig{
		Contracts: map[string]*ContractConfig{
			"MyContract": {
				Name: "MyContract",
				Functions: map[string]*FunctionConfig{
					"myFunction": {
						FromAddress: "GA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA",
					},
				},
			},
		},
	}

	validContractID, _ := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))

	t.Run("success", func(t *testing.T) {
		enqueuedCount := 0
		simulatedCount := 0

		mockTxm := &mockTxManager{
			enqueueFunc: func(ctx context.Context, req txm.TxRequest) (string, error) {
				enqueuedCount++
				assert.Equal(t, "my-tx-id", req.ID)
				assert.Equal(t, "GA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA", req.FromAddress)
				assert.Len(t, req.Operations, 1)
				return "my-tx-id", nil
			},
			simulateFunc: func(ctx context.Context, req txm.TxRequest) (protocolrpc.SimulateTransactionResponse, error) {
				simulatedCount++
				return protocolrpc.SimulateTransactionResponse{}, nil
			},
		}

		cw, err := NewChainWriter(lggr, mockTxm, ks, validConfig)
		require.NoError(t, err)

		err = cw.Start(t.Context())
		require.NoError(t, err)

		args := []stellartypes.ScVal{
			{Type: stellartypes.ScValTypeU32, U32: func(v uint32) *uint32 { return &v }(42)},
		}

		err = cw.SubmitTransaction(
			t.Context(),
			"MyContract",
			"myFunction",
			args,
			"my-tx-id",
			validContractID,
			&commontypes.TxMeta{},
			nil,
		)
		require.NoError(t, err)
		assert.Equal(t, 1, enqueuedCount)
		assert.Equal(t, 1, simulatedCount)
	})
	
	t.Run("unsupported args type", func(t *testing.T) {
		mockTxm := &mockTxManager{}
		cw, _ := NewChainWriter(lggr, mockTxm, ks, validConfig)

		err := cw.SubmitTransaction(
			t.Context(),
			"MyContract",
			"myFunction",
			"invalid-args-type",
			"my-tx-id",
			validContractID,
			&commontypes.TxMeta{},
			nil,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported args type")
	})

	t.Run("simulation failure", func(t *testing.T) {
		mockTxm := &mockTxManager{
			simulateFunc: func(ctx context.Context, req txm.TxRequest) (protocolrpc.SimulateTransactionResponse, error) {
				return protocolrpc.SimulateTransactionResponse{Error: "simulation failed due to insufficient gas"}, nil
			},
		}
		cw, _ := NewChainWriter(lggr, mockTxm, ks, validConfig)

		args := []stellartypes.ScVal{}
		err := cw.SubmitTransaction(
			t.Context(),
			"MyContract",
			"myFunction",
			args,
			"my-tx-id",
			validContractID,
			&commontypes.TxMeta{},
			nil,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "simulation returned error")
	})
}

