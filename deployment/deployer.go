package deployment

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Compile-time check that Deployer satisfies the common bindings.Invoker interface.
var _ bindings.Invoker = (*Deployer)(nil)

// sorobanSimulationInstructionLeeway is extra CPU instructions allowed during simulateTransaction
// (Soroban RPC resourceConfig.instructionLeeway). CCIP Router→OnRamp→TokenPool→SAC auth trees
// can exceed the default simulation ceiling; 100M was still returning ExceededLimit in integration.
const sorobanSimulationInstructionLeeway uint64 = 2_000_000_000

// sorobanSimResourceConfig sets instruction leeway for simulateTransaction so the RPC can
// measure large multi-contract invocations (e.g. Router → OnRamp → pool). With the default
// leeway of 0, simulation may return HostError(Budget, ExceededLimit) even when the submitted
// transaction is valid after assembly.
func sorobanSimResourceConfig() *protocolrpc.ResourceConfig {
	return &protocolrpc.ResourceConfig{
		InstructionLeeway: sorobanSimulationInstructionLeeway,
	}
}

// stellarRPCClient abstracts the Soroban RPC methods used by Deployer,
// allowing tests to inject a mock without hitting a real network.
type stellarRPCClient interface {
	SimulateTransaction(ctx context.Context, req protocolrpc.SimulateTransactionRequest) (protocolrpc.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocolrpc.SendTransactionRequest) (protocolrpc.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocolrpc.GetTransactionRequest) (protocolrpc.GetTransactionResponse, error)
	GetLedgerEntries(ctx context.Context, req protocolrpc.GetLedgerEntriesRequest) (protocolrpc.GetLedgerEntriesResponse, error)
	GetEvents(ctx context.Context, req protocolrpc.GetEventsRequest) (protocolrpc.GetEventsResponse, error)
}

// DeployerOption configures optional Deployer behaviour.
type DeployerOption func(*Deployer)

// WithAutoRestore controls whether the Deployer automatically submits a
// RestoreFootprint transaction when simulation indicates that persistent
// ledger entries have expired. Enabled by default.
func WithAutoRestore(enabled bool) DeployerOption {
	return func(d *Deployer) { d.autoRestore = enabled }
}

// Deployer handles Soroban contract deployment and initialization.
type Deployer struct {
	rpcClient         stellarRPCClient
	networkPassphrase string
	signer            *keypair.Full
	// Account sequence number tracking
	accountSequence int64
	// autoRestore controls automatic RestoreFootprint handling for expired
	// persistent ledger entries. True by default.
	autoRestore bool
}

// NewDeployer creates a new Deployer instance. Options can be passed to
// customise behaviour (e.g. WithAutoRestore(false) to disable automatic
// restoration of expired persistent entries).
func NewDeployer(rpcClient *rpcclient.Client, networkPassphrase string, signer *keypair.Full, opts ...DeployerOption) *Deployer {
	d := &Deployer{
		rpcClient:         rpcClient,
		networkPassphrase: networkPassphrase,
		signer:            signer,
		accountSequence:   -1,
		autoRestore:       true,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// DeployContract deploys a Soroban contract from a WASM file and returns the contract ID.
// This performs two operations:
// 1. Upload the WASM code (installContractCode)
// 2. Deploy a contract instance (createContract)
func (d *Deployer) DeployContract(ctx context.Context, wasmPath string, salt [32]byte) (string, error) {
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return "", fmt.Errorf("failed to read WASM file: %w", err)
	}

	wasmHash, err := d.uploadWASM(ctx, wasmBytes)
	if err != nil {
		return "", fmt.Errorf("failed to upload WASM: %w", err)
	}

	// Create contract instance
	contractID, err := d.createContractInstance(ctx, wasmHash, salt)
	if err != nil {
		return "", fmt.Errorf("failed to create contract instance: %w", err)
	}

	return contractID, nil
}

// uploadWASM uploads WASM code to the network and returns the code hash.
func (d *Deployer) uploadWASM(ctx context.Context, wasmBytes []byte) (xdr.Hash, error) {
	// Get source account
	sourceAccount, err := d.getSourceAccount(ctx)
	if err != nil {
		return xdr.Hash{}, fmt.Errorf("failed to get source account: %w", err)
	}

	// Build upload WASM operation
	uploadOp := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeUploadContractWasm,
			Wasm: &wasmBytes,
		},
		SourceAccount: d.signer.Address(),
	}

	// Build and submit transaction
	resultMeta, err := d.buildAndSubmitTransaction(ctx, sourceAccount, uploadOp)
	if err != nil {
		return xdr.Hash{}, err
	}

	// Extract WASM hash from result
	wasmHash, err := extractWASMHash(resultMeta)
	if err != nil {
		return xdr.Hash{}, fmt.Errorf("failed to extract WASM hash: %w", err)
	}

	return wasmHash, nil
}

