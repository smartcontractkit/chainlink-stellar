//go:build manual

package txm

// Manual integration test for the Stellar TXM against a live testnet node.
//
// REQUIRED env vars:
//   STELLAR_RPC_URL      - e.g. https://soroban-testnet.stellar.org
//   STELLAR_SECRET_KEY   - S... Stellar secret key (account must have testnet XLM)
//
// OPTIONAL env vars (enable the full pipeline + sequential tests):
//   STELLAR_CONTRACT_ID    - C... strkey of any deployed Soroban contract
//   STELLAR_FUNCTION_NAME  - a state-mutating function on that contract (e.g. "increment")
//   STELLAR_NETWORK_PASSPHRASE - defaults to Stellar public testnet passphrase
//
// Run:
//   STELLAR_RPC_URL=https://soroban-testnet.stellar.org \
//   STELLAR_SECRET_KEY=S... \
//   STELLAR_CONTRACT_ID=C... \
//   STELLAR_FUNCTION_NAME=increment \
//   go test ./relayer/txm -run TestTXMManual -tags=manual -v -timeout=120s
//
// If STELLAR_CONTRACT_ID / STELLAR_FUNCTION_NAME are omitted the full pipeline
// tests are skipped, but connectivity, sequence fetch, and simulate-path tests
// still run (they need the contract ID for simulate).

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/smartcontractkit/chainlink-common/pkg/config"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	commontypes "github.com/smartcontractkit/chainlink-common/pkg/types"
	"github.com/smartcontractkit/chainlink-stellar/relayer/client"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const defaultTestnetPassphrase = "Test SDF Network ; September 2015"

// ── env config ───────────────────────────────────────────────────────────────

type manualCfg struct {
	RPCURL            string
	SecretKey         string
	Address           string // G... derived from SecretKey
	ContractID        string // C... (optional)
	FunctionName      string // state-mutating fn (optional)
	NetworkPassphrase string
}

func loadManualCfg(t *testing.T) manualCfg {
	t.Helper()

	rpcURL := os.Getenv("STELLAR_RPC_URL")
	if rpcURL == "" {
		t.Skip("STELLAR_RPC_URL not set — skipping manual test")
	}
	secretKey := os.Getenv("STELLAR_SECRET_KEY")
	if secretKey == "" {
		t.Skip("STELLAR_SECRET_KEY not set — skipping manual test")
	}

	kp, err := keypair.ParseFull(secretKey)
	require.NoError(t, err, "invalid STELLAR_SECRET_KEY")

	passphrase := os.Getenv("STELLAR_NETWORK_PASSPHRASE")
	if passphrase == "" {
		passphrase = defaultTestnetPassphrase
	}

	return manualCfg{
		RPCURL:            rpcURL,
		SecretKey:         secretKey,
		Address:           kp.Address(),
		ContractID:        os.Getenv("STELLAR_CONTRACT_ID"),
		FunctionName:      os.Getenv("STELLAR_FUNCTION_NAME"),
		NetworkPassphrase: passphrase,
	}
}

// ── keystore ─────────────────────────────────────────────────────────────────

// realKeystore implements loop.Keystore using a raw ed25519 key derived from
// a Stellar secret key. The TXM calls Sign(address, txHash) during broadcast.
type realKeystore struct {
	privKey ed25519.PrivateKey
	address string
}

func newRealKeystore(secretKey string) (*realKeystore, error) {
	kp, err := keypair.ParseFull(secretKey)
	if err != nil {
		return nil, fmt.Errorf("parse keypair: %w", err)
	}
	// Decode the S... strkey to get the raw 32-byte ed25519 seed.
	rawSeed, err := strkey.Decode(strkey.VersionByteSeed, secretKey)
	if err != nil {
		return nil, fmt.Errorf("decode seed: %w", err)
	}
	privKey := ed25519.NewKeyFromSeed(rawSeed)
	return &realKeystore{privKey: privKey, address: kp.Address()}, nil
}

func (k *realKeystore) Accounts(_ context.Context) ([]string, error) {
	return []string{k.address}, nil
}

func (k *realKeystore) Sign(_ context.Context, id string, data []byte) ([]byte, error) {
	if id != k.address {
		return nil, fmt.Errorf("unknown key %q (have %q)", id, k.address)
	}
	return ed25519.Sign(k.privKey, data), nil
}

func (k *realKeystore) Decrypt(_ context.Context, _ string, data []byte) ([]byte, error) {
	return data, nil // TXM never calls Decrypt
}

// ── helpers ───────────────────────────────────────────────────────────────────

