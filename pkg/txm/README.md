# pkg/txm — Stellar Transaction Manager

Lifecycle-managed Soroban transaction submission for Chainlink nodes on Stellar.

For architecture details and design decisions, see [docs/txm-architecture.md](../../docs/txm-architecture.md).

## Quick Start

```go
import (
    "github.com/smartcontractkit/chainlink-stellar/pkg/txm"
    ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"

    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/keypair"
    "github.com/stellar/go-stellar-sdk/xdr"
)

// 1. Create the shared Client (cache, rate limiter, polling utilities).
//    All Stellar components should share a single Client instance.
rpc := rpcclient.New("https://soroban-rpc.example.com")
client := ccvclient.NewClient(rpc)               // default config
// — or with custom config —
// client := ccvclient.NewClientWithConfig(rpc, ccvclient.ClientConfig{
//     LedgerCacheTTL:  5 * time.Second,
//     RateLimitPerSec: 20,
//     RateLimitBurst:  40,
//     PollInterval:    500 * time.Millisecond,
// })

// 2. Create a keystore
kp := keypair.MustParseFull("S...")
ks := txm.NewKeypairKeystore(kp)

// 3. Create and start the TXM
cfg := txm.DefaultConfig()
t := txm.NewTxm(client, "Test SDF Network ; September 2015", ks, cfg, logger)
t.Start(ctx)
defer t.Close()

// 4a. Async: fire and forget (txID is auto-generated)
txID, _ := t.Enqueue(ctx, txm.TxRequest{
    ContractID:   "CABC...",
    FunctionName: "transfer",
    Args:         []xdr.ScVal{...},
})

// 4b. Sync: block until confirmed
result, err := t.EnqueueAndWait(ctx, txm.TxRequest{
    ContractID:   "CABC...",
    FunctionName: "approve",
    Args:         []xdr.ScVal{...},
})

// 5. Query status
status, _ := t.GetTransactionStatus(ctx, txID)
fullResult, _ := t.GetTransactionResult(ctx, txID)
fee, _ := t.GetTransactionFee(ctx, txID)
channelDepth, unconfirmed := t.InflightCount()
```

## Using with ContractTransmitter

`ContractTransmitter` expects a `bindings.Invoker`. Use `InvokerAdapter` to bridge:

```go
adapter := txm.NewInvokerAdapter(t, client.RPC)
// adapter satisfies bindings.Invoker — pass it to ContractTransmitter
```

`InvokerAdapter` maps:
- `InvokeContract` → `TxManager.EnqueueAndWait` (blocks until confirmed)
- `SimulateContract` → `TxManager.EnqueueAndWait` with `SimulateOnly: true`
- `GetEvents` → direct `RPCClient.GetEvents` (no transaction needed)

## Signing

Two `Keystore` implementations are provided:

| Implementation | Use Case |
|----------------|----------|
| `KeypairKeystore` | Wraps `*keypair.Full` for direct ed25519 signing (CCIP path) |
| `LoopKeystoreSigner` | Wraps a `LoopKeystore` interface for CRE keystore service signing |

```go
// Direct keypair signing
ks := txm.NewKeypairKeystore(keypair.MustParseFull("S..."))

// CRE keystore service signing
ks := txm.NewLoopKeystoreSigner(loopKeystore, "keyID", "G...")
```

## File Layout

| File | Purpose |
|------|---------|
| `interfaces.go` | `TxManager` interface |
| `types.go` | `TxStatus`, `TxRequest`, `TxResult`, internal `txEntry` |
| `config.go` | `Config` struct and `DefaultConfig()` |
| `errors.go` | Sentinel errors |
| `keystore.go` | `Keystore` interface, `KeypairKeystore`, `LoopKeystoreSigner` |
| `sequence_store.go` | Account sequence tracking with failed-seq reuse |
| `fee_estimator.go` | Soroban simulation + transaction assembly + fee bumping |
| `tx_store.go` | In-memory transaction lifecycle store |
| `broadcaster.go` | Simulate → sign → submit with Layer 1/2 retry loops |
| `confirmer.go` | Confirmation polling + expiry detection + Layer 3 retries |
| `txm.go` | `Txm` struct — orchestrator implementing `TxManager` |
| `invoker_adapter.go` | `bindings.Invoker` → `TxManager` bridge |
| `metrics.go` | Prometheus counters/gauges for observability |

