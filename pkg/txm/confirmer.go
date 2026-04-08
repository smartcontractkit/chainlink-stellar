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
	snapshots := c.store.BroadcastSnapshots()
	if len(snapshots) == 0 {
		return
	}

	latestLedger, err := c.client.LatestLedger(ctx)
	if err != nil {
		c.lggr.Warn().Err(err).Msg("Failed to get latest ledger for confirmation check")
		return
	}

	for _, snap := range snapshots {
		c.checkOne(ctx, snap, latestLedger.Sequence)
	}
}

func (c *confirmer) checkOne(ctx context.Context, snap BroadcastSnapshot, currentLedger uint32) {
	resp, err := c.rpc.GetTransaction(ctx, protocolrpc.GetTransactionRequest{
		Hash: snap.Hash,
	})
	if err != nil {
		c.lggr.Debug().Err(err).Str("txID", snap.ID).Msg("GetTransaction error, will retry")
		return
	}

	switch resp.Status {
	case protocolrpc.TransactionStatusSuccess:
		var meta xdr.TransactionMeta
		if resp.ResultMetaXDR != "" {
			if err := xdr.SafeUnmarshalBase64(resp.ResultMetaXDR, &meta); err != nil {
				c.lggr.Error().Err(err).Str("txID", snap.ID).Msg("Failed to decode result meta")
				c.store.SetFailed(snap.ID, fmt.Errorf("decode result meta: %w", err))
				c.seqStore.Confirm(snap.Seq, true)
				return
			}
		}
		c.store.SetConfirmed(snap.ID, &meta, resp.Ledger, resp.ResultMetaXDR, 0)
		c.seqStore.Confirm(snap.Seq, true)
		c.metrics.IncrFinalized()
		c.lggr.Info().
			Str("txID", snap.ID).
			Str("hash", snap.Hash).
			Uint32("ledger", resp.Ledger).
			Msg("Transaction confirmed")

	case protocolrpc.TransactionStatusFailed:
		c.seqStore.Confirm(snap.Seq, true) // consumed on-chain
		c.metrics.IncrRevert()
		c.lggr.Warn().
			Str("txID", snap.ID).
			Str("hash", snap.Hash).
			Msg("Transaction failed on-chain")
		c.maybeRetry(snap.ID, ErrTxFailed)

	case protocolrpc.TransactionStatusNotFound:
		if snap.MaxLedger > 0 && currentLedger > snap.MaxLedger {
			c.seqStore.Confirm(snap.Seq, false) // not consumed, can reuse
			c.metrics.IncrDrop()
			c.lggr.Warn().
				Str("txID", snap.ID).
				Str("hash", snap.Hash).
				Uint32("maxLedger", snap.MaxLedger).
				Uint32("currentLedger", currentLedger).
				Msg("Transaction expired (ledger bounds exceeded)")
			c.maybeRetry(snap.ID, ErrTxExpired)
			return
		}
		if time.Since(snap.Created) > c.cfg.TxTimeout {
			c.seqStore.Confirm(snap.Seq, false) // not consumed, can reuse
			c.metrics.IncrDrop()
			c.lggr.Warn().
				Str("txID", snap.ID).
				Str("hash", snap.Hash).
				Dur("elapsed", time.Since(snap.Created)).
				Msg("Transaction expired (wall-clock timeout)")
			c.maybeRetry(snap.ID, ErrTxExpired)
		}
	}
}

// maybeRetry attempts to re-enqueue a failed/expired transaction for a full
// retry cycle (Layer 3). Increments the attempt counter and applies fee
// bumping on the next broadcast.
func (c *confirmer) maybeRetry(txID string, reason error) {
	newAttempt := c.store.IncrementRetry(txID)

	if newAttempt >= c.cfg.MaxRetries {
		c.store.SetFailed(txID, reason)
		c.metrics.IncrReject()
		c.lggr.Warn().
			Str("txID", txID).
			Int("attempts", newAttempt).
			Msg("Exhausted lifecycle retries")
		return
	}

	entry := c.store.GetEntryForRetry(txID)
	if entry == nil {
		return
	}

	select {
	case c.retryCh <- entry:
		c.metrics.IncrRetry()
		c.lggr.Info().
			Str("txID", txID).
			Int("attempt", newAttempt).
			Msg("Re-enqueued for lifecycle retry")
	default:
		c.store.SetFailed(txID, fmt.Errorf("retry queue full: %w", reason))
		c.metrics.IncrReject()
	}
}

// withJitter applies ±20% jitter to a duration to prevent thundering herd.
func withJitter(d time.Duration) time.Duration {
	jitter := float64(d) * 0.2 * (rand.Float64()*2 - 1)
	return d + time.Duration(jitter)
}
