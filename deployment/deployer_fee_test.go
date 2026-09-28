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

// TestAssembleTransaction_FeeCountsResourceFeeOnce pins the inclusion-fee rule:
// the envelope fee is the Soroban resource fee (with its bump, carried in
// SorobanData.ResourceFee) plus the per-operation base fee. MinResourceFee+bump
// must never also go into BaseFee — txnbuild adds both, so that double-counts
// the resource fee and overflows the uint32 max fee for large simulations
// (observed on Stellar testnet: timelock initialize sims at ~2e9 stroops).
func TestAssembleTransaction_FeeCountsResourceFeeOnce(t *testing.T) {
	t.Parallel()

	mock := &mockRPC{}
	d := newTestDeployer(t, mock)

	const (
		simMinResourceFee = 1_000_000
		simResourceFee    = 1_000_000
		// bump = feeBumpExtra(1_000_000, 1.25) = 250_000 (above the 10_000 floor)
	)

	sorobanData := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{},
		ResourceFee: simResourceFee,
	}
	dataXDR, err := xdr.MarshalBase64(sorobanData)
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

	sim := protocolrpc.SimulateTransactionResponse{
		TransactionDataXDR: dataXDR,
		MinResourceFee:     simMinResourceFee,
	}

	assembled, err := d.assembleTransaction(ctx, tx, sim, time.Now().Add(time.Minute))
	require.NoError(t, err)

	// Resource fee (bumped once) + inclusion base fee — nothing else.
	expected := int64(simResourceFee) + 250_000 + txnbuild.MinBaseFee
	require.Equal(t, expected, assembled.MaxFee())

	// And the pre-fix bug shape must not reappear: fee != 2x(resource fee).
	require.NotEqual(t, int64(2*(simResourceFee+250_000)), assembled.MaxFee())
}

// TestRestoreFootprint_FeeCountsResourceFeeOnce pins the same rule on the
// restore path: the restore tx fee is the preamble's resource fee (with its
// bump) plus the inclusion base fee — the old code put MinResourceFee+bump
// into BaseFee as well and double-counted.
func TestRestoreFootprint_FeeCountsResourceFeeOnce(t *testing.T) {
	t.Parallel()

	const preambleMinResourceFee = 100_000
	const preambleResourceFee = 100_000
	// bump = feeBumpExtra(100_000, 1.25) = 25_000

	sorobanData := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{},
		ResourceFee: preambleResourceFee,
	}
	dataXDR, err := xdr.MarshalBase64(sorobanData)
	require.NoError(t, err)

	var capturedFee int64
	mock := &mockRPC{
		SendTransactionFn: func(_ context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error) {
			var env xdr.TransactionEnvelope
			require.NoError(t, xdr.SafeUnmarshalBase64(req.Transaction, &env))
			capturedFee = int64(env.V1.Tx.Fee)
			return protocolrpc.SendTransactionResponse{Status: "PENDING", Hash: "r1"}, nil
		},
		GetTransactionFn: func(_ context.Context, _ protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error) {
			return successGetTxResponse(t), nil
		},
	}

	d := newTestDeployer(t, mock)
	err = d.restoreFootprint(context.Background(), protocolrpc.RestorePreamble{
		TransactionDataXDR: dataXDR,
		MinResourceFee:     preambleMinResourceFee,
	})
	require.NoError(t, err)

	// Resource fee (bumped once) + inclusion base fee — nothing else.
	expected := int64(preambleResourceFee) + 25_000 + txnbuild.MinBaseFee
	require.Equal(t, expected, capturedFee)
	require.NotEqual(t, int64(2*(preambleResourceFee+25_000)), capturedFee)
}
