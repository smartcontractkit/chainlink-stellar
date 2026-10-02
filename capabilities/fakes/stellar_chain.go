// Package fakes provides an in-process implementation of the Stellar chain
// capability for cre-cli's `cre workflow simulate`. Stellar counterpart to
// chainlink-solana/contracts/capabilities/fakes. See README.md.
package fakes

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	commonCap "github.com/smartcontractkit/chainlink-common/pkg/capabilities"
	caperrors "github.com/smartcontractkit/chainlink-common/pkg/capabilities/errors"
	stellarcap "github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar"
	stellarserver "github.com/smartcontractkit/chainlink-common/pkg/capabilities/v2/chain-capabilities/stellar/server"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"
)

const (
	defaultTxTimeout    = 60 * time.Second
	defaultPollInterval = time.Second
	simulateTimeBound   = 5 * time.Minute
)

// RPCClient is the subset of the Stellar RPC client the fake needs.
// *rpcclient.Client satisfies it.
type RPCClient interface {
	GetLatestLedger(ctx context.Context) (protocol.GetLatestLedgerResponse, error)
	SimulateTransaction(ctx context.Context, request protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, request protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, request protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error)
	LoadAccount(ctx context.Context, address string) (txnbuild.Account, error)
}

// Config configures a FakeStellarChain.
type Config struct {
	ChainSelector     uint64
	NetworkPassphrase string
	// ForwarderID is the C… address of a deployed mock forwarder
	// (contracts/cre/mock_forwarder). Reports are delivered through its
	// report() entrypoint, which skips DON signature verification.
	ForwarderID string
	// Transmitter signs and pays for writes. Required unless DryRun. In dry-run
	// mode a nil Transmitter is replaced by a placeholder account.
	Transmitter *keypair.Full
	// DryRun simulates writes with simulateTransaction instead of submitting.
	DryRun bool
	// TxTimeout bounds a broadcast write from submission to confirmation.
	TxTimeout time.Duration
	// PollInterval is the getTransaction polling interval while confirming.
	PollInterval time.Duration
}

// FakeStellarChain implements the Stellar chain capability for simulation.
type FakeStellarChain struct {
	lggr      logger.Logger
	client    RPCClient
	cfg       Config
	forwarder xdr.ScAddress
	// writeMu serializes broadcast writes so concurrent reports from one
	// execution never race on the transmitter's sequence number.
	writeMu sync.Mutex
}

var _ stellarserver.ClientCapability = (*FakeStellarChain)(nil)

// NewFakeStellarChain validates cfg and returns a fake ready to register with
// stellarserver.NewClientServer.
func NewFakeStellarChain(lggr logger.Logger, client RPCClient, cfg Config) (*FakeStellarChain, error) {
	if client == nil {
		return nil, errors.New("rpc client is required")
	}
	if cfg.NetworkPassphrase == "" {
		return nil, errors.New("network passphrase is required")
	}
	forwarder, err := contractAddress(cfg.ForwarderID)
	if err != nil {
		return nil, fmt.Errorf("forwarder: %w", err)
	}
	if !cfg.DryRun && cfg.Transmitter == nil {
		return nil, errors.New("a transmitter key is required unless DryRun is set")
	}
	if cfg.TxTimeout <= 0 {
		cfg.TxTimeout = defaultTxTimeout
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &FakeStellarChain{
		lggr:      logger.Named(lggr, "FakeStellarChain"),
		client:    client,
		cfg:       cfg,
		forwarder: forwarder,
	}, nil
}

func (c *FakeStellarChain) GetLatestLedger(ctx context.Context, _ commonCap.RequestMetadata, _ *stellarcap.GetLatestLedgerRequest) (*commonCap.ResponseAndMetadata[*stellarcap.GetLatestLedgerResponse], caperrors.Error) {
	res, err := c.client.GetLatestLedger(ctx)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("stellar getLatestLedger failed: %w", err), caperrors.Unavailable)
	}
	hashBytes, err := hex.DecodeString(res.Hash)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("invalid ledger hash %q: %w", res.Hash, err), caperrors.Internal)
	}
	header, err := decodeOptionalBase64(res.LedgerHeader)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("invalid ledger header XDR: %w", err), caperrors.Internal)
	}
	meta, err := decodeOptionalBase64(res.LedgerMetadata)
	if err != nil {
		return nil, caperrors.NewPublicSystemError(fmt.Errorf("invalid ledger metadata XDR: %w", err), caperrors.Internal)
	}
	return &commonCap.ResponseAndMetadata[*stellarcap.GetLatestLedgerResponse]{
		Response: &stellarcap.GetLatestLedgerResponse{
			Hash:              hashBytes,
			ProtocolVersion:   res.ProtocolVersion,
			Sequence:          res.Sequence,
			LedgerCloseTime:   res.LedgerCloseTime,
			LedgerHeaderXdr:   header,
			LedgerMetadataXdr: meta,
		},
	}, nil
}

