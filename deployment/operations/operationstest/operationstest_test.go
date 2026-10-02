package operationstest

import (
	"context"
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func scvalPtr(b byte) *xdr.ScVal {
	u32 := xdr.Uint32(b)
	v := xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u32}
	return &v
}

// TestSimulateContractPrecedence pins the stub lookup order and the
// last-writer-wins semantics the lane-sequence tests rely on: a test can stub
// not-configured errors wholesale and then flip individual reads to configured
// results (or back) by setting the same (contract, fn) pair again.
func TestSimulateContractPrecedence(t *testing.T) {
	ctx := context.Background()
	contractA, contractB := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAS2I", "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAKSI"
	pairErr := errors.New("pair error")
	fnErr := errors.New("fn error")

	t.Run("pair error beats fn error", func(t *testing.T) {
		inv := NewRecordingInvoker().
			WithSimulateErrorForFn("get_dest_chain_config", fnErr).
			WithSimulateErrorForContract(contractA, "get_dest_chain_config", pairErr)
		_, err := inv.SimulateContract(ctx, contractA, "get_dest_chain_config", nil)
		require.ErrorIs(t, err, pairErr)
	})

	t.Run("pair error beats pair result on another contract", func(t *testing.T) {
		inv := NewRecordingInvoker().
			WithSimulateResultForContract(contractA, "get_onramp", scvalPtr(1)).
			WithSimulateErrorForContract(contractB, "get_onramp", pairErr)
		_, err := inv.SimulateContract(ctx, contractB, "get_onramp", nil)
		require.ErrorIs(t, err, pairErr)
		// The other contract still sees its result.
		v, err := inv.SimulateContract(ctx, contractA, "get_onramp", nil)
		require.NoError(t, err)
		require.Equal(t, *scvalPtr(1), *v)
	})

	t.Run("result then error on the same pair is last writer wins", func(t *testing.T) {
		inv := NewRecordingInvoker().
			WithSimulateResultForContract(contractA, "get_dest_chain_config", scvalPtr(1)).
			WithSimulateErrorForContract(contractA, "get_dest_chain_config", pairErr)
		_, err := inv.SimulateContract(ctx, contractA, "get_dest_chain_config", nil)
		require.ErrorIs(t, err, pairErr)
	})

	t.Run("error then result on the same pair is last writer wins", func(t *testing.T) {
		inv := NewRecordingInvoker().
			WithSimulateErrorForContract(contractA, "get_dest_chain_config", pairErr).
			WithSimulateResultForContract(contractA, "get_dest_chain_config", scvalPtr(2))
		v, err := inv.SimulateContract(ctx, contractA, "get_dest_chain_config", nil)
		require.NoError(t, err)
		require.Equal(t, *scvalPtr(2), *v)
	})

	t.Run("fn result beats global result", func(t *testing.T) {
		inv := NewRecordingInvoker().
			WithSimulateResult(scvalPtr(1)).
			WithSimulateResultForFn("owner", scvalPtr(2))
		v, err := inv.SimulateContract(ctx, contractA, "owner", nil)
		require.NoError(t, err)
		require.Equal(t, *scvalPtr(2), *v)
		// Another fn falls through to the global result.
		v, err = inv.SimulateContract(ctx, contractA, "get_onramp", nil)
		require.NoError(t, err)
		require.Equal(t, *scvalPtr(1), *v)
	})

	t.Run("fn error beats pair result", func(t *testing.T) {
		// Documented precedence: errByPair → errByFn → simulateByPair →
		// simulateByFn → global. A fn-level error outlives a pair-level
		// result set before it.
		inv := NewRecordingInvoker().
			WithSimulateResultForContract(contractA, "get_dest_chain_config", scvalPtr(1)).
			WithSimulateErrorForFn("get_dest_chain_config", fnErr)
		_, err := inv.SimulateContract(ctx, contractA, "get_dest_chain_config", nil)
		require.ErrorIs(t, err, fnErr)
	})

	t.Run("calls are recorded", func(t *testing.T) {
		inv := NewRecordingInvoker()
		_, _ = inv.SimulateContract(ctx, contractA, "get_onramp", nil)
		_, _ = inv.InvokeContract(ctx, contractB, "apply", nil)
		recs := inv.Records()
		require.Len(t, recs, 2)
		require.Equal(t, contractA, recs[0].ContractID)
		require.Equal(t, "get_onramp", recs[0].Fn)
		last := inv.Last()
		require.Equal(t, contractB, last.ContractID)
		require.Equal(t, "apply", last.Fn)
	})
}
