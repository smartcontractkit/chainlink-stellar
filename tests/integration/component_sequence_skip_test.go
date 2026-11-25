//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	sequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// TestTokenAdminRegistrySequenceSkipIfExists proves constructor creation and
// rerun safety on a live network: one creating transaction sets the owner, a
// no-ref rerun adopts the instance with zero transactions, and a ref-backed
// rerun sends nothing and reads nothing.
func TestTokenAdminRegistrySequenceSkipIfExists(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, passphrase, _ := GetIsolatedTestEnv(ctx, t)

	deps := sequences.ComponentDeps{
		StellarDeps:       stellardeps.FromDeployer(deployer),
		NetworkPassphrase: passphrase,
		DeployerAddress:   deployerKP.Address(),
		Ledger:            deployer,
	}
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "token_admin_registry.wasm")
	sel := chainsel.STELLAR_LOCALNET.Selector
	in := sequences.DeployTokenAdminRegistryInput{ChainSelector: sel, WasmPath: wasmPath}
	run := func(in sequences.DeployTokenAdminRegistryInput) (sequences.ComponentDeployOutput, []string) {
		t.Helper()
		reporter := cldfops.NewMemoryReporter()
		b := cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Nop(), reporter)
		rep, err := cldfops.ExecuteSequence(b, sequences.DeployTokenAdminRegistry, deps, in)
		require.NoError(t, err)
		reports, err := reporter.GetReports()
		require.NoError(t, err)
		ids := make([]string, len(reports))
		for i, r := range reports {
			ids[i] = r.Def.ID
		}
		return rep.Output, ids
	}

	fresh, ids := run(in)
	require.True(t, fresh.Deployed)
	require.True(t, fresh.NeedsConfiguration)
	require.Len(t, fresh.Refs, 1)
	require.Equal(t, []string{"token-admin-registry:deploy", "stellar-deploy-token-admin-registry"}, ids)

	tar := tarbindings.NewTokenAdminRegistryClient(deployer, fresh.ContractID)
	owner, err := tar.Owner(ctx)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, deployerKP.Address(), *owner)
	_, err = deployer.SimulateContract(ctx, fresh.ContractID, "initialize", []xdr.ScVal{scval.AddressToScVal(deployerKP.Address())})
	require.Error(t, err, "initialize must not exist on a constructor-built registry")

	// No refs and no reports, as after a crash before refs persisted: adopt.
	adopted, ids := run(in)
	require.False(t, adopted.Deployed)
	require.True(t, adopted.NeedsConfiguration)
	require.Equal(t, fresh.ContractID, adopted.ContractID)
	require.Len(t, adopted.Refs, 1)
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, ids)

	in.ExistingAddresses = fresh.Refs
	reused, ids := run(in)
	require.False(t, reused.Deployed)
	require.False(t, reused.NeedsConfiguration)
	require.Equal(t, fresh.ContractID, reused.ContractID)
	require.Empty(t, reused.Refs)
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, ids)
}
