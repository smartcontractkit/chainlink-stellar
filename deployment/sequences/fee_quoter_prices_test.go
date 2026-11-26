package sequences

import (
	"context"
	"testing"

	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func timestampedPrice(t *testing.T, ts uint64) *xdr.ScVal {
	t.Helper()
	v, err := scval.BuildStructScVal(map[string]xdr.ScVal{
		"timestamp": scval.Uint64ToScVal(ts),
		"value":     scval.U128ToScVal(xdr.UInt128Parts{Lo: 1}),
	})
	require.NoError(t, err)
	return &v
}

func desiredPrices() fqbindings.PriceUpdates {
	return fqbindings.PriceUpdates{
		TokenPriceUpdates: []fqbindings.TokenPriceUpdate{{
			Token:       characterizationKeypair().Address(),
			UsdPerToken: scval.U128(xdr.UInt128Parts{Lo: 7}),
		}},
		GasPriceUpdates: []fqbindings.GasPriceUpdate{{
			DestChainSelector: 42,
			UsdPerUnitGas:     scval.U128(xdr.UInt128Parts{Lo: 9}),
		}},
	}
}

func runSetUnsetPrices(t *testing.T, tokenTS, gasTS uint64) []operationstest.InvokeRecord {
	t.Helper()
	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("get_token_price", timestampedPrice(t, tokenTS)).
		WithSimulateResultForFn("get_dest_chain_gas_price", timestampedPrice(t, gasTS))
	b := cldf_ops.NewBundle(func() context.Context { return t.Context() }, cldflogger.Nop(), cldf_ops.NewMemoryReporter())
	deps := stellardeps.StellarDeps{Invoker: inv}
	require.NoError(t, setUnsetPrices(b, deps, predictedID(t, "fee-quoter"), characterizationKeypair().Address(), desiredPrices()))
	return inv.Invocations()
}

func TestSetUnsetPrices_AllPricesSetSendsNothing(t *testing.T) {
	t.Parallel()
	// #given every price already has a timestamp on chain
	// #when the deploy reapplies its configuration
	calls := runSetUnsetPrices(t, 100, 100)
	// #then no update_prices transaction is sent
	require.Empty(t, calls)
}

func TestSetUnsetPrices_SendsOnlyUnsetPrices(t *testing.T) {
	t.Parallel()
	// #given the token price is set and the gas price is not
	// #when the deploy reapplies its configuration
	calls := runSetUnsetPrices(t, 100, 0)
	// #then one update_prices call carries only the gas price
	require.Len(t, calls, 1)
	require.Equal(t, "update_prices", calls[0].Fn)
	updates, err := fqbindings.PriceUpdatesFromScVal(calls[0].Args[1])
	require.NoError(t, err)
	require.Empty(t, updates.TokenPriceUpdates)
	require.Len(t, updates.GasPriceUpdates, 1)
	require.Equal(t, uint64(42), updates.GasPriceUpdates[0].DestChainSelector)
}
