package adapters

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestBytesToContractStrkey(t *testing.T) {
	t.Parallel()
	pool := stellarutil.MustGenerateMockContractID("deployer", "migrator-pool")
	poolHex, err := stellarutil.StrkeyToHex(pool)
	require.NoError(t, err)

	raw, err := bytesToContractStrkey(mustDecodeContractStrkey(t, pool))
	require.NoError(t, err)
	require.Equal(t, pool, raw)

	// The encoded strkey keeps the AddressRefToBytes byte form.
	cid, err := bytesToContractStrkey(mustDecodeContractStrkey(t, pool))
	require.NoError(t, err)
	want, err := (&StellarTokenAdapter{}).AddressRefToBytes(datastore.AddressRef{Address: poolHex})
	require.NoError(t, err)
	require.Equal(t, want, mustDecodeContractStrkey(t, cid))

	_, err = bytesToContractStrkey(common.LeftPadBytes([]byte{1}, 20))
	require.ErrorContains(t, err, "address bytes must be 32")
}

func TestProbeSupportedChains(t *testing.T) {
	t.Parallel()
	pool := stellarutil.MustGenerateMockContractID("deployer", "migrator-pool")

	t.Run("supported candidates are returned sorted", func(t *testing.T) {
		t.Parallel()
		v := scval.BoolToScVal(true)
		inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("is_supported_chain", &v)
		supported, err := probeSupportedChains(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, pool, []uint64{9, 7, 8})
		require.NoError(t, err)
		require.Equal(t, []uint64{7, 8, 9}, supported)
	})

	t.Run("unsupported candidates are excluded", func(t *testing.T) {
		t.Parallel()
		v := scval.BoolToScVal(false)
		inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("is_supported_chain", &v)
		supported, err := probeSupportedChains(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, pool, []uint64{7, 8})
		require.NoError(t, err)
		require.Empty(t, supported)
	})

	t.Run("probe failure is fatal", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().WithSimulateErrorForFn("is_supported_chain", errors.New("probe boom"))
		_, err := probeSupportedChains(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, pool, []uint64{7})
		require.ErrorContains(t, err, "probe remote support")
	})
}

func TestReadMigrationRemoteToken(t *testing.T) {
	t.Parallel()
	pool := stellarutil.MustGenerateMockContractID("deployer", "migrator-pool")
	want := []byte{1, 2, 3, 4}
	v := scval.BytesToScVal(want)
	inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("get_remote_token", &v)
	got, err := readMigrationRemoteToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, pool, 7)
	require.NoError(t, err)
	require.Equal(t, want, got)
	rec := inv.Last()
	require.Equal(t, pool, rec.ContractID)
	require.Equal(t, "get_remote_token", rec.Fn)
	remote, ok := rec.Args[0].GetU64()
	require.True(t, ok)
	require.Equal(t, uint64(7), uint64(remote))
}

func TestReadMigrationRemotePools(t *testing.T) {
	t.Parallel()
	pool := stellarutil.MustGenerateMockContractID("deployer", "migrator-pool")
	want := [][]byte{{1, 2, 3, 4}, {5, 6, 7, 8}}
	vals := make([]xdr.ScVal, 0, len(want))
	for _, b := range want {
		vals = append(vals, scval.BytesToScVal(b))
	}
	vec := scval.VecToScVal(vals)
	inv := operationstest.NewRecordingInvoker().WithSimulateResultForFn("get_remote_pools", &vec)
	got, err := readMigrationRemotePools(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, pool, 7)
	require.NoError(t, err)
	require.Equal(t, want, got)
	rec := inv.Last()
	require.Equal(t, pool, rec.ContractID)
	require.Equal(t, "get_remote_pools", rec.Fn)
}
