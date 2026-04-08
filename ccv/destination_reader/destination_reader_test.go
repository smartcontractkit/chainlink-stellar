package destinationreader

import (
	"context"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/bindings"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/internal/mocks"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	// Valid Soroban contract strkey (fixtures / human-readable).
	testStellarContractID = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
)

// Second contract strkey for RMN vs OffRamp IDs (GetCCVSForMessage uses ContractId bytes for all C addresses).
var testStellarContractID2 string

func init() {
	raw := [32]byte{7: 0x3a, 31: 0x01}
	s, err := strkey.Encode(strkey.VersionByteContract, raw[:])
	if err != nil {
		panic(err)
	}
	testStellarContractID2 = s
}

func testLogger(t *testing.T) *zerolog.Logger {
	t.Helper()
	z := zerolog.New(io.Discard).Level(zerolog.Disabled)
	return &z
}

func testRPCClient(t *testing.T) *rpcclient.Client {
	t.Helper()
	return rpcclient.NewClient("http://127.0.0.1:9", &http.Client{Timeout: time.Millisecond})
}

func mustTestMessage(t *testing.T) protocol.Message {
	t.Helper()
	onRamp, err := protocol.RandomAddress()
	require.NoError(t, err)
	offRamp, err := protocol.RandomAddress()
	require.NoError(t, err)
	sender, err := protocol.RandomAddress()
	require.NoError(t, err)
	receiver, err := protocol.RandomAddress()
	require.NoError(t, err)
	msg, err := protocol.NewMessage(
		protocol.ChainSelector(1337),
		protocol.ChainSelector(2337),
		protocol.SequenceNumber(1),
		onRamp,
		offRamp,
		protocol.Finality(0),
		200_000,
		100_000,
		protocol.Bytes32{},
		sender,
		receiver,
		nil,
		[]byte("data"),
		nil,
	)
	require.NoError(t, err)
	return *msg
}

