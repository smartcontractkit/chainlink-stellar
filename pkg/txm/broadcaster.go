package txm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// broadcaster handles simulation, assembly, signing, and submission of
// pending transactions. It processes one transaction at a time from the
// enqueue channel.
type broadcaster struct {
	client     *ccvclient.Client
	rpc        ccvclient.RPCClient
	ks         Keystore
	seqStore   *SequenceStore
	feeEst     *FeeEstimator
	store      *TxStore
	metrics    *Metrics
	cfg        Config
	passphrase string
	lggr       logger.Logger
}

func newBroadcaster(
	client *ccvclient.Client,
	ks Keystore,
	seqStore *SequenceStore,
	feeEst *FeeEstimator,
	store *TxStore,
	metrics *Metrics,
	cfg Config,
	passphrase string,
	lggr logger.Logger,
) *broadcaster {
	return &broadcaster{
		client:     client,
		rpc:        client.RPC,
		ks:         ks,
		seqStore:   seqStore,
		feeEst:     feeEst,
		store:      store,
		metrics:    metrics,
		cfg:        cfg,
		passphrase: passphrase,
		lggr:       logger.Named(lggr, "Broadcaster"),
	}
}

// broadcast processes a single pending txEntry through the full pipeline:
// allocate sequence -> simulate (Layer 2 retries) -> restore if needed ->
// assemble with fee bump -> sign -> submit (Layer 1 retries).
func (b *broadcaster) broadcast(ctx context.Context, entry *txEntry) error {
	req := entry.Request

	account, allocSeq, err := b.seqStore.NextSequence(ctx)
	if err != nil {
		return fmt.Errorf("get sequence: %w", err)
	}

	latestLedger, err := b.client.LatestLedger(ctx)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("get latest ledger: %w", err)
	}

	ledgerOffset := b.cfg.LedgerBoundsOffset
	if req.LedgerBoundsOffset > 0 {
		ledgerOffset = req.LedgerBoundsOffset
	}
	maxLedger := latestLedger.Sequence + ledgerOffset

	op, err := b.buildInvokeOp(req)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("build invoke op: %w", err)
	}

	preconditions := txnbuild.Preconditions{
		TimeBounds: txnbuild.NewTimeout(300),
		LedgerBounds: &txnbuild.LedgerBounds{
			MaxLedger: maxLedger,
		},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        account,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        preconditions,
	})
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("build transaction: %w", err)
	}

	// Layer 2: Simulate with retries for sequence races
	simResult, err := b.simulateWithRetries(ctx, tx, op, preconditions, &account, &allocSeq)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return err
	}

	// Handle RestoreFootprint if needed
	if b.cfg.AutoRestore && simResult.RestorePreamble != nil {
		if err := b.restoreFootprint(ctx, *simResult.RestorePreamble); err != nil {
			b.seqStore.Release(allocSeq)
			return fmt.Errorf("restore footprint: %w", err)
		}
		b.metrics.IncrRestore()

		// Re-allocate sequence after restore consumed one
		b.seqStore.Release(allocSeq)
		account, allocSeq, err = b.seqStore.NextSequence(ctx)
		if err != nil {
			return fmt.Errorf("get sequence after restore: %w", err)
		}

		tx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        account,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{op},
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        preconditions,
		})
		if err != nil {
			b.seqStore.Release(allocSeq)
			return fmt.Errorf("rebuild transaction after restore: %w", err)
		}

		simResult, err = b.feeEst.Simulate(ctx, tx)
		if err != nil {
			b.seqStore.Release(allocSeq)
			return err
		}
	}

	// SimulateOnly: return the simulation result without broadcasting.
	if req.SimulateOnly {
		var meta *xdr.TransactionMeta
		if simResult.ReturnValue != nil {
			meta = &xdr.TransactionMeta{
				V: 3,
				V3: &xdr.TransactionMetaV3{
					SorobanMeta: &xdr.SorobanTransactionMeta{
						ReturnValue: *simResult.ReturnValue,
					},
				},
			}
		}
		b.seqStore.Release(allocSeq)
		b.store.SetConfirmed(req.ID, meta, 0, "", 0)
		return nil
	}

	// Assemble with fee bumping based on lifecycle attempt
	assembledTx, err := b.feeEst.AssembleTransaction(tx, simResult, account, preconditions, entry.Attempt)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("assemble transaction: %w", err)
	}

	signedTx, err := b.ks.Sign(assembledTx, b.passphrase)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("sign transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("encode signed transaction: %w", err)
	}

	// Layer 1: Submit with retries for transient failures
	hash, err := b.submitWithRetries(ctx, signedXDR)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return err
	}

	b.seqStore.AddUnconfirmed(allocSeq, hash)
	b.store.SetBroadcast(req.ID, hash, allocSeq, maxLedger)
	b.metrics.IncrBroadcasted()
	b.lggr.Infow("Transaction broadcast",
		"txID", req.ID, "hash", hash, "seq", allocSeq,
		"maxLedger", maxLedger, "attempt", entry.Attempt)

	return nil
}

