package fakes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonCap "github.com/smartcontractkit/chainlink-common/pkg/capabilities"
	stellarcap "github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar"
	"github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar/scval"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	sdkpb "github.com/smartcontractkit/chainlink-protos/cre/go/sdk"
)

const (
	testForwarder = "CDNKVWAPQZWVA2FLT3H5IQZ7N5WLYEDKUNRZ33E4KY7TI5KOBU6MTAVW"
	testReceiver  = "CD4EQYJ2L5EG6M7KVDWWIIXYLP5HYNOWU5KNP5AMOIKALXXZVYGDRQ67"
	testSelector  = 4894814558906953166
)

// fakeRPC records requests and replays scripted responses.
type fakeRPC struct {
	mu sync.Mutex

	simReqs []protocol.SimulateTransactionRequest
	simResp protocol.SimulateTransactionResponse
	simErr  error

	sendReqs []protocol.SendTransactionRequest
	sendResp protocol.SendTransactionResponse

	// getTx responses are returned in order; the last one repeats.
	getTxResps []protocol.GetTransactionResponse
	getTxCalls int

	accountSeq int64
	loadErr    error

	ledger protocol.GetLatestLedgerResponse
}

func (f *fakeRPC) GetLatestLedger(context.Context) (protocol.GetLatestLedgerResponse, error) {
	return f.ledger, nil
}

func (f *fakeRPC) SimulateTransaction(_ context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.simReqs = append(f.simReqs, req)
	return f.simResp, f.simErr
}

func (f *fakeRPC) SendTransaction(_ context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendReqs = append(f.sendReqs, req)
	return f.sendResp, nil
}

func (f *fakeRPC) GetTransaction(context.Context, protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := min(f.getTxCalls, len(f.getTxResps)-1)
	f.getTxCalls++
	return f.getTxResps[i], nil
}

func (f *fakeRPC) LoadAccount(_ context.Context, address string) (txnbuild.Account, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return &txnbuild.SimpleAccount{AccountID: address, Sequence: f.accountSeq}, nil
}

// --- XDR fixtures ---

func contractID(t *testing.T, c string) xdr.ContractId {
	t.Helper()
	a, err := contractAddress(c)
	require.NoError(t, err)
	return *a.ContractId
}

func contractEvent(t *testing.T, from string, name string, data xdr.ScVal) xdr.ContractEvent {
	t.Helper()
	id := contractID(t, from)
	sym := xdr.ScSymbol(name)
	return xdr.ContractEvent{
		ContractId: &id,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{
			Topics: []xdr.ScVal{{Type: xdr.ScValTypeScvSymbol, Sym: &sym}},
			Data:   data,
		}},
	}
}

func boolVal(b bool) xdr.ScVal { return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &b} }

func b64(t *testing.T, v interface{}) string {
	t.Helper()
	s, err := xdr.MarshalBase64(v)
	require.NoError(t, err)
	return s
}

func diagnostic(t *testing.T, ev xdr.ContractEvent, successful bool) string {
	return b64(t, xdr.DiagnosticEvent{InSuccessfulContractCall: successful, Event: ev})
}

func reportProcessedSim(t *testing.T, success bool) protocol.SimulateTransactionResponse {
	return protocol.SimulateTransactionResponse{
		LatestLedger:       42,
		MinResourceFee:     1000,
		TransactionDataXDR: b64(t, xdr.SorobanTransactionData{}),
		EventsXDR: []string{
			diagnostic(t, contractEvent(t, testReceiver, "FeedUpdated", boolVal(true)), true),
			diagnostic(t, contractEvent(t, testForwarder, reportProcessedTopic, boolVal(success)), true),
		},
		Results: []protocol.SimulateHostFunctionResult{{}},
	}
}

func txResultXDR(t *testing.T, fee int64, success bool) string {
	code := xdr.TransactionResultCodeTxSuccess
	if !success {
		code = xdr.TransactionResultCodeTxFailed
	}
	results := []xdr.OperationResult{}
	return b64(t, xdr.TransactionResult{
		FeeCharged: xdr.Int64(fee),
		Result:     xdr.TransactionResultResult{Code: code, Results: &results},
	})
}

func validReport() *sdkpb.ReportResponse {
	raw := make([]byte, 109)
	raw[0] = 1
	return &sdkpb.ReportResponse{RawReport: append(raw, 0xde, 0xad), ReportContext: make([]byte, 96)}
}

