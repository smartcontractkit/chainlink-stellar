package offramp_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestGetSourceChainConfig_operation(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("offramp", "get-source-chain-config-op-test")
	routerID := stellarutil.MustGenerateMockContractID("router", "get-source-chain-config-op-test")
	cvID := stellarutil.MustGenerateMockContractID("cv", "get-source-chain-config-op-test")
	remoteOnRamp := []byte{0x01, 0x02, 0x03, 0x04}
	input := offramp.GetSourceChainConfigInput{ContractID: contractID, SourceChainSelector: 42}

	t.Run("not configured returns nil output", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_source_chain_config",
				operationstest.ContractErrorCode(stellarops.SourceChainNotEnabledCode))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), offramp.GetSourceChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Nil(t, report.Output)
	})

	t.Run("configured returns the on-chain config", func(t *testing.T) {
		t.Parallel()
		want := offrampbindings.SourceChainConfig{
			DefaultCcvs:      []string{cvID},
			IsEnabled:        true,
			LaneMandatedCcvs: []string{},
			OnRamps:          [][]byte{remoteOnRamp},
			Router:           routerID,
		}
		val, err := want.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_source_chain_config", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), offramp.GetSourceChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.NotNil(t, report.Output)
		require.Equal(t, want, *report.Output)
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_source_chain_config",
				operationstest.ContractErrorCode(1))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), offramp.GetSourceChainConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.ErrorContains(t, err, "Error(Contract, #1)")
		require.Nil(t, report.Output)
	})
}