// buildInvokeOp constructs an InvokeHostFunction operation for a Soroban contract.
func buildInvokeOp(t *testing.T, contractID, fnName string, args []xdr.ScVal) *txnbuild.InvokeHostFunction {
	t.Helper()

	contractBytes, err := strkey.Decode(strkey.VersionByteContract, contractID)
	require.NoError(t, err, "decode contractID %q", contractID)

	var cid xdr.ContractId
	copy(cid[:], contractBytes)

	return &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: xdr.ScAddress{
					Type:       xdr.ScAddressTypeScAddressTypeContract,
					ContractId: &cid,
				},
				FunctionName: xdr.ScSymbol(fnName),
				Args:         args,
			},
		},
	}
}

// watchStatus polls GetStatus every second while fn runs, printing live status
// transitions. Call it in a goroutine alongside EnqueueAndWait.
func watchStatus(t *testing.T, txm *StellarTxm, txID string, done <-chan struct{}) {
	t.Helper()
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	prev := ""
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			status, err := txm.GetStatus(txID)
			chanLen, storeCount := txm.InflightCount()
			cur := fmt.Sprintf("%v", status)
			if err != nil {
				cur = fmt.Sprintf("error(%v)", err)
			}
			if cur != prev {
				t.Logf("  [watcher] txID=%s  status=%-12s  broadcastChan=%d  unconfirmed=%d",
					txID[:8], cur, chanLen, storeCount)
				prev = cur
			}
		}
	}
}

// ── fixtures ──────────────────────────────────────────────────────────────────

