package txm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func (s *StellarTxm) buildPreliminaryTx(tx *StellarTx, nextSubmitSeq int64, maxLedger uint32) (*txnbuild.Transaction, error) {
	if tx == nil {
		return nil, errors.New("buildPreliminaryTx: tx is nil")
	}
	// TxTimeoutSecs is set by txm.New via cfg.Resolve(); no per-call nil check.
	// nextSubmitSeq is the next sequence number this tx will consume (TxStore convention).
	// currentSequence is the last-used sequence on ledger (nextSubmitSeq-1), which txnbuild.NewSimpleAccount expects.
	// IncrementSequenceNum:true then produces exactly nextSubmitSeq on the wire.
	currentSequence := max(int64(0), nextSubmitSeq-1)
	sourceAccount := txnbuild.NewSimpleAccount(tx.FromAddress, currentSequence)

	return txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sourceAccount,
		IncrementSequenceNum: true,
		Operations:           tx.Operations,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: txnbuild.NewTimeout(*s.config.TxTimeoutSecs),
			LedgerBounds: &txnbuild.LedgerBounds{
				MaxLedger: maxLedger,
			},
		},
	})
}

func (s *StellarTxm) simulateTransaction(ctx context.Context, client RPCClient, tx *txnbuild.Transaction) (protocolrpc.SimulateTransactionResponse, error) {
	if client == nil {
		return protocolrpc.SimulateTransactionResponse{}, errors.New("client is nil")
	}
	if tx == nil {
		return protocolrpc.SimulateTransactionResponse{}, errors.New("transaction is nil")
	}

	txXDR, err := tx.Base64()
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("failed to base64 encode preliminary tx: %w", err)
	}

	start := time.Now()
	simResult, err := client.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
		Transaction: txXDR,
	})
	s.metrics.ObserveSimulationDuration(ctx, time.Since(start).Milliseconds())
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("rpc simulate transaction failed: %w", err)
	}

	if simResult.Error != "" {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("simulation error: %w", errors.New(simResult.Error))
	}

	return simResult, nil
}

// isRetryableSimulationError decides whether to retry Soroban simulation after a
// failed SimulateTransaction call. It is intentionally heuristic: we only see
// unstructured error strings from the JSON-RPC client and Soroban (no stable
// machine-readable code across all failure modes).
//
// Hint lists come from StellarTxm config (SimulationTerminalHints and
// SimulationRetryableHints); see Config.Resolve for defaults.
//
// Anything that matches neither list is treated as non-retryable (fail closed).
func (s *StellarTxm) isRetryableSimulationError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	for _, hint := range s.config.SimulationTerminalHints {
		if strings.Contains(msg, hint) {
			return false
		}
	}

	for _, hint := range s.config.SimulationRetryableHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	// Fail closed: unknown errors are not retried so we do not spin on new fatal
	// RPC or Soroban messages that omit our terminal hints.
	return false
}

// assembleTransaction rebuilds tx with simulation results and a caller-supplied inclusionFee.
// inclusionFee is computed by the caller via FeeStrategy (SeedInclusionFee / BumpInclusionFee
// with feeTracker percentiles); it must NOT include
// the resource fee — txnbuild computes the envelope fee as BaseFee*numOps + sorobanData.ResourceFee,
// so folding resource fee into BaseFee would double-count it.
func (s *StellarTxm) assembleTransaction(tx *txnbuild.Transaction, sim protocolrpc.SimulateTransactionResponse, inclusionFee int64, maxLedger uint32, perRequestMaxResourceFee uint64) (*txnbuild.Transaction, int64, error) {
	if tx == nil {
		return nil, 0, errors.New("assembleTransaction: tx is nil")
	}
	ops := tx.Operations()
	if len(ops) == 0 {
		return nil, 0, errors.New("transaction has no operations")
	}

	resourceFee := int64(0)

	if sim.TransactionDataXDR != "" {
		var sorobanData xdr.SorobanTransactionData
		if err := xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &sorobanData); err != nil {
			return nil, 0, fmt.Errorf("failed to decode soroban data: %w", err)
		}

		// Apply the resource fee buffer here, inside the SorobanData, so
		// txnbuild picks it up correctly when computing the envelope fee.
		resourceFee = sim.MinResourceFee + s.feeStrat.ResourceFeeBuffer
		// Enforce the per-transaction resource-fee cap before baking the fee into the
		// SorobanData and signing.
		effectiveCap := s.feeStrat.MaxResourceFee
		if perRequestMaxResourceFee > 0 {
			if effectiveCap == 0 || int64(perRequestMaxResourceFee) < effectiveCap {
				effectiveCap = int64(perRequestMaxResourceFee)
			}
		}
		if effectiveCap > 0 && resourceFee > effectiveCap {
			return nil, 0, fmt.Errorf("resource fee %d stroops exceeds cap %d (sim.MinResourceFee=%d, buffer=%d)",
				resourceFee, effectiveCap, sim.MinResourceFee, s.feeStrat.ResourceFeeBuffer)
		}
		sorobanData.ResourceFee = xdr.Int64(resourceFee)

		if ihf, ok := ops[0].(*txnbuild.InvokeHostFunction); ok {
			ihf.Ext = xdr.TransactionExt{
				V:           1,
				SorobanData: &sorobanData,
			}

			if len(sim.Results) > 0 && sim.Results[0].AuthXDR != nil && len(*sim.Results[0].AuthXDR) > 0 {
				auth := make([]xdr.SorobanAuthorizationEntry, len(*sim.Results[0].AuthXDR))
				for i, authXDR := range *sim.Results[0].AuthXDR {
					if err := xdr.SafeUnmarshalBase64(authXDR, &auth[i]); err != nil {
						return nil, 0, fmt.Errorf("failed to decode auth: %w", err)
					}
				}
				ihf.Auth = auth
			}
		}
	}

	// Rebuild transaction: txnbuild sets envelope fee = inclusionFee + sorobanData.ResourceFee.
	sourceAccount := txnbuild.NewSimpleAccount(tx.SourceAccount().AccountID, tx.SourceAccount().Sequence)

	assembledTx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &sourceAccount,
		IncrementSequenceNum: false, // already incremented in preliminary tx
		Operations:           ops,
		BaseFee:              inclusionFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: tx.Timebounds(),
			LedgerBounds: &txnbuild.LedgerBounds{
				MaxLedger: maxLedger,
			},
		},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to rebuild transaction with fee: %w", err)
	}

	return assembledTx, inclusionFee + resourceFee, nil
}

