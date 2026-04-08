package txm

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/rs/zerolog"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// confirmer polls broadcast transactions for their on-chain status.
// It runs as a goroutine ticking at Config.ConfirmPollInterval with jitter.
type confirmer struct {
	client   *ccvclient.Client
	rpc      ccvclient.RPCClient
	store    *TxStore
	seqStore *SequenceStore
	metrics  *Metrics
	cfg      Config
	lggr     zerolog.Logger
	retryCh  chan<- *txEntry // channel to re-enqueue for Layer 3 retries
}

func newConfirmer(
	client *ccvclient.Client,
	store *TxStore,
	seqStore *SequenceStore,
	metrics *Metrics,
	cfg Config,
	lggr zerolog.Logger,
	retryCh chan<- *txEntry,
) *confirmer {
	return &confirmer{
		client:   client,
		rpc:      client.RPC,
		store:    store,
		seqStore: seqStore,
		metrics:  metrics,
		cfg:      cfg,
		lggr:     lggr.With().Str("component", "txm.confirmer").Logger(),
		retryCh:  retryCh,
	}
}

// run is the main confirm loop. It ticks at the configured interval with
// jitter to avoid thundering-herd polling across nodes.
func (c *confirmer) run(ctx context.Context) {
	for {
		interval := withJitter(c.cfg.ConfirmPollInterval)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			c.checkAll(ctx)
		}
	}
}

func (c *confirmer) checkAll(ctx context.Context) {
	entries := c.store.BroadcastEntries()
	if len(entries) == 0 {
		return
	}

	latestLedger, err := c.client.LatestLedger(ctx)
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
				c.seqStore.Confirm(entry.Seq, true)
				return
			}
		}
		c.store.SetConfirmed(entry.Request.ID, &meta, resp.Ledger, resp.ResultMetaXDR, 0)
		c.seqStore.Confirm(entry.Seq, true)
		c.metrics.IncrFinalized()
		c.lggr.Info().
			Str("txID", entry.Request.ID).
			Str("hash", entry.Hash).
			Uint32("ledger", resp.Ledger).
			Msg("Transaction confirmed")

	case protocolrpc.TransactionStatusFailed:
		c.seqStore.Confirm(entry.Seq, true) // consumed on-chain
		c.metrics.IncrRevert()
		c.lggr.Warn().
			Str("txID", entry.Request.ID).
			Str("hash", entry.Hash).
			Msg("Transaction failed on-chain")
		c.maybeRetry(entry, ErrTxFailed)

	case protocolrpc.TransactionStatusNotFound:
		if entry.MaxLedger > 0 && currentLedger > entry.MaxLedger {
			c.seqStore.Confirm(entry.Seq, false) // not consumed, can reuse
			c.metrics.IncrDrop()
			c.lggr.Warn().
				Str("txID", entry.Request.ID).
				Str("hash", entry.Hash).
				Uint32("maxLedger", entry.MaxLedger).
				Uint32("currentLedger", currentLedger).
				Msg("Transaction expired (ledger bounds exceeded)")
			c.maybeRetry(entry, ErrTxExpired)
			return
		}
		if time.Since(entry.Created) > c.cfg.TxTimeout {
			c.seqStore.Confirm(entry.Seq, false) // not consumed, can reuse
			c.metrics.IncrDrop()
			c.lggr.Warn().
				Str("txID", entry.Request.ID).
				Str("hash", entry.Hash).
				Dur("elapsed", time.Since(entry.Created)).
				Msg("Transaction expired (wall-clock timeout)")
			c.maybeRetry(entry, ErrTxExpired)
		}
	}
}

// maybeRetry attempts to re-enqueue a failed/expired transaction for a full
// retry cycle (Layer 3). Increments the attempt counter and applies fee
// bumping on the next broadcast.
func (c *confirmer) maybeRetry(entry *txEntry, reason error) {
	newAttempt := c.store.IncrementRetry(entry.Request.ID)

	if newAttempt >= c.cfg.MaxRetries {
		c.store.SetFailed(entry.Request.ID, reason)
		c.metrics.IncrReject()
		c.lggr.Warn().
			Str("txID", entry.Request.ID).
			Int("attempts", newAttempt).
			Msg("Exhausted lifecycle retries")
		return
	}

	select {
	case c.retryCh <- entry:
		c.metrics.IncrRetry()
		c.lggr.Info().
			Str("txID", entry.Request.ID).
			Int("attempt", newAttempt).
			Msg("Re-enqueued for lifecycle retry")
	default:
		c.store.SetFailed(entry.Request.ID, fmt.Errorf("retry queue full: %w", reason))
		c.metrics.IncrReject()
	}
}

// withJitter applies ±20% jitter to a duration to prevent thundering herd.
func withJitter(d time.Duration) time.Duration {
	jitter := float64(d) * 0.2 * (rand.Float64()*2 - 1)
	return d + time.Duration(jitter)
}
