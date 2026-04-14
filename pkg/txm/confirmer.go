package txm

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
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
	lggr     logger.Logger
	retryCh  chan<- *txEntry // channel to re-enqueue for Layer 3 retries
}

func newConfirmer(
	client *ccvclient.Client,
	store *TxStore,
	seqStore *SequenceStore,
	metrics *Metrics,
	cfg Config,
	lggr logger.Logger,
	retryCh chan<- *txEntry,
) *confirmer {
	return &confirmer{
		client:   client,
		rpc:      client.RPC,
		store:    store,
		seqStore: seqStore,
		metrics:  metrics,
		cfg:      cfg,
		lggr:     logger.Named(lggr, "Confirmer"),
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
	snapshots := c.store.BroadcastEntries()
	if len(snapshots) == 0 {
		return
	}

	latestLedger, err := c.client.LatestLedger(ctx)
	if err != nil {
		c.lggr.Warnw("Failed to get latest ledger for confirmation check", "err", err)
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
		c.lggr.Debugw("GetTransaction error, will retry", "err", err, "txID", snap.ID)
		return
	}

	switch resp.Status {
	case protocolrpc.TransactionStatusSuccess:
		var meta xdr.TransactionMeta
		if resp.ResultMetaXDR != "" {
			if err := xdr.SafeUnmarshalBase64(resp.ResultMetaXDR, &meta); err != nil {
				c.lggr.Errorw("Failed to decode result meta", "err", err, "txID", snap.ID)
				c.store.SetFailed(snap.ID, fmt.Errorf("decode result meta: %w", err))
				c.seqStore.Confirm(snap.Seq, true)
				return
			}
		}
		var feeCharged int64
		if resp.ResultXDR != "" {
			var txResult xdr.TransactionResult
			if err := xdr.SafeUnmarshalBase64(resp.ResultXDR, &txResult); err == nil {
				feeCharged = int64(txResult.FeeCharged)
			}
		}
		c.store.SetConfirmed(snap.ID, &meta, resp.Ledger, resp.ResultMetaXDR, feeCharged)
		c.seqStore.Confirm(snap.Seq, true)
		c.metrics.IncrFinalized()
		c.lggr.Infow("Transaction confirmed", "txID", snap.ID, "hash", snap.Hash, "ledger", resp.Ledger)

	case protocolrpc.TransactionStatusFailed:
		c.seqStore.Confirm(snap.Seq, true) // consumed on-chain
		c.metrics.IncrRevert()
		c.lggr.Warnw("Transaction failed on-chain", "txID", snap.ID, "hash", snap.Hash)
		c.maybeRetry(snap, ErrTxFailed)

	case protocolrpc.TransactionStatusNotFound:
		if snap.MaxLedger > 0 && currentLedger > snap.MaxLedger {
			c.seqStore.Confirm(snap.Seq, false) // not consumed, can reuse
			c.metrics.IncrDrop()
			c.lggr.Warnw("Transaction expired (ledger bounds exceeded)",
				"txID", snap.ID, "hash", snap.Hash,
				"maxLedger", snap.MaxLedger, "currentLedger", currentLedger)
			c.maybeRetry(snap, ErrTxExpired)
			return
		}
		if time.Since(snap.Created) > c.cfg.TxTimeout {
			c.seqStore.Confirm(snap.Seq, false) // not consumed, can reuse
			c.metrics.IncrDrop()
			c.lggr.Warnw("Transaction expired (wall-clock timeout)",
				"txID", snap.ID, "hash", snap.Hash,
				"elapsed", time.Since(snap.Created))
			c.maybeRetry(snap, ErrTxExpired)
		}
	}
}

// maybeRetry attempts to re-enqueue a failed/expired transaction for a full
// retry cycle (Layer 3). Increments the attempt counter and applies fee
// bumping on the next broadcast.
func (c *confirmer) maybeRetry(snap BroadcastSnapshot, reason error) {
	newAttempt := c.store.IncrementRetry(snap.ID)

	if newAttempt >= c.cfg.MaxRetries {
		c.store.SetFailed(snap.ID, reason)
		c.metrics.IncrReject()
		c.lggr.Warnw("Exhausted lifecycle retries", "txID", snap.ID, "attempts", newAttempt)
		return
	}

	entry := c.store.Get(snap.ID)
	if entry == nil {
		return
	}
	select {
	case c.retryCh <- entry:
		c.metrics.IncrRetry()
		c.lggr.Infow("Re-enqueued for lifecycle retry", "txID", snap.ID, "attempt", newAttempt)
	default:
		c.store.SetFailed(snap.ID, fmt.Errorf("retry queue full: %w", reason))
		c.metrics.IncrReject()
	}
}

// withJitter applies ±20% jitter to a duration to prevent thundering herd.
func withJitter(d time.Duration) time.Duration {
	jitter := float64(d) * 0.2 * (rand.Float64()*2 - 1)
	return d + time.Duration(jitter)
}
