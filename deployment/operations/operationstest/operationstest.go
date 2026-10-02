// Package operationstest provides shared stubs for unit-testing Soroban
// deployment operations without a live network: a recording invoker that
// captures every InvokeContract/SimulateContract call, a no-op fake deployer,
// and a test bundle builder. These are arm64-safe (no network, no Dup2) and
// are imported by the per-package operation _test files.
package operationstest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ContractErrorCode renders the error text the Soroban host surfaces through
// the RPC client for a contract error code, exactly as
// stellarops.IsContractErrorCode matches it. Use it to stub not-configured
// reads: the bindings clients wrap the invoker error with %w, so the text
// reaches IsContractErrorCode unchanged — the same path a real RPC takes.
func ContractErrorCode(code uint32) error {
	return fmt.Errorf("Error(Contract, #%d)", code)
}

// MockContractID is a placeholder contract ID for op tests that do not exercise
// strkey/hex normalization (the op passes it straight through to the invoker).
const MockContractID = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"

// InvokeRecord captures a single InvokeContract/SimulateContract call.
type InvokeRecord struct {
	ContractID string
	Fn         string
	Args       []xdr.ScVal
}

// invokeKey keys per-contract stubs by (contract ID, fn): the router, for
// example, serves both get_onramp and get_offramps, and OnRamp, FeeQuoter and
// Executor all expose get_dest_chain_config with different not-configured
// error codes — neither contract alone nor fn alone is enough to pin a stub.
type invokeKey struct {
	contractID string
	fn         string
}

// RecordingInvoker is a bindings.Invoker that records every call and returns
// the configured Simulate result (nil by default). InvokeContract calls are
// captured but otherwise no-op.
type RecordingInvoker struct {
	mu             sync.Mutex
	records        []InvokeRecord
	simulateResult *xdr.ScVal
	simulateByFn   map[string]*xdr.ScVal
	simulateByPair map[invokeKey]*xdr.ScVal
	errByFn        map[string]error
	errByPair      map[invokeKey]error
}

// NewRecordingInvoker returns an empty RecordingInvoker.
func NewRecordingInvoker() *RecordingInvoker {
	return &RecordingInvoker{
		simulateByFn:   map[string]*xdr.ScVal{},
		simulateByPair: map[invokeKey]*xdr.ScVal{},
		errByFn:        map[string]error{},
		errByPair:      map[invokeKey]error{},
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
// WithSimulateResult. Used to stub owner reads in sequence tests. Setting a
// result clears a previously set error for the same fn (last write wins).
func (r *RecordingInvoker) WithSimulateResultForFn(fn string, v *xdr.ScVal) *RecordingInvoker {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.simulateByFn[fn] = v
	delete(r.errByFn, fn)
	return r
}

// WithSimulateResultForContract sets the ScVal returned by SimulateContract
// for calls to one fn on one contract, taking precedence over the per-fn and
// global setters. Needed when contracts share fn names: OnRamp, FeeQuoter and
// Executor all expose get_dest_chain_config. Setting a result clears a
// previously set error for the same (contract, fn) — last write wins — so a
// test can first stub not-configured wholesale and then flip one read to a
// configured result.
func (r *RecordingInvoker) WithSimulateResultForContract(contractID, fn string, v *xdr.ScVal) *RecordingInvoker {
	r.mu.Lock()
	defer r.mu.Unlock()
	pair := invokeKey{contractID, fn}
	r.simulateByPair[pair] = v
	delete(r.errByPair, pair)
	return r
}

// WithSimulateErrorForFn makes SimulateContract return err for a specific
// function name. The bindings clients wrap the invoker error with %w, so the
// message text (e.g. "Error(Contract, #37)") reaches IsContractErrorCode
// unchanged — the same path a real RPC takes. Setting an error clears a
// previously set result for the same fn (last write wins).
func (r *RecordingInvoker) WithSimulateErrorForFn(fn string, err error) *RecordingInvoker {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errByFn[fn] = err
	delete(r.simulateByFn, fn)
	return r
}

// WithSimulateErrorForContract makes SimulateContract return err for calls to
// one fn on one contract, taking precedence over the per-fn setters. Use it to
// stub not-configured errors when contracts share fn names
// (get_dest_chain_config renders #37 on OnRamp but #25 on FeeQuoter/Executor).
// Setting an error clears a previously set result for the same
// (contract, fn) — last write wins.
func (r *RecordingInvoker) WithSimulateErrorForContract(contractID, fn string, err error) *RecordingInvoker {
	r.mu.Lock()
	defer r.mu.Unlock()
	pair := invokeKey{contractID, fn}
	r.errByPair[pair] = err
	delete(r.simulateByPair, pair)
	return r
}

func (r *RecordingInvoker) InvokeContract(_ context.Context, contractID, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	r.mu.Lock()
	r.records = append(r.records, InvokeRecord{ContractID: contractID, Fn: functionName, Args: args})
	r.mu.Unlock()
	return nil, nil
}

// SimulateContract consults stubs in precedence order: per-(contract, fn)
// error, per-fn error, per-(contract, fn) result, per-fn result, global result.
func (r *RecordingInvoker) SimulateContract(_ context.Context, contractID, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	r.mu.Lock()
	r.records = append(r.records, InvokeRecord{ContractID: contractID, Fn: functionName, Args: args})
	defer r.mu.Unlock()
	pair := invokeKey{contractID, functionName}
	if err, ok := r.errByPair[pair]; ok {
		return nil, err
	}
	if err, ok := r.errByFn[functionName]; ok {
		return nil, err
	}
	if v, ok := r.simulateByPair[pair]; ok {
		return v, nil
	}
	if v, ok := r.simulateByFn[functionName]; ok {
		return v, nil
	}
	return r.simulateResult, nil
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
