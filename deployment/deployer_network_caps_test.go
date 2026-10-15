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

// TestAssembleTransaction_ClampsScaledResourcesToNetworkCaps pins the
// submission-time clamp: the retry scaling in assembleTransaction must never
// push a declared resource past the network config's per-tx caps — the node
// rejects such a transaction at validation ("transaction instructions
// resources exceed network config limit"), so the send fails without ever
// executing. CI shape: 28.8M-instruction simulation ×4 on attempt 3 =
// 115.2M declared vs the localnet 100M per-tx cap.
func TestAssembleTransaction_ClampsScaledResourcesToNetworkCaps(t *testing.T) {
	t.Parallel()

	seq := testInitialSeq
	mock := &mockRPC{
		GetLedgerEntriesFn: func(_ context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
			return networkCapsLedgerEntries(t, req, &seq), nil
		},
	}
	d := newTestDeployer(t, mock)

	// The bare simulation's resources; attempt 3 scales them by 4.
	sorobanData := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Instructions:  30_000_000,
			DiskReadBytes: 10_000,
			WriteBytes:    50_000,
		},
		ResourceFee: 1_000_000,
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

	assembled, err := d.assembleTransaction(ctx, tx, protocolrpc.SimulateTransactionResponse{
		TransactionDataXDR: dataXDR,
		MinResourceFee:     1_000_000,
	}, time.Now().Add(time.Minute), 3)
	require.NoError(t, err)

	op, ok := assembled.Operations()[0].(*txnbuild.InvokeHostFunction)
	require.True(t, ok)
	res := op.Ext.SorobanData.Resources
	// 30M ×4 = 120M, past the 100M cap → clamped to the cap.
	require.Equal(t, xdr.Uint32(100_000_000), res.Instructions)
	// 10K ×4 = 40K under the 200K disk-read cap; 50K ×4 = 200K exactly at the
	// write cap → both unclamped.
	require.Equal(t, xdr.Uint32(40_000), res.DiskReadBytes)
	require.Equal(t, xdr.Uint32(200_000), res.WriteBytes)
}

// TestAssembleTransaction_NoCapsLeavesResourcesUnclamped pins the best-effort
// fallback: when the network-config entries cannot be read (here the RPC
// answers every ledger-key request with the account entry), the assembled
// values — scaled or not — pass through unchanged.
func TestAssembleTransaction_NoCapsLeavesResourcesUnclamped(t *testing.T) {
	t.Parallel()

	// newTestDeployer installs the account-entry-only GetLedgerEntries mock.
	d := newTestDeployer(t, &mockRPC{})

	sorobanData := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{Instructions: 30_000_000},
		ResourceFee: 1_000_000,
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

	assembled, err := d.assembleTransaction(ctx, tx, protocolrpc.SimulateTransactionResponse{
		TransactionDataXDR: dataXDR,
		MinResourceFee:     1_000_000,
	}, time.Now().Add(time.Minute), 3)
	require.NoError(t, err)

	op, ok := assembled.Operations()[0].(*txnbuild.InvokeHostFunction)
	require.True(t, ok)
	// 30M ×4 = 120M with no readable cap → passes through.
	require.Equal(t, xdr.Uint32(120_000_000), op.Ext.SorobanData.Resources.Instructions)
}

// networkCapsLedgerEntries serves the signer account entry and the Soroban
// network-config entries (compute + ledger cost) with localnet-shaped caps:
// 100M per-tx instructions, 200K per-tx disk read/write bytes.
func networkCapsLedgerEntries(t *testing.T, req protocolrpc.GetLedgerEntriesRequest, seq *int64) protocolrpc.GetLedgerEntriesResponse {
	t.Helper()
	var entries []protocolrpc.LedgerEntryResult
	for _, keyB64 := range req.Keys {
		var key xdr.LedgerKey
		require.NoError(t, xdr.SafeUnmarshalBase64(keyB64, &key))
		switch key.Type {
		case xdr.LedgerEntryTypeAccount:
			entry := xdr.LedgerEntryData{
				Type: xdr.LedgerEntryTypeAccount,
				Account: &xdr.AccountEntry{
					AccountId: key.Account.AccountId,
					SeqNum:    xdr.SequenceNumber(*seq),
				},
			}
			b64, err := xdr.MarshalBase64(entry)
			require.NoError(t, err)
			entries = append(entries, protocolrpc.LedgerEntryResult{DataXDR: b64})
		case xdr.LedgerEntryTypeConfigSetting:
			entry := xdr.LedgerEntryData{
				Type:          xdr.LedgerEntryTypeConfigSetting,
				ConfigSetting: &xdr.ConfigSettingEntry{ConfigSettingId: key.ConfigSetting.ConfigSettingId},
			}
			switch key.ConfigSetting.ConfigSettingId {
			case xdr.ConfigSettingIdConfigSettingContractComputeV0:
				entry.ConfigSetting.ContractCompute = &xdr.ConfigSettingContractComputeV0{
					TxMaxInstructions: 100_000_000,
				}
			case xdr.ConfigSettingIdConfigSettingContractLedgerCostV0:
				entry.ConfigSetting.ContractLedgerCost = &xdr.ConfigSettingContractLedgerCostV0{
					TxMaxDiskReadBytes: 200_000,
					TxMaxWriteBytes:    200_000,
				}
			}
			b64, err := xdr.MarshalBase64(entry)
			require.NoError(t, err)
			entries = append(entries, protocolrpc.LedgerEntryResult{DataXDR: b64})
		}
	}
	return protocolrpc.GetLedgerEntriesResponse{Entries: entries}
}
