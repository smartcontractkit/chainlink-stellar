package txm

import (
	"context"
	"fmt"
	"strings"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// handleRestore submits a RestoreFootprint transaction for the archived entries in preamble
// and waits for it to be included. inclusionFee is the bid seeded for this broadcast; the
// resource fee goes into SorobanData, not BaseFee (see assembleTransaction).
func (s *StellarTxm) handleRestore(
	ctx context.Context,
	client RPCClient,
	tx *StellarTx,
	preamble protocolrpc.RestorePreamble,
	seq int64,
	inclusionFee int64,
) error {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(preamble.TransactionDataXDR, &sorobanData); err != nil {
		return fmt.Errorf("failed to decode restore preamble soroban data: %w", err)
	}

	resourceFee, err := s.feeStrat.ResourceFee(preamble.MinResourceFee, *s.config.RestoreFeeBuffer, tx.MaxResourceFee)
	if err != nil {
		return fmt.Errorf("restore preamble fee rejected: %w", err)
	}
	sorobanData.ResourceFee = xdr.Int64(resourceFee)
	s.metrics.ObserveInclusionFee(ctx, inclusionFee)
	s.metrics.ObserveResourceFee(ctx, resourceFee)

	restoreOp := &txnbuild.RestoreFootprint{
		SourceAccount: tx.FromAddress,
		Ext: xdr.TransactionExt{
			V:           1,
			SorobanData: &sorobanData,
		},
	}

	// seq is the next sequence this restore will consume (TxStore / GetNextSequence).
	// txnbuild.SimpleAccount.Sequence is the last-used on-ledger seq, so pass seq-1
	// with IncrementSequenceNum:true to emit exactly seq on the wire (same as
	// buildPreliminaryTx).
	currentSequence := max(int64(0), seq-1)
	sourceAccount := txnbuild.NewSimpleAccount(tx.FromAddress, currentSequence)
	restoreTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sourceAccount,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{restoreOp},
		BaseFee:              inclusionFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: txnbuild.NewTimeout(*s.config.TxTimeoutSecs),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build restore transaction: %w", err)
	}

	signedTx, localHash, err := s.signTransaction(ctx, restoreTx, tx.FromAddress)
	if err != nil {
		return fmt.Errorf("failed to sign restore transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		return fmt.Errorf("failed to encode signed restore transaction: %w", err)
	}

	s.metrics.IncrementRestore(ctx, RestoreOutcomeInitiated)

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
			if !strings.EqualFold(submitResult.Hash, localHash) {
				ctxLogger.Errorw("rpc reported a restore hash that does not match the signed envelope; polling local hash",
					"rpcHash", submitResult.Hash, "localHash", localHash)
			}
			ctxLogger.Debugw("restore transaction accepted", "attempt", attempt, "seq", seq, "hash", localHash)

			resp, err := s.pollRestoreTransaction(ctx, client, localHash)
			if err != nil {
				lastErr = fmt.Errorf("restore transaction polling failed: %w", err)
				ctxLogger.Warnw("restore poll failed, retrying", "attempt", attempt, "hash", localHash, "error", err)
				if !s.sleepBeforeRestoreRetry(ctx) {
					return ctx.Err()
				}
				continue
			}
			if resp.Status == protocolrpc.TransactionStatusSuccess {
				s.metrics.IncrementRestore(ctx, RestoreOutcomeSuccess)
				ctxLogger.Infow("restore transaction confirmed", "seq", seq, "hash", localHash)
				if err := s.resyncSequence(ctx, client, tx); err != nil {
					return fmt.Errorf("failed to resync sequence after restore: %w", err)
				}
				return nil
			}
			s.metrics.IncrementRestore(ctx, RestoreOutcomeFailed)
			return fmt.Errorf("restore transaction failed on-chain: %s", resp.Status)

		case stellarcore.TXStatusTryAgainLater:
			lastErr = fmt.Errorf("restore transaction rejected with %s", stellarcore.TXStatusTryAgainLater)
			ctxLogger.Warnw("restore transaction asked to try again later", "attempt", attempt)
			if !s.sleepBeforeRestoreRetry(ctx) {
				return ctx.Err()
			}
			continue

		case stellarcore.TXStatusError:
			s.metrics.IncrementRestore(ctx, RestoreOutcomeFailed)
			if submitResult.ErrorResultXDR != "" {
				return fmt.Errorf("restore transaction rejected: %s", submitResult.ErrorResultXDR)
			}
			return fmt.Errorf("restore transaction rejected with %s", stellarcore.TXStatusError)

		default:
			s.metrics.IncrementRestore(ctx, RestoreOutcomeFailed)
			return fmt.Errorf("unexpected restore transaction status: %s", submitResult.Status)
		}
	}

	s.metrics.IncrementRestore(ctx, RestoreOutcomeFailed)
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

// pollRestoreTransaction polls GetTransaction until SUCCESS, FAILED, or
// TxTimeoutSecs elapses. Inlined here because the bare RPCClient interface —
// unlike the SDK's *rpcclient.Client — doesn't expose a PollTransaction helper.
func (s *StellarTxm) pollRestoreTransaction(ctx context.Context, client RPCClient, hash string) (protocolrpc.GetTransactionResponse, error) {
	timeout := time.Duration(*s.config.TxTimeoutSecs) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(s.config.ConfirmPollInterval.Duration())
	defer ticker.Stop()

	var zero protocolrpc.GetTransactionResponse
	for {
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("poll timed out for tx %s: %w", hash, ctx.Err())
		case <-ticker.C:
			resp, err := client.GetTransaction(ctx, protocolrpc.GetTransactionRequest{Hash: hash})
			if err != nil {
				continue
			}
			switch resp.Status {
			case protocolrpc.TransactionStatusSuccess, protocolrpc.TransactionStatusFailed:
				return resp, nil
			}
		}
	}
}