func TestNew_validation(t *testing.T) {
	lg := testLogger(t)
	rpc := testRPCClient(t)
	contract := "CCONTRACTAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAADGKZ"
	d := deployment.NewDeployer(rpc, "Standalone Network ; February 2017", keypair.MustRandom())

	tests := []struct {
		name    string
		invoker bindings.Invoker
		rpc     ccvclient.RPCClient
		off     string
		rmn     string
		log     *zerolog.Logger
		wantSub string
	}{
		{name: "nil invoker", invoker: nil, rpc: rpc, off: contract, rmn: contract, log: lg, wantSub: "invoker is required"},
		{name: "nil rpc", invoker: d, rpc: nil, off: contract, rmn: contract, log: lg, wantSub: "rpc client is required"},
		{name: "empty offramp", invoker: d, rpc: rpc, off: "", rmn: contract, log: lg, wantSub: "offramp contract ID is required"},
		{name: "empty rmn", invoker: d, rpc: rpc, off: contract, rmn: "", log: lg, wantSub: "rmn remote contract ID is required"},
		{name: "nil logger", invoker: d, rpc: rpc, off: contract, rmn: contract, log: nil, wantSub: "logger is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.invoker, tt.rpc, tt.off, tt.rmn, tt.log, time.Minute)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestNew_successAndClose(t *testing.T) {
	inv := mocks.NewMockInvoker(t)
	d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.NoError(t, d.Close())
}

func TestNewStellarExecutionAttemptPoller_validation(t *testing.T) {
	rpc := testRPCClient(t)
	lg := testLogger(t)

	tests := []struct {
		name    string
		rpc     ccvclient.RPCClient
		ctr     string
		log     *zerolog.Logger
		wantSub string
	}{
		{rpc: nil, ctr: testStellarContractID, log: lg, wantSub: "rpc client cannot be nil"},
		{rpc: rpc, ctr: testStellarContractID, log: nil, wantSub: "logger cannot be nil"},
		{rpc: rpc, ctr: "", log: lg, wantSub: "offramp contract ID cannot be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.wantSub, func(t *testing.T) {
			p, err := NewStellarExecutionAttemptPoller(tt.rpc, tt.ctr, tt.log, time.Minute)
			require.Error(t, err)
			require.Nil(t, p)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestDestinationReader_GetMessageSuccess(t *testing.T) {
	ctx := context.Background()
	msg := mustTestMessage(t)

	t.Run("true when success state", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		stateScVal, err := offrampbindings.MessageExecutionStateSuccess.ToScVal()
		require.NoError(t, err)
		inv.On("SimulateContract", mock.Anything, testStellarContractID, "get_execution_state", mock.Anything).
			Return(&stateScVal, nil).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		ok, err := d.GetMessageSuccess(ctx, msg)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("false when untouched", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		stateScVal, err := offrampbindings.MessageExecutionStateUntouched.ToScVal()
		require.NoError(t, err)
		inv.On("SimulateContract", mock.Anything, testStellarContractID, "get_execution_state", mock.Anything).
			Return(&stateScVal, nil).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		ok, err := d.GetMessageSuccess(ctx, msg)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("wraps simulate error", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		inv.On("SimulateContract", mock.Anything, testStellarContractID, "get_execution_state", mock.Anything).
			Return((*xdr.ScVal)(nil), assert.AnError).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		_, err = d.GetMessageSuccess(ctx, msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get execution state")
	})
}

func TestDestinationReader_GetCCVSForMessage(t *testing.T) {
	ctx := context.Background()
	msg := mustTestMessage(t)

	t.Run("optional threshold 1 when defaults present", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		cfg := offrampbindings.SourceChainConfig{
			DefaultCcvs:      []string{testStellarContractID},
			IsEnabled:        true,
			LaneMandatedCcvs: []string{testStellarContractID2},
			Router:           testStellarContractID,
		}
		cfgScVal, err := cfg.ToScVal()
		require.NoError(t, err)
		inv.On("SimulateContract", mock.Anything, testStellarContractID, "get_source_chain_config", mock.Anything).
			Return(&cfgScVal, nil).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		info, err := d.GetCCVSForMessage(ctx, msg)
		require.NoError(t, err)
		require.Len(t, info.RequiredCCVs, 1)
		require.Len(t, info.OptionalCCVs, 1)
		assert.Equal(t, uint8(1), info.OptionalThreshold)
	})

	t.Run("optional threshold 0 when no defaults", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		cfg := offrampbindings.SourceChainConfig{
			LaneMandatedCcvs: []string{testStellarContractID2},
			Router:           testStellarContractID,
		}
		cfgScVal, err := cfg.ToScVal()
		require.NoError(t, err)
		inv.On("SimulateContract", mock.Anything, testStellarContractID, "get_source_chain_config", mock.Anything).
			Return(&cfgScVal, nil).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		info, err := d.GetCCVSForMessage(ctx, msg)
		require.NoError(t, err)
		assert.Empty(t, info.OptionalCCVs)
		assert.Equal(t, uint8(0), info.OptionalThreshold)
	})

	t.Run("wraps error when get_source_chain_config fails", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		inv.On("SimulateContract", mock.Anything, testStellarContractID, "get_source_chain_config", mock.Anything).
			Return((*xdr.ScVal)(nil), assert.AnError).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		_, err = d.GetCCVSForMessage(ctx, msg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get source chain config")
	})
}

func TestDestinationReader_GetRMNCursedSubjects(t *testing.T) {
	ctx := context.Background()

	t.Run("decodes vec", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		s1 := [16]byte{1}
		s2 := [16]byte{2}
		vecScVal := scval.Bytes16SliceToScVal([][16]byte{s1, s2})
		inv.On("SimulateContract", mock.Anything, testStellarContractID2, "get_cursed_subjects", mock.Anything).
			Return(&vecScVal, nil).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		out, err := d.GetRMNCursedSubjects(ctx)
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, protocol.Bytes16(s1), out[0])
		assert.Equal(t, protocol.Bytes16(s2), out[1])
	})

	t.Run("wraps error", func(t *testing.T) {
		inv := mocks.NewMockInvoker(t)
		inv.On("SimulateContract", mock.Anything, testStellarContractID2, "get_cursed_subjects", mock.Anything).
			Return((*xdr.ScVal)(nil), assert.AnError).Once()

		d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
		require.NoError(t, err)
		t.Cleanup(func() { _ = d.Close() })

		_, err = d.GetRMNCursedSubjects(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get cursed subjects")
	})
}

func TestDestinationReader_serviceSurface(t *testing.T) {
	inv := mocks.NewMockInvoker(t)
	d, err := New(inv, testRPCClient(t), testStellarContractID, testStellarContractID2, testLogger(t), time.Minute)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })

	assert.Equal(t, "StellarDestinationReader", d.Name())
	require.NoError(t, d.Ready())
	report := d.HealthReport()
	assert.Contains(t, report, "StellarDestinationReader")
	assert.Nil(t, report["StellarDestinationReader"])
	assert.Contains(t, report, stellarPollerServiceName)
}

func TestStellarExecutionAttemptPoller_GetExecutionAttempts_cacheMiss(t *testing.T) {
	p, err := NewStellarExecutionAttemptPoller(testRPCClient(t), testStellarContractID, testLogger(t), time.Minute)
	require.NoError(t, err)

	attempts, err := p.GetExecutionAttempts(context.Background(), mustTestMessage(t))
	require.NoError(t, err)
	assert.Nil(t, attempts)
}

func TestDecodeExecuteArgsToAttempt(t *testing.T) {
	t.Run("wrong arg count", func(t *testing.T) {
		_, err := decodeExecuteArgsToAttempt([]xdr.ScVal{{}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected 4 args")
	})

	t.Run("success", func(t *testing.T) {
		msg := mustTestMessage(t)
		encoded, err := msg.Encode()
		require.NoError(t, err)
		args := []xdr.ScVal{
			scval.BytesToScVal(encoded),
			scval.AddressSliceToScVal([]string{testStellarContractID}),
			scval.BytesSliceToScVal([][]byte{{0xab, 0xcd}}),
			scval.Uint32ToScVal(4242),
		}
		attempt, err := decodeExecuteArgsToAttempt(args)
		require.NoError(t, err)
		assert.Equal(t, big.NewInt(4242), attempt.TransactionGasLimit)
		idWant, err := msg.MessageID()
		require.NoError(t, err)
		assert.Equal(t, idWant, attempt.Report.Message.MustMessageID())
	})
}

func TestExtractInvokeContractArgs_invalidEnvelope(t *testing.T) {
	_, err := extractInvokeContractArgs("not-valid-base64-!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal transaction envelope")
}
