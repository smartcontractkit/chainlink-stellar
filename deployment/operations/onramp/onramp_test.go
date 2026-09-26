package onramp_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type invokeRecord struct {
	contractID string
	fn         string
	args       []xdr.ScVal
}

type recordingInvoker struct {
	mu      sync.Mutex
	records []invokeRecord
}

func (r *recordingInvoker) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	r.mu.Lock()
	r.records = append(r.records, invokeRecord{contractID: contractID, fn: functionName, args: args})
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

func TestWithdrawFeeTokens_operation(t *testing.T) {
	t.Parallel()
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Invoker: inv}
	feeTokens := []string{
		stellarutil.MustGenerateMockContractID("deployer", "withdraw-fee-onramp-op-test"),
		keypair.MustRandom().Address(),
	}

	report, err := cldfops.ExecuteOperation(testBundle(t), onramp.WithdrawFeeTokens, deps, onramp.WithdrawFeeTokensInput{
		ContractID: "CONRAMPTESTFEE0000000000000000000000000000000000",
		FeeTokens:  feeTokens,
	})
	require.NoError(t, err)
	require.Nil(t, report.Err)

	rec := inv.last()
	require.Equal(t, "CONRAMPTESTFEE0000000000000000000000000000000000", rec.contractID)
	require.Equal(t, "withdraw_fee_tokens", rec.fn)
	require.Len(t, rec.args, 1)
	vec, ok := rec.args[0].GetVec()
	require.True(t, ok, "first argument should be a vector of addresses")
	require.Len(t, *vec, len(feeTokens))
	for i, want := range feeTokens {
		got, err := scval.AddressFromScVal((*vec)[i])
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestUpgrade_operation(t *testing.T) {
	t.Parallel()
	inv := &recordingInvoker{}
	deps := stellardeps.StellarDeps{Invoker: inv}
	cid := "CONRAMPUPGRADE00000000000000000000000000000000000"
	var hash [32]byte
	hash[0] = 0xAB

	_, err := cldfops.ExecuteOperation(testBundle(t), onramp.Upgrade, deps, onramp.UpgradeInput{
		ContractID:  cid,
		NewWasmHash: hash,
	})
	require.NoError(t, err)

	rec := inv.last()
	require.Equal(t, cid, rec.contractID)
	require.Equal(t, "upgrade", rec.fn)
	require.Len(t, rec.args, 1)
	got, err := scval.Bytes32FromScVal(rec.args[0])
	require.NoError(t, err)
	require.Equal(t, hash, got)
}
