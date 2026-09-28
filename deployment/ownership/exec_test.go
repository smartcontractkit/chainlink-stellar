package ownership

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	aph "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks"
	burnmint "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	cciprecv "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
	cv "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	creforwarder "github.com/smartcontractkit/chainlink-stellar/deployment/operations/cre_forwarder"
	executor "github.com/smartcontractkit/chainlink-stellar/deployment/operations/executor"
	fq "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	lrp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	offramp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	onramp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	rr "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
	rmnproxy "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
	rmnremote "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	router "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	slrp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tar "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	tlb "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
	vvr "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

// TestExecuteUpgrade_routesEveryUpgradeableType verifies the dispatcher routes
// each of the 18 upgradeable contract types to its `upgrade` op and threads the
// new Wasm hash through as the single argument.
func TestExecuteUpgrade_routesEveryUpgradeableType(t *testing.T) {
	t.Parallel()

	upgradeableTypes := []string{
		mcmsops.ContractType, offramp.ContractType, onramp.ContractType, router.ContractType,
		fq.ContractType, executor.ContractType, rmnremote.ContractType, rmnproxy.ContractType,
		rr.ContractType, tar.ContractType, cv.ContractType, vvr.ContractType,
		lrp.ContractType, slrp.ContractType, burnmint.ContractType, tlb.ContractType,
		aph.ContractType, cciprecv.ContractType,
	}

	for _, ct := range upgradeableTypes {
		ct := ct
		t.Run(ct, func(t *testing.T) {
			t.Parallel()
			inv := operationstest.NewRecordingInvoker()
			deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
			cid := stellarutil.MustGenerateMockContractID("deployer", "upgrade-dispatch-"+ct)
			var hash [32]byte
			hash[0] = 0xCD

			err := ExecuteUpgrade(operationstest.NewBundle(t), deps, datastore.AddressRef{
				Address: cid, Type: datastore.ContractType(ct),
			}, hash)
			require.NoError(t, err)

			rec := inv.Last()
			require.Equal(t, cid, rec.ContractID)
			require.Equal(t, "upgrade", rec.Fn)
			require.Len(t, rec.Args, 1)
			got, err := scval.Bytes32FromScVal(rec.Args[0])
			require.NoError(t, err)
			require.Equal(t, hash, got)
		})
	}
}

// TestExecuteUpgrade_rejectsUnsupportedType verifies cre_forwarder (which has a
// TransferOwnership op but no Upgrade binding) and an unknown type both error
// without invoking anything.
func TestExecuteUpgrade_rejectsUnsupportedType(t *testing.T) {
	t.Parallel()
	cases := []string{creforwarder.ContractType, "NotARealType"}
	for _, ct := range cases {
		ct := ct
		t.Run(ct, func(t *testing.T) {
			t.Parallel()
			inv := operationstest.NewRecordingInvoker()
			deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
			cid := stellarutil.MustGenerateMockContractID("deployer", "upgrade-unsupported-"+ct)
			var hash [32]byte
			err := ExecuteUpgrade(operationstest.NewBundle(t), deps, datastore.AddressRef{
				Address: cid, Type: datastore.ContractType(ct),
			}, hash)
			require.Error(t, err)
			require.Contains(t, err.Error(), "unsupported contract type")
			require.Empty(t, inv.Records(), "no op must run for an unsupported type")
		})
	}
}
