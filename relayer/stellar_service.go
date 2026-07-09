package relayer

import (
	"context"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-framework/multinode"

	relaytypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"

	"github.com/smartcontractkit/chainlink-stellar/relayer/chain"
	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"
)

const simulateTransactionTimeBound = 1 * time.Minute

// StellarTxManager is the subset of txm.StellarTxm used by SubmitTransaction.
type StellarTxManager interface {
	EnqueueAndWait(ctx context.Context, req txm.TxRequest) (*txm.TxResult, error)
}

type stellarService struct {
	relaytypes.UnimplementedStellarService
	chain chain.Chain
	txMgr StellarTxManager
}

var _ relaytypes.StellarService = (*stellarService)(nil)

func newStellarService(ch chain.Chain) stellarService {
	return stellarService{chain: ch, txMgr: ch.TxManager()}
}

func (s *stellarService) GetSigningAccount(ctx context.Context) (stellartypes.GetSigningAccountResponse, error) {
	ks := s.chain.KeyStore()
	if ks == nil {
		return stellartypes.GetSigningAccountResponse{}, fmt.Errorf("keystore is not configured")
	}
	accounts, err := ks.Accounts(ctx)
	if err != nil {
		return stellartypes.GetSigningAccountResponse{}, fmt.Errorf("keystore accounts: %w", err)
	}
	if len(accounts) == 0 {
		return stellartypes.GetSigningAccountResponse{}, fmt.Errorf("keystore has no accounts")
	}
	return stellartypes.GetSigningAccountResponse{AccountAddress: accounts[0]}, nil
}

