package fakes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	commonCap "github.com/smartcontractkit/chainlink-common/pkg/capabilities"
	caperrors "github.com/smartcontractkit/chainlink-common/pkg/capabilities/errors"
	stellarcap "github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar"
)

const (
	forwarderReportFunction = "report"
	reportProcessedTopic    = "forwarder_ReportProcessed"
	// Resource-fee headroom over the simulated minimum, as a divisor (1/5 = 20%).
	resourceFeeHeadroomDivisor = 5
)

// WriteReport delivers the report to input.ContractId through the mock
// forwarder's report(transmitter, receiver, raw_report, report_context,
// signatures). The mock skips signature verification, so an empty signature
// vector is sent: simulator-signed reports carry no ed25519 DON signatures.
//
// Result mapping mirrors production:
//   - forwarder rejects the call (malformed report, replay): TX_STATUS_REVERTED;
//   - forwarder accepts it: TX_STATUS_SUCCESS, with the receiver's outcome
//     (ReportProcessed.success) in ReceiverContractExecutionStatus.
func (c *FakeStellarChain) WriteReport(ctx context.Context, _ commonCap.RequestMetadata, input *stellarcap.WriteReportRequest) (*commonCap.ResponseAndMetadata[*stellarcap.WriteReportReply], caperrors.Error) {
	if input == nil || input.Report == nil {
		return nil, caperrors.NewPublicUserError(errors.New("writeReport request must include a report"), caperrors.InvalidArgument)
	}
	receiver, err := contractAddress(input.ContractId)
	if err != nil {
		return nil, caperrors.NewPublicUserError(fmt.Errorf("invalid receiver: %w", err), caperrors.InvalidArgument)
	}

	transmitter := placeholderAccount
	if c.cfg.Transmitter != nil {
		transmitter = c.cfg.Transmitter.Address()
	}
	args, err := reportArgs(transmitter, receiver, input.Report.RawReport, input.Report.ReportContext)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(err, caperrors.Internal)
	}

	var reply *stellarcap.WriteReportReply
	var cerr caperrors.Error
	if c.cfg.DryRun {
		reply, cerr = c.writeDryRun(ctx, transmitter, input.ContractId, args)
	} else {
		reply, cerr = c.writeBroadcast(ctx, input.ContractId, args)
	}
	if cerr != nil {
		return nil, cerr
	}
	return &commonCap.ResponseAndMetadata[*stellarcap.WriteReportReply]{Response: reply}, nil
}

func reportArgs(transmitter string, receiver xdr.ScAddress, rawReport, reportContext []byte) ([]xdr.ScVal, error) {
	transmitterAddr, err := accountAddress(transmitter)
	if err != nil {
		return nil, err
	}
	raw := xdr.ScBytes(append([]byte(nil), rawReport...))
	reportCtx := xdr.ScBytes(append([]byte(nil), reportContext...))
	sigs := &xdr.ScVec{}
	return []xdr.ScVal{
		{Type: xdr.ScValTypeScvAddress, Address: &transmitterAddr},
		{Type: xdr.ScValTypeScvAddress, Address: &receiver},
		{Type: xdr.ScValTypeScvBytes, Bytes: &raw},
		{Type: xdr.ScValTypeScvBytes, Bytes: &reportCtx},
		{Type: xdr.ScValTypeScvVec, Vec: &sigs},
	}, nil
}

func (c *FakeStellarChain) writeDryRun(ctx context.Context, transmitter, receiverID string, args []xdr.ScVal) (*stellarcap.WriteReportReply, caperrors.Error) {
	tx, cerr := c.buildInvokeTx(&txnbuild.SimpleAccount{AccountID: transmitter}, c.forwarder, forwarderReportFunction, args, time.Now().Add(simulateTimeBound))
	if cerr != nil {
		return nil, cerr
	}
	txHash, err := tx.HashHex(c.cfg.NetworkPassphrase)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to hash Stellar transaction: %w", err), caperrors.Internal)
	}
	// Record mode: the transmitter's require_auth is recorded, not enforced.
	sim, cerr := c.simulate(ctx, tx, protocol.AuthModeRecord)
	if cerr != nil {
		return nil, cerr
	}

	ledger := sim.LatestLedger
	if msg := simulationError(sim); msg != "" {
		c.lggr.Warnw("Stellar dry-run write: forwarder rejected the report", "receiver", receiverID, "error", msg)
		return revertedWrite(fmt.Sprintf("forwarder rejected the report in simulation: %s", msg), nil, &ledger), nil
	}

	events := successfulContractEvents(sim.EventsXDR)
	c.logReceiverEvents(receiverID, events)
	success, found := c.reportProcessed(events)
	if !found {
		return nil, caperrors.NewPublicSystemError(errors.New("simulation succeeded without a ReportProcessed event; is the forwarder a CRE mock forwarder?"), caperrors.Internal)
	}

	fee := uint64(txnbuild.MinBaseFee) + uint64(max(sim.MinResourceFee, 0)) //nolint:gosec // non-negative
	reply := &stellarcap.WriteReportReply{
		TxStatus:       stellarcap.TxStatus_TX_STATUS_SUCCESS,
		TxHash:         &txHash,
		TransactionFee: &fee,
		LedgerSequence: &ledger,
	}
	setReceiverStatus(reply, success, receiverID, receiverFailureReason(sim.EventsXDR))
	c.lggr.Infow("Stellar dry-run write simulated (not submitted)",
		"receiver", receiverID, "forwarder", c.cfg.ForwarderID, "receiverSuccess", success, "simulatedTxHash", txHash)
	return reply, nil
}

