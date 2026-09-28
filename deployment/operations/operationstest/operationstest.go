// Package operationstest provides shared stubs for unit-testing Soroban
// deployment operations without a live network: a recording invoker that
// captures every InvokeContract/SimulateContract call, a no-op fake deployer,
// and a test bundle builder. These are arm64-safe (no network, no Dup2) and
// are imported by the per-package operation _test files.
package operationstest

import (
	"context"
	"sync"
	"testing"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// MockContractID is a placeholder contract ID for op tests that do not exercise
// strkey/hex normalization (the op passes it straight through to the invoker).
const MockContractID = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"

// InvokeRecord captures a single InvokeContract/SimulateContract call.
type InvokeRecord struct {
	ContractID string
	Fn         string
	Args       []xdr.ScVal
}

// RecordingInvoker is a bindings.Invoker that records every call and returns
// the configured Simulate result (nil by default). InvokeContract calls are
// captured but otherwise no-op.
type RecordingInvoker struct {
	mu                 sync.Mutex
	records            []InvokeRecord
	simulateResult     *xdr.ScVal
	simulateByFn       map[string]*xdr.ScVal
	simulateByContract map[string]*xdr.ScVal
}

// NewRecordingInvoker returns an empty RecordingInvoker.
func NewRecordingInvoker() *RecordingInvoker {
	return &RecordingInvoker{
		simulateByFn:       map[string]*xdr.ScVal{},
		simulateByContract: map[string]*xdr.ScVal{},
	}
}

// WithSimulateResult sets the ScVal returned by SimulateContract for every call.
func (r *RecordingInvoker) WithSimulateResult(v *xdr.ScVal) *RecordingInvoker {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.simulateResult = v
	return r
}

// WithSimulateResultForFn sets the ScVal returned by SimulateContract for a
// specific function name (e.g. "owner"), taking precedence over the global
// WithSimulateResult. Used to stub owner reads in sequence tests.
func (r *RecordingInvoker) WithSimulateResultForFn(fn string, v *xdr.ScVal) *RecordingInvoker {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.simulateByFn[fn] = v
	return r
}

func (r *RecordingInvoker) InvokeContract(_ context.Context, contractID, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	r.mu.Lock()
	r.records = append(r.records, InvokeRecord{ContractID: contractID, Fn: functionName, Args: args})
	r.mu.Unlock()
	return nil, nil
}

func (r *RecordingInvoker) SimulateContract(_ context.Context, contractID, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	r.mu.Lock()
	r.records = append(r.records, InvokeRecord{ContractID: contractID, Fn: functionName, Args: args})
	if v, ok := r.simulateByFn[functionName]; ok {
		r.mu.Unlock()
		return v, nil
	}
	v := r.simulateResult
	r.mu.Unlock()
	return v, nil
}

func (r *RecordingInvoker) GetEvents(_ context.Context, _ string, _ uint32, _ []string) ([]protocolrpc.EventInfo, error) {
	return nil, nil
}

// Records returns a copy of all captured calls.
func (r *RecordingInvoker) Records() []InvokeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]InvokeRecord, len(r.records))
	copy(out, r.records)
	return out
}

// Last returns the most recent captured call, or a zero record if none.
func (r *RecordingInvoker) Last() InvokeRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.records) == 0 {
		return InvokeRecord{}
	}
	return r.records[len(r.records)-1]
}

// FakeDeployer is a stellardeps.SorobanContractDeployer that records the last
// wasm path + salt and returns a fixed contract ID.
type FakeDeployer struct {
	LastWasm string
	LastSalt [32]byte
}

func (f *FakeDeployer) DeployContract(_ context.Context, wasmPath string, salt [32]byte) (string, error) {
	f.LastWasm = wasmPath
	f.LastSalt = salt
	return MockContractID, nil
}

// NewBundle returns a cldfops.Bundle suitable for ExecuteOperation/ExecuteSequence.
func NewBundle(t *testing.T) cldfops.Bundle {
	t.Helper()
	return cldfops.NewBundle(
		func() context.Context { return context.Background() },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
}
