package txm

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// confirmer polls broadcast transactions for their on-chain status.
// It runs as a goroutine ticking at Config.ConfirmPollInterval.
type confirmer struct {
	rpc   ccvclient.RPCClient
	store *TxStore
	cfg   Config
	lggr  zerolog.Logger
}

func newConfirmer(
	rpc ccvclient.RPCClient,
	store *TxStore,
	cfg Config,
	lggr zerolog.Logger,
) *confirmer {
	return &confirmer{
		rpc:   rpc,
		store: store,
		cfg:   cfg,
		lggr:  lggr.With().Str("component", "txm.confirmer").Logger(),
	}
}

// run is the main confirm loop. It ticks at the configured interval,
// checking all broadcast txs.
func (c *confirmer) run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.ConfirmPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkAll(ctx)
		}
	}
}

func (c *confirmer) checkAll(ctx context.Context) {
	entries := c.store.BroadcastEntries()
	if len(entries) == 0 {
		return
	}

	latestLedger, err := c.rpc.GetLatestLedger(ctx)
	if err != nil {
		c.lggr.Warn().Err(err).Msg("Failed to get latest ledger for confirmation check")
		return
	}

	for _, entry := range entries {
		c.checkOne(ctx, entry, latestLedger.Sequence)
	}
}

func (c *confirmer) checkOne(ctx context.Context, entry *txEntry, currentLedger uint32) {
	resp, err := c.rpc.GetTransaction(ctx, protocolrpc.GetTransactionRequest{
		Hash: entry.Hash,
	})
	if err != nil {
		c.lggr.Debug().Err(err).Str("txID", entry.Request.ID).Msg("GetTransaction error, will retry")
		return
	}

	switch resp.Status {
	case protocolrpc.TransactionStatusSuccess:
		var meta xdr.TransactionMeta
		if resp.ResultMetaXDR != "" {
			if err := xdr.SafeUnmarshalBase64(resp.ResultMetaXDR, &meta); err != nil {
				c.lggr.Error().Err(err).Str("txID", entry.Request.ID).Msg("Failed to decode result meta")
				c.store.SetFailed(entry.Request.ID, fmt.Errorf("decode result meta: %w", err))
				return
			}
		}
		c.store.SetConfirmed(entry.Request.ID, &meta, resp.Ledger)
		c.lggr.Info().
			Str("txID", entry.Request.ID).
			Str("hash", entry.Hash).
			Uint32("ledger", resp.Ledger).
			Msg("Transaction confirmed")

	case protocolrpc.TransactionStatusFailed:
		c.store.SetFailed(entry.Request.ID, ErrTxFailed)
		c.lggr.Warn().
			Str("txID", entry.Request.ID).
			Str("hash", entry.Hash).
			Msg("Transaction failed on-chain")

	case protocolrpc.TransactionStatusNotFound:
		if entry.MaxLedger > 0 && currentLedger > entry.MaxLedger {
			c.store.SetExpired(entry.Request.ID)
			c.lggr.Warn().
				Str("txID", entry.Request.ID).
				Str("hash", entry.Hash).
				Uint32("maxLedger", entry.MaxLedger).
				Uint32("currentLedger", currentLedger).
				Msg("Transaction expired (ledger bounds exceeded)")
			return
		}
		if time.Since(entry.Created) > c.cfg.TxTimeout {
			c.store.SetExpired(entry.Request.ID)
			c.lggr.Warn().
				Str("txID", entry.Request.ID).
				Str("hash", entry.Hash).
				Dur("elapsed", time.Since(entry.Created)).
				Msg("Transaction expired (wall-clock timeout)")
		}
	}
}

// waitForTxConfirmation is a synchronous helper that polls GetTransaction
// until the tx confirms, fails, or times out. Used by the broadcaster for
// RestoreFootprint and by the synchronous EnqueueAndWait path.
func waitForTxConfirmation(ctx context.Context, rpc ccvclient.RPCClient, hash string, timeout time.Duration) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	deadline := time.After(timeout)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("confirmation timed out for %s", hash)
		case <-ticker.C:
			resp, err := rpc.GetTransaction(ctx, protocolrpc.GetTransactionRequest{Hash: hash})
			if err != nil {
				continue
			}
			switch resp.Status {
			case protocolrpc.TransactionStatusSuccess:
				return nil
			case protocolrpc.TransactionStatusFailed:
				return ErrTxFailed
			case protocolrpc.TransactionStatusNotFound:
				continue
			}
		}
	}
}