// createContractInstance creates a new contract instance from uploaded WASM code.
func (d *Deployer) createContractInstance(ctx context.Context, wasmHash xdr.Hash, salt [32]byte) (string, error) {
	// Get source account
	sourceAccount, err := d.getSourceAccount(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get source account: %w", err)
	}

	// Get deployer's public key bytes
	pubKeyBytes, err := strkey.Decode(strkey.VersionByteAccountID, d.signer.Address())
	if err != nil {
		return "", fmt.Errorf("failed to decode public key: %w", err)
	}
	var pubKey256 xdr.Uint256
	copy(pubKey256[:], pubKeyBytes)

	// Build create contract operation
	createOp := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeCreateContract,
			CreateContract: &xdr.CreateContractArgs{
				ContractIdPreimage: xdr.ContractIdPreimage{
					Type: xdr.ContractIdPreimageTypeContractIdPreimageFromAddress,
					FromAddress: &xdr.ContractIdPreimageFromAddress{
						Address: xdr.ScAddress{
							Type: xdr.ScAddressTypeScAddressTypeAccount,
							AccountId: &xdr.AccountId{
								Type:    xdr.PublicKeyTypePublicKeyTypeEd25519,
								Ed25519: &pubKey256,
							},
						},
						Salt: xdr.Uint256(salt),
					},
				},
				Executable: xdr.ContractExecutable{
					Type:     xdr.ContractExecutableTypeContractExecutableWasm,
					WasmHash: &wasmHash,
				},
			},
		},
		SourceAccount: d.signer.Address(),
	}

	// Build and submit transaction
	resultMeta, err := d.buildAndSubmitTransaction(ctx, sourceAccount, createOp)
	if err != nil {
		return "", err
	}

	fmt.Printf("createContractInstance result: %+v\n", resultMeta)

	// Extract contract ID from result
	contractID, err := extractContractID(resultMeta)
	if err != nil {
		return "", fmt.Errorf("failed to extract contract ID: %w", err)
	}

	return contractID, nil
}

// InvokeContract invokes a contract function and returns the result.
func (d *Deployer) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	// Get source account
	sourceAccount, err := d.getSourceAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get source account: %w", err)
	}

	// Decode contract ID to get raw bytes
	contractBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return nil, fmt.Errorf("failed to decode contract ID: %w", err)
	}

	// Build contract address using XDR marshaling for proper type handling
	contractAddr := scval.BuildContractScAddress(contractBytes)
	if contractAddr == nil {
		return nil, fmt.Errorf("failed to build contract address")
	}

	// Build invoke operation
	invokeOp := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: *contractAddr,
				FunctionName:    xdr.ScSymbol(functionName),
				Args:            args,
			},
		},
		SourceAccount: d.signer.Address(),
	}

	// Build and submit transaction
	resultMeta, err := d.buildAndSubmitTransaction(ctx, sourceAccount, invokeOp)
	if err != nil {
		return nil, err
	}

	// Extract return value from result
	returnVal, err := extractReturnValue(resultMeta)
	if err != nil {
		return nil, fmt.Errorf("failed to extract return value: %w", err)
	}

	return returnVal, nil
}

