package sequences

import (
	"context"
	"strconv"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/offchain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// TestRunStellarCCIPFullDeploy_ExecutorPool pins the per-pool executor deploy:
// a topology executor pool beyond the default that covers this chain gets its
// own qualified Executor (contract and proxy refs, dest chains enabled), while
// the default instance's refs are untouched.
func TestRunStellarCCIPFullDeploy_ExecutorPool(t *testing.T) {
	_ = characterizationWasms(t)

	reporter := cldf_ops.NewMemoryReporter()
	b := cldf_ops.NewBundle(
		func() context.Context { return t.Context() },
		cldflogger.Nop(),
		reporter,
	)

	inv := operationstest.NewRecordingInvoker().WithSimulateResult(&xdr.ScVal{Type: xdr.ScValTypeScvVoid})
	deps := stellardeps.StellarDeps{
		Deploy:  newCharacterizationDeployer(),
		Invoker: inv,
	}
	host := newFakeCCIPDevenvHost(t)

	localKey := strconv.FormatUint(characterizationSelector, 10)
	poolFor := func() offchain.ExecutorPoolConfig {
		return offchain.ExecutorPoolConfig{
			ChainConfigs: map[string]offchain.ChainExecutorPoolConfig{
				localKey: {},
			},
		}
	}
	topology := &offchain.EnvironmentTopology{
		ExecutorPools: map[string]offchain.ExecutorPoolConfig{
			stellarccip.DefaultExecutorQualifier: poolFor(),
			"custom":                             poolFor(),
		},
	}

	out, err := RunStellarCCIPFullDeploy(b.GetContext(), b, deps, host, topology, DeployStellarCCIPInnerInput{
		ChainSelector: characterizationSelector,
		AllSelectors:  []uint64{characterizationSelector, characterizationRemoteSel},
	})
	require.NoError(t, err)

	requireRef := func(refType datastore.ContractType, qualifier string) string {
		t.Helper()
		for _, ref := range out.Addresses {
			if ref.ChainSelector == characterizationSelector &&
				ref.Type == refType &&
				ref.Qualifier == qualifier {
				return ref.Address
			}
		}
		t.Fatalf("no %s ref with qualifier %q in deploy output", refType, qualifier)
		return ""
	}
	execType := stellarccip.ExecutorDatastoreRefWithQualifier("custom").Type
	proxyType := stellarccip.ExecutorProxyDatastoreRef("custom").Type

	execDefault := requireRef(execType, stellarccip.DefaultExecutorQualifier)
	proxyDefault := requireRef(proxyType, stellarccip.DefaultExecutorQualifier)
	execCustom := requireRef(execType, "custom")
	proxyCustom := requireRef(proxyType, "custom")

	require.NotEqual(t, execDefault, execCustom, "custom executor must be a distinct contract")
	require.NotEqual(t, proxyDefault, proxyCustom, "custom executor proxy row must be distinct")
	// Soroban has no delegate proxy: both rows point at the same contract.
	require.Equal(t, execCustom, proxyCustom)

	// 50 = the 46 baseline reports plus 4 for the custom executor: deploy, init,
	// the sequence report, and the dest chain updates.
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	require.Len(t, reports, 50)
}