func (c *FakeStellarChain) writeBroadcast(ctx context.Context, receiverID string, args []xdr.ScVal) (*stellarcap.WriteReportReply, caperrors.Error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	transmitter := c.cfg.Transmitter.Address()
	account, err := c.client.LoadAccount(ctx, transmitter)
	if err != nil {
		return nil, caperrors.NewPublicUserError(fmt.Errorf("failed to load transmitter account %s (does it exist and is it funded?): %w", transmitter, err), caperrors.FailedPrecondition)
	}
	seq, err := account.GetSequenceNumber()
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("transmitter sequence: %w", err), caperrors.Internal)
	}
	deadline := time.Now().Add(c.cfg.TxTimeout)
	newSource := func() *txnbuild.SimpleAccount { return &txnbuild.SimpleAccount{AccountID: transmitter, Sequence: seq} }

	tx, cerr := c.buildInvokeTx(newSource(), c.forwarder, forwarderReportFunction, args, deadline)
	if cerr != nil {
		return nil, cerr
	}
	sim, cerr := c.simulate(ctx, tx, "")
	if cerr != nil {
		return nil, cerr
	}
	if msg := simulationError(sim); msg != "" {
		// The transaction would fail on-chain; don't spend fees submitting it.
		ledger := sim.LatestLedger
		c.lggr.Warnw("Stellar write: forwarder rejected the report in preflight; not submitted", "receiver", receiverID, "error", msg)
		return revertedWrite(fmt.Sprintf("forwarder rejected the report in preflight: %s", msg), nil, &ledger), nil
	}

	assembled, cerr := c.assemble(newSource(), tx, sim, deadline)
	if cerr != nil {
		return nil, cerr
	}
	signed, err := assembled.Sign(c.cfg.NetworkPassphrase, c.cfg.Transmitter)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to sign Stellar transaction: %w", err), caperrors.Internal)
	}
	signedXDR, err := signed.Base64()
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to encode signed Stellar transaction: %w", err), caperrors.Internal)
	}
	txHash, err := signed.HashHex(c.cfg.NetworkPassphrase)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to hash Stellar transaction: %w", err), caperrors.Internal)
	}

	sent, err := c.client.SendTransaction(ctx, protocol.SendTransactionRequest{Transaction: signedXDR})
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("stellar sendTransaction failed: %w", err), caperrors.Unavailable)
	}
	switch sent.Status {
	case "PENDING", "DUPLICATE":
	case "TRY_AGAIN_LATER":
		return nil, caperrors.NewPublicSystemError(errors.New("stellar sendTransaction: TRY_AGAIN_LATER"), caperrors.Unavailable)
	default:
		msg := fmt.Sprintf("transaction rejected with status %s", sent.Status)
		if sent.ErrorResultXDR != "" {
			msg += ": " + describeTxResult(sent.ErrorResultXDR)
		}
		return &stellarcap.WriteReportReply{TxStatus: stellarcap.TxStatus_TX_STATUS_FATAL, TxHash: &txHash, ErrorMessage: &msg}, nil
	}
	if sent.Hash != "" {
		txHash = sent.Hash
	}

	res, cerr := c.waitForTransaction(ctx, txHash, deadline)
	if cerr != nil {
		return nil, cerr
	}
	return c.confirmedReply(receiverID, txHash, res), nil
}