// SimulateContract simulates a contract invocation without submitting.
func (d *Deployer) SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	// Decode contract ID to get raw bytes
	contractBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return nil, fmt.Errorf("failed to decode contract ID: %w", err)
	}

	// Build contract address using XDR marshaling for proper type handling
	contractAddr := scval.BuildContractScAddress(contractBytes)
	if contractAddr == nil {
		return nil, fmt.Errorf("failed to build contract address")
	}

	// Build invoke host function
	hostFn := xdr.HostFunction{
		Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
		InvokeContract: &xdr.InvokeContractArgs{
			ContractAddress: *contractAddr,
			FunctionName:    xdr.ScSymbol(functionName),
			Args:            args,
		},
	}

	// Get source account
	sourceAccount, err := d.getSourceAccount(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get source account: %w", err)
	}

	// Build a transaction for simulation
	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        sourceAccount,
			IncrementSequenceNum: true,
			Operations: []txnbuild.Operation{
				&txnbuild.InvokeHostFunction{
					HostFunction:  hostFn,
					SourceAccount: d.signer.Address(),
				},
			},
			BaseFee:       txnbuild.MinBaseFee,
			Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build transaction: %w", err)
	}

	// Get transaction envelope XDR
	txXDR, err := tx.Base64()
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction XDR: %w", err)
	}

	// Simulate the transaction
	simResult, err := d.rpcClient.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
		Transaction:    txXDR,
		ResourceConfig: sorobanSimResourceConfig(),
	})
	if err != nil {
		// #region agent log
		DebugSessionLogStellar("H3", "deployment/deployer.go:SimulateContract", err.Error(), map[string]any{
			"stage":        "SimulateTransactionRPC",
			"path":         "SimulateContract",
			"functionName": functionName,
		})
		// #endregion
		return nil, fmt.Errorf("simulation failed: %w", err)
	}

	if simResult.Error != "" {
		// #region agent log
		DebugSessionLogStellar("H1", "deployment/deployer.go:SimulateContract", simResult.Error, map[string]any{
			"stage":                    "SimulateContract",
			"functionName":             functionName,
			"txDataLen":                len(simResult.TransactionDataXDR),
			"instructionLeewayApplied": sorobanSimulationInstructionLeeway,
		})
		// #endregion
		return nil, fmt.Errorf("simulation error: %s", simResult.Error)
	}

	// If the simulation indicates expired persistent ledger entries, restore
	// them first, then re-simulate so the read returns fresh data.
	if d.autoRestore && simResult.RestorePreamble != nil {
		if err := d.restoreFootprint(ctx, *simResult.RestorePreamble); err != nil {
			return nil, fmt.Errorf("failed to restore expired ledger entries: %w", err)
		}

		sourceAccount, err = d.getSourceAccount(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get source account after restore: %w", err)
		}

		tx, err = txnbuild.NewTransaction(
			txnbuild.TransactionParams{
				SourceAccount:        sourceAccount,
				IncrementSequenceNum: true,
				Operations: []txnbuild.Operation{
					&txnbuild.InvokeHostFunction{
						HostFunction:  hostFn,
						SourceAccount: d.signer.Address(),
					},
				},
				BaseFee:       txnbuild.MinBaseFee,
				Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to rebuild transaction after restore: %w", err)
		}

		txXDR, err = tx.Base64()
		if err != nil {
			return nil, fmt.Errorf("failed to get transaction XDR after restore: %w", err)
		}

		simResult, err = d.rpcClient.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
			Transaction:    txXDR,
			ResourceConfig: sorobanSimResourceConfig(),
		})
		if err != nil {
			// #region agent log
			DebugSessionLogStellar("H3", "deployment/deployer.go:SimulateContract:afterRestore", err.Error(), map[string]any{
				"stage": "SimulateTransactionRPC",
				"path":  "SimulateContractAfterRestore",
			})
			// #endregion
			return nil, fmt.Errorf("simulation failed after restore: %w", err)
		}

		if simResult.Error != "" {
			// #region agent log
			DebugSessionLogStellar("H1", "deployment/deployer.go:SimulateContract:afterRestore", simResult.Error, map[string]any{
				"stage":                    "SimulateContract",
				"path":                     "afterRestore",
				"instructionLeewayApplied": sorobanSimulationInstructionLeeway,
			})
			// #endregion
			return nil, fmt.Errorf("simulation error after restore: %s", simResult.Error)
		}
	}

	// Extract result from simulation
	if len(simResult.Results) == 0 {
		return nil, nil // No return value
	}

	// The result is returned as a base64-encoded XDR ScVal
	// Try different field names based on SDK version
	result := simResult.Results[0]

	// Access via reflection or try common field patterns
	// The SDK may have the result in different field names
	var resultXDR string

	// Try to extract from the result - SDK versions may vary
	// Use the struct's string representation as fallback
	if xdr, ok := getResultXDR(result); ok {
		resultXDR = xdr
	} else {
		return nil, nil
	}

	if resultXDR == "" {
		return nil, nil
	}

	var scVal xdr.ScVal
	if err := xdr.SafeUnmarshalBase64(resultXDR, &scVal); err != nil {
		return nil, fmt.Errorf("failed to decode result: %w", err)
	}

	return &scVal, nil
}

// getSourceAccount fetches the current account sequence from the network.
// It always queries the ledger to stay in sync with the on-chain state,
// which is safe because every write path waits for transaction confirmation
// (via waitForTransaction) before returning.
func (d *Deployer) getSourceAccount(ctx context.Context) (*txnbuild.SimpleAccount, error) {
	accountKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: xdr.MustAddress(d.signer.Address()),
		},
	}

	keyXDR, err := accountKey.MarshalBinaryBase64()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal account key: %w", err)
	}

	resp, err := d.rpcClient.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get ledger entries: %w", err)
	}

	if len(resp.Entries) == 0 {
		d.accountSequence = 0
	} else {
		entryXDR, ok := getLedgerEntryXDR(resp.Entries[0])
		if !ok || entryXDR == "" {
			d.accountSequence = 0
		} else {
			var entry xdr.LedgerEntryData
			if err := xdr.SafeUnmarshalBase64(entryXDR, &entry); err != nil {
				return nil, fmt.Errorf("failed to unmarshal account entry: %w", err)
			}
			account := entry.MustAccount()
			d.accountSequence = int64(account.SeqNum)
		}
	}

	return &txnbuild.SimpleAccount{
		AccountID: d.signer.Address(),
		Sequence:  d.accountSequence,
	}, nil
}