## Configuration

All fields have sensible defaults via `DefaultConfig()`:

```go
cfg := txm.DefaultConfig()
// Override as needed:
cfg.MaxRetries = 5
cfg.LedgerBoundsOffset = 100       // ~10 min at 6s/ledger
cfg.FeeBuffer = 50_000             // 50k stroops buffer
cfg.FeeBumpMultiplier = 2.0        // 2x fee bump per retry
cfg.MaxInclusionFee = 5_000_000    // 0.5 XLM cap
```

| Field | Default | Description |
|-------|---------|-------------|
| `MaxQueueSize` | 256 | Enqueue channel capacity |
| `ConfirmPollInterval` | 2s | Confirmer tick interval (with ±20% jitter) |
| `TxTimeout` | 60s | Wall-clock confirmation timeout |
| `LedgerBoundsOffset` | 50 | Ledger-based expiry window |
| `MaxRetries` | 3 | Layer 3 lifecycle retries (confirm loop re-enqueue) |
| `MaxSubmitAttempts` | 5 | Layer 1 HTTP submit retries |
| `SubmitRetryDelay` | 2s | Delay between submit retries |
| `MaxSimulateAttempts` | 5 | Layer 2 simulation retries (sequence races) |
| `FeeBuffer` | 10,000 | Base inclusion fee (stroops) |
| `FeeBumpMultiplier` | 1.5 | Geometric fee bump per lifecycle retry |
| `MaxInclusionFee` | 1,000,000 | Safety cap on inclusion fee |
| `AutoRestore` | true | Auto-restore expired persistent ledger entries |
| `PruneThreshold` | 5m | Age after which terminal txs are removed |
| `PruneInterval` | 30s | Minimum time between prune runs |

## Retry Layers

The TXM uses three layers of retry to handle different failure modes:

**Layer 1 — HTTP Submit Retries** (`MaxSubmitAttempts`):
Handles transient RPC failures during `SendTransaction`. Retries on
`TRY_AGAIN_LATER` and network errors. Does not retry `tx_bad_seq` (propagated
to Layer 3).

**Layer 2 — Simulation Retries** (`MaxSimulateAttempts`):
Handles sequence number races during `SimulateTransaction`. On `tx_bad_seq` in
simulation, syncs the sequence from chain and retries with a fresh sequence.

**Layer 3 — Lifecycle Retries** (`MaxRetries`):
Handles transactions that were accepted by the mempool but failed on-chain or
expired. The confirm loop detects these and re-enqueues them with an
incremented attempt counter, triggering fee bumping on the next broadcast.

## Metrics

Prometheus counters and gauges registered automatically:

| Metric | Type | Description |
|--------|------|-------------|
| `stellar_txm_broadcasted` | Counter | Txs submitted to network |
| `stellar_txm_finalized` | Counter | Txs confirmed on-chain (SUCCESS) |
| `stellar_txm_error` | Counter | Broadcast-time errors |
| `stellar_txm_revert` | Counter | On-chain failures (FAILED status) |
| `stellar_txm_reject` | Counter | Max retries exhausted or queue full |
| `stellar_txm_drop` | Counter | Channel full or expired txs |
| `stellar_txm_retry` | Counter | Lifecycle retry attempts |
| `stellar_txm_restore` | Counter | RestoreFootprint txs submitted |
| `stellar_txm_pending` | Gauge | Current enqueue channel depth |

Labels: `chainID`, `fromAddress`.

## Error Handling

Sentinel errors in `errors.go` can be checked with `errors.Is`:

```go
txID, err := t.Enqueue(ctx, req)
if errors.Is(err, txm.ErrQueueFull) {
    // back-pressure: queue is at capacity
}
if errors.Is(err, txm.ErrDuplicateTx) {
    // idempotency key collision
}
```

## Dependencies

- `ccv/client.Client` — shared Stellar RPC client with caching, rate limiting, and polling
- `ccv/client.RPCClient` — unified Stellar RPC interface (satisfied by `*rpcclient.Client`)
- `github.com/stellar/go-stellar-sdk` — Stellar Go SDK for transaction building, XDR, keypairs
- `github.com/rs/zerolog` — structured logging
- `github.com/prometheus/client_golang` — Prometheus metrics
- `github.com/google/uuid` — transaction ID generation