// assemble applies the simulated footprint, resource fee (plus headroom) and
// auth entries to the operation and rebuilds the transaction.
func (c *FakeStellarChain) assemble(source *txnbuild.SimpleAccount, tx *txnbuild.Transaction, sim protocol.SimulateTransactionResponse, deadline time.Time) (*txnbuild.Transaction, caperrors.Error) {
	ops := tx.Operations()
	ihf, ok := ops[0].(*txnbuild.InvokeHostFunction)
	if !ok || sim.TransactionDataXDR == "" {
		return nil, caperrors.NewPublicSystemError(errors.New("simulation returned no Soroban transaction data"), caperrors.Internal)
	}
	var data xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &data); err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to decode Soroban transaction data: %w", err), caperrors.Internal)
	}
	data.ResourceFee += xdr.Int64(sim.MinResourceFee / resourceFeeHeadroomDivisor)
	ihf.Ext = xdr.TransactionExt{V: 1, SorobanData: &data}

	if len(sim.Results) > 0 && sim.Results[0].AuthXDR != nil {
		auth := make([]xdr.SorobanAuthorizationEntry, len(*sim.Results[0].AuthXDR))
		for i, a := range *sim.Results[0].AuthXDR {
			if err := xdr.SafeUnmarshalBase64(a, &auth[i]); err != nil {
				return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to decode auth entry: %w", err), caperrors.Internal)
			}
		}
		ihf.Auth = auth
	}

	// BaseFee is the inclusion fee only; txnbuild adds SorobanData.ResourceFee.
	assembled, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        source,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{ihf},
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimebounds(0, deadline.Unix())},
	})
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("failed to assemble Stellar transaction: %w", err), caperrors.Internal)
	}
	return assembled, nil
}

func (c *FakeStellarChain) waitForTransaction(ctx context.Context, hash string, deadline time.Time) (protocol.GetTransactionResponse, caperrors.Error) {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	for {
		res, err := c.client.GetTransaction(ctx, protocol.GetTransactionRequest{Hash: hash, Format: protocol.FormatBase64})
		if err == nil && res.Status != protocol.TransactionStatusNotFound {
			return res, nil
		}
		if err != nil {
			c.lggr.Debugw("getTransaction failed; retrying", "hash", hash, "err", err)
		}
		select {
		case <-ctx.Done():
			return protocol.GetTransactionResponse{}, caperrors.NewPublicSystemError(fmt.Errorf("timed out waiting for Stellar transaction %s: %w", hash, ctx.Err()), caperrors.DeadlineExceeded)
		case <-ticker.C:
		}
	}
}

func (c *FakeStellarChain) confirmedReply(receiverID, txHash string, res protocol.GetTransactionResponse) *stellarcap.WriteReportReply {
	ledger := res.Ledger
	reply := &stellarcap.WriteReportReply{TxHash: &txHash, LedgerSequence: &ledger}
	if fee, ok := feeCharged(res.ResultXDR); ok {
		reply.TransactionFee = &fee
	}
	if res.LedgerCloseTime > 0 {
		ts := uint64(res.LedgerCloseTime) * uint64(time.Second/time.Microsecond) //nolint:gosec // positive unix time
		reply.BlockTimestamp = &ts
	}

	if res.Status != protocol.TransactionStatusSuccess {
		reply.TxStatus = stellarcap.TxStatus_TX_STATUS_REVERTED
		msg := fmt.Sprintf("transaction %s failed", txHash)
		if res.ResultXDR != "" {
			msg += ": " + describeTxResult(res.ResultXDR)
		}
		reply.ErrorMessage = &msg
		c.lggr.Warnw("Stellar write: transaction failed", "receiver", receiverID, "txHash", txHash, "error", msg)
		return reply
	}

	reply.TxStatus = stellarcap.TxStatus_TX_STATUS_SUCCESS
	events := confirmedContractEvents(res)
	c.logReceiverEvents(receiverID, events)
	success, found := c.reportProcessed(events)
	if !found {
		msg := "transaction succeeded but no ReportProcessed event was found; receiver outcome unknown"
		reply.ErrorMessage = &msg
		return reply
	}
	setReceiverStatus(reply, success, receiverID, "")
	c.lggr.Infow("Stellar write confirmed", "receiver", receiverID, "txHash", txHash, "ledger", ledger, "receiverSuccess", success)
	return reply
}

// reportProcessed finds the forwarder's ReportProcessed event and returns its
// success flag.
func (c *FakeStellarChain) reportProcessed(events []xdr.ContractEvent) (success, found bool) {
	for _, ev := range events {
		if ev.ContractId == nil || !c.isForwarder(*ev.ContractId) || eventName(ev) != reportProcessedTopic {
			continue
		}
		if b, ok := ev.Body.V0.Data.GetB(); ok {
			return b, true
		}
	}
	return false, false
}

func (c *FakeStellarChain) isForwarder(id xdr.ContractId) bool {
	return c.forwarder.ContractId != nil && *c.forwarder.ContractId == id
}