func (s *stellarService) GetLedgerEntries(ctx context.Context, req stellartypes.GetLedgerEntriesRequest) (stellartypes.GetLedgerEntriesResponse, error) {
	rpc, err := s.chain.GetClient(ctx)
	if err != nil {
		return stellartypes.GetLedgerEntriesResponse{}, fmt.Errorf("failed to get client: %w", err)
	}

	keys := make([]string, len(req.Keys))
	for i, k := range req.Keys {
		keys[i] = k
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
	if err != nil {
		return stellartypes.GetLedgerEntriesResponse{}, err
	}

	entries := make([]stellartypes.LedgerEntryResult, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		entry := stellartypes.LedgerEntryResult{
			KeyXDR:             e.KeyXDR,
			DataXDR:            e.DataXDR,
			LastModifiedLedger: e.LastModifiedLedger,
			ExtensionXDR:       e.ExtensionXDR,
		}
		if e.LiveUntilLedgerSeq != nil {
			v := *e.LiveUntilLedgerSeq
			entry.LiveUntilLedgerSeq = &v
		}
		entries = append(entries, entry)
	}

	return stellartypes.GetLedgerEntriesResponse{
		Entries:      entries,
		LatestLedger: resp.LatestLedger,
	}, nil
}

func (s *stellarService) GetLatestLedger(ctx context.Context) (stellartypes.GetLatestLedgerResponse, error) {
	rpc, err := s.chain.GetClient(ctx)
	if err != nil {
		return stellartypes.GetLatestLedgerResponse{}, fmt.Errorf("failed to get client: %w", err)
	}

	resp, err := rpc.GetLatestLedger(ctx)
	if err != nil {
		return stellartypes.GetLatestLedgerResponse{}, fmt.Errorf("%w: %w", multinode.ErrNodeError, err)
	}

	return stellartypes.GetLatestLedgerResponse{
		Hash:              resp.Hash,
		ProtocolVersion:   resp.ProtocolVersion,
		Sequence:          resp.Sequence,
		LedgerCloseTime:   resp.LedgerCloseTime,
		LedgerHeaderXDR:   resp.LedgerHeader,
		LedgerMetadataXDR: resp.LedgerMetadata,
	}, nil
}

// SimulateTransaction performs a Soroban InvokeHostFunction simulation.
//
// It builds an unsigned InvokeHostFunction transaction, sends it to Stellar RPC's
// simulateTransaction endpoint, and maps the simulation response into the domain
// response. Host-function failures are represented in the response Error field
// with a nil Go error.
func (s *stellarService) SimulateTransaction(ctx context.Context, req stellartypes.SimulateTransactionRequest) (stellartypes.SimulateTransactionResponse, error) {
	if req.ContractID == "" {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("contract id is required")
	}
	if req.Function == "" {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("function is required")
	}

	authMode, err := validateSimulateAuthMode(req.AuthMode)
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, err
	}

	rpc, err := s.chain.GetClient(ctx)
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to get chain client: %w", err)
	}

	contractBytes, err := strkey.Decode(strkey.VersionByteContract, req.ContractID)
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to decode contract id %q: %w", req.ContractID, err)
	}

	contractAddr := scval.BuildContractScAddress(contractBytes)
	if contractAddr == nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to build contract address from contractID: %s", req.ContractID)
	}

	args, err := convertScValsToDomain(req.Args)
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to convert args: %w", err)
	}

	hostFn := xdr.HostFunction{
		Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
		InvokeContract: &xdr.InvokeContractArgs{
			ContractAddress: *contractAddr,
			FunctionName:    xdr.ScSymbol(req.Function),
			Args:            args,
		},
	}

	srcAddr := req.SourceAccount
	if srcAddr == "" {
		srcAddr, err = strkey.Encode(strkey.VersionByteAccountID, make([]byte, 32))
		if err != nil {
			return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to build placeholder source account: %w", err)
		}
	} else if _, err = strkey.Decode(strkey.VersionByteAccountID, srcAddr); err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to decode source account %q: %w", srcAddr, err)
	}

	sourceAccount := &txnbuild.SimpleAccount{AccountID: srcAddr, Sequence: 0}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        sourceAccount,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{
			&txnbuild.InvokeHostFunction{
				HostFunction:  hostFn,
				SourceAccount: srcAddr,
			},
		},
		BaseFee: txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{
			TimeBounds: txnbuild.NewTimebounds(0, time.Now().Add(simulateTransactionTimeBound).Unix()),
		},
	})
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to build transaction: %w", err)
	}

	txXDR, err := tx.Base64()
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to encode transaction: %w", err)
	}

	simReq := protocol.SimulateTransactionRequest{
		Transaction: txXDR,
		AuthMode:    authMode,
		Format:      protocol.FormatBase64,
	}
	if req.ResourceConfig != nil {
		simReq.ResourceConfig = &protocol.ResourceConfig{
			InstructionLeeway: req.ResourceConfig.InstructionLeeway,
		}
	}

	simResult, err := rpc.SimulateTransaction(ctx, simReq)
	if err != nil {
		return stellartypes.SimulateTransactionResponse{}, fmt.Errorf("failed to simulate transaction: %w: %w", multinode.ErrNodeError, err)
	}

	resp := stellartypes.SimulateTransactionResponse{
		LedgerSequence:     simResult.LatestLedger,
		Success:            simResult.Error == "",
		Error:              simResult.Error,
		EventsXDR:          simResult.EventsXDR,
		TransactionDataXDR: simResult.TransactionDataXDR,
		MinResourceFee:     simResult.MinResourceFee,
	}

	switch len(simResult.Results) {
	case 0:
		if resp.Success && simResult.RestorePreamble == nil {
			return resp, fmt.Errorf("simulation succeeded but returned no host function results")
		}
	case 1:
		result := simResult.Results[0]
		if result.ReturnValueXDR != nil {
			resp.ReturnValueXDR = *result.ReturnValueXDR
		}
		if result.AuthXDR != nil {
			resp.RequiredAuthXDR = *result.AuthXDR
		}
	default:
		return resp, fmt.Errorf("unexpected simulation result count: %d", len(simResult.Results))
	}

	if simResult.RestorePreamble != nil {
		resp.RestorePreamble = &stellartypes.SimulateRestorePreamble{
			TransactionDataXDR: simResult.RestorePreamble.TransactionDataXDR,
			MinResourceFee:     simResult.RestorePreamble.MinResourceFee,
		}
	}

	return resp, nil
}

func (s *stellarService) GetEvents(ctx context.Context, req stellartypes.GetEventsRequest) (stellartypes.GetEventsResponse, error) {
	rpc, err := s.chain.GetClient(ctx)
	if err != nil {
		return stellartypes.GetEventsResponse{}, fmt.Errorf("failed to get chain client: %w", err)
	}

	protocolReq, err := convertGetEventsRequestsFromDomain(req)
	if err != nil {
		return stellartypes.GetEventsResponse{}, fmt.Errorf("invalid get events request: %w", err)
	}

	resp, err := rpc.GetEvents(ctx, protocolReq)
	if err != nil {
		return stellartypes.GetEventsResponse{}, fmt.Errorf("failed to get events: %w: %w", multinode.ErrNodeError, err)
	}

	return convertGetEventsResponseToDomain(resp)
}

