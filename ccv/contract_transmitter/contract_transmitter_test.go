package contracttransmitter

import (
	"context"
	"encoding/binary"
	"errors"
	"math/big"
	"os"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccv/executor"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/internal/mocks"
)

func testLogger() *zerolog.Logger {
	lggr := zerolog.New(os.Stdout).With().Timestamp().Logger().Level(zerolog.DebugLevel)
	return &lggr
}

func testAddress(index byte) []byte {
	addr := make([]byte, 32)
	addr[0] = index
	return addr
}

func mustCreateMessage(t *testing.T, sourceChain, destChain uint64, seqNum uint64, gasLimit uint32) protocol.Message {
	t.Helper()
	msg, err := protocol.NewMessage(
		protocol.ChainSelector(sourceChain),
		protocol.ChainSelector(destChain),
		protocol.SequenceNumber(seqNum),
		testAddress(0x01), // onRampAddress
		testAddress(0x02), // offRampAddress
		1,                 // finality
		gasLimit,
		gasLimit,           // ccipReceiveGasLimit
		protocol.Bytes32{}, // ccvAndExecutorHash
		testAddress(0x03),  // sender
		testAddress(0x04),  // receiver
		[]byte{},           // destBlob
		[]byte("test"),     // data
		nil,                // tokenTransfer
	)
	require.NoError(t, err)
	return *msg
}

// ---------------------------------------------------------------------------
// ConvertAndWriteMessageToChain tests
// ---------------------------------------------------------------------------

