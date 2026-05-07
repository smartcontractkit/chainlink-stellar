package txm

import (
	"context"
	"fmt"
	"time"

	"github.com/smartcontractkit/chainlink-stellar/relayer/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func (s *StellarTxm) handleRestore(
	ctx context.Context,
	client *client.Client,
	tx *StellarTx,
	preamble protocolrpc.RestorePreamble,
	seq int64,
) error {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(preamble.TransactionDataXDR, &sorobanData); err != nil {
		return fmt.Errorf("failed to decode restore preamble soroban data: %w", err)
	}

	restoreOp := &txnbuild.RestoreFootprint{
		SourceAccount: tx.FromAddress,
		Ext: xdr.TransactionExt{
			V:           1,
			SorobanData: &sorobanData,
		},
	}

	sourceAccount := txnbuild.NewSimpleAccount(tx.FromAddress, seq-1)
	restoreFee := s.feeStrat.CalculateRestoreFee(preamble.MinResourceFee, *s.config.RestoreFeeBuffer)
	restoreTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sourceAccount,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{restoreOp},
		BaseFee:              restoreFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: txnbuild.NewTimeout(*s.config.TxTimeoutSecs),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build restore transaction: %w", err)
	}

	signedTx, err := s.signTransaction(ctx, restoreTx, tx.FromAddress)
	if err != nil {
		return fmt.Errorf("failed to sign restore transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		return fmt.Errorf("failed to encode signed restore transaction: %w", err)
	}

	// Count one RestoreTotal per logical restore
	s.metrics.IncrementRestoreTotal(ctx)

	var lastErr error
	for attempt := uint(0); attempt < *s.config.MaxRestoreAttempts; attempt++ {
		submitResult, err := client.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
			Transaction: signedXDR,
		})
		if err != nil {
			lastErr = fmt.Errorf("failed to submit restore transaction: %w", err)
			ctxLogger.Warnw("restore submit failed, retrying", "attempt", attempt, "error", err)
			if !s.sleepBeforeRestoreRetry(ctx) {
				return ctx.Err()
			}
			continue
		}

		switch submitResult.Status {
		case stellarcore.TXStatusPending, stellarcore.TXStatusDuplicate:
			ctxLogger.Debugw("restore transaction accepted", "attempt", attempt, "seq", seq, "hash", submitResult.Hash)

			timeout := time.Duration(*s.config.TxTimeoutSecs) * time.Second
			resp, err := client.PollTransaction(ctx, submitResult.Hash, timeout)
			if err != nil {
				lastErr = fmt.Errorf("restore transaction polling failed: %w", err)
				ctxLogger.Warnw("restore poll failed, retrying", "attempt", attempt, "hash", submitResult.Hash, "error", err)
				if !s.sleepBeforeRestoreRetry(ctx) {
					return ctx.Err()
				}
				continue
			}
			if resp.Status == protocolrpc.TransactionStatusSuccess {
				s.metrics.IncrementRestoreSuccess(ctx)
				ctxLogger.Infow("restore transaction confirmed", "seq", seq, "hash", submitResult.Hash)
				if err := s.resyncSequence(ctx, client, tx); err != nil {
					return fmt.Errorf("failed to resync sequence after restore: %w", err)
				}
				return nil
			}
			s.metrics.IncrementRestoreFailed(ctx)
			return fmt.Errorf("restore transaction failed on-chain: %s", resp.Status)

		case stellarcore.TXStatusTryAgainLater:
			lastErr = fmt.Errorf("restore transaction rejected with %s", stellarcore.TXStatusTryAgainLater)
			ctxLogger.Warnw("restore transaction asked to try again later", "attempt", attempt)
			if !s.sleepBeforeRestoreRetry(ctx) {
				return ctx.Err()
			}
			continue

		case stellarcore.TXStatusError:
			s.metrics.IncrementRestoreFailed(ctx)
			if submitResult.ErrorResultXDR != "" {
				return fmt.Errorf("restore transaction rejected: %s", submitResult.ErrorResultXDR)
			}
			return fmt.Errorf("restore transaction rejected with %s", stellarcore.TXStatusError)

		default:
			s.metrics.IncrementRestoreFailed(ctx)
			return fmt.Errorf("unexpected restore transaction status: %s", submitResult.Status)
		}
	}

	s.metrics.IncrementRestoreFailed(ctx)
	if lastErr != nil {
		return fmt.Errorf("restore attempts exhausted: %w", lastErr)
	}
	return fmt.Errorf("restore attempts exhausted")
}

func (s *StellarTxm) sleepBeforeRestoreRetry(ctx context.Context) bool {
	select {
	case <-time.After(s.config.SubmitRetryDelay.Duration()):
		return true
	case <-ctx.Done():
		return false
	}
}