func newFake(t *testing.T, rpc *fakeRPC, cfg Config) *FakeStellarChain {
	t.Helper()
	cfg.ChainSelector = testSelector
	cfg.NetworkPassphrase = network.TestNetworkPassphrase
	cfg.ForwarderID = testForwarder
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Millisecond
	}
	c, err := NewFakeStellarChain(logger.Test(t), rpc, cfg)
	require.NoError(t, err)
	return c
}

func write(t *testing.T, c *FakeStellarChain) *stellarcap.WriteReportReply {
	t.Helper()
	resp, cerr := c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: testReceiver, Report: validReport()})
	require.Nil(t, cerr)
	return resp.Response
}

// invocation decodes the single InvokeContract operation of a tx envelope.
func invocation(t *testing.T, envB64 string) (xdr.TransactionEnvelope, xdr.InvokeContractArgs) {
	t.Helper()
	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(envB64, &env))
	ops := env.Operations()
	require.Len(t, ops, 1)
	return env, *ops[0].Body.InvokeHostFunctionOp.HostFunction.InvokeContract
}

// --- constructor ---

func TestNewFakeStellarChain_Validation(t *testing.T) {
	t.Parallel()
	base := Config{NetworkPassphrase: network.TestNetworkPassphrase, ForwarderID: testForwarder, DryRun: true}

	_, err := NewFakeStellarChain(logger.Test(t), nil, base)
	require.ErrorContains(t, err, "rpc client")

	cfg := base
	cfg.NetworkPassphrase = ""
	_, err = NewFakeStellarChain(logger.Test(t), &fakeRPC{}, cfg)
	require.ErrorContains(t, err, "passphrase")

	cfg = base
	cfg.ForwarderID = "GABC"
	_, err = NewFakeStellarChain(logger.Test(t), &fakeRPC{}, cfg)
	require.ErrorContains(t, err, "forwarder")

	cfg = base
	cfg.DryRun = false
	_, err = NewFakeStellarChain(logger.Test(t), &fakeRPC{}, cfg)
	require.ErrorContains(t, err, "transmitter")
}

// --- dry-run writes ---

func TestWriteReport_DryRun_CallsForwarderReport(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{simResp: reportProcessedSim(t, true)}
	c := newFake(t, rpc, Config{DryRun: true})

	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_SUCCESS, r.TxStatus)
	assert.Equal(t, stellarcap.ReceiverContractExecutionStatus_RECEIVER_CONTRACT_EXECUTION_STATUS_SUCCESS, *r.ReceiverContractExecutionStatus)
	assert.Len(t, *r.TxHash, 64)
	assert.Equal(t, uint32(42), *r.LedgerSequence)
	assert.Equal(t, uint64(1100), *r.TransactionFee)
	assert.Nil(t, r.ErrorMessage)
	assert.Empty(t, rpc.sendReqs, "dry-run must never submit")

	require.Len(t, rpc.simReqs, 1)
	assert.Equal(t, protocol.AuthModeRecord, rpc.simReqs[0].AuthMode)
	_, ic := invocation(t, rpc.simReqs[0].Transaction)
	assert.Equal(t, xdr.ScSymbol("report"), ic.FunctionName)
	assert.Equal(t, contractID(t, testForwarder), *ic.ContractAddress.ContractId)

	require.Len(t, ic.Args, 5)
	assert.Equal(t, xdr.ScAddressTypeScAddressTypeAccount, ic.Args[0].Address.Type, "transmitter")
	assert.Equal(t, contractID(t, testReceiver), *ic.Args[1].Address.ContractId, "receiver")
	assert.Equal(t, validReport().RawReport, []byte(*ic.Args[2].Bytes))
	assert.Len(t, []byte(*ic.Args[3].Bytes), 96)
	assert.Empty(t, **ic.Args[4].Vec, "no signatures are sent")
}

func TestWriteReport_DryRun_UsesTransmitterWhenConfigured(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	rpc := &fakeRPC{simResp: reportProcessedSim(t, true)}
	c := newFake(t, rpc, Config{DryRun: true, Transmitter: kp})
	write(t, c)

	env, ic := invocation(t, rpc.simReqs[0].Transaction)
	src := env.SourceAccount().ToAccountId()
	assert.Equal(t, kp.Address(), src.Address())
	transmitter := ic.Args[0].Address.AccountId
	assert.Equal(t, kp.Address(), transmitter.Address())
}

