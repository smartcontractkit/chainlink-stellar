package chainwriter

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	stellartypes "github.com/smartcontractkit/chainlink-common/pkg/types/chains/stellar"
	"github.com/smartcontractkit/chainlink-common/pkg/types/core"
	"github.com/smartcontractkit/chainlink-common/pkg/utils"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/relayer/txm"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type stellarChainWriter struct {
	logger   logger.Logger
	txm      txm.TxManager
	keystore core.Keystore
	config   ChainWriterConfig

	rrSelector TransmitterSelector

	starter utils.StartStopOnce
}

var _ commontypes.ContractWriter = (*stellarChainWriter)(nil)

func NewChainWriter(lgr logger.Logger, txm txm.TxManager, keystore core.Keystore, config ChainWriterConfig) (commontypes.ContractWriter, error) {
	return &stellarChainWriter{
		logger:   logger.Named(lgr, "StellarChainWriter"),
		txm:      txm,
		keystore: keystore,
		config:   config,
	}, nil
}

func (s *stellarChainWriter) Name() string {
	return s.logger.Name()
}

func (s *stellarChainWriter) Ready() error {
	return s.starter.Ready()
}

func (s *stellarChainWriter) HealthReport() map[string]error {
	return map[string]error{s.Name(): s.starter.Healthy()}
}

func (s *stellarChainWriter) Start(ctx context.Context) error {
	return s.starter.StartOnce(s.Name(), func() error {
		selector, err := NewRoundRobinSelector(ctx, s.keystore)
		if err != nil {
			s.logger.Warnw("failed to initialize round-robin transmitter selection (fallback may fail)", "err", err)
		} else {
			s.rrSelector = selector
		}
		return nil
	})
}

func (s *stellarChainWriter) Close() error {
	return s.starter.StopOnce(s.Name(), func() error {
		return nil
	})
}

func (s *stellarChainWriter) SubmitTransaction(ctx context.Context, contractName, method string, args any, transactionID string, toAddress string, meta *commontypes.TxMeta, value *big.Int) error {
	if value != nil && value.Sign() != 0 {
		return fmt.Errorf("value is not supported")
	}

	contractConfig, ok := s.config.Contracts[contractName]
	if !ok {
		return fmt.Errorf("no such contract: %s", contractName)
	}

	funcConfig, ok := contractConfig.Functions[method]
	if !ok {
		return fmt.Errorf("no such method: %s", method)
	}

	// 1. Encode `args` into Soroban `txnbuild.InvokeHostFunction` operations
	var stellarArgs []stellartypes.ScVal
	switch v := args.(type) {
	case []stellartypes.ScVal:
		stellarArgs = v
	default:
		return fmt.Errorf("unsupported args type: expected []stellartypes.ScVal, got %T", args)
	}

	var xdrArgs []xdr.ScVal
	for _, arg := range stellarArgs {
		xdrArg, err := toXDRScVal(arg)
		if err != nil {
			return fmt.Errorf("failed to convert argument to XDR ScVal: %w", err)
		}
		xdrArgs = append(xdrArgs, xdrArg)
	}

	contractID := toAddress
	if contractID == "" {
		return fmt.Errorf("toAddress (contract ID) is required")
	}

	contractBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return fmt.Errorf("invalid contract ID %q: %w", contractID, err)
	}
	contractAddr := scval.BuildContractScAddress(contractBytes)
	if contractAddr == nil {
		return fmt.Errorf("failed to build contract address for %q", contractID)
	}

	fromAddress := funcConfig.FromAddress
	if fromAddress == "" {
		if s.rrSelector == nil {
			return fmt.Errorf("no FromAddress specified in config and round-robin selector is unavailable")
		}
		var err error
		fromAddress, err = s.rrSelector.Next()
		if err != nil {
			return fmt.Errorf("failed to select transmitter: %w", err)
		}
	}
	
	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: *contractAddr,
				FunctionName:    xdr.ScSymbol(method),
				Args:            xdrArgs,
			},
		},
		SourceAccount: fromAddress,
	}

	req := txm.TxRequest{
		ID:          transactionID,
		FromAddress: fromAddress,
		Operations:  []txnbuild.Operation{op},
		Metadata:    meta,
	}

	// 2. Simulate to check viability before enqueueing
	simResult, err := s.txm.Simulate(ctx, req)
	if err != nil {
		return fmt.Errorf("simulation failed: %w", err)
	}
	if simResult.RestorePreamble != nil {
		s.logger.Warnw("restore required before simulation can be completed, enqueueing transaction anyway for TXM to handle", "contract", contractID, "method", method)
	} else if simResult.Error != "" {
		return fmt.Errorf("simulation returned error: %s", simResult.Error)
	}

	// 3. Enqueue
	_, err = s.txm.Enqueue(ctx, req)
	if err != nil {
		s.logger.Errorw("failed to enqueue transaction", "contractName", contractName, "method", method, "toAddress", toAddress, "error", err)
		return fmt.Errorf("failed to enqueue transaction %s: %w", transactionID, err)
	}

	s.logger.Infow("submitted transaction for execution", "contractName", contractName, "method", method, "toAddress", toAddress)
	return nil
}

func (s *stellarChainWriter) GetTransactionStatus(ctx context.Context, transactionID string) (commontypes.TransactionStatus, error) {
	return s.txm.GetStatus(transactionID)
}

func (s *stellarChainWriter) GetFeeComponents(ctx context.Context) (*commontypes.ChainFeeComponents, error) {
	// Stellar TXM handles fees dynamically using feeTracker, returning empty/zero or fetch from feeTracker
	return &commontypes.ChainFeeComponents{
		ExecutionFee:        big.NewInt(0),
		DataAvailabilityFee: big.NewInt(0),
	}, nil
}

func (s *stellarChainWriter) GetEstimateFee(ctx context.Context, contract, method string, args any, toAddress string, meta *commontypes.TxMeta, val *big.Int) (commontypes.EstimateFee, error) {
	return commontypes.EstimateFee{}, errors.New("estimate fee is not implemented for stellar")
}
