package txm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type simulationErrorSource int

const (
	simulationErrorSourceRPC simulationErrorSource = iota
	simulationErrorSourceResponse
)

type simulationError struct {
	source simulationErrorSource
	err    error
}

func (e *simulationError) Error() string {
	switch e.source {
	case simulationErrorSourceRPC:
		return fmt.Sprintf("RPC SimulateTransaction failed: %v", e.err)
	case simulationErrorSourceResponse:
		return fmt.Sprintf("simulation error: %v", e.err)
	default:
		return e.err.Error()
	}
}

func (e *simulationError) Unwrap() error {
	return e.err
}

func (s *StellarTxm) buildPreliminaryTx(tx *StellarTx, seq int64, maxLedger uint32) (*txnbuild.Transaction, error) {
	// seq is the NEXT sequence to submit (TxStore convention).
	// txnbuild.NewSimpleAccount expects the LAST USED sequence, so pass seq-1;
	// IncrementSequenceNum:true then produces exactly seq on the wire.
	sourceAccount := txnbuild.NewSimpleAccount(tx.FromAddress, seq-1)

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

func (s *StellarTxm) simulateTransaction(ctx context.Context, client *ccvclient.Client, tx *txnbuild.Transaction) (protocolrpc.SimulateTransactionResponse, error) {
	txXDR, err := tx.Base64()
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("failed to base64 encode preliminary tx: %w", err)
	}

	start := time.Now()
	simResult, err := client.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
		Transaction: txXDR,
	})
	s.metrics.ObserveSimulationDuration(ctx, time.Since(start).Seconds())
	if err != nil {
		return protocolrpc.SimulateTransactionResponse{}, &simulationError{source: simulationErrorSourceRPC, err: err}
	}

	if simResult.Error != "" {
		return protocolrpc.SimulateTransactionResponse{}, &simulationError{source: simulationErrorSourceResponse, err: errors.New(simResult.Error)}
	}

	return simResult, nil
}

func isRetryableSimulationError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	terminalHints := []string{
		"error(contract",
		"contract error",
		"trapped",
		"trap",
		"malformed",
		"bad auth",
		"invalid",
		"unknown function",
		"no such contract",
	}
	for _, hint := range terminalHints {
		if strings.Contains(msg, hint) {
			return false
		}
	}

	retryableHints := []string{
		"timeout",
		"temporarily unavailable",
		"try_again_later",
		"too many requests",
		"rate limit",
		"connection refused",
		"connection reset",
		"eof",
		"bad_seq",
		"tx_bad_seq",
		"sequence",
		"stale",
		"ledger",
	}

	var simErr *simulationError
	if errors.As(err, &simErr) && simErr.source == simulationErrorSourceResponse {
		for _, hint := range retryableHints {
			if strings.Contains(msg, hint) {
				return true
			}
		}
		return false
	}

	for _, hint := range retryableHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return true
}

// assembleTransaction rebuilds tx with simulation results and a caller-supplied inclusionFee.
// inclusionFee is computed by the caller from getFeeStats + geometric bump; it must NOT include
// the resource fee — txnbuild computes the envelope fee as BaseFee*numOps + sorobanData.ResourceFee,
// so folding resource fee into BaseFee would double-count it.
func (s *StellarTxm) assembleTransaction(tx *txnbuild.Transaction, sim protocolrpc.SimulateTransactionResponse, inclusionFee int64, maxLedger uint32) (*txnbuild.Transaction, int64, error) {
	ops := tx.Operations()
	if len(ops) == 0 {
		return nil, 0, fmt.Errorf("transaction has no operations")
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
) (accepted bool, fatalErr bool, retryReason string) {
	ctxLogger := GetContextedTxLogger(s.baseLogger, tx.ID, tx.Metadata)

	switch submitResult.Status {
	case "PENDING", "DUPLICATE":
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

	case "TRY_AGAIN_LATER":
		return false, false, ErrorReasonTryAgainLater

	case "ERROR":
		resultCode := s.classifyErrorResult(submitResult.ErrorResultXDR)
		ctxLogger.Warnw("tx rejected by network", "resultCode", resultCode, "errorXDR", submitResult.ErrorResultXDR)

		switch resultCode {
		case xdr.TransactionResultCodeTxBadSeq.String():
			return false, false, ErrorReasonBadSeq
		case xdr.TransactionResultCodeTxInsufficientBalance.String(), xdr.TransactionResultCodeTxBadAuth.String():
			return false, true, resultCode
		default:
			return false, false, resultCode
		}

	default:
		ctxLogger.Errorw("unknown submit status", "status", submitResult.Status)
		return false, true, ErrorReasonUnknownSubmit
	}
}

func (s *StellarTxm) classifyErrorResult(errorResultXDR string) string {
	if errorResultXDR == "" {
		return "unknown_error"
	}

	var txResult xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(errorResultXDR, &txResult); err != nil {
		return "decode_error"
	}

	return txResult.Result.Code.String()
}