func TestWriteReport_DryRun_ReceiverFailed(t *testing.T) {
	t.Parallel()
	sim := reportProcessedSim(t, false)
	errSym := xdr.ScSymbol("error")
	code := xdr.Uint32(7)
	failure := contractEvent(t, testReceiver, "error", xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &code})
	failure.Body.V0.Topics = []xdr.ScVal{{Type: xdr.ScValTypeScvSymbol, Sym: &errSym}, {Type: xdr.ScValTypeScvU32, U32: &code}}
	sim.EventsXDR = append(sim.EventsXDR, diagnostic(t, failure, false))
	c := newFake(t, &fakeRPC{simResp: sim}, Config{DryRun: true})

	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_SUCCESS, r.TxStatus, "forwarder catches receiver failures")
	assert.Equal(t, stellarcap.ReceiverContractExecutionStatus_RECEIVER_CONTRACT_EXECUTION_STATUS_REVERTED, *r.ReceiverContractExecutionStatus)
	require.NotNil(t, r.ErrorMessage)
	assert.Contains(t, *r.ErrorMessage, "on_report did not succeed")
	assert.Contains(t, *r.ErrorMessage, "7")
}

func TestWriteReport_DryRun_ForwarderRejects(t *testing.T) {
	t.Parallel()
	c := newFake(t, &fakeRPC{simResp: protocol.SimulateTransactionResponse{Error: "HostError: Error(Contract, #13)", LatestLedger: 9}}, Config{DryRun: true})
	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_REVERTED, r.TxStatus)
	assert.Contains(t, *r.ErrorMessage, "#13")
	assert.Nil(t, r.ReceiverContractExecutionStatus)
}

func TestWriteReport_DryRun_ArchivedState(t *testing.T) {
	t.Parallel()
	c := newFake(t, &fakeRPC{simResp: protocol.SimulateTransactionResponse{RestorePreamble: &protocol.RestorePreamble{}}}, Config{DryRun: true})
	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_REVERTED, r.TxStatus)
	assert.Contains(t, *r.ErrorMessage, "archived")
}

func TestWriteReport_DryRun_NoReportProcessedEvent(t *testing.T) {
	t.Parallel()
	sim := reportProcessedSim(t, true)
	sim.EventsXDR = sim.EventsXDR[:1] // drop the forwarder event
	c := newFake(t, &fakeRPC{simResp: sim}, Config{DryRun: true})
	_, cerr := c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: testReceiver, Report: validReport()})
	require.NotNil(t, cerr)
	assert.Contains(t, cerr.Error(), "ReportProcessed")
}

func TestWriteReport_InputValidation(t *testing.T) {
	t.Parallel()
	c := newFake(t, &fakeRPC{}, Config{DryRun: true})
	_, cerr := c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: testReceiver})
	require.NotNil(t, cerr)
	_, cerr = c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: "GABC", Report: validReport()})
	require.NotNil(t, cerr)
	assert.Contains(t, cerr.Error(), "invalid receiver")
}

func TestWriteReport_RPCError(t *testing.T) {
	t.Parallel()
	c := newFake(t, &fakeRPC{simErr: errors.New("connection refused")}, Config{DryRun: true})
	_, cerr := c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: testReceiver, Report: validReport()})
	require.NotNil(t, cerr)
	assert.Contains(t, cerr.Error(), "connection refused")
}

// --- broadcast writes ---

func confirmedTx(t *testing.T, success bool) protocol.GetTransactionResponse {
	status := protocol.TransactionStatusSuccess
	if !success {
		status = protocol.TransactionStatusFailed
	}
	return protocol.GetTransactionResponse{
		LedgerCloseTime: 1_700_000_000,
		TransactionDetails: protocol.TransactionDetails{
			Status:    status,
			Ledger:    77,
			ResultXDR: txResultXDR(t, 54321, success),
			Events: protocol.Events{ContractEventsXDR: [][]string{{
				b64(t, contractEvent(t, testReceiver, "FeedUpdated", boolVal(true))),
				b64(t, contractEvent(t, testForwarder, reportProcessedTopic, boolVal(true))),
			}}},
		},
	}
}

