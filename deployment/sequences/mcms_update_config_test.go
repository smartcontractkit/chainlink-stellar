package sequences

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	mcmsops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/mcms"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func testSetConfigInput(t *testing.T) mcmsops.SetConfigInput {
	t.Helper()
	cfg := mcmstypes.Config{Quorum: 1, Signers: []common.Address{common.HexToAddress("0x00000000000000000000000000000000000000aa")}}
	addrs, groups, quorums, parents, _, err := mcmsutil.ConfigToStellarSetConfig(&cfg, true)
	require.NoError(t, err)
	return mcmsops.SetConfigInput{
		SignerAddresses: addrs,
		SignerGroups:    groups,
		GroupQuorums:    quorums,
		GroupParents:    parents,
		ClearRoot:       true,
	}
}

func TestSetStellarMCMSConfigForRef_timelockOwnerEmitsSetConfigOp(t *testing.T) {
	t.Parallel()
	deployerAddr := keypair.MustRandom().Address()
	timelockAddr := stellarutil.MustGenerateMockContractID("deployer", "update-config-timelock")
	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("owner", ptrScVal(scval.AddressToScVal(timelockAddr)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	cid := stellarutil.MustGenerateMockContractID("deployer", "update-config-proposer")
	ref := datastore.AddressRef{Address: cid, Type: "ProposerManyChainMultiSig", Qualifier: "CLLCCIP"}
	wo, err := setStellarMCMSConfigForRef(t.Context(), operationstest.NewBundle(t), deps,
		deployerAddr, timelockAddr, 999, ref, testSetConfigInput(t))
	require.NoError(t, err)

	require.False(t, wo.Executed())
	require.Equal(t, cid, wo.Tx.To)
	require.Equal(t, "ProposerManyChainMultiSig", wo.Tx.OperationMetadata.ContractType)
	require.JSONEq(t, `{"family":"stellar","encodingVersion":1}`, string(wo.Tx.AdditionalFields))

	fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(wo.Tx.Data)
	require.NoError(t, err)
	require.Equal(t, "set_config", fn)
	require.Len(t, args, 5)
	require.Equal(t, scval.BoolToScVal(true), args[4])

	for _, r := range inv.Records() {
		require.NotEqual(t, "set_config", r.Fn, "the timelock path must not call set_config on chain")
	}
}

func TestSetStellarMCMSConfigForRef_deployerOwnerCallsSetConfig(t *testing.T) {
	t.Parallel()
	deployerAddr := keypair.MustRandom().Address()
	timelockAddr := stellarutil.MustGenerateMockContractID("deployer", "update-config-timelock")
	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("owner", ptrScVal(scval.AddressToScVal(deployerAddr)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	cid := stellarutil.MustGenerateMockContractID("deployer", "update-config-direct")
	ref := datastore.AddressRef{Address: cid, Type: "ProposerManyChainMultiSig", Qualifier: "CLLCCIP"}
	wo, err := setStellarMCMSConfigForRef(t.Context(), operationstest.NewBundle(t), deps,
		deployerAddr, timelockAddr, 999, ref, testSetConfigInput(t))
	require.NoError(t, err)

	require.True(t, wo.Executed())
	require.Empty(t, wo.Tx.Data)
	rec := inv.Last()
	require.Equal(t, "set_config", rec.Fn)
	require.Equal(t, cid, rec.ContractID)
}

func TestSetStellarMCMSConfigForRef_rejectsOtherOwner(t *testing.T) {
	t.Parallel()
	deployerAddr := keypair.MustRandom().Address()
	otherTimelock := stellarutil.MustGenerateMockContractID("deployer", "update-config-other-timelock")
	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("owner", ptrScVal(scval.AddressToScVal(otherTimelock)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	cid := stellarutil.MustGenerateMockContractID("deployer", "update-config-other-owner")
	ref := datastore.AddressRef{Address: cid, Type: "ProposerManyChainMultiSig", Qualifier: "CLLCCIP"}

	for name, timelockAddr := range map[string]string{
		"different timelock": stellarutil.MustGenerateMockContractID("deployer", "update-config-timelock"),
		"no timelock ref":    "",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := setStellarMCMSConfigForRef(t.Context(), operationstest.NewBundle(t), deps,
				deployerAddr, timelockAddr, 999, ref, testSetConfigInput(t))
			require.ErrorContains(t, err, "is neither deployer")
		})
	}
}