// buildAndSubmitTransaction builds, signs, and submits a transaction.
func (d *Deployer) buildAndSubmitTransaction(ctx context.Context, sourceAccount *txnbuild.SimpleAccount, op txnbuild.Operation) (*xdr.TransactionMeta, error) {
	// Build transaction
	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        sourceAccount,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{op},
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build transaction: %w", err)
	}

	// Get transaction envelope XDR for simulation
	txXDR, err := tx.Base64()
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction XDR: %w", err)
	}

	// Simulate to get resource estimates
	simResult, err := d.rpcClient.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
		Transaction:    txXDR,
		ResourceConfig: sorobanSimResourceConfig(),
	})
	if err != nil {
		// #region agent log
		DebugSessionLogStellar("H3", "deployment/deployer.go:buildAndSubmitTransaction", err.Error(), map[string]any{
			"stage": "SimulateTransactionRPC",
			"path":  "buildAndSubmitTransaction",
		})
		// #endregion
		return nil, fmt.Errorf("simulation failed: %w", err)
	}

	if simResult.Error != "" {
		// #region agent log
		DebugSessionLogStellar("H1", "deployment/deployer.go:buildAndSubmitTransaction", simResult.Error, map[string]any{
			"stage":                    "buildAndSubmitTransaction",
			"path":                     "first_sim",
			"txDataLen":                len(simResult.TransactionDataXDR),
			"hasRestorePre":            simResult.RestorePreamble != nil,
			"minResourceFee":           simResult.MinResourceFee,
			"resultsLen":               len(simResult.Results),
			"instructionLeewayApplied": sorobanSimulationInstructionLeeway,
			"hypothesisNote":           "H1=budget; if leeway applied and still fails, tx may exceed host max or stale WASM",
		})
		// #endregion
		return nil, fmt.Errorf("simulation error: %s", simResult.Error)
	}

	// If the simulation indicates expired persistent ledger entries, restore
	// them first, then rebuild and re-simulate the original transaction.
	if d.autoRestore && simResult.RestorePreamble != nil {
		if err := d.restoreFootprint(ctx, *simResult.RestorePreamble); err != nil {
			return nil, fmt.Errorf("failed to restore expired ledger entries: %w", err)
		}

		sourceAccount, err = d.getSourceAccount(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get source account after restore: %w", err)
		}

		tx, err = txnbuild.NewTransaction(
			txnbuild.TransactionParams{
				SourceAccount:        sourceAccount,
				IncrementSequenceNum: true,
				Operations:           []txnbuild.Operation{op},
				BaseFee:              txnbuild.MinBaseFee,
				Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to rebuild transaction after restore: %w", err)
		}

		txXDR, err = tx.Base64()
		if err != nil {
			return nil, fmt.Errorf("failed to get transaction XDR after restore: %w", err)
		}

		simResult, err = d.rpcClient.SimulateTransaction(ctx, protocolrpc.SimulateTransactionRequest{
			Transaction:    txXDR,
			ResourceConfig: sorobanSimResourceConfig(),
		})
		if err != nil {
			// #region agent log
			DebugSessionLogStellar("H3", "deployment/deployer.go:buildAndSubmitTransaction:afterRestore", err.Error(), map[string]any{
				"stage": "SimulateTransactionRPC",
				"path":  "afterRestore",
			})
			// #endregion
			return nil, fmt.Errorf("simulation failed after restore: %w", err)
		}

		if simResult.Error != "" {
			// #region agent log
			DebugSessionLogStellar("H1", "deployment/deployer.go:buildAndSubmitTransaction:afterRestore", simResult.Error, map[string]any{
				"stage":                    "buildAndSubmitTransaction",
				"path":                     "after_restore_sim",
				"instructionLeewayApplied": sorobanSimulationInstructionLeeway,
			})
			// #endregion
			return nil, fmt.Errorf("simulation error after restore: %s", simResult.Error)
		}
	}

	// Assemble the transaction with simulation results
	assembledTx, err := d.assembleTransaction(tx, simResult)
	if err != nil {
		return nil, fmt.Errorf("failed to assemble transaction: %w", err)
	}

	// Sign the transaction
	signedTx, err := assembledTx.Sign(d.networkPassphrase, d.signer)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	// Get signed transaction XDR
	signedXDR, err := signedTx.Base64()
	if err != nil {
		return nil, fmt.Errorf("failed to get signed transaction XDR: %w", err)
	}

	// Submit transaction
	submitResult, err := d.rpcClient.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
		Transaction: signedXDR,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to submit transaction: %w", err)
	}

	// Check submission status - the transaction may have been rejected
	switch submitResult.Status {
	case "PENDING", "DUPLICATE":
		// Transaction was accepted, continue to wait for confirmation
	case "TRY_AGAIN_LATER":
		return nil, fmt.Errorf("transaction submission failed: server overloaded, try again later")
	case "ERROR":
		// Transaction was rejected - decode the error
		if submitResult.ErrorResultXDR != "" {
			return nil, fmt.Errorf("transaction rejected: %v (diagnostics: %v)", submitResult.ErrorResultXDR, submitResult.DiagnosticEventsXDR)
		}
		return nil, fmt.Errorf("transaction rejected with status ERROR")
	default:
		return nil, fmt.Errorf("unexpected transaction status: %s", submitResult.Status)
	}

	// Wait for transaction confirmation
	txResult, err := d.waitForTransaction(ctx, submitResult.Hash)
	if err != nil {
		return nil, err
	}

	return txResult, nil
}