func TestWriteReport_Broadcast_SignsSubmitsAndConfirms(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	rpc := &fakeRPC{
		simResp:    reportProcessedSim(t, true),
		accountSeq: 100,
		sendResp:   protocol.SendTransactionResponse{Status: "PENDING"},
		getTxResps: []protocol.GetTransactionResponse{{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound}}, confirmedTx(t, true)},
	}
	c := newFake(t, rpc, Config{Transmitter: kp})

	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_SUCCESS, r.TxStatus)
	assert.Equal(t, stellarcap.ReceiverContractExecutionStatus_RECEIVER_CONTRACT_EXECUTION_STATUS_SUCCESS, *r.ReceiverContractExecutionStatus)
	assert.Equal(t, uint32(77), *r.LedgerSequence)
	assert.Equal(t, uint64(54321), *r.TransactionFee)
	assert.Equal(t, uint64(1_700_000_000_000_000), *r.BlockTimestamp)
	assert.Equal(t, 2, rpc.getTxCalls, "polls until the transaction is found")

	require.Len(t, rpc.simReqs, 1)
	assert.Empty(t, rpc.simReqs[0].AuthMode, "broadcast preflight uses the RPC default auth mode")
	require.Len(t, rpc.sendReqs, 1)

	env, ic := invocation(t, rpc.sendReqs[0].Transaction)
	assert.Equal(t, xdr.ScSymbol("report"), ic.FunctionName)
	assert.Equal(t, int64(101), env.SeqNum(), "uses the next sequence number")
	assert.Equal(t, kp.Address(), ic.Args[0].Address.AccountId.Address())

	// Soroban data from the simulation is attached, with resource-fee headroom.
	require.NotNil(t, env.V1.Tx.Ext.SorobanData)
	assert.Equal(t, xdr.Int64(1000/resourceFeeHeadroomDivisor), env.V1.Tx.Ext.SorobanData.ResourceFee)

	// Signed by the transmitter.
	require.Len(t, env.Signatures(), 1)
	gtx, err := txnbuild.TransactionFromXDR(rpc.sendReqs[0].Transaction)
	require.NoError(t, err)
	tx, ok := gtx.Transaction()
	require.True(t, ok)
	hash, err := tx.Hash(network.TestNetworkPassphrase)
	require.NoError(t, err)
	require.NoError(t, kp.Verify(hash[:], env.Signatures()[0].Signature))
	assert.Equal(t, kp.Hint(), [4]byte(tx.Signatures()[0].Hint))
}

func TestWriteReport_Broadcast_PreflightRejectNotSubmitted(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{simResp: protocol.SimulateTransactionResponse{Error: "HostError: Error(Contract, #2)"}, accountSeq: 1}
	c := newFake(t, rpc, Config{Transmitter: keypair.MustRandom()})
	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_REVERTED, r.TxStatus)
	assert.Contains(t, *r.ErrorMessage, "preflight")
	assert.Empty(t, rpc.sendReqs)
}

func TestWriteReport_Broadcast_TransactionFailed(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{
		simResp:    reportProcessedSim(t, true),
		accountSeq: 1,
		sendResp:   protocol.SendTransactionResponse{Status: "PENDING"},
		getTxResps: []protocol.GetTransactionResponse{confirmedTx(t, false)},
	}
	c := newFake(t, rpc, Config{Transmitter: keypair.MustRandom()})
	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_REVERTED, r.TxStatus)
	assert.Contains(t, *r.ErrorMessage, "TxFailed")
	assert.Equal(t, uint64(54321), *r.TransactionFee)
}

func TestWriteReport_Broadcast_SubmissionRejected(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{
		simResp:    reportProcessedSim(t, true),
		accountSeq: 1,
		sendResp:   protocol.SendTransactionResponse{Status: "ERROR", ErrorResultXDR: txResultXDR(t, 100, false)},
	}
	c := newFake(t, rpc, Config{Transmitter: keypair.MustRandom()})
	r := write(t, c)
	assert.Equal(t, stellarcap.TxStatus_TX_STATUS_FATAL, r.TxStatus)
	assert.Contains(t, *r.ErrorMessage, "ERROR")
	assert.Zero(t, rpc.getTxCalls)
}

func TestWriteReport_Broadcast_UnfundedTransmitter(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{loadErr: errors.New("account not found")}
	c := newFake(t, rpc, Config{Transmitter: keypair.MustRandom()})
	_, cerr := c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: testReceiver, Report: validReport()})
	require.NotNil(t, cerr)
	assert.Contains(t, cerr.Error(), "funded")
}

