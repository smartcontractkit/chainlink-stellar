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

// TestRunStellarCCIPFullDeploy_SecondaryCommittee pins the per-committee
// deploy: a topology committee beyond the default that covers this chain gets
// its own qualified VVR + CV pair and impl updates, while the default pair's
// refs are untouched.
func TestRunStellarCCIPFullDeploy_SecondaryCommittee(t *testing.T) {
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
	committeeFor := func(qualifier string) offchain.CommitteeConfig {
		return offchain.CommitteeConfig{
			Qualifier: qualifier,
			ChainConfigs: map[string]offchain.ChainCommitteeConfig{
				localKey: {Threshold: 1},
			},
		}
	}
	topology := &offchain.EnvironmentTopology{
		NOPTopology: &offchain.NOPTopology{
			Committees: map[string]offchain.CommitteeConfig{
				stellarccip.DefaultCommitteeVerifierQualifier: committeeFor(stellarccip.DefaultCommitteeVerifierQualifier),
				"secondary":                                   committeeFor("secondary"),
			},
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
	cvType := stellarccip.CommitteeVerifierDatastoreRefWithQualifier("secondary").Type
	vvrType := stellarccip.VVRDatastoreRefWithQualifier("secondary").Type

	cvDefault := requireRef(cvType, stellarccip.DefaultCommitteeVerifierQualifier)
	vvrDefault := requireRef(vvrType, stellarccip.DefaultCommitteeVerifierQualifier)
	cvSecondary := requireRef(cvType, "secondary")
	vvrSecondary := requireRef(vvrType, "secondary")

	require.NotEqual(t, cvDefault, cvSecondary, "secondary CV must be a distinct contract")
	require.NotEqual(t, vvrDefault, vvrSecondary, "secondary VVR must be a distinct contract")

	// 55 = the 46 baseline reports plus 9 for the secondary pair: VVR and CV
	// sequences (deploy + init + sequence report each), the two impl updates,
	// and the secondary CV's remote chain config updates.
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	require.Len(t, reports, 55)
}
