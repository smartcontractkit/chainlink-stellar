package fee_quoter_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestGetDestChainConfig_operation(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("fee-quoter", "get-dest-chain-config-op-test")
	input := fee_quoter.GetDestChainConfigInput{ContractID: contractID, DestChainSelector: 42}

	t.Run("not configured returns nil output", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_dest_chain_config",
				operationstest.ContractErrorCode(stellarops.DestinationChainNotEnabledCode))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), fee_quoter.GetDestChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Nil(t, report.Output)
	})

	t.Run("configured returns the on-chain config", func(t *testing.T) {
		t.Parallel()
		want := fqbindings.DestChainConfig{
			IsEnabled:             true,
			MaxDataBytes:          50_000,
			MaxPerMsgGasLimit:     4_000_000,
			DestGasOverhead:       350_000,
			DestGasPerPayloadByte: 16,
			DefaultTokenFeeUsd:    50,
			DefaultTokenDestGas:   50_000,
			DefaultTxGasLimit:     200_000,
			NetworkFeeUsdCents:    100,
			LinkPremiumPercent:    90,
		}
		val, err := want.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_dest_chain_config", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), fee_quoter.GetDestChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.NotNil(t, report.Output)
		require.Equal(t, want, *report.Output)
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_dest_chain_config",
				operationstest.ContractErrorCode(1))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), fee_quoter.GetDestChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.ErrorContains(t, err, "Error(Contract, #1)")
		require.Nil(t, report.Output)
	})
}