// waitForTransaction polls for transaction completion.
func (d *Deployer) waitForTransaction(ctx context.Context, hash string) (*xdr.TransactionMeta, error) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(60 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, fmt.Errorf("transaction timed out")
		case <-ticker.C:
			result, err := d.rpcClient.GetTransaction(ctx, protocolrpc.GetTransactionRequest{
				Hash: hash,
			})
			if err != nil {
				continue // Retry on error
			}

			switch result.Status {
			case "SUCCESS":
				// Parse the result meta XDR
				if result.ResultMetaXDR == "" {
					return nil, fmt.Errorf("no result meta XDR")
				}
				var meta xdr.TransactionMeta
				if err := xdr.SafeUnmarshalBase64(result.ResultMetaXDR, &meta); err != nil {
					return nil, fmt.Errorf("failed to decode result meta: %w", err)
				}
				return &meta, nil
			case "FAILED":
				return nil, fmt.Errorf("transaction failed")
			case "NOT_FOUND":
				continue // Still pending
			}
		}
	}
}

// restoreFootprint submits a RestoreFootprint transaction using the data provided
// by a simulation's RestorePreamble. Soroban returns this preamble when the
// transaction's read/write footprint references persistent ledger entries whose
// TTL has expired (archived). The restore must succeed before the original
// transaction can be retried.
func (d *Deployer) restoreFootprint(ctx context.Context, preamble protocolrpc.RestorePreamble) error {
	sourceAccount, err := d.getSourceAccount(ctx)
	if err != nil {
		return fmt.Errorf("failed to get source account for restore: %w", err)
	}

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(preamble.TransactionDataXDR, &sorobanData); err != nil {
		return fmt.Errorf("failed to decode restore preamble soroban data: %w", err)
	}

	restoreOp := &txnbuild.RestoreFootprint{
		SourceAccount: d.signer.Address(),
		Ext: xdr.TransactionExt{
			V:           1,
			SorobanData: &sorobanData,
		},
	}

	baseFee := preamble.MinResourceFee + 10000

	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        sourceAccount,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{restoreOp},
			BaseFee:              baseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to build restore transaction: %w", err)
	}

	signedTx, err := tx.Sign(d.networkPassphrase, d.signer)
	if err != nil {
		return fmt.Errorf("failed to sign restore transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		return fmt.Errorf("failed to get signed restore transaction XDR: %w", err)
	}

	submitResult, err := d.rpcClient.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
		Transaction: signedXDR,
	})
	if err != nil {
		return fmt.Errorf("failed to submit restore transaction: %w", err)
	}

	switch submitResult.Status {
	case "PENDING", "DUPLICATE":
		// Transaction was accepted
	case "TRY_AGAIN_LATER":
		return fmt.Errorf("restore transaction submission failed: server overloaded, try again later")
	case "ERROR":
		if submitResult.ErrorResultXDR != "" {
			return fmt.Errorf("restore transaction rejected: %v (diagnostics: %v)", submitResult.ErrorResultXDR, submitResult.DiagnosticEventsXDR)
		}
		return fmt.Errorf("restore transaction rejected with status ERROR")
	default:
		return fmt.Errorf("unexpected restore transaction status: %s", submitResult.Status)
	}

	_, err = d.waitForTransaction(ctx, submitResult.Hash)
	if err != nil {
		return fmt.Errorf("restore transaction failed: %w", err)
	}

	return nil
}

