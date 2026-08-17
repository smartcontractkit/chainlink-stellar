package deployment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// captureUploadedWASM stops the deployer at simulation and records the WASM
// payload of the upload host function, so the two upload entry points can be
// compared without a network.
func captureUploadedWASM(t *testing.T, got *[]byte) func(context.Context, protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
	t.Helper()
	return func(_ context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error) {
		var env xdr.TransactionEnvelope
		require.NoError(t, xdr.SafeUnmarshalBase64(req.Transaction, &env))

		ops := env.Operations()
		require.Len(t, ops, 1)
		op := ops[0].Body.InvokeHostFunctionOp
		require.NotNil(t, op)
		require.Equal(t, xdr.HostFunctionTypeHostFunctionTypeUploadContractWasm, op.HostFunction.Type)
		require.NotNil(t, op.HostFunction.Wasm)
		*got = *op.HostFunction.Wasm

		return protocolrpc.SimulateTransactionResponse{}, fmt.Errorf("stop after capture")
	}
}

func TestUploadContractWASMBytes(t *testing.T) {
	wasm := append([]byte("\x00asm\x01\x00\x00\x00"), []byte("payload")...)

	var fromBytes []byte
	bytesMock := &mockRPC{}
	bytesMock.SimulateTransactionFn = captureUploadedWASM(t, &fromBytes)
	_, err := newTestDeployer(t, bytesMock).UploadContractWASMBytes(context.Background(), wasm)
	require.Error(t, err) // capture aborts the flow; the payload is what matters
	require.Equal(t, wasm, fromBytes)

	// The file-path wrapper must upload a byte-identical payload.
	path := filepath.Join(t.TempDir(), "contract.wasm")
	require.NoError(t, os.WriteFile(path, wasm, 0o600))

	var fromPath []byte
	pathMock := &mockRPC{}
	pathMock.SimulateTransactionFn = captureUploadedWASM(t, &fromPath)
	pathDeployer := newTestDeployer(t, pathMock)
	_, err = pathDeployer.UploadContractWASM(context.Background(), path)
	require.Error(t, err)
	require.Equal(t, fromBytes, fromPath)

	// A missing file fails on the read, before any RPC call.
	_, err = pathDeployer.UploadContractWASM(context.Background(), filepath.Join(t.TempDir(), "missing.wasm"))
	require.ErrorContains(t, err, "failed to read WASM file")
}