// simulateWithRetries runs simulation with retries for sequence number races
// (Layer 2). On tx_bad_seq during simulation, it syncs the sequence from chain
// and retries with a corrected sequence.
func (b *broadcaster) simulateWithRetries(
	ctx context.Context,
	tx *txnbuild.Transaction,
	op *txnbuild.InvokeHostFunction,
	preconditions txnbuild.Preconditions,
	account **txnbuild.SimpleAccount,
	allocSeq *int64,
) (*SimulationResult, error) {
	var lastErr error
	for attempt := 0; attempt < b.cfg.MaxSimulateAttempts; attempt++ {
		simResult, err := b.feeEst.Simulate(ctx, tx)
		if err == nil {
			return simResult, nil
		}

		lastErr = err
		if !isSimSequenceError(err) {
			return nil, err
		}

		b.lggr.Debugw("Sequence race during simulation, syncing and retrying", "simAttempt", attempt+1)

		b.seqStore.Release(*allocSeq)
		if syncErr := b.seqStore.Sync(ctx); syncErr != nil {
			return nil, fmt.Errorf("sync sequence after sim race: %w", syncErr)
		}

		newAccount, newSeq, seqErr := b.seqStore.NextSequence(ctx)
		if seqErr != nil {
			return nil, fmt.Errorf("get sequence after sim sync: %w", seqErr)
		}
		*account = newAccount
		*allocSeq = newSeq

		var txErr error
		tx, txErr = txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        newAccount,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{op},
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        preconditions,
		})
		if txErr != nil {
			return nil, fmt.Errorf("rebuild transaction after sim sync: %w", txErr)
		}
	}
	return nil, fmt.Errorf("exhausted %d simulation attempts: %w", b.cfg.MaxSimulateAttempts, lastErr)
}

// submitWithRetries submits a signed transaction with retries for transient
// failures (Layer 1). Handles TRY_AGAIN_LATER and transient network errors.
// tx_bad_seq errors are propagated to the caller for sequence resync.
func (b *broadcaster) submitWithRetries(ctx context.Context, signedXDR string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < b.cfg.MaxSubmitAttempts; attempt++ {
		submitResult, err := b.rpc.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
			Transaction: signedXDR,
		})

		if err != nil {
			lastErr = err
			if isTransientNetworkError(err) {
			b.lggr.Debugw("Transient network error, retrying submit", "submitAttempt", attempt+1, "err", err)
				sleepCtx(ctx, b.cfg.SubmitRetryDelay)
				continue
			}
			return "", fmt.Errorf("send transaction: %w", err)
		}

		switch submitResult.Status {
		case "PENDING", "DUPLICATE":
			return submitResult.Hash, nil

		case "TRY_AGAIN_LATER":
			lastErr = ErrOverloaded
			b.lggr.Debugw("Server overloaded (TRY_AGAIN_LATER), retrying", "submitAttempt", attempt+1)
			sleepCtx(ctx, b.cfg.SubmitRetryDelay)
			continue

		case "ERROR":
			errMsg := submitResult.ErrorResultXDR
			if isSequenceError(errMsg) {
				return "", fmt.Errorf("%w: %s", ErrSequence, errMsg)
			}
			if errMsg != "" {
				return "", fmt.Errorf("transaction rejected: %s (diagnostics: %v)", errMsg, submitResult.DiagnosticEventsXDR)
			}
			return "", fmt.Errorf("transaction rejected with status ERROR")

		default:
			return "", fmt.Errorf("unexpected send status: %s", submitResult.Status)
		}
	}

	b.metrics.IncrReject()
	return "", fmt.Errorf("exhausted %d submit attempts: %w", b.cfg.MaxSubmitAttempts, lastErr)
}

