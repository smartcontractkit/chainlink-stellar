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

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	sequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// TestTokenAdminRegistrySequenceSkipIfExists proves the component sequence's
// rerun safety on a live network: a fresh run deploys and initializes, and a
// second run with an empty datastore (fresh bundle, no reports) adopts the
// on-chain contract with zero deploy and initialize transactions.
func TestTokenAdminRegistrySequenceSkipIfExists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, passphrase, _ := GetSharedTestEnv(ctx, t)

	deps := sequences.ComponentDeps{
		StellarDeps:       stellardeps.FromDeployer(deployer),
		NetworkPassphrase: passphrase,
		DeployerAddress:   deployerKP.Address(),
		Ledger:            deployer,
	}
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "token_admin_registry.wasm")
	sel := chainsel.STELLAR_LOCALNET.Selector

	freshReporter := cldfops.NewMemoryReporter()
	freshBundle := cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Nop(), freshReporter)
	rep, err := cldfops.ExecuteSequence(freshBundle, sequences.DeployTokenAdminRegistry, deps, sequences.DeployTokenAdminRegistryInput{
		ChainSelector: sel,
		WasmPath:      wasmPath,
	})
	require.NoError(t, err)
	require.True(t, rep.Output.Deployed)
	require.True(t, rep.Output.Initialized)
	require.Len(t, rep.Output.Refs, 1)

	// Rerun with an empty datastore and a fresh bundle: layer 2 predicts the same
	// contract ID, adopts it (WASM hash matches), and layer 3 sees the owner we
	// set. Zero transactions: the reporter must hold only the sequence report,
	// no token-admin-registry:* op reports.
	rerunReporter := cldfops.NewMemoryReporter()
	rerunBundle := cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Nop(), rerunReporter)
	rep2, err := cldfops.ExecuteSequence(rerunBundle, sequences.DeployTokenAdminRegistry, deps, sequences.DeployTokenAdminRegistryInput{
		ChainSelector: sel,
		WasmPath:      wasmPath,
	})
	require.NoError(t, err)
	require.False(t, rep2.Output.Deployed)
	require.False(t, rep2.Output.Initialized)
	require.Equal(t, rep.Output.ContractID, rep2.Output.ContractID)
	require.Len(t, rep2.Output.Refs, 1)
	reports, err := rerunReporter.GetReports()
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "stellar-deploy-token-admin-registry", reports[0].Def.ID)
}
