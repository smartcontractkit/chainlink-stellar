package offramp_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
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
	return "COFFRAMPFAKE000000000000000000000000000000000000000000", nil
}

type invokeRecord struct {
	contractID string
	fn         string
	argLen     int
}

type recordingInvoker struct {
	mu      sync.Mutex
	records []invokeRecord
}

func (r *recordingInvoker) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	r.mu.Lock()
	r.records = append(r.records, invokeRecord{contractID: contractID, fn: functionName, argLen: len(args)})
	r.mu.Unlock()
	return nil, nil
}

func (r *recordingInvoker) SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	_ = contractID
	_ = functionName
	_ = args
	return nil, nil
}

func (r *recordingInvoker) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	_ = ctx
	_ = contractID
	_ = startLedger
	_ = topics
	return nil, nil
}

func (r *recordingInvoker) last() invokeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) == 0 {
		return invokeRecord{}
	}
	return r.records[len(r.records)-1]
}

func testBundle(t *testing.T) cldfops.Bundle {
	t.Helper()
	return cldfops.NewBundle(
		func() context.Context { return context.Background() },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
}

func TestDeploy_operation(t *testing.T) {
	t.Parallel()
	fd := &fakeDeployer{}
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Deploy: fd, Invoker: inv}

	var salt [32]byte
	salt[0] = 9

	report, err := cldfops.ExecuteOperation(testBundle(t), offramp.Deploy, deps, stellarops.DeployInput{
		WasmPath: "/tmp/offramp.wasm",
		Salt:     salt,
	})
	require.NoError(t, err)
	require.Nil(t, report.Err)
	require.Equal(t, "COFFRAMPFAKE000000000000000000000000000000000000000000", report.Output.ContractID)
	require.Equal(t, "/tmp/offramp.wasm", fd.lastWasm)
	require.Equal(t, byte(9), fd.lastSalt[0])
}

func TestInitialize_operation(t *testing.T) {
	t.Parallel()
	fd := &fakeDeployer{}
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Deploy: fd, Invoker: inv}

	cid := "COFFRAMP0000000000000000000000000000000000000000000000"
	_, err := cldfops.ExecuteOperation(testBundle(t), offramp.Initialize, deps, offramp.InitializeInput{
		ContractID: cid,
		Owner:      "GOWNERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Config: offrampbindings.StaticConfig{
			ChainSelector:      123,
			RmnProxy:           "GRMNAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			TokenAdminRegistry: "GTARAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		},
	})
	require.NoError(t, err)
	rec := inv.last()
	require.Equal(t, cid, rec.contractID)
	require.Equal(t, "initialize", rec.fn)
	require.Equal(t, 2, rec.argLen)
}

func TestApplySourceChainCfgUpdates_operation(t *testing.T) {
	t.Parallel()
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Deploy: &fakeDeployer{}, Invoker: inv}

	cid := "COFFRAMP0000000000000000000000000000000000000000000000"
	_, err := cldfops.ExecuteOperation(testBundle(t), offramp.ApplySourceChainCfgUpdates, deps, offramp.ApplySourceChainCfgUpdatesInput{
		ContractID: cid,
		Updates: []offrampbindings.SourceChainConfigArgs{
			{
				SourceChainSelector: 999,
				IsEnabled:           true,
				Router:              "GROUTERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
				DefaultCcvs:         []string{"GCCVAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
				LaneMandatedCcvs:    nil,
				OnRamps:             [][]byte{{0x01, 0x02}},
			},
		},
	})
	require.NoError(t, err)
	rec := inv.last()
	require.Equal(t, cid, rec.contractID)
	require.Equal(t, "apply_source_chain_cfg_updates", rec.fn)
	require.Equal(t, 1, rec.argLen)
}

func TestExecute_operation(t *testing.T) {
	t.Parallel()
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Deploy: &fakeDeployer{}, Invoker: inv}

	cid := "COFFRAMP0000000000000000000000000000000000000000000000"
	_, err := cldfops.ExecuteOperation(testBundle(t), offramp.Execute, deps, offramp.ExecuteInput{
		ContractID:       cid,
		EncodedMessage:   []byte{0xab},
		Ccvs:             []string{"GCCVAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		VerifierResults:  [][]byte{{0xcd}},
		GasLimitOverride: 42,
	})
	require.NoError(t, err)
	rec := inv.last()
	require.Equal(t, cid, rec.contractID)
	require.Equal(t, "execute", rec.fn)
	require.Equal(t, 4, rec.argLen)
}

func TestTransferOwnership_operation(t *testing.T) {
	t.Parallel()
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Deploy: &fakeDeployer{}, Invoker: inv}

	cid := "COFFRAMP0000000000000000000000000000000000000000000000"
	_, err := cldfops.ExecuteOperation(testBundle(t), offramp.TransferOwnership, deps, offramp.TransferOwnershipInput{
		ContractID: cid,
		NewOwner:   "GNEWAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	require.NoError(t, err)
	rec := inv.last()
	require.Equal(t, "transfer_ownership", rec.fn)
}

func TestAcceptOwnership_operation(t *testing.T) {
	t.Parallel()
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Deploy: &fakeDeployer{}, Invoker: inv}

	cid := "COFFRAMP0000000000000000000000000000000000000000000000"
	_, err := cldfops.ExecuteOperation(testBundle(t), offramp.AcceptOwnership, deps, offramp.AcceptOwnershipInput{
		ContractID: cid,
	})
	require.NoError(t, err)
	rec := inv.last()
	require.Equal(t, "accept_ownership", rec.fn)
	require.Equal(t, 0, rec.argLen)
}

func TestOperationInputs_areCLDFSerializable(t *testing.T) {
	t.Parallel()
	lg := cldfops.NewBundle(
		func() context.Context { return context.Background() },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	).Logger

	var salt [32]byte
	salt[31] = 1

	require.True(t, cldfops.IsSerializable(lg, stellarops.DeployInput{WasmPath: "x.wasm", Salt: salt}))
	require.True(t, cldfops.IsSerializable(lg, offramp.InitializeInput{
		ContractID: "C1",
		Owner:      "G1",
		Config: offrampbindings.StaticConfig{
			ChainSelector:      1,
			RmnProxy:           "G2",
			TokenAdminRegistry: "G3",
		},
	}))
	require.True(t, cldfops.IsSerializable(lg, offramp.ApplySourceChainCfgUpdatesInput{
		ContractID: "C1",
		Updates: []offrampbindings.SourceChainConfigArgs{
			{SourceChainSelector: 2, Router: "G4", OnRamps: [][]byte{{1, 2, 3}}},
		},
	}))
	require.True(t, cldfops.IsSerializable(lg, offramp.ExecuteInput{
		ContractID:     "C1",
		EncodedMessage: []byte{1},
		Ccvs:           []string{"G5"},
		VerifierResults: [][]byte{
			{4, 5},
		},
		GasLimitOverride: 0,
	}))
	require.True(t, cldfops.IsSerializable(lg, offramp.TransferOwnershipInput{ContractID: "C1", NewOwner: "G6"}))
	require.True(t, cldfops.IsSerializable(lg, offramp.AcceptOwnershipInput{ContractID: "C1"}))
	require.True(t, cldfops.IsSerializable(lg, stellarops.Void{}))
}
