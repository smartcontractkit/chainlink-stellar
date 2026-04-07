package txm

import (
	"context"
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// broadcaster handles simulation, assembly, signing, and submission of
// pending transactions. It processes one transaction at a time from the
// enqueue channel.
type broadcaster struct {
	rpc       ccvclient.RPCClient
	ks        Keystore
	seqMgr    *SequenceManager
	feeEst    *FeeEstimator
	store     *TxStore
	cfg       Config
	passphrase string
	lggr      zerolog.Logger
}

func newBroadcaster(
	rpc ccvclient.RPCClient,
	ks Keystore,
	seqMgr *SequenceManager,
	feeEst *FeeEstimator,
	store *TxStore,
	cfg Config,
	passphrase string,
	lggr zerolog.Logger,
) *broadcaster {
	return &broadcaster{
		rpc:        rpc,
		ks:         ks,
		seqMgr:     seqMgr,
		feeEst:     feeEst,
		store:      store,
		cfg:        cfg,
		passphrase: passphrase,
		lggr:       lggr.With().Str("component", "txm.broadcaster").Logger(),
	}
}

// broadcast processes a single pending txEntry: simulate → assemble → sign → send.
// It returns an error if any step fails.
func (b *broadcaster) broadcast(ctx context.Context, entry *txEntry) error {
	req := entry.Request

	sourceAccount, err := b.seqMgr.NextSequence(ctx)
	if err != nil {
		return fmt.Errorf("get sequence: %w", err)
	}

	// Get the current latest ledger for LedgerBounds
	latestLedger, err := b.rpc.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("get latest ledger: %w", err)
	}

	ledgerOffset := b.cfg.LedgerBoundsOffset
	if req.LedgerBoundsOffset > 0 {
		ledgerOffset = req.LedgerBoundsOffset
	}
	maxLedger := latestLedger.Sequence + ledgerOffset

	// Build the InvokeHostFunction operation
	op, err := b.buildInvokeOp(req)
	if err != nil {
		return fmt.Errorf("build invoke op: %w", err)
	}

	preconditions := txnbuild.Preconditions{
		TimeBounds: txnbuild.NewTimeout(300),
		LedgerBounds: &txnbuild.LedgerBounds{
			MaxLedger: maxLedger,
		},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        sourceAccount,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        preconditions,
	})
	if err != nil {
		return fmt.Errorf("build transaction: %w", err)
	}

	// Simulate
	simResult, err := b.feeEst.Simulate(ctx, tx)
	if err != nil {
		return err
	}

	// Handle RestoreFootprint if needed
	if b.cfg.AutoRestore && simResult.RestorePreamble != nil {
		if err := b.restoreFootprint(ctx, *simResult.RestorePreamble); err != nil {
			return fmt.Errorf("restore footprint: %w", err)
		}

		// Re-fetch sequence and rebuild after restore consumed a sequence
		sourceAccount, err = b.seqMgr.NextSequence(ctx)
		if err != nil {
			return fmt.Errorf("get sequence after restore: %w", err)
		}

		tx, err = txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        sourceAccount,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{op},
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        preconditions,
		})
		if err != nil {
			return fmt.Errorf("rebuild transaction after restore: %w", err)
		}

		simResult, err = b.feeEst.Simulate(ctx, tx)
		if err != nil {
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
		b.store.SetConfirmed(req.ID, meta, 0)
		return nil
	}

	// Assemble with simulation results (preserving preconditions incl. LedgerBounds)
	assembledTx, err := b.feeEst.AssembleTransaction(tx, simResult, sourceAccount, preconditions)
	if err != nil {
		return fmt.Errorf("assemble transaction: %w", err)
	}

	// Sign
	signedTx, err := b.ks.Sign(assembledTx, b.passphrase)
	if err != nil {
		return fmt.Errorf("sign transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		return fmt.Errorf("encode signed transaction: %w", err)
	}

	// Submit
	submitResult, err := b.rpc.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
		Transaction: signedXDR,
	})
	if err != nil {
		return fmt.Errorf("send transaction: %w", err)
	}

	switch submitResult.Status {
	case "PENDING", "DUPLICATE":
		b.seqMgr.Confirm()
		b.store.SetBroadcast(req.ID, submitResult.Hash, sourceAccount.Sequence+1, maxLedger)
		b.lggr.Info().
			Str("txID", req.ID).
			Str("hash", submitResult.Hash).
			Uint32("maxLedger", maxLedger).
			Msg("Transaction broadcast")
		return nil

	case "TRY_AGAIN_LATER":
		return ErrOverloaded

	case "ERROR":
		errMsg := submitResult.ErrorResultXDR
		if isSequenceError(errMsg) {
			return fmt.Errorf("%w: %s", ErrSequence, errMsg)
		}
		if errMsg != "" {
			return fmt.Errorf("transaction rejected: %s (diagnostics: %v)", errMsg, submitResult.DiagnosticEventsXDR)
		}
		return fmt.Errorf("transaction rejected with status ERROR")

	default:
		return fmt.Errorf("unexpected send status: %s", submitResult.Status)
	}
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
	sourceAccount, err := b.seqMgr.NextSequence(ctx)
	if err != nil {
		return fmt.Errorf("get sequence for restore: %w", err)
	}

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(preamble.TransactionDataXDR, &sorobanData); err != nil {
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
		SourceAccount:        sourceAccount,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{restoreOp},
		BaseFee:              baseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
	if err != nil {
		return fmt.Errorf("build restore transaction: %w", err)
	}

	signedTx, err := b.ks.Sign(tx, b.passphrase)
	if err != nil {
		return fmt.Errorf("sign restore transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		return fmt.Errorf("encode signed restore transaction: %w", err)
	}

	submitResult, err := b.rpc.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
		Transaction: signedXDR,
	})
	if err != nil {
		return fmt.Errorf("send restore transaction: %w", err)
	}

	switch submitResult.Status {
	case "PENDING", "DUPLICATE":
	case "TRY_AGAIN_LATER":
		return fmt.Errorf("restore: %w", ErrOverloaded)
	case "ERROR":
		return fmt.Errorf("restore rejected: %s", submitResult.ErrorResultXDR)
	default:
		return fmt.Errorf("restore unexpected status: %s", submitResult.Status)
	}

	b.seqMgr.Confirm()

	// Wait for restore to confirm
	if err := waitForTxConfirmation(ctx, b.rpc, submitResult.Hash, b.cfg.TxTimeout); err != nil {
		return fmt.Errorf("restore confirmation: %w", err)
	}

	return nil
}

// isSequenceError returns true if the error string indicates a sequence
// number conflict (tx_bad_seq).
func isSequenceError(errXDR string) bool {
	return strings.Contains(errXDR, "tx_bad_seq")
}