func (s *StellarTxm) signTransaction(ctx context.Context, tx *txnbuild.Transaction, fromAddress string) (*txnbuild.Transaction, error) {
	if tx == nil {
		return nil, errors.New("signTransaction: tx is nil")
	}
	hash, err := tx.Hash(s.networkPassphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to hash transaction: %w", err)
	}

	signature, err := s.keystore.Sign(ctx, fromAddress, hash[:])
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	var hint [4]byte
	addr, err := xdr.AddressToAccountId(fromAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to parse fromAddress for hint: %w", err)
	}
	copy(hint[:], addr.Ed25519[28:])

	decoratedSig := xdr.DecoratedSignature{
		Hint:      xdr.SignatureHint(hint),
		Signature: xdr.Signature(signature),
	}

	signedTx, err := tx.AddSignatureDecorated(decoratedSig)
	if err != nil {
		return nil, fmt.Errorf("failed to add signature: %w", err)
	}

	return signedTx, nil
}

func (s *StellarTxm) handleSendResult(
	ctx context.Context,
	tx *StellarTx,
	submitResult protocolrpc.SendTransactionResponse,
	seq int64,
	txStore *TxStore,
	maxLedger uint32,
) (accepted bool, fatalErr bool, retryReason ErrorReason) {
	if tx == nil {
		s.baseLogger.Errorw("handleSendResult: tx is nil")
		return false, true, ErrorReasonNilTx
	}
	if txStore == nil {
		s.baseLogger.Errorw("handleSendResult: txStore is nil", "txID", tx.ID)
		return false, true, ErrorReasonNilTxStore
	}
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)

	switch submitResult.Status {
	case stellarcore.TXStatusPending, stellarcore.TXStatusDuplicate:
		if submitResult.Hash == "" {
			ctxLogger.Errorw("accepted transaction response missing hash", "status", submitResult.Status)
			return false, true, ErrorReasonNoHash
		}

		err := txStore.AddUnconfirmed(seq, submitResult.Hash, maxLedger, tx)
		if err != nil {
			ctxLogger.Errorw("failed to add unconfirmed tx", "error", err)
			return false, true, ErrorReasonStoreAdd
		}
		s.updateTransactionHash(tx, submitResult.Hash)
		s.updateTransactionResultXDR(tx, "")
		s.updateTransactionResultMeta(tx, "")
		s.updateTransactionResultCode(tx, "")
		return true, false, ""

	case stellarcore.TXStatusTryAgainLater:
		return false, false, ErrorReasonTryAgainLater

	case stellarcore.TXStatusError:
		typedCode, decoded := parseSubmitErrorResult(submitResult.ErrorResultXDR)
		ctxLogger.Warnw("tx rejected by network", "resultCode", typedCode.String(), "errorXDR", submitResult.ErrorResultXDR)

		if !decoded {
			return false, true, ErrorReasonSubmitErrorUndecoded
		}
		fatal, reason := classifySubmitErrorCode(typedCode)
		return false, fatal, reason

	default:
		ctxLogger.Errorw("unknown submit status", "status", submitResult.Status)
		return false, true, ErrorReasonUnknownSubmit
	}
}

// parseSubmitErrorResult decodes errorResultXDR into a transaction result code.
// ok is false when errorResultXDR is empty or cannot be unmarshaled as XDR.
func parseSubmitErrorResult(errorResultXDR string) (code xdr.TransactionResultCode, ok bool) {
	if errorResultXDR == "" {
		return 0, false
	}
	var txResult xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(errorResultXDR, &txResult); err != nil {
		return 0, false
	}
	return txResult.Result.Code, true
}

func classifySubmitErrorCode(code xdr.TransactionResultCode) (fatal bool, reason ErrorReason) {
	switch code {
	case xdr.TransactionResultCodeTxBadSeq:
		return false, ErrorReasonBadSeq
	case xdr.TransactionResultCodeTxInsufficientFee:
		return false, ErrorReasonInsufficientFee
	case xdr.TransactionResultCodeTxInternalError:
		return false, ErrorReasonInternalError
	default:
		return true, ErrorReason(code.String())
	}
}
