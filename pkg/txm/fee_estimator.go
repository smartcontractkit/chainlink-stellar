package txm

import (
	"context"
	"fmt"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// SimulationResult holds the outputs of a successful Soroban simulation
// needed to assemble and submit the final transaction.
type SimulationResult struct {
	SorobanData *xdr.SorobanTransactionData
	Auth        []xdr.SorobanAuthorizationEntry
	MinFee      int64
	ReturnValue *xdr.ScVal
	// RestorePreamble is non-nil when persistent ledger entries need restoring.
	RestorePreamble *protocolrpc.RestorePreamble
}

// FeeEstimator wraps Soroban simulation to produce fee estimates and
// assembled transaction data. Extracted from Deployer.assembleTransaction.
type FeeEstimator struct {
	rpc       ccvclient.RPCClient
	feeBuffer int64
}

// NewFeeEstimator creates a FeeEstimator.
func NewFeeEstimator(rpc ccvclient.RPCClient, feeBuffer int64) *FeeEstimator {
	return &FeeEstimator{rpc: rpc, feeBuffer: feeBuffer}
}

// Simulate runs a preflight simulation and returns the assembled data
// required to submit the transaction.
func (fe *FeeEstimator) Simulate(ctx context.Context, tx *txnbuild.Transaction) (*SimulationResult, error) {
	txXDR, err := tx.Base64()
	if err != nil {
		return nil, fmt.Errorf("encode tx for simulation: %w", err)
	}

	sim, err := fe.rpc.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
		Transaction: txXDR,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSimulation, err)
	}
	if sim.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrSimulation, sim.Error)
	}

	result := &SimulationResult{
		MinFee:          sim.MinResourceFee,
		RestorePreamble: sim.RestorePreamble,
	}

	if sim.TransactionDataXDR != "" {
		var sorobanData xdr.SorobanTransactionData
		if err := xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &sorobanData); err != nil {
			return nil, fmt.Errorf("decode soroban data: %w", err)
		}
		result.SorobanData = &sorobanData
	}

	if len(sim.Results) > 0 {
		r := sim.Results[0]
		if r.AuthXDR != nil && len(*r.AuthXDR) > 0 {
			auth := make([]xdr.SorobanAuthorizationEntry, len(*r.AuthXDR))
			for i, authXDR := range *r.AuthXDR {
				if err := xdr.SafeUnmarshalBase64(authXDR, &auth[i]); err != nil {
					return nil, fmt.Errorf("decode auth entry %d: %w", i, err)
				}
			}
			result.Auth = auth
		}
		if r.ReturnValueXDR != nil && *r.ReturnValueXDR != "" {
			var scVal xdr.ScVal
			if err := xdr.SafeUnmarshalBase64(*r.ReturnValueXDR, &scVal); err != nil {
				return nil, fmt.Errorf("decode return value: %w", err)
			}
			result.ReturnValue = &scVal
		}
	}

	return result, nil
}

// AssembleTransaction applies simulation results to the original transaction,
// updating the InvokeHostFunction operation with SorobanData and auth entries,
// then rebuilds the transaction with the calculated fee. The caller must pass
// the original preconditions so LedgerBounds are preserved across the rebuild.
func (fe *FeeEstimator) AssembleTransaction(
	tx *txnbuild.Transaction,
	sim *SimulationResult,
	sourceAccount *txnbuild.SimpleAccount,
	preconditions txnbuild.Preconditions,
) (*txnbuild.Transaction, error) {
	ops := tx.Operations()
	if len(ops) == 0 {
		return tx, nil
	}

	if sim.SorobanData != nil {
		if ihf, ok := ops[0].(*txnbuild.InvokeHostFunction); ok {
			ihf.Ext = xdr.TransactionExt{
				V:           1,
				SorobanData: sim.SorobanData,
			}
			if len(sim.Auth) > 0 {
				ihf.Auth = sim.Auth
			}
		}
	}

	if sim.MinFee > 0 {
		return txnbuild.NewTransaction(
			txnbuild.TransactionParams{
				SourceAccount:        sourceAccount,
				IncrementSequenceNum: true,
				Operations:           ops,
				BaseFee:              sim.MinFee + fe.feeBuffer,
				Preconditions:        preconditions,
			},
		)
	}

	return tx, nil
}