func TestConvertAndWriteMessageToChain(t *testing.T) {
	lggr := testLogger()
	offRampID := "COFFRAMP"

	ccv1 := testAddress(0xAA)
	ccv2 := testAddress(0xBB)

	t.Run("successful transmission", func(t *testing.T) {
		mockInvoker := mocks.NewMockInvoker(t)

		var capturedContractID, capturedFuncName string
		var capturedArgs []xdr.ScVal

		mockInvoker.On("InvokeContract", mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.Anything).
			Run(func(args mock.Arguments) {
				capturedContractID = args.Get(1).(string)
				capturedFuncName = args.Get(2).(string)
				capturedArgs = args.Get(3).([]xdr.ScVal)
			}).
			Return((*xdr.ScVal)(nil), nil).
			Once()

		ct, err := NewContractTransmitterWithClient(mockInvoker, offRampID, "statechanged", "RMNREMOTE", lggr)
		require.NoError(t, err)

		msg := mustCreateMessage(t, 1, 2, 100, 500000)
		report := protocol.AbstractAggregatedReport{
			CCVS:    []protocol.UnknownAddress{ccv1, ccv2},
			CCVData: [][]byte{{0x01, 0x02}, {0x03, 0x04}},
			Message: msg,
		}

		err = ct.ConvertAndWriteMessageToChain(context.Background(), report)
		require.NoError(t, err)

		assert.Equal(t, offRampID, capturedContractID)
		assert.Equal(t, "execute", capturedFuncName)
		require.Len(t, capturedArgs, 4, "execute expects 4 args: encoded_message, ccvs, verifier_results, gas_limit_override")

		// arg[0]: encoded_message (Bytes)
		encodedMsg, ok := capturedArgs[0].GetBytes()
		assert.True(t, ok, "first arg should be bytes")
		expectedEncodedMsg, _ := msg.Encode()
		assert.Equal(t, xdr.ScBytes(expectedEncodedMsg), encodedMsg)

		// arg[1]: ccvs (Vec<Address>)
		ccvsVec, ok := capturedArgs[1].GetVec()
		require.True(t, ok, "second arg should be a vec")
		require.NotNil(t, ccvsVec)
		assert.Len(t, *ccvsVec, 2)

		// arg[2]: verifier_results (Vec<Bytes>)
		resultsVec, ok := capturedArgs[2].GetVec()
		require.True(t, ok, "third arg should be a vec")
		require.NotNil(t, resultsVec)
		assert.Len(t, *resultsVec, 2)

		// arg[3]: gas_limit_override (u32)
		gasOverride, ok := capturedArgs[3].GetU32()
		assert.True(t, ok, "fourth arg should be u32")
		assert.Equal(t, xdr.Uint32(DefaultGasLimitOverride), gasOverride)
	})

	t.Run("empty CCVs and CCVData", func(t *testing.T) {
		mockInvoker := mocks.NewMockInvoker(t)

		var capturedArgs []xdr.ScVal
		mockInvoker.On("InvokeContract", mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.Anything).
			Run(func(args mock.Arguments) {
				capturedArgs = args.Get(3).([]xdr.ScVal)
			}).
			Return((*xdr.ScVal)(nil), nil).
			Once()

		ct, err := NewContractTransmitterWithClient(mockInvoker, offRampID, "statechanged", "RMNREMOTE", lggr)
		require.NoError(t, err)

		report := protocol.AbstractAggregatedReport{
			CCVS:    []protocol.UnknownAddress{},
			CCVData: [][]byte{},
			Message: mustCreateMessage(t, 1, 2, 101, 300000),
		}

		err = ct.ConvertAndWriteMessageToChain(context.Background(), report)
		require.NoError(t, err)

		// ccvs vec should be empty
		ccvsVec, ok := capturedArgs[1].GetVec()
		require.True(t, ok)
		assert.Len(t, *ccvsVec, 0)

		// verifier_results vec should be empty
		resultsVec, ok := capturedArgs[2].GetVec()
		require.True(t, ok)
		assert.Len(t, *resultsVec, 0)
	})

	t.Run("message encoding error returns ErrMessageEncoding", func(t *testing.T) {
		mockInvoker := mocks.NewMockInvoker(t)

		ct, err := NewContractTransmitterWithClient(mockInvoker, offRampID, "statechanged", "RMNREMOTE", lggr)
		require.NoError(t, err)

		report := protocol.AbstractAggregatedReport{
			Message: protocol.Message{
				SenderLength: 99, // mismatch triggers validation error
				Sender:       []byte{0x01},
			},
		}

		err = ct.ConvertAndWriteMessageToChain(context.Background(), report)
		require.Error(t, err)
		assert.True(t, errors.Is(err, executor.ErrMessageEncoding),
			"should wrap executor.ErrMessageEncoding so the executor skips retries")
	})

	t.Run("invoker error is propagated", func(t *testing.T) {
		mockInvoker := mocks.NewMockInvoker(t)

		invokeErr := errors.New("soroban rpc unavailable")
		mockInvoker.On("InvokeContract", mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.Anything).
			Return((*xdr.ScVal)(nil), invokeErr).
			Once()

		ct, err := NewContractTransmitterWithClient(mockInvoker, offRampID, "statechanged", "RMNREMOTE", lggr)
		require.NoError(t, err)

		report := protocol.AbstractAggregatedReport{
			CCVS:    []protocol.UnknownAddress{ccv1},
			CCVData: [][]byte{{0x01}},
			Message: mustCreateMessage(t, 1, 2, 103, 300000),
		}

		err = ct.ConvertAndWriteMessageToChain(context.Background(), report)
		require.Error(t, err)
		assert.ErrorIs(t, err, invokeErr)
		assert.Contains(t, err.Error(), "failed to call execute")
	})

	t.Run("invoker receives the correct contract ID", func(t *testing.T) {
		mockInvoker := mocks.NewMockInvoker(t)

		customOffRampID := "CDXHXQJHQVFBPHKBYB73MA5KZAWWHBXWZRKQJDTY4ZYR3GYQHXADOKEP"
		var capturedID string
		mockInvoker.On("InvokeContract", mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.Anything).
			Run(func(args mock.Arguments) {
				capturedID = args.Get(1).(string)
			}).
			Return((*xdr.ScVal)(nil), nil).
			Once()

		ct, err := NewContractTransmitterWithClient(mockInvoker, customOffRampID, "statechanged", "RMNREMOTE", lggr)
		require.NoError(t, err)

		report := protocol.AbstractAggregatedReport{
			CCVS:    []protocol.UnknownAddress{},
			CCVData: [][]byte{},
			Message: mustCreateMessage(t, 1, 2, 104, 300000),
		}

		err = ct.ConvertAndWriteMessageToChain(context.Background(), report)
		require.NoError(t, err)
		assert.Equal(t, customOffRampID, capturedID)
	})
}

// ---------------------------------------------------------------------------
// ConvertVerifierBlobToEIP2098 tests
// ---------------------------------------------------------------------------

// buildV27Blob constructs a verifier result blob in the CCV aggregator's format:
// [4B version_tag][2B BE sig_payload_len][N × 64B R||S]
func buildV27Blob(versionTag [4]byte, sigs [][64]byte) []byte {
	sigPayloadLen := len(sigs) * 64
	blob := make([]byte, 6+sigPayloadLen)
	copy(blob[:4], versionTag[:])
	binary.BigEndian.PutUint16(blob[4:6], uint16(sigPayloadLen))
	for i, sig := range sigs {
		copy(blob[6+i*64:6+(i+1)*64], sig[:])
	}
	return blob
}

