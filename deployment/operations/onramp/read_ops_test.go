package onramp_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestGetDestChainConfig_operation(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("onramp", "get-dest-chain-config-op-test")
	routerID := stellarutil.MustGenerateMockContractID("router", "get-dest-chain-config-op-test")
	executorID := stellarutil.MustGenerateMockContractID("executor", "get-dest-chain-config-op-test")
	cvID := stellarutil.MustGenerateMockContractID("cv", "get-dest-chain-config-op-test")
	remoteOffRamp := []byte{0x01, 0x02, 0x03, 0x04}
	input := onramp.GetDestChainConfigInput{ContractID: contractID, DestChainSelector: 42}

	t.Run("not configured returns nil output", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_dest_chain_config",
				operationstest.ContractErrorCode(stellarops.DestinationChainNotSupportedCode))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), onramp.GetDestChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Nil(t, report.Output)
	})

	t.Run("configured returns the on-chain config", func(t *testing.T) {
		t.Parallel()
		want := onrampbindings.DestChainConfig{
			AddressBytesLength:        20,
			BaseExecutionGasCost:      175_000,
			DefaultCcvs:               []string{cvID},
			DefaultExecutor:           executorID,
			ExecutionFeeUsdCents:      5,
			LaneMandatedCcvs:          []string{},
			MessageNetworkFeeUsdCents: 10,
			OffRamp:                   remoteOffRamp,
			Router:                    routerID,
			TokenNetworkFeeUsdCents:   25,
			TokenReceiverAllowed:      false,
		}
		val, err := want.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_dest_chain_config", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), onramp.GetDestChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.NotNil(t, report.Output)
		// MessageNumber is pool state, not lane config; compare the config fields.
		want.MessageNumber = report.Output.MessageNumber
		require.Equal(t, want, *report.Output)
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_dest_chain_config",
				operationstest.ContractErrorCode(1))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), onramp.GetDestChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.ErrorContains(t, err, "Error(Contract, #1)")
		require.Nil(t, report.Output)
	})
}
