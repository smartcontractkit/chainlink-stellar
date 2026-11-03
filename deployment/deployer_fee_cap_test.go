package deployment

import (
	"context"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestAssembleTransaction_ResourceFeeCap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		maxResourceFee int64
		attempt        int
		budgetExceeded bool
		wantFee        xdr.Int64
		wantErr        bool
	}{
		{
			name:           "above cap",
			maxResourceFee: 1_000_000,
			attempt:        1,
			wantErr:        true,
		},
		{
			name:           "at cap",
			maxResourceFee: 1_250_000,
			attempt:        1,
			wantFee:        1_250_000,
		},
		{
			name:           "scaled fee above cap",
			maxResourceFee: 4_000_000,
			attempt:        3,
			budgetExceeded: true,
			wantErr:        true,
		},
		{
			name:           "scaled fee at cap",
			maxResourceFee: 5_000_000,
			attempt:        3,
			budgetExceeded: true,
			wantFee:        5_000_000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newTestDeployer(t, &mockRPC{})
			WithMaxResourceFee(tc.maxResourceFee)(d)

			dataXDR, err := xdr.MarshalBase64(xdr.SorobanTransactionData{ResourceFee: 1_000_000})
			require.NoError(t, err)

			ctx := context.Background()
			sourceAccount, err := d.getSourceAccount(ctx)
			require.NoError(t, err)
			tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
				SourceAccount:        sourceAccount,
				IncrementSequenceNum: true,
				Operations:           []txnbuild.Operation{testInvokeOp(d.signer.Address())},
				BaseFee:              txnbuild.MinBaseFee,
				Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimebounds(0, time.Now().Add(time.Minute).Unix())},
			})
			require.NoError(t, err)

			assembled, err := d.assembleTransaction(ctx, tx, protocolrpc.SimulateTransactionResponse{
				TransactionDataXDR: dataXDR,
				MinResourceFee:     1_000_000,
			}, time.Now().Add(time.Minute), tc.attempt, tc.budgetExceeded)
			if tc.wantErr {
				require.ErrorContains(t, err, "exceeds configured cap")
				return
			}
			require.NoError(t, err)

			op, ok := assembled.Operations()[0].(*txnbuild.InvokeHostFunction)
			require.True(t, ok)
			require.Equal(t, tc.wantFee, op.Ext.SorobanData.ResourceFee)
		})
	}
}

func TestRestoreFootprint_ResourceFeeCap(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		maxResourceFee int64
		wantErr        bool
	}{
		{name: "above cap", maxResourceFee: 100_000, wantErr: true},
		{name: "at cap", maxResourceFee: 125_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dataXDR, err := xdr.MarshalBase64(xdr.SorobanTransactionData{ResourceFee: 100_000})
			require.NoError(t, err)

			mock := &mockRPC{
				SendTransactionFn: func(_ context.Context, _ protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
					return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "r1"}, nil
				},
				GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
					return successGetTxResponse(t), nil
				},
			}
			d := newTestDeployer(t, mock)
			WithMaxResourceFee(tc.maxResourceFee)(d)

			err = d.restoreFootprint(context.Background(), protocolrpc.RestorePreamble{
				TransactionDataXDR: dataXDR,
				MinResourceFee:     100_000,
			})
			if tc.wantErr {
				require.ErrorContains(t, err, "exceeds configured cap")
				return
			}
			require.NoError(t, err)
		})
	}
}