func TestConvertVerifierBlobToEIP2098(t *testing.T) {
	versionTag := stellarutil.DefaultCommitteeVerifierVersionTag()

	t.Run("recovery_id=0: low-S signature unchanged", func(t *testing.T) {
		// Construct a synthetic v=27-normalized R||S where S <= n/2 (recovery_id=0)
		var r32 [32]byte
		r32[31] = 0x01

		// Pick S = 42, which is trivially <= n/2
		sOriginal := new(big.Int).SetInt64(42)
		require.True(t, sOriginal.Cmp(secp256k1HalfN) <= 0, "test precondition: S must be <= n/2")

		var s32 [32]byte
		sBytes := sOriginal.Bytes()
		copy(s32[32-len(sBytes):], sBytes)

		var sig [64]byte
		copy(sig[:32], r32[:])
		copy(sig[32:], s32[:])

		blob := buildV27Blob(versionTag, [][64]byte{sig})
		result, err := ConvertVerifierBlobToEIP2098(blob)
		require.NoError(t, err)

		// For low-S, the output should be identical to input (high bit clear)
		assert.Equal(t, blob, result)
		assert.Equal(t, byte(0), result[6+32]&0x80, "high bit of S should be 0 for recovery_id=0")
	})

	t.Run("recovery_id=1: high-S is un-flipped and bit 255 set", func(t *testing.T) {
		// Create a synthetic v=27-normalized signature where S > n/2,
		// meaning original v was 28 (recovery_id=1)
		var r32 [32]byte
		r32[31] = 0x42

		// Choose S_original < n/2, then S_v27_norm = n - S_original (which will be > n/2)
		sOriginal := new(big.Int).SetInt64(12345678)
		sNormalized := new(big.Int).Sub(secp256k1N, sOriginal)
		require.True(t, sNormalized.Cmp(secp256k1HalfN) > 0, "test precondition: S_norm must be > n/2")

		var s32 [32]byte
		sBytes := sNormalized.Bytes()
		copy(s32[32-len(sBytes):], sBytes)

		var sig [64]byte
		copy(sig[:32], r32[:])
		copy(sig[32:], s32[:])

		blob := buildV27Blob(versionTag, [][64]byte{sig})
		result, err := ConvertVerifierBlobToEIP2098(blob)
		require.NoError(t, err)

		// Extract the converted S
		resultS := result[6+32 : 6+64]
		assert.Equal(t, byte(0x80), resultS[0]&0x80, "high bit must be set for recovery_id=1")

		// Clear the high bit and check S equals the original
		resultSClean := make([]byte, 32)
		copy(resultSClean, resultS)
		resultSClean[0] &= 0x7F
		sRecovered := new(big.Int).SetBytes(resultSClean)
		assert.Equal(t, sOriginal, sRecovered, "S must equal the original pre-flip value")
	})

	t.Run("multiple signatures in one blob", func(t *testing.T) {
		sOriginal := new(big.Int).SetInt64(99999)
		sNorm := new(big.Int).Sub(secp256k1N, sOriginal) // high-S
		var sig1, sig2 [64]byte
		sig1[31] = 0x01
		sNormBytes := sNorm.Bytes()
		copy(sig1[64-len(sNormBytes):], sNormBytes)

		// sig2: low-S (recovery_id=0)
		sig2[31] = 0x02
		sig2[63] = 0x07

		blob := buildV27Blob(versionTag, [][64]byte{sig1, sig2})
		result, err := ConvertVerifierBlobToEIP2098(blob)
		require.NoError(t, err)

		// sig1: high bit should be set
		assert.Equal(t, byte(0x80), result[6+32]&0x80)
		// sig2: should be unchanged
		assert.Equal(t, byte(0x00), result[6+64+32]&0x80)
		assert.Equal(t, byte(0x07), result[6+64+63])
	})

	t.Run("empty blob is passed through", func(t *testing.T) {
		vt := stellarutil.DefaultCommitteeVerifierVersionTag()
		blob := append(append([]byte(nil), vt[:]...), 0x00, 0x00)
		result, err := ConvertVerifierBlobToEIP2098(blob)
		require.NoError(t, err)
		assert.Equal(t, blob, result)
	})

	t.Run("short blob is passed through", func(t *testing.T) {
		blob := []byte{0x01, 0x02}
		result, err := ConvertVerifierBlobToEIP2098(blob)
		require.NoError(t, err)
		assert.Equal(t, blob, result)
	})

	t.Run("malformed signature length returns error", func(t *testing.T) {
		// sig_len = 63 (not a multiple of 64)
		blob := make([]byte, 6+63)
		copy(blob[:4], versionTag[:])
		binary.BigEndian.PutUint16(blob[4:6], 63)
		_, err := ConvertVerifierBlobToEIP2098(blob)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a multiple of")
	})
}