func (b *broadcaster) buildInvokeOp(req TxRequest) (*txnbuild.InvokeHostFunction, error) {
	contractBytes, err := strkey.Decode(strkey.VersionByteContract, req.ContractID)
	if err != nil {
		return nil, fmt.Errorf("decode contract ID: %w", err)
	}

	contractAddr := scval.BuildContractScAddress(contractBytes)
	if contractAddr == nil {
		return nil, fmt.Errorf("build contract address")
	}

	return &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: *contractAddr,
				FunctionName:    xdr.ScSymbol(req.FunctionName),
				Args:            req.Args,
			},
		},
		SourceAccount: b.ks.SignerAddress(),
	}, nil
}

func (b *broadcaster) restoreFootprint(ctx context.Context, preamble protocolrpc.RestorePreamble) error {
	account, allocSeq, err := b.seqStore.NextSequence(ctx)
	if err != nil {
		return fmt.Errorf("get sequence for restore: %w", err)
	}

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(preamble.TransactionDataXDR, &sorobanData); err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("decode restore preamble: %w", err)
	}

	restoreOp := &txnbuild.RestoreFootprint{
		SourceAccount: b.ks.SignerAddress(),
		Ext: xdr.TransactionExt{
			V:           1,
			SorobanData: &sorobanData,
		},
	}

	baseFee := preamble.MinResourceFee + b.cfg.FeeBuffer

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        account,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{restoreOp},
		BaseFee:              baseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("build restore transaction: %w", err)
	}

	signedTx, err := b.ks.Sign(tx, b.passphrase)
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("sign restore transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		b.seqStore.Release(allocSeq)
		return fmt.Errorf("encode signed restore transaction: %w", err)
	}

	var submitResult protocolrpc.SendTransactionResponse
	var lastErr error
	for attempt := 0; attempt < b.cfg.MaxSubmitAttempts; attempt++ {
		submitResult, err = b.rpc.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
			Transaction: signedXDR,
		})
		if err != nil {
			lastErr = err
			if isTransientNetworkError(err) {
				sleepCtx(ctx, b.cfg.SubmitRetryDelay)
				continue
			}
			b.seqStore.Release(allocSeq)
			return fmt.Errorf("send restore transaction: %w", err)
		}

		switch submitResult.Status {
		case "PENDING", "DUPLICATE":
			goto submitted
		case "TRY_AGAIN_LATER":
			lastErr = ErrOverloaded
			b.lggr.Debugw("Restore TRY_AGAIN_LATER, retrying", "attempt", attempt+1)
			sleepCtx(ctx, b.cfg.SubmitRetryDelay)
			continue
		case "ERROR":
			b.seqStore.Release(allocSeq)
			return fmt.Errorf("restore rejected: %s", submitResult.ErrorResultXDR)
		default:
			b.seqStore.Release(allocSeq)
			return fmt.Errorf("restore unexpected status: %s", submitResult.Status)
		}
	}
	b.seqStore.Release(allocSeq)
	return fmt.Errorf("restore exhausted %d submit attempts: %w", b.cfg.MaxSubmitAttempts, lastErr)

submitted:
	b.seqStore.AddUnconfirmed(allocSeq, submitResult.Hash)

	resp, err := b.client.PollTransaction(ctx, submitResult.Hash, b.cfg.TxTimeout)
	if err != nil {
		b.seqStore.Confirm(allocSeq, false)
		return fmt.Errorf("restore confirmation: %w", err)
	}
	if resp.Status == protocolrpc.TransactionStatusFailed {
		b.seqStore.Confirm(allocSeq, true)
		return fmt.Errorf("restore transaction failed on-chain")
	}

	b.seqStore.Confirm(allocSeq, true)
	return nil
}

// isSequenceError returns true if the error string indicates a sequence
// number conflict (tx_bad_seq).
func isSequenceError(errXDR string) bool {
	return strings.Contains(errXDR, "tx_bad_seq")
}

// isSimSequenceError checks whether a simulation error is a sequence race.
func isSimSequenceError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "tx_bad_seq")
}

// isTransientNetworkError is a heuristic for retryable network failures.
func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "temporary failure")
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
