package versioned_verifier_resolver_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

func TestUpgrade_operation(t *testing.T) {
	t.Parallel()
	inv := operationstest.NewRecordingInvoker()
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
	cid := operationstest.MockContractID
	var hash [32]byte
	hash[0] = 0xAB
	_, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), versioned_verifier_resolver.Upgrade, deps, versioned_verifier_resolver.UpgradeInput{
		ContractID:  cid,
		NewWasmHash: hash,
	})
	require.NoError(t, err)
	rec := inv.Last()
	require.Equal(t, cid, rec.ContractID)
	require.Equal(t, "upgrade", rec.Fn)
	require.Len(t, rec.Args, 1)
	got, err := scval.Bytes32FromScVal(rec.Args[0])
	require.NoError(t, err)
	require.Equal(t, hash, got)
}