// logReceiverEvents surfaces events the receiver emitted (e.g. feed updates
// or permission rejections) so simulation output shows what it did.
func (c *FakeStellarChain) logReceiverEvents(receiverID string, events []xdr.ContractEvent) {
	for _, ev := range events {
		if ev.ContractId == nil || c.isForwarder(*ev.ContractId) {
			continue
		}
		c.lggr.Infow("Stellar write: contract event", "receiver", receiverID, "event", eventName(ev))
	}
}

func setReceiverStatus(reply *stellarcap.WriteReportReply, success bool, receiverID, reason string) {
	if success {
		reply.ReceiverContractExecutionStatus = stellarcap.ReceiverContractExecutionStatus_RECEIVER_CONTRACT_EXECUTION_STATUS_SUCCESS.Enum()
		return
	}
	reply.ReceiverContractExecutionStatus = stellarcap.ReceiverContractExecutionStatus_RECEIVER_CONTRACT_EXECUTION_STATUS_REVERTED.Enum()
	msg := fmt.Sprintf("receiver %s on_report did not succeed", receiverID)
	if reason != "" {
		msg += ": " + reason
	}
	reply.ErrorMessage = &msg
}

func revertedWrite(msg string, txHash *string, ledger *uint32) *stellarcap.WriteReportReply {
	return &stellarcap.WriteReportReply{TxStatus: stellarcap.TxStatus_TX_STATUS_REVERTED, ErrorMessage: &msg, TxHash: txHash, LedgerSequence: ledger}
}

func eventName(ev xdr.ContractEvent) string {
	if ev.Body.V0 == nil || len(ev.Body.V0.Topics) == 0 {
		return ""
	}
	if sym, ok := ev.Body.V0.Topics[0].GetSym(); ok {
		return string(sym)
	}
	return ""
}

// successfulContractEvents extracts contract events that survived execution
// from a simulation's diagnostic events.
func successfulContractEvents(diagnosticXDR []string) []xdr.ContractEvent {
	var out []xdr.ContractEvent
	for _, e := range diagnosticXDR {
		var d xdr.DiagnosticEvent
		if err := xdr.SafeUnmarshalBase64(e, &d); err != nil {
			continue
		}
		if d.InSuccessfulContractCall && d.Event.Type == xdr.ContractEventTypeContract && d.Event.Body.V0 != nil {
			out = append(out, d.Event)
		}
	}
	return out
}

// receiverFailureReason pulls the host error out of the rolled-back receiver
// call, if the simulation reported one.
func receiverFailureReason(diagnosticXDR []string) string {
	for _, e := range diagnosticXDR {
		var d xdr.DiagnosticEvent
		if err := xdr.SafeUnmarshalBase64(e, &d); err != nil || d.InSuccessfulContractCall || d.Event.Body.V0 == nil {
			continue
		}
		body := d.Event.Body.V0
		if len(body.Topics) > 1 && eventName(d.Event) == "error" {
			return strings.TrimSpace(body.Topics[1].String() + " " + body.Data.String())
		}
	}
	return ""
}

// confirmedContractEvents returns the contract events of a confirmed
// transaction, preferring the RPC's per-operation events and falling back to
// the result meta (v3/v4).
func confirmedContractEvents(res protocol.GetTransactionResponse) []xdr.ContractEvent {
	var out []xdr.ContractEvent
	for _, op := range res.Events.ContractEventsXDR {
		for _, e := range op {
			var ev xdr.ContractEvent
			if err := xdr.SafeUnmarshalBase64(e, &ev); err == nil {
				out = append(out, ev)
			}
		}
	}
	if len(out) > 0 || res.ResultMetaXDR == "" {
		return out
	}
	var meta xdr.TransactionMeta
	if err := xdr.SafeUnmarshalBase64(res.ResultMetaXDR, &meta); err != nil {
		return nil
	}
	switch meta.V {
	case 3:
		if v3 := meta.MustV3(); v3.SorobanMeta != nil {
			out = append(out, v3.SorobanMeta.Events...)
		}
	case 4:
		for _, op := range meta.MustV4().Operations {
			out = append(out, op.Events...)
		}
	}
	return out
}

func feeCharged(resultXDR string) (uint64, bool) {
	if resultXDR == "" {
		return 0, false
	}
	var r xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &r); err != nil || r.FeeCharged < 0 {
		return 0, false
	}
	return uint64(r.FeeCharged), true
}

func describeTxResult(resultXDR string) string {
	var r xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &r); err != nil {
		return resultXDR
	}
	return strings.TrimPrefix(r.Result.Code.String(), "TransactionResultCode")
}
