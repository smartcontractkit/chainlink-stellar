package committee_verifier_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestGetSignatureConfig_operation(t *testing.T) {
	t.Parallel()
	contractID := stellarutil.MustGenerateMockContractID("committee-verifier", "get-signature-config-op-test")
	input := committee_verifier.GetSignatureConfigInput{ContractID: contractID, SourceChainSelector: 42}

	paddedSigner := func(hexAddr string) [32]byte {
		var padded [32]byte
		copy(padded[12:], common.HexToAddress(hexAddr).Bytes())
		return padded
	}

	t.Run("not configured returns nil output", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_signature_config",
				operationstest.ContractErrorCode(stellarops.SourceSignersNotConfiguredCode))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), committee_verifier.GetSignatureConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.Nil(t, report.Output)
	})

	t.Run("configured returns the on-chain quorum", func(t *testing.T) {
		t.Parallel()
		want := cvbindings.SignatureQuorumConfig{
			SourceChainSelector: 42,
			Threshold:           2,
			Signers: [][32]byte{
				paddedSigner("0x1111111111111111111111111111111111111111"),
				paddedSigner("0x2222222222222222222222222222222222222222"),
			},
		}
		val, err := want.ToScVal()
		require.NoError(t, err)
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(contractID, "get_signature_config", &val)
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), committee_verifier.GetSignatureConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.NoError(t, err)
		require.NotNil(t, report.Output)
		require.Equal(t, want, *report.Output)
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(contractID, "get_signature_config",
				operationstest.ContractErrorCode(1))
		report, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), committee_verifier.GetSignatureConfig, stellardeps.StellarDeps{Invoker: inv}, input)
		require.ErrorContains(t, err, "Error(Contract, #1)")
		require.Nil(t, report.Output)
	})
}
