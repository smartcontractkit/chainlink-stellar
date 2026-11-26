package adapters

import (
	"context"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// staleReportVersions are versions written before the current constructor
// migration step: 2.0.0 predates constructors, 3.0.0 covers only TokenAdminRegistry.
var staleReportVersions = []*semver.Version{semver.MustParse("2.0.0"), semver.MustParse("3.0.0")}

// runWithSeededReport seeds a successful report for in at version (nil: current)
// and runs seq on an empty chain set, so only a cache hit can succeed.
func runWithSeededReport[IN, OUT any](
	t *testing.T,
	seq *cldf_ops.Sequence[IN, OUT, cldf_chain.BlockChains],
	version *semver.Version,
	in IN,
	cached OUT,
) error {
	t.Helper()
	if version == nil {
		version = semver.MustParse(seq.Version())
	}
	def := cldf_ops.Definition{ID: seq.ID(), Version: version, Description: seq.Description()}
	seeded := cldf_ops.NewReport(def, in, cached, nil)
	reporter := cldf_ops.NewMemoryReporter(cldf_ops.WithReports([]cldf_ops.Report[any, any]{seeded.ToGenericReport()}))
	b := cldf_ops.NewBundle(func() context.Context { return t.Context() }, cldflogger.Nop(), reporter)
	_, err := cldf_ops.ExecuteSequence(b, seq, cldf_chain.NewBlockChains(nil), in)
	return err
}

func TestDeployReports_StaleVersionDoesNotReplay(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*testing.T, *semver.Version) error{
		"protocol contracts": func(t *testing.T, v *semver.Version) error {
			in := ccvdeploymentadapters.ProtocolContractsDeployInput{ChainSelector: 1}
			return runWithSeededReport(t, stellarDeployProtocolContracts, v, in, ccvdeploymentadapters.ProtocolContractsDeployOutput{})
		},
		"committee verifier and resolver": func(t *testing.T, v *semver.Version) error {
			in := ccvdeploymentadapters.CommitteeVerifierDeployInput{ChainSelector: 1}
			return runWithSeededReport(t, stellarDeployCommitteeVerifierAndResolver, v, in, ccvdeploymentadapters.CommitteeVerifierDeployOutput{})
		},
		"full deploy chain contracts": func(t *testing.T, v *semver.Version) error {
			in := ccvadapters.DeployChainContractsInput{ChainSelector: 1}
			return runWithSeededReport(t, sequences.StellarDeployChainContracts, v, in, sequences.DeployChainContractsOutput{})
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, v := range staleReportVersions {
				require.ErrorContains(t, run(t, v), "stellar chain not found",
					"a %s report must not replay", v)
			}
			// Control: a report at the current version does replay, so the check above
			// would catch a missed version bump.
			require.NoError(t, run(t, nil))
		})
	}
}