// assembleTransaction adds simulation results to a transaction.
func (d *Deployer) assembleTransaction(tx *txnbuild.Transaction, sim protocolrpc.SimulateTransactionResponse) (*txnbuild.Transaction, error) {
	// Get the operations and modify with simulation data
	ops := tx.Operations()
	if len(ops) == 0 {
		return tx, nil
	}

	// If there's soroban data, we need to rebuild the transaction
	if sim.TransactionDataXDR != "" {
		var sorobanData xdr.SorobanTransactionData
		if err := xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &sorobanData); err != nil {
			return nil, fmt.Errorf("failed to decode soroban data: %w", err)
		}

		// Check if the first op is an InvokeHostFunction and set the ext field
		if ihf, ok := ops[0].(*txnbuild.InvokeHostFunction); ok {
			ihf.Ext = xdr.TransactionExt{
				V:           1,
				SorobanData: &sorobanData,
			}

			// Set auth entries if provided
			if len(sim.Results) > 0 && sim.Results[0].AuthXDR != nil && len(*sim.Results[0].AuthXDR) > 0 {
				auth := make([]xdr.SorobanAuthorizationEntry, len(*sim.Results[0].AuthXDR))
				for i, authXDR := range *sim.Results[0].AuthXDR {
					if err := xdr.SafeUnmarshalBase64(authXDR, &auth[i]); err != nil {
						return nil, fmt.Errorf("failed to decode auth: %w", err)
					}
				}
				ihf.Auth = auth
			}
		}
	}

	// Calculate the fee
	minFee := sim.MinResourceFee
	if minFee > 0 {
		// Add buffer to ensure transaction goes through
		newFee := minFee + 10000

		// Rebuild transaction with new fee
		sourceAccount, err := d.getSourceAccount(context.Background())
		if err != nil {
			return nil, err
		}

		return txnbuild.NewTransaction(
			txnbuild.TransactionParams{
				SourceAccount:        sourceAccount,
				IncrementSequenceNum: true,
				Operations:           ops,
				BaseFee:              newFee,
				Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
			},
		)
	}

	return tx, nil
}

// extractWASMHash extracts the WASM hash from a transaction result.
func extractWASMHash(meta *xdr.TransactionMeta) (xdr.Hash, error) {
	if meta == nil {
		return xdr.Hash{}, fmt.Errorf("nil transaction meta")
	}

	var returnVal *xdr.ScVal

	// Versions below refer to protocol versions (20 and 21+)
	switch meta.V {
	case 4:
		v := meta.MustV4()
		if v.SorobanMeta == nil {
			return xdr.Hash{}, fmt.Errorf("no soroban meta")
		}
		returnVal = v.SorobanMeta.ReturnValue
	case 3:
		v := meta.MustV3()
		if v.SorobanMeta == nil {
			return xdr.Hash{}, fmt.Errorf("no soroban meta")
		}
		returnVal = &v.SorobanMeta.ReturnValue
	default:
		return xdr.Hash{}, fmt.Errorf("unsupported version: %d", meta.V)
	}

	bytes, ok := returnVal.GetBytes()
	if !ok {
		return xdr.Hash{}, fmt.Errorf("return value is not bytes")
	}

	var hash xdr.Hash
	copy(hash[:], bytes)
	return hash, nil
}

// extractContractID extracts the contract ID from a transaction result.
func extractContractID(meta *xdr.TransactionMeta) (string, error) {
	if meta == nil {
		return "", fmt.Errorf("nil transaction meta")
	}

	var returnVal *xdr.ScVal

	// Versions below refer to protocol versions (20 and 21+)
	switch meta.V {
	case 4:
		v := meta.MustV4()
		if v.SorobanMeta == nil {
			return "", fmt.Errorf("no soroban meta")
		}
		returnVal = v.SorobanMeta.ReturnValue
	case 3:
		v := meta.MustV3()
		if v.SorobanMeta == nil {
			return "", fmt.Errorf("no soroban meta")
		}
		returnVal = &v.SorobanMeta.ReturnValue
	default:
		return "", fmt.Errorf("unsupported version: %d", meta.V)
	}

	addr, ok := returnVal.GetAddress()
	if !ok {
		return "", fmt.Errorf("return value is not address")
	}

	if addr.Type != xdr.ScAddressTypeScAddressTypeContract {
		return "", fmt.Errorf("address is not contract type")
	}

	contractID := addr.MustContractId()
	return strkey.Encode(strkey.VersionByteContract, contractID[:])
}

// extractReturnValue extracts the return value from a transaction result.
func extractReturnValue(meta *xdr.TransactionMeta) (*xdr.ScVal, error) {
	if meta == nil {
		return nil, nil
	}

	// Versions below refer to protocol versions (20 and 21+)
	switch meta.V {
	case 4:
		v := meta.MustV4()
		if v.SorobanMeta == nil {
			return nil, nil // No return value
		}
		return v.SorobanMeta.ReturnValue, nil // V4 ReturnValue is already a pointer
	case 3:
		v := meta.MustV3()
		if v.SorobanMeta == nil {
			return nil, nil // No return value
		}
		return &v.SorobanMeta.ReturnValue, nil // V3 ReturnValue is a value, need address-of
	default:
		return nil, fmt.Errorf("unsupported transaction meta version: %d", meta.V)
	}
}

