package bnm_token_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func ptrScVal(v xdr.ScVal) *xdr.ScVal { return &v }

func TestInitialize_operation(t *testing.T) {
	t.Parallel()
	inv := operationstest.NewRecordingInvoker()
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
	cid := operationstest.MockContractID
	admin := keypair.MustRandom().Address()

	_, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), bnm_token.Initialize, deps, bnm_token.InitializeInput{
		ContractID: cid,
		Admin:      admin,
		Name:       "CCIP BnM",
		Symbol:     "BnM",
		Decimals:   7,
	})
	require.NoError(t, err)

	rec := inv.Last()
	require.Equal(t, cid, rec.ContractID)
	require.Equal(t, "initialize", rec.Fn)
	require.Len(t, rec.Args, 4)

	gotAdmin, err := scval.AddressFromScVal(rec.Args[0])
	require.NoError(t, err)
	require.Equal(t, admin, gotAdmin)

	gotName, err := scval.StringFromScVal(rec.Args[1])
	require.NoError(t, err)
	require.Equal(t, "CCIP BnM", gotName)

	gotSymbol, err := scval.StringFromScVal(rec.Args[2])
	require.NoError(t, err)
	require.Equal(t, "BnM", gotSymbol)

	gotDecimals, err := scval.Uint32FromScVal(rec.Args[3])
	require.NoError(t, err)
	require.Equal(t, uint32(7), gotDecimals)
}

func TestInitialize_rejectsEmptyAdmin(t *testing.T) {
	t.Parallel()
	inv := operationstest.NewRecordingInvoker()
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
	_, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), bnm_token.Initialize, deps, bnm_token.InitializeInput{
		ContractID: operationstest.MockContractID,
		Admin:      "",
		Name:       "CCIP BnM",
		Symbol:     "BnM",
		Decimals:   7,
	})
	require.Error(t, err)
}

func TestDrip_operation(t *testing.T) {
	t.Parallel()
	inv := operationstest.NewRecordingInvoker()
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
	cid := operationstest.MockContractID
	to := keypair.MustRandom().Address()

	_, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), bnm_token.Drip, deps, bnm_token.DripInput{
		ContractID: cid,
		To:         to,
	})
	require.NoError(t, err)

	rec := inv.Last()
	require.Equal(t, cid, rec.ContractID)
	require.Equal(t, "drip", rec.Fn)
	require.Len(t, rec.Args, 1)
	gotTo, err := scval.AddressFromScVal(rec.Args[0])
	require.NoError(t, err)
	require.Equal(t, to, gotTo)
}

func TestAdmin_read(t *testing.T) {
	t.Parallel()
	admin := keypair.MustRandom().Address()
	inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("admin", ptrScVal(scval.AddressToScVal(admin)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	out, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), bnm_token.Admin, deps, bnm_token.AdminInput{
		ContractID: operationstest.MockContractID,
	})
	require.NoError(t, err)
	require.Equal(t, admin, out.Output.Admin)
}

func TestDecimals_read(t *testing.T) {
	t.Parallel()
	inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("decimals", ptrScVal(scval.Uint32ToScVal(7)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	out, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), bnm_token.Decimals, deps, bnm_token.DecimalsInput{
		ContractID: operationstest.MockContractID,
	})
	require.NoError(t, err)
	require.Equal(t, uint32(7), out.Output.Decimals)
}

func TestBalance_read(t *testing.T) {
	t.Parallel()
	amt := big.NewInt(1_000_000) // 0.1 token at 7 decimals
	inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("balance", ptrScVal(scval.I128ToScVal(amt)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
	id := keypair.MustRandom().Address()

	out, err := cldfops.ExecuteOperation(operationstest.NewBundle(t), bnm_token.Balance, deps, bnm_token.BalanceInput{
		ContractID: operationstest.MockContractID,
		ID:         id,
	})
	require.NoError(t, err)
	require.Equal(t, 0, amt.Cmp(out.Output.Balance))

	// The balance read must pass the `id` address as the single arg.
	rec := inv.Last()
	require.Equal(t, "balance", rec.Fn)
	require.Len(t, rec.Args, 1)
	gotID, err := scval.AddressFromScVal(rec.Args[0])
	require.NoError(t, err)
	require.Equal(t, id, gotID)
}