func (s *stellarService) GetTransaction(ctx context.Context, req stellartypes.GetTransactionRequest) (stellartypes.GetTransactionResponse, error) {
	if req.TxHash == "" {
		return stellartypes.GetTransactionResponse{}, fmt.Errorf("tx hash is required")
	}

	rpc, err := s.chain.GetClient(ctx)
	if err != nil {
		return stellartypes.GetTransactionResponse{}, fmt.Errorf("failed to get client: %w", err)
	}

	resp, err := rpc.GetTransaction(ctx, protocol.GetTransactionRequest{Hash: req.TxHash})
	if err != nil {
		return stellartypes.GetTransactionResponse{}, fmt.Errorf("%w: %w", multinode.ErrNodeError, err)
	}
	if resp.Status == protocol.TransactionStatusNotFound {
		return stellartypes.GetTransactionResponse{}, fmt.Errorf("transaction not found: %s", req.TxHash)
	}

	var feeStroops uint64
	if resp.ResultXDR != "" {
		var txResult xdr.TransactionResult
		if decodeErr := xdr.SafeUnmarshalBase64(resp.ResultXDR, &txResult); decodeErr == nil {
			feeStroops = uint64(txResult.FeeCharged)
		}
	}

	return stellartypes.GetTransactionResponse{
		FeeStroops:      feeStroops,
		LedgerSequence:  resp.Ledger,
		LedgerCloseTime: resp.LedgerCloseTime,
	}, nil
}

// SubmitTransaction invokes a Soroban contract via the TXM pipeline.
// It converts the high-level domain request into an InvokeHostFunction operation,
// enqueues it through the TXM, and maps the terminal status into a response.
func (s *stellarService) SubmitTransaction(ctx context.Context, req stellartypes.SubmitTransactionRequest) (*stellartypes.SubmitTransactionResponse, error) {
	if s.txMgr == nil {
		return nil, fmt.Errorf("submit transaction: txm is not configured")
	}
	if req.ContractID == "" {
		return nil, fmt.Errorf("submit transaction: contractID is required")
	}
	if req.Function == "" {
		return nil, fmt.Errorf("submit transaction: function is required")
	}

	xdrArgs, err := convertScValsToDomain(req.Args)
	if err != nil {
		return nil, fmt.Errorf("submit transaction: convert args: %w", err)
	}

	op, err := txm.BuildInvokeContractOperation(req.ContractID, req.Function, xdrArgs, req.FromAddress)
	if err != nil {
		return nil, fmt.Errorf("submit transaction: build operation: %w", err)
	}

	result, err := s.txMgr.EnqueueAndWait(ctx, txm.TxRequest{
		ID:                 req.IdempotencyKey,
		FromAddress:        req.FromAddress,
		Operations:         []txnbuild.Operation{op},
		LedgerBoundsOffset: req.LedgerBoundsOffset,
	})
	if err != nil {
		return nil, fmt.Errorf("submit transaction: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("submit transaction: nil result returned by TXM")
	}

	reply := &stellartypes.SubmitTransactionResponse{
		TxHash:           result.Hash,
		TxIdempotencyKey: result.ID,
		ResultXDR:        result.ResultXDR,
		ResultMetaXDR:    result.ResultMetaXDR,
	}
	if result.Fee != nil && result.Fee.IsUint64() {
		fee := result.Fee.Uint64()
		reply.TransactionFee = &fee
	}
	if result.LedgerCloseTime > 0 {
		ts := uint64(result.LedgerCloseTime) * 1_000_000
		reply.BlockTimestamp = &ts
	}

	switch result.Status {
	case relaytypes.Finalized:
		reply.TxStatus = stellartypes.TxSuccess
	case relaytypes.Failed:
		reply.TxStatus = stellartypes.TxFailed
		if result.Error != nil {
			return reply, result.Error
		}
	default:
		reply.TxStatus = stellartypes.TxFatal
		if result.Error != nil {
			return reply, result.Error
		}
		return reply, fmt.Errorf("submit transaction: unexpected terminal status %v", result.Status)
	}

	return reply, nil
}
