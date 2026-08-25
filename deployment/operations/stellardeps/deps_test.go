package stellardeps_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/smartcontractkit/chainlink-stellar/bindings"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type fakeDeployer struct {
	lastWasm string
	lastSalt [32]byte
}

func (f *fakeDeployer) DeployContract(ctx context.Context, wasmPath string, salt [32]byte) (string, error) {
	_ = ctx
	f.lastWasm = wasmPath
	f.lastSalt = salt
	return "CDEPLOYFAKE000000000000000000000000000000000000000000000", nil
}

type fakeInvoker struct {
	lastContract string
	lastFn       string
}

func (f *fakeInvoker) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	_ = args
	f.lastContract = contractID
	f.lastFn = functionName
	return nil, nil
}

func (f *fakeInvoker) SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	_ = contractID
	_ = functionName
	_ = args
	return nil, nil
}

func (f *fakeInvoker) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	_ = ctx
	_ = contractID
	_ = startLedger
	_ = topics
	return nil, nil
}

func TestDeployer_satisfiesStellarDepsInterfaces(t *testing.T) {
	t.Parallel()
	var _ stellardeps.SorobanContractDeployer = (*stellardeployment.Deployer)(nil)
	var _ bindings.Invoker = (*stellardeployment.Deployer)(nil)
}

func TestFromDeployer_nil(t *testing.T) {
	t.Parallel()
	d := stellardeps.FromDeployer(nil)
	require.Nil(t, d.Deploy)
	require.Nil(t, d.Invoker)
}

func TestExecuteOperation_withStellarDeps(t *testing.T) {
	t.Parallel()

	fd := &fakeDeployer{}
	fi := &fakeInvoker{}
	deps := stellardeps.StellarDeps{Deploy: fd, Invoker: fi}

	bundle := cldfops.NewBundle(
		func() context.Context { return context.Background() },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)

	type in struct {
		Wasm string `json:"wasm"`
	}
	type out struct {
		Contract string `json:"contract"`
	}

	op := cldfops.NewOperation(
		"stellardeps:self-test",
		stellarops.ContractDeploymentVersion,
		"smoke test that deps wiring matches CLDF operation execution",
		func(b cldfops.Bundle, d stellardeps.StellarDeps, input in) (out, error) {
			ctx := b.GetContext()
			id, err := d.Deploy.DeployContract(ctx, input.Wasm, [32]byte{7})
			if err != nil {
				return out{}, err
			}
			_, err = d.Invoker.InvokeContract(ctx, id, "__self_test__", nil)
			return out{Contract: id}, err
		},
	)

	report, err := cldfops.ExecuteOperation(bundle, op, deps, in{Wasm: "/tmp/example.wasm"})
	require.NoError(t, err)
	require.Nil(t, report.Err)
	require.Equal(t, "/tmp/example.wasm", fd.lastWasm)
	require.Equal(t, byte(7), fd.lastSalt[0])
	require.Equal(t, fi.lastContract, report.Output.Contract)
	require.Equal(t, "__self_test__", fi.lastFn)
}