func setupTXM(t *testing.T, cfg manualCfg) *StellarTxm {
	t.Helper()

	lggr, err := logger.New()
	require.NoError(t, err)

	ks, err := newRealKeystore(cfg.SecretKey)
	require.NoError(t, err)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	rpc := rpcclient.NewClient(cfg.RPCURL, httpClient)
	clientCfg := client.DefaultClientConfig()
	clientCfg.ChainID = "manual-test"
	clientCfg.RPCURL = cfg.RPCURL
	c := client.NewClient(rpc, &clientCfg)

	getClient := func() (*client.Client, error) { return c, nil }

	txmCfg := DefaultConfigSet
	txmCfg.ConfirmPollInterval = config.MustNewDuration(3 * time.Second)

	stellarTxm, err := New(lggr, ks, txmCfg, getClient, "manual-test", cfg.NetworkPassphrase)
	require.NoError(t, err)

	require.NoError(t, stellarTxm.Start(context.Background()))
	t.Cleanup(func() {
		if err := stellarTxm.Close(); err != nil {
			t.Logf("warn: TXM close: %v", err)
		}
	})

	return stellarTxm
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestTXMManual(t *testing.T) {
	cfg := loadManualCfg(t)

	t.Logf("=== TXM Manual Test ===")
	t.Logf("  RPC:     %s", cfg.RPCURL)
	t.Logf("  Address: %s", cfg.Address)
	t.Logf("  Network: %s", cfg.NetworkPassphrase)
	if cfg.ContractID != "" {
		t.Logf("  Contract: %s  fn=%q", cfg.ContractID, cfg.FunctionName)
	}

	// ── 1. Connectivity + sequence number fetch ───────────────────────────────

	t.Run("1_connectivity_and_sequence", func(t *testing.T) {
		t.Log("--- Checking RPC connectivity and on-chain sequence number ---")

		httpClient := &http.Client{Timeout: 15 * time.Second}
		rpc := rpcclient.NewClient(cfg.RPCURL, httpClient)
		c1Cfg := client.DefaultClientConfig()
		c1Cfg.ChainID = "manual-test"
		c1Cfg.RPCURL = cfg.RPCURL
		c := client.NewClient(rpc, &c1Cfg)

		ledger, err := c.GetLatestLedger(context.Background())
		require.NoError(t, err, "GetLatestLedger failed — is the RPC reachable?")
		t.Logf("  ✓ RPC alive  latestLedger=%d  protocolVersion=%d", ledger.Sequence, ledger.ProtocolVersion)

		// Fetch account sequence the same way the TXM does it.
		seqLggr, _ := logger.New()
		stellarTxm, err := New(seqLggr, &mockKeystore{}, Config{}, func() (*client.Client, error) { return c, nil }, "test", cfg.NetworkPassphrase)
		require.NoError(t, err)

		seq, err := stellarTxm.getSequenceNumber(context.Background(), c, cfg.Address)
		require.NoError(t, err, "getSequenceNumber — does the account exist and have XLM?")
		t.Logf("  ✓ Account exists  onChainSeqNum(last used)=%d  nextValid=%d", seq, seq+1)
	})

	// ── 2. Simulate (read-only, no gas consumed) ──────────────────────────────

	t.Run("2_simulate_read_only", func(t *testing.T) {
		if cfg.ContractID == "" || cfg.FunctionName == "" {
			t.Skip("STELLAR_CONTRACT_ID / STELLAR_FUNCTION_NAME not set — skipping simulate test")
		}
		t.Logf("--- Simulate (read-only, no broadcast) ---")

		stellarTxm := setupTXM(t, cfg)
		op := buildInvokeOp(t, cfg.ContractID, cfg.FunctionName, nil)

		simResult, err := stellarTxm.Simulate(context.Background(), TxRequest{
			FromAddress: cfg.Address,
			Operations:  []txnbuild.Operation{op},
		})
		require.NoError(t, err, "Simulate failed")

		t.Logf("  ✓ Simulate OK")
		t.Logf("    minResourceFee = %d stroops", simResult.MinResourceFee)
		t.Logf("    sorobanDataXDR = %s", truncate(simResult.TransactionDataXDR, 60))
		if simResult.RestorePreamble != nil {
			t.Logf("    ⚠  RestorePreamble present — some ledger entries are archived")
		}
	})

	// ── 3. Full pipeline: Enqueue → Simulate → Assemble → Sign → Send → Confirm

	t.Run("3_full_pipeline_enqueue_and_wait", func(t *testing.T) {
		if cfg.ContractID == "" || cfg.FunctionName == "" {
			t.Skip("STELLAR_CONTRACT_ID / STELLAR_FUNCTION_NAME not set — skipping full pipeline test")
		}
		t.Logf("--- Full TXM pipeline: Enqueue → broadcast → confirm ---")

		stellarTxm := setupTXM(t, cfg)
		op := buildInvokeOp(t, cfg.ContractID, cfg.FunctionName, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		txID := "manual-test-full-pipeline"
		req := TxRequest{
			ID:          txID,
			FromAddress: cfg.Address,
			Operations:  []txnbuild.Operation{op},
		}

		// Start live status watcher in background.
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			watchStatus(t, stellarTxm, txID, ctx.Done())
		}()

		t.Logf("  Calling EnqueueAndWait (will block until Finalized/Failed or 90s timeout)...")
		result, err := stellarTxm.EnqueueAndWait(ctx, req)
		<-watchDone

		require.NoError(t, err, "EnqueueAndWait returned error")
		require.NotNil(t, result)

		t.Logf("  ✓ Transaction completed")
		t.Logf("    status        = %v", result.Status)
		t.Logf("    hash          = %s", result.Hash)
		if result.Fee != nil {
			t.Logf("    feeCharged    = %s stroops (actual, from ResultXDR)", result.Fee.String())
		}
		if result.ResultMetaXDR != "" {
			t.Logf("    resultMetaXDR = %s...", truncate(result.ResultMetaXDR, 60))
		}
		if result.Error != nil {
			t.Logf("    error         = %v", result.Error)
		}

		assert.Equal(t, commontypes.Finalized, result.Status, "expected Finalized, got %v", result.Status)
		assert.NotEmpty(t, result.Hash, "expected non-empty tx hash")
		assert.NotNil(t, result.Fee, "expected actual fee from ResultXDR")
	})

	// ── 4. Sequential transactions (sequence management) ─────────────────────

	t.Run("4_sequential_transactions", func(t *testing.T) {
		if cfg.ContractID == "" || cfg.FunctionName == "" {
			t.Skip("STELLAR_CONTRACT_ID / STELLAR_FUNCTION_NAME not set — skipping sequential test")
		}
		t.Logf("--- Sequential transactions: verify sequence numbers don't collide ---")

		stellarTxm := setupTXM(t, cfg)

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		const n = 2
		for i := range n {
			txID := fmt.Sprintf("manual-seq-tx-%d", i)
			op := buildInvokeOp(t, cfg.ContractID, cfg.FunctionName, nil)

			t.Logf("  [tx %d/%d] enqueuing %s", i+1, n, txID)
			result, err := stellarTxm.EnqueueAndWait(ctx, TxRequest{
				ID:          txID,
				FromAddress: cfg.Address,
				Operations:  []txnbuild.Operation{op},
			})
			require.NoError(t, err, "tx %d failed: %v", i, err)
			t.Logf("  [tx %d/%d] ✓ status=%v  hash=%s  fee=%s stroops",
				i+1, n, result.Status, result.Hash, feeStr(result))
		}
		t.Log("  ✓ All sequential transactions confirmed without sequence collision")
	})

	// ── 5. Concurrent enqueue (broadcast channel + ordering) ─────────────────

	t.Run("5_concurrent_enqueue", func(t *testing.T) {
		if cfg.ContractID == "" || cfg.FunctionName == "" {
			t.Skip("STELLAR_CONTRACT_ID / STELLAR_FUNCTION_NAME not set — skipping concurrent test")
		}
		t.Logf("--- Concurrent enqueue: 3 transactions submitted in parallel ---")

		stellarTxm := setupTXM(t, cfg)

		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()

		const n = 3
		type outcome struct {
			i      int
			result *TxResult
			err    error
		}
		results := make(chan outcome, n)

		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				op := buildInvokeOp(t, cfg.ContractID, cfg.FunctionName, nil)
				txID := fmt.Sprintf("manual-concurrent-tx-%d", i)
				r, err := stellarTxm.EnqueueAndWait(ctx, TxRequest{
					ID:          txID,
					FromAddress: cfg.Address,
					Operations:  []txnbuild.Operation{op},
				})
				results <- outcome{i: i, result: r, err: err}
			}(i)
		}

		go func() { wg.Wait(); close(results) }()

		for o := range results {
			if o.err != nil {
				t.Errorf("  [tx %d] FAILED: %v", o.i, o.err)
				continue
			}
			t.Logf("  [tx %d] ✓ status=%v  hash=%s  fee=%s stroops",
				o.i, o.result.Status, o.result.Hash, feeStr(o.result))
		}
	})

	// ── 6. Idempotency (duplicate ID rejection) ───────────────────────────────

	t.Run("6_duplicate_id_rejection", func(t *testing.T) {
		t.Log("--- Duplicate ID: second enqueue must be rejected ---")

		stellarTxm := setupTXM(t, cfg)

		op := &txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &xdr.ContractId{},
					},
					FunctionName: xdr.ScSymbol("noop"),
				},
			},
		}

		fixedID := "manual-dup-id-test"
		_, err := stellarTxm.Enqueue(context.Background(), TxRequest{
			ID:         fixedID,
			Operations: []txnbuild.Operation{op},
		})
		require.NoError(t, err, "first enqueue should succeed")

		_, err = stellarTxm.Enqueue(context.Background(), TxRequest{
			ID:         fixedID,
			Operations: []txnbuild.Operation{op},
		})
		require.Error(t, err, "second enqueue with same ID should be rejected")
		assert.Contains(t, err.Error(), "already exists")
		t.Logf("  ✓ Duplicate rejected: %v", err)
	})

	// ── 7. Backpressure (broadcast channel full) ──────────────────────────────

	t.Run("7_backpressure_channel_full", func(t *testing.T) {
		t.Log("--- Backpressure: channel-full enqueue must be rejected ---")

		lggr, _ := logger.New()
		ks, err := newRealKeystore(cfg.SecretKey)
		require.NoError(t, err)

		httpClient := &http.Client{Timeout: 15 * time.Second}
		rpc := rpcclient.NewClient(cfg.RPCURL, httpClient)
		c7Cfg := client.DefaultClientConfig()
		c7Cfg.ChainID = "manual-test"
		c7Cfg.RPCURL = cfg.RPCURL
		c := client.NewClient(rpc, &c7Cfg)

		// Size-1 channel so the second enqueue immediately saturates it.
		tiny := DefaultConfigSet
		tiny.BroadcastChanSize = ptr(uint(1))

		txm2, err := New(lggr, ks, tiny, func() (*client.Client, error) { return c, nil }, "test", cfg.NetworkPassphrase)
		require.NoError(t, err)
		// NOTE: do not Start() — we want the channel to stay full.

		op := &txnbuild.InvokeHostFunction{
			HostFunction: xdr.HostFunction{
				Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
				InvokeContract: &xdr.InvokeContractArgs{
					ContractAddress: xdr.ScAddress{
						Type:       xdr.ScAddressTypeScAddressTypeContract,
						ContractId: &xdr.ContractId{},
					},
					FunctionName: xdr.ScSymbol("noop"),
				},
			},
		}

		_, err = txm2.Enqueue(context.Background(), TxRequest{Operations: []txnbuild.Operation{op}})
		require.NoError(t, err, "first enqueue (fills channel)")

		_, err = txm2.Enqueue(context.Background(), TxRequest{Operations: []txnbuild.Operation{op}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "broadcast channel full")
		t.Logf("  ✓ Backpressure triggered: %v", err)
	})
}

// ── small utilities ───────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func feeStr(r *TxResult) string {
	if r == nil || r.Fee == nil {
		return "nil"
	}
	return r.Fee.String()
}