// GetEvents returns events from a ledger range.
func (d *Deployer) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	// Build topic filter following the exact pattern from source_reader.go
	// First topic is the symbol, rest use wildcard
	var topicScVals []*xdr.ScVal
	for _, topic := range topics {
		topicScVals = append(topicScVals, scval.SymbolToScValPtr(topic))
	}

	// Use wildcard to match any remaining topics
	zeroOrMore := protocolrpc.WildCardZeroOrMore

	// Build topic filter - use SegmentFilter type from SDK
	topicFilter := protocolrpc.TopicFilter{}
	for _, scVal := range topicScVals {
		topicFilter = append(topicFilter, protocolrpc.SegmentFilter{ScVal: scVal})
	}
	// Add wildcard
	topicFilter = append(topicFilter, protocolrpc.SegmentFilter{Wildcard: &zeroOrMore})

	resp, err := d.rpcClient.GetEvents(ctx, protocolrpc.GetEventsRequest{
		StartLedger: startLedger,
		Filters: []protocolrpc.EventFilter{
			{
				EventType:   protocolrpc.EventTypeSet{protocolrpc.EventTypeContract: nil},
				ContractIDs: []string{contractID},
				Topics:      []protocolrpc.TopicFilter{topicFilter},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get events: %w", err)
	}

	return resp.Events, nil
}

// GenerateDeterministicSalt generates a deterministic salt for contract deployment.
func GenerateDeterministicSalt(deployerAddress, contractName string) [32]byte {
	saltInput := fmt.Sprintf("%s-%s", deployerAddress, contractName)
	return sha256.Sum256([]byte(saltInput))
}

// getResultXDR attempts to extract the XDR result from SimulateHostFunctionResult.
// This handles different SDK versions that may have different field names.
func getResultXDR(result protocolrpc.SimulateHostFunctionResult) (string, bool) {
	if result.ReturnValueXDR != nil && *result.ReturnValueXDR != "" {
		return *result.ReturnValueXDR, true
	}
	return "", false
}

// getLedgerEntryXDR extracts the XDR from a ledger entry result.
func getLedgerEntryXDR(entry protocolrpc.LedgerEntryResult) (string, bool) {
	if entry.DataXDR != "" {
		return entry.DataXDR, true
	}
	return "", false
}

// NativeAccountState returns the account native balance (stroops) and sequence number from the ledger.
// rawAccountKey must be the 32-byte Ed25519 public key (Stellar account id).
func (d *Deployer) NativeAccountState(ctx context.Context, rawAccountKey []byte) (balance *big.Int, seq uint64, exists bool, err error) {
	if len(rawAccountKey) != 32 {
		return nil, 0, false, fmt.Errorf("expected 32-byte account key, got %d", len(rawAccountKey))
	}
	gAddr, err := strkey.Encode(strkey.VersionByteAccountID, rawAccountKey)
	if err != nil {
		return nil, 0, false, fmt.Errorf("encode account id: %w", err)
	}
	aid, err := xdr.AddressToAccountId(gAddr)
	if err != nil {
		return nil, 0, false, fmt.Errorf("account id from address: %w", err)
	}
	lk := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: aid,
		},
	}
	keyXDR, err := lk.MarshalBinaryBase64()
	if err != nil {
		return nil, 0, false, fmt.Errorf("marshal account ledger key: %w", err)
	}
	resp, err := d.rpcClient.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	})
	if err != nil {
		return nil, 0, false, fmt.Errorf("get ledger entries: %w", err)
	}
	if len(resp.Entries) == 0 {
		return big.NewInt(0), 0, false, nil
	}
	entryXDR, ok := getLedgerEntryXDR(resp.Entries[0])
	if !ok || entryXDR == "" {
		return big.NewInt(0), 0, false, nil
	}
	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(entryXDR, &entry); err != nil {
		return nil, 0, false, fmt.Errorf("unmarshal account entry: %w", err)
	}
	account := entry.MustAccount()
	seqN := int64(account.SeqNum)
	if seqN < 0 {
		return nil, 0, false, fmt.Errorf("invalid account sequence %d", seqN)
	}
	return big.NewInt(int64(account.Balance)), uint64(seqN), true, nil
}

