package router_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestGetOnramp_operation(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("router", "get-onramp-op-test")
	onRampID := stellarutil.MustGenerateMockContractID("onramp", "get-onramp-op-test")
	input := router.GetOnrampInput{ContractID: contractID, DestChainSelector: 42}

	t.Run("not routed returns nil output", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_onramp",
				operationstest.ContractErrorCode(stellarops.UnsupportedDestinationChainCode))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), router.GetOnramp, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Nil(t, report.Output)
	})

	t.Run("routed returns the onramp address", func(t *testing.T) {
		t.Parallel()
		val := scval.AddressToScVal(onRampID)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_onramp", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), router.GetOnramp, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.NotNil(t, report.Output)
		require.Equal(t, onRampID, *report.Output)
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_onramp",
				operationstest.ContractErrorCode(1))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), router.GetOnramp, stellardeps.StellarDeps{Invoker: inv}, input)
		require.ErrorContains(t, err, "Error(Contract, #1)")
		require.Nil(t, report.Output)
	})
}

func TestGetOfframps_operation(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("router", "get-offramps-op-test")
	offRampID := stellarutil.MustGenerateMockContractID("offramp", "get-offramps-op-test")
	input := router.GetOfframpsInput{ContractID: contractID}

	t.Run("empty table returns an empty slice", func(t *testing.T) {
		t.Parallel()
		val := scval.StructSliceToScVal([]routerbindings.OffRampEntry{})
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_offramps", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), router.GetOfframps, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Empty(t, report.Output)
	})

	t.Run("table returns the entries", func(t *testing.T) {
		t.Parallel()
		want := []routerbindings.OffRampEntry{
			{Offramp: offRampID, SourceChainSelector: 42},
			{Offramp: offRampID, SourceChainSelector: 43},
		}
		val := scval.StructSliceToScVal(want)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_offramps", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), router.GetOfframps, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Equal(t, want, report.Output)
	})
}
