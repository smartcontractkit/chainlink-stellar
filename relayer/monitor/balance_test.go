package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	clconfig "github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/monitoring/balance"

	"github.com/smartcontractkit/chainlink-stellar/relayer/monitor/mocks"
)

const testAddress = "GAAZI4TCR3TY5OJHCTJC2A4QSY6CJWJH5IAJTGKIN2ER7LBNVKOCCWN7"

func newTestClient(t *testing.T, rpc BalanceMonitorRPCClient) *balanceClient {
	t.Helper()
	return &balanceClient{
		getClient: func(context.Context) (BalanceMonitorRPCClient, error) { return rpc, nil },
		timeout:   time.Second,
		lggr:      logger.Test(t),
	}
}

// accountEntryXDR builds a base64 XDR LedgerEntryData for an account entry.
func accountEntryXDR(t *testing.T, balanceStroops int64) string {
	t.Helper()
	accountID, err := xdr.AddressToAccountId(testAddress)
	require.NoError(t, err)

	b64, err := xdr.MarshalBase64(xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.AccountEntry{
			AccountId: accountID,
			Balance:   xdr.Int64(balanceStroops),
		},
	})
	require.NoError(t, err)
	return b64
}

func entriesResponse(dataXDR string) protocolrpc.GetLedgerEntriesResponse {
	return protocolrpc.GetLedgerEntriesResponse{
		Entries: []protocolrpc.LedgerEntryResult{{DataXDR: dataXDR}},
	}
}

func TestBalanceClient_GetAccountBalance(t *testing.T) {
	t.Run("decodes balance and converts stroops to XLM", func(t *testing.T) {
		entry := accountEntryXDR(t, 1_234_567_890)
		var gotKeys []string
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error) {
				gotKeys = req.Keys
				return entriesResponse(entry), nil
			})

		got, err := newTestClient(t, rpc).GetAccountBalance(testAddress)
		require.NoError(t, err)
		assert.InDelta(t, 123.456789, got, 1e-9)
		require.Len(t, gotKeys, 1, "expected exactly one ledger key per fetch")
	})

	t.Run("unfunded account returns zero without error", func(t *testing.T) {
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
			Return(protocolrpc.GetLedgerEntriesResponse{}, nil)

		got, err := newTestClient(t, rpc).GetAccountBalance(testAddress)
		require.NoError(t, err)
		assert.Zero(t, got)
	})

	t.Run("invalid address errors", func(t *testing.T) {
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)

		_, err := newTestClient(t, rpc).GetAccountBalance("not-a-strkey")
		require.ErrorContains(t, err, "invalid stellar account address")
	})

	t.Run("rpc error propagates", func(t *testing.T) {
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
			Return(protocolrpc.GetLedgerEntriesResponse{}, errors.New("boom"))

		_, err := newTestClient(t, rpc).GetAccountBalance(testAddress)
		require.ErrorContains(t, err, "boom")
	})

	t.Run("client selection error propagates", func(t *testing.T) {
		c := &balanceClient{
			getClient: func(context.Context) (BalanceMonitorRPCClient, error) { return nil, errors.New("no live nodes") },
			timeout:   time.Second,
			lggr:      logger.Test(t),
		}
		_, err := c.GetAccountBalance(testAddress)
		require.ErrorContains(t, err, "no live nodes")
	})

	t.Run("empty entry data errors", func(t *testing.T) {
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
			Return(entriesResponse(""), nil)

		_, err := newTestClient(t, rpc).GetAccountBalance(testAddress)
		require.ErrorContains(t, err, "empty entry data")
	})

	t.Run("malformed XDR errors", func(t *testing.T) {
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
			Return(entriesResponse("!!!not-base64-xdr!!!"), nil)

		_, err := newTestClient(t, rpc).GetAccountBalance(testAddress)
		require.ErrorContains(t, err, "failed to unmarshal account entry")
	})

	t.Run("non-account entry errors", func(t *testing.T) {
		accountID, err := xdr.AddressToAccountId(testAddress)
		require.NoError(t, err)
		b64, err := xdr.MarshalBase64(xdr.LedgerEntryData{
			Type: xdr.LedgerEntryTypeTrustline,
			TrustLine: &xdr.TrustLineEntry{
				AccountId: accountID,
				Asset:     xdr.TrustLineAsset{Type: xdr.AssetTypeAssetTypeNative},
			},
		})
		require.NoError(t, err)
		rpc := mocks.NewMockBalanceMonitorRPCClient(t)
		rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
			Return(entriesResponse(b64), nil)

		_, err = newTestClient(t, rpc).GetAccountBalance(testAddress)
		require.ErrorContains(t, err, "not an account entry")
	})
}

func TestNewBalanceMonitor_RejectsZeroTimeout(t *testing.T) {
	_, err := NewBalanceMonitor(BalanceMonitorOpts{
		Config:   balance.GenericBalanceConfig{BalancePollPeriod: *clconfig.MustNewDuration(10 * time.Second)},
		Logger:   logger.Test(t),
		Keystore: mocks.NewMockKeystore(t),
		GetClient: func(ctx context.Context) (BalanceMonitorRPCClient, error) {
			return mocks.NewMockBalanceMonitorRPCClient(t), nil
		},
		Timeout: 0,
	})
	require.ErrorContains(t, err, "timeout must be positive")
}

// TestBalanceMonitor_StaysHealthyOnRPCErrors starts the full monitor with an
// always-failing RPC and asserts cycles are skipped without degrading health.
func TestBalanceMonitor_StaysHealthyOnRPCErrors(t *testing.T) {
	lggr, logs := logger.TestObserved(t, zapcore.ErrorLevel)
	rpc := mocks.NewMockBalanceMonitorRPCClient(t)
	rpc.EXPECT().GetLedgerEntries(mock.Anything, mock.Anything).
		Return(protocolrpc.GetLedgerEntriesResponse{}, errors.New("node down"))

	ks := mocks.NewMockKeystore(t)
	ks.EXPECT().Accounts(mock.Anything).Return([]string{testAddress}, nil)

	m, err := NewBalanceMonitor(BalanceMonitorOpts{
		ChainInfo: balance.ChainInfo{
			ChainFamilyName: "stellar",
			ChainID:         "stellar-testnet",
			NetworkName:     "testnet",
			NetworkNameFull: "stellar-testnet",
		},
		Config:    balance.GenericBalanceConfig{BalancePollPeriod: *clconfig.MustNewDuration(10 * time.Millisecond)},
		Logger:    lggr,
		Keystore:  ks,
		GetClient: func(context.Context) (BalanceMonitorRPCClient, error) { return rpc, nil },
		Timeout:   time.Second,
	})
	require.NoError(t, err)

	require.NoError(t, m.Start(t.Context()))
	defer func() { require.NoError(t, m.Close()) }()

	require.Eventually(t, func() bool {
		return len(logs.FilterMessageSnippet("Failed to get balance").All()) > 0
	}, 5*time.Second, 10*time.Millisecond, "expected failed cycles to be logged")

	for name, err := range m.HealthReport() {
		require.NoError(t, err, "service %s must stay healthy through RPC errors", name)
	}
}