func (c *FakeStellarChain) ReadContract(ctx context.Context, _ commonCap.RequestMetadata, input *stellarcap.ReadContractRequest) (*commonCap.ResponseAndMetadata[*stellarcap.ReadContractResponse], caperrors.Error) {
	if input == nil {
		return nil, caperrors.NewPublicUserError(errors.New("readContract request is nil"), caperrors.InvalidArgument)
	}
	contract, err := contractAddress(input.ContractId)
	if err != nil {
		return nil, caperrors.NewPublicUserError(err, caperrors.InvalidArgument)
	}
	if input.Function == "" {
		return nil, caperrors.NewPublicUserError(errors.New("readContract function is required"), caperrors.InvalidArgument)
	}
	args, err := scValsToXDR(input.Args)
	if err != nil {
		return nil, caperrors.NewPublicUserError(fmt.Errorf("invalid readContract args: %w", err), caperrors.InvalidArgument)
	}
	source := input.SourceAccount
	if source == "" {
		source = placeholderAccount
	} else if !strkey.IsValidEd25519PublicKey(source) {
		return nil, caperrors.NewPublicUserError(fmt.Errorf("invalid source account %q", source), caperrors.InvalidArgument)
	}

	tx, cerr := c.buildInvokeTx(&txnbuild.SimpleAccount{AccountID: source}, contract, input.Function, args, time.Now().Add(simulateTimeBound))
	if cerr != nil {
		return nil, cerr
	}
	sim, cerr := c.simulate(ctx, tx, "")
	if cerr != nil {
		return nil, cerr
	}

	resp := &stellarcap.ReadContractResponse{LedgerSequence: sim.LatestLedger, Error: simulationError(sim)}
	if resp.Error == "" {
		if len(sim.Results) != 1 || sim.Results[0].ReturnValueXDR == nil {
			resp.Error = fmt.Sprintf("simulation returned %d results without a return value", len(sim.Results))
		} else {
			resp.Result = *sim.Results[0].ReturnValueXDR
		}
	}
	return &commonCap.ResponseAndMetadata[*stellarcap.ReadContractResponse]{Response: resp}, nil
}

// placeholderAccount is the all-zero G… account; simulation does not require
// the source account to exist.
var placeholderAccount = func() string {
	s, err := strkey.Encode(strkey.VersionByteAccountID, make([]byte, 32))
	if err != nil {
		panic(err)
	}
	return s
}()

// buildInvokeTx builds an unsigned single-operation InvokeHostFunction
// transaction. The account's sequence is incremented by txnbuild, so callers
// must pass a fresh SimpleAccount per build.
func (c *FakeStellarChain) buildInvokeTx(source *txnbuild.SimpleAccount, contract xdr.ScAddress, function string, args []xdr.ScVal, deadline time.Time) (*txnbuild.Transaction, caperrors.Error) {
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        source,
		IncrementSequenceNum: true,
		Operations: []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: contract,
					FunctionName:    xdr.ScSymbol(function),
					Args:            args,
				},
			},
			SourceAccount: source.AccountID,
		}},
		BaseFee:       txnbuild.MinBaseFee,
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimebounds(0, deadline.Unix())},
	})
	if err != nil {
		return nil, caperrors.NewPublicUserError(fmt.Errorf("failed to build Stellar transaction: %w", err), caperrors.InvalidArgument)
	}
	return tx, nil
}

// simulate runs tx through simulateTransaction. Host-function failures are in
// the response (see simulationError); only transport/encoding failures error.
func (c *FakeStellarChain) simulate(ctx context.Context, tx *txnbuild.Transaction, authMode string) (protocol.SimulateTransactionResponse, caperrors.Error) {
	txXDR, err := tx.Base64()
	if err != nil {
		return protocol.SimulateTransactionResponse{}, caperrors.NewPublicSystemError(fmt.Errorf("failed to encode Stellar transaction: %w", err), caperrors.Internal)
	}
	sim, err := c.client.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{
		Transaction: txXDR,
		AuthMode:    authMode,
		Format:      protocol.FormatBase64,
	})
	if err != nil {
		return protocol.SimulateTransactionResponse{}, caperrors.NewPublicSystemError(fmt.Errorf("stellar simulateTransaction failed: %w", err), caperrors.Unavailable)
	}
	return sim, nil
}

// simulationError returns the reason a simulation cannot succeed as-is, or "".
func simulationError(sim protocol.SimulateTransactionResponse) string {
	if sim.Error != "" {
		return sim.Error
	}
	if sim.RestorePreamble != nil {
		return "contract state is archived and must be restored before this call can succeed"
	}
	return ""
}

func decodeOptionalBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func (c *FakeStellarChain) ChainSelector() uint64          { return c.cfg.ChainSelector }
func (c *FakeStellarChain) Start(context.Context) error    { return nil }
func (c *FakeStellarChain) Close() error                   { return nil }
func (c *FakeStellarChain) HealthReport() map[string]error { return map[string]error{c.Name(): nil} }
func (c *FakeStellarChain) Name() string {
	return fmt.Sprintf("FakeStellarChain-%d", c.cfg.ChainSelector)
}
func (c *FakeStellarChain) Description() string {
	return "Fake Stellar chain capability for workflow simulation"
}
func (c *FakeStellarChain) Ready() error { return nil }
func (c *FakeStellarChain) Initialise(context.Context, core.StandardCapabilitiesDependencies) error {
	return nil
}