func TestWriteReport_Broadcast_Timeout(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{
		simResp:    reportProcessedSim(t, true),
		accountSeq: 1,
		sendResp:   protocol.SendTransactionResponse{Status: "PENDING"},
		getTxResps: []protocol.GetTransactionResponse{{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound}}},
	}
	c := newFake(t, rpc, Config{Transmitter: keypair.MustRandom(), TxTimeout: 30 * time.Millisecond})
	_, cerr := c.WriteReport(context.Background(), commonCap.RequestMetadata{}, &stellarcap.WriteReportRequest{ContractId: testReceiver, Report: validReport()})
	require.NotNil(t, cerr)
	assert.Contains(t, cerr.Error(), "timed out")
}

func TestConfirmedContractEvents_FallsBackToMeta(t *testing.T) {
	t.Parallel()
	ev := contractEvent(t, testForwarder, reportProcessedTopic, boolVal(true))
	meta := xdr.TransactionMeta{V: 4, V4: &xdr.TransactionMetaV4{Operations: []xdr.OperationMetaV2{{Events: []xdr.ContractEvent{ev}}}}}
	got := confirmedContractEvents(protocol.GetTransactionResponse{TransactionDetails: protocol.TransactionDetails{ResultMetaXDR: b64(t, meta)}})
	require.Len(t, got, 1)
	assert.Equal(t, reportProcessedTopic, eventName(got[0]))
}

// --- reads ---

func TestReadContract(t *testing.T) {
	t.Parallel()
	ret := "AAAAAQ=="
	rpc := &fakeRPC{simResp: protocol.SimulateTransactionResponse{
		LatestLedger: 9,
		Results:      []protocol.SimulateHostFunctionResult{{ReturnValueXDR: &ret}},
	}}
	c := newFake(t, rpc, Config{DryRun: true})

	feed := make([]byte, 32)
	feed[0] = 1
	resp, cerr := c.ReadContract(context.Background(), commonCap.RequestMetadata{}, &stellarcap.ReadContractRequest{
		ContractId: testReceiver,
		Function:   "latest_round",
		Args:       []*scval.ScVal{{Value: &scval.ScVal_Vec{Vec: &scval.ScVec{Values: []*scval.ScVal{{Value: &scval.ScVal_BytesVal{BytesVal: feed}}}}}}},
	})
	require.Nil(t, cerr)
	assert.Equal(t, ret, resp.Response.Result)
	assert.Equal(t, uint32(9), resp.Response.LedgerSequence)
	assert.Empty(t, resp.Response.Error)

	env, ic := invocation(t, rpc.simReqs[0].Transaction)
	src := env.SourceAccount().ToAccountId()
	assert.Equal(t, placeholderAccount, src.Address())
	assert.Equal(t, xdr.ScSymbol("latest_round"), ic.FunctionName)
	vec := **ic.Args[0].Vec
	assert.Equal(t, feed, []byte(*vec[0].Bytes))
}

func TestReadContract_Errors(t *testing.T) {
	t.Parallel()
	c := newFake(t, &fakeRPC{simResp: protocol.SimulateTransactionResponse{Error: "HostError: missing"}}, Config{DryRun: true})
	resp, cerr := c.ReadContract(context.Background(), commonCap.RequestMetadata{}, &stellarcap.ReadContractRequest{ContractId: testReceiver, Function: "f"})
	require.Nil(t, cerr)
	assert.Equal(t, "HostError: missing", resp.Response.Error)

	_, cerr = c.ReadContract(context.Background(), commonCap.RequestMetadata{}, &stellarcap.ReadContractRequest{ContractId: testReceiver, Function: "f", SourceAccount: "nope"})
	require.NotNil(t, cerr)
	assert.Contains(t, cerr.Error(), "source account")
}

func TestGetLatestLedger(t *testing.T) {
	t.Parallel()
	rpc := &fakeRPC{ledger: protocol.GetLatestLedgerResponse{Hash: "0a0b", Sequence: 5, LedgerHeader: "AQI="}}
	c := newFake(t, rpc, Config{DryRun: true})
	resp, cerr := c.GetLatestLedger(context.Background(), commonCap.RequestMetadata{}, nil)
	require.Nil(t, cerr)
	assert.Equal(t, []byte{0x0a, 0x0b}, resp.Response.Hash)
	assert.Equal(t, uint32(5), resp.Response.Sequence)
	assert.Equal(t, []byte{1, 2}, resp.Response.LedgerHeaderXdr)
}

func TestPlaceholderAccountIsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, strkey.IsValidEd25519PublicKey(placeholderAccount))
}