// SubmitClassicOperation builds, signs, and submits a single classic Stellar
// operation (e.g. ChangeTrust, Payment) via the Soroban RPC.
// Classic operations cannot be simulated through the Soroban RPC, so this
// method bypasses simulation and submits the transaction directly.
func (d *Deployer) SubmitClassicOperation(ctx context.Context, op txnbuild.Operation) error {
	src, err := d.getSourceAccount(ctx)
	if err != nil {
		return fmt.Errorf("load source account: %w", err)
	}

	tx, err := txnbuild.NewTransaction(
		txnbuild.TransactionParams{
			SourceAccount:        src,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{op},
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		},
	)
	if err != nil {
		return fmt.Errorf("build transaction: %w", err)
	}

	signedTx, err := tx.Sign(d.networkPassphrase, d.signer)
	if err != nil {
		return fmt.Errorf("sign transaction: %w", err)
	}

	signedXDR, err := signedTx.Base64()
	if err != nil {
		return fmt.Errorf("encode signed transaction: %w", err)
	}

	submitResult, err := d.rpcClient.SendTransaction(ctx, protocolrpc.SendTransactionRequest{
		Transaction: signedXDR,
	})
	if err != nil {
		return fmt.Errorf("submit transaction: %w", err)
	}

	switch submitResult.Status {
	case "PENDING", "DUPLICATE":
	case "TRY_AGAIN_LATER":
		return fmt.Errorf("transaction submission failed: server overloaded, try again later")
	case "ERROR":
		if submitResult.ErrorResultXDR != "" {
			return fmt.Errorf("transaction rejected: %v (diagnostics: %v)", submitResult.ErrorResultXDR, submitResult.DiagnosticEventsXDR)
		}
		return fmt.Errorf("transaction rejected with status ERROR")
	default:
		return fmt.Errorf("unexpected transaction status: %s", submitResult.Status)
	}

	_, err = d.waitForTransaction(ctx, submitResult.Hash)
	return err
}

// DeploySACToken deploys a Soroban Asset Contract (SAC) wrapper for a classic
// Stellar asset and returns the resulting contract ID (C… strkey).
// The contract address is deterministic based on the network passphrase and asset.
func (d *Deployer) DeploySACToken(ctx context.Context, asset xdr.Asset) (string, error) {
	src, err := d.getSourceAccount(ctx)
	if err != nil {
		return "", fmt.Errorf("load source account: %w", err)
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeCreateContract,
			CreateContract: &xdr.CreateContractArgs{
				ContractIdPreimage: xdr.ContractIdPreimage{
					Type:      xdr.ContractIdPreimageTypeContractIdPreimageFromAsset,
					FromAsset: &asset,
				},
				Executable: xdr.ContractExecutable{
					Type: xdr.ContractExecutableTypeContractExecutableStellarAsset,
				},
			},
		},
		SourceAccount: d.signer.Address(),
	}

	if _, err = d.buildAndSubmitTransaction(ctx, src, op); err != nil {
		return "", fmt.Errorf("deploy SAC: %w", err)
	}

	contractID, err := ComputeSACContractID(d.networkPassphrase, asset)
	if err != nil {
		return "", fmt.Errorf("compute SAC contract ID: %w", err)
	}
	return contractID, nil
}

// ComputeSACContractID returns the deterministic contract address (C… strkey)
// for a Soroban Asset Contract wrapping the given classic Stellar asset.
func ComputeSACContractID(networkPassphrase string, asset xdr.Asset) (string, error) {
	networkID := sha256.Sum256([]byte(networkPassphrase))
	preimage := xdr.HashIdPreimage{
		Type: xdr.EnvelopeTypeEnvelopeTypeContractId,
		ContractId: &xdr.HashIdPreimageContractId{
			NetworkId: networkID,
			ContractIdPreimage: xdr.ContractIdPreimage{
				Type:      xdr.ContractIdPreimageTypeContractIdPreimageFromAsset,
				FromAsset: &asset,
			},
		},
	}
	b, err := preimage.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal preimage: %w", err)
	}
	h := sha256.Sum256(b)
	return strkey.Encode(strkey.VersionByteContract, h[:])
}

// SignerAddress returns the G… strkey of the deployer's signing keypair.
func (d *Deployer) SignerAddress() string {
	return d.signer.Address()
}

// SendNativePayment submits a payment of stroops native XLM from the deployer's account.
func (d *Deployer) SendNativePayment(ctx context.Context, destinationStrkey string, stroops int64) error {
	if stroops <= 0 {
		return fmt.Errorf("payment amount must be positive")
	}
	if _, err := xdr.AddressToAccountId(destinationStrkey); err != nil {
		return fmt.Errorf("invalid destination account: %w", err)
	}
	src, err := d.getSourceAccount(ctx)
	if err != nil {
		return fmt.Errorf("load source account: %w", err)
	}
	payment := &txnbuild.Payment{
		Destination: destinationStrkey,
		Amount:      fmt.Sprintf("%d", stroops),
		Asset:       txnbuild.NativeAsset{},
	}
	if _, err = d.buildAndSubmitTransaction(ctx, src, payment); err != nil {
		return err
	}
	return nil
}
