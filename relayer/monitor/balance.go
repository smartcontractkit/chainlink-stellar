package monitor

import (
	"context"
	"fmt"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/monitoring/balance"
	"github.com/smartcontractkit/chainlink-common/pkg/services"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"
)

// BalanceMonitorRPCClient is the subset of the Stellar Soroban JSON-RPC client used by the balance monitor.
type BalanceMonitorRPCClient interface {
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
}

const (
	stroopsPerXLM = 10_000_000
)

// BalanceMonitorOpts contains the options for creating a new Stellar account balance monitor.
type BalanceMonitorOpts struct {
	ChainInfo balance.ChainInfo

	Config   balance.GenericBalanceConfig
	Logger   logger.Logger
	Keystore core.Keystore

	// GetClient returns a healthy RPC client from the multinode pool. It is
	// called on every balance fetch so no node is pinned across poll cycles.
	GetClient func(ctx context.Context) (BalanceMonitorRPCClient, error)
	// Timeout bounds each balance fetch; GenericBalanceClient carries no context.
	Timeout time.Duration
}

// NewBalanceMonitor returns a balance monitoring services.Service which reports
// the XLM balance of all keystore accounts as the Beholder gauge `account_balance`.
func NewBalanceMonitor(opts BalanceMonitorOpts) (services.Service, error) {
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("balance monitor timeout must be positive, got %s", opts.Timeout)
	}
	return balance.NewGenericBalanceMonitor(balance.GenericBalanceMonitorOpts{
		ChainInfo:           opts.ChainInfo,
		ChainNativeCurrency: "XLM",

		Config:   opts.Config,
		Logger:   opts.Logger,
		Keystore: opts.Keystore,
		NewGenericBalanceClient: func() (balance.GenericBalanceClient, error) {
			return &balanceClient{getClient: opts.GetClient, timeout: opts.Timeout}, nil
		},
		KeyToAccountMapper: func(_ context.Context, pk string) (string, error) { return pk, nil },
	})
}

type balanceClient struct {
	getClient func(ctx context.Context) (BalanceMonitorRPCClient, error)
	timeout   time.Duration
}

var _ balance.GenericBalanceClient = (*balanceClient)(nil)

// GetAccountBalance returns the raw account balance in XLM.
func (c *balanceClient) GetAccountBalance(addr string) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	accountID, err := xdr.AddressToAccountId(addr)
	if err != nil {
		return 0, fmt.Errorf("invalid stellar account address %q: %w", addr, err)
	}
	accountKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: accountID,
		},
	}
	keyXDR, err := accountKey.MarshalBinaryBase64()
	if err != nil {
		return 0, fmt.Errorf("failed to marshal account key: %w", err)
	}

	client, err := c.getClient(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get RPC client: %w", err)
	}

	resp, err := client.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get ledger entries: %w", err)
	}

	if len(resp.Entries) == 0 {
		return 0, nil
	}

	entryXDR := resp.Entries[0].DataXDR
	if entryXDR == "" {
		return 0, fmt.Errorf("empty entry data for account %s", addr)
	}

	var entry xdr.LedgerEntryData
	if err = xdr.SafeUnmarshalBase64(entryXDR, &entry); err != nil {
		return 0, fmt.Errorf("failed to unmarshal account entry: %w", err)
	}

	account, ok := entry.GetAccount()
	if !ok {
		return 0, fmt.Errorf("ledger entry for %s is not an account entry (type=%v)", addr, entry.Type)
	}

	return float64(account.Balance) / stroopsPerXLM, nil
}
