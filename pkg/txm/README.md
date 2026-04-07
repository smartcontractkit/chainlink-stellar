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

// 1. Create the RPC client (satisfies ccvclient.RPCClient)
rpc := rpcclient.New("https://soroban-rpc.example.com")

// 2. Create a keystore
kp := keypair.MustParseFull("S...")
ks := txm.NewKeypairKeystore(kp)

// 3. Create and start the TXM
cfg := txm.DefaultConfig()
t := txm.NewTxm(rpc, "Test SDF Network ; September 2015", ks, cfg, logger)
t.Start(ctx)
defer t.Close()

// 4a. Async: fire and forget
t.Enqueue(ctx, txm.TxRequest{
    ID:           "unique-id-1",
    ContractID:   "CABC...",
    FunctionName: "transfer",
    Args:         []xdr.ScVal{...},
})

// 4b. Sync: block until confirmed
result, err := t.EnqueueAndWait(ctx, txm.TxRequest{
    ID:           "unique-id-2",
    ContractID:   "CABC...",
    FunctionName: "approve",
    Args:         []xdr.ScVal{...},
})
```

## Using with ContractTransmitter

`ContractTransmitter` expects a `bindings.Invoker`. Use `InvokerAdapter` to bridge:

```go
adapter := txm.NewInvokerAdapter(t, rpc)
// adapter satisfies bindings.Invoker — pass it to ContractTransmitter
```

`InvokerAdapter` maps:
- `InvokeContract` → `TxManager.EnqueueAndWait` (blocks until confirmed)
- `SimulateContract` → `TxManager.EnqueueAndWait` with `SimulateOnly: true`
- `GetEvents` → direct `RPCClient.GetEvents` (no transaction needed)

## File Layout

| File | Purpose |
|------|---------|
| `interfaces.go` | `TxManager` interface |
| `types.go` | `TxStatus`, `TxRequest`, `TxResult`, internal `txEntry` |
| `config.go` | `Config` struct and `DefaultConfig()` |
| `errors.go` | Sentinel errors |
| `keystore.go` | `Keystore` interface, `KeypairKeystore` adapter |
| `sequence_manager.go` | Account sequence (nonce) tracking |
| `fee_estimator.go` | Soroban simulation + transaction assembly |
| `tx_store.go` | In-memory transaction lifecycle store |
| `broadcaster.go` | Simulate → sign → submit goroutine |
| `confirmer.go` | Confirmation polling + expiry detection goroutine |
| `txm.go` | `Txm` struct — orchestrator implementing `TxManager` |
| `invoker_adapter.go` | `bindings.Invoker` → `TxManager` bridge |

## Configuration

All fields have sensible defaults via `DefaultConfig()`:

```go
cfg := txm.DefaultConfig()
// Override as needed:
cfg.MaxRetries = 5
cfg.LedgerBoundsOffset = 100  // ~10 min at 6s/ledger
cfg.FeeBuffer = 50_000        // 50k stroops buffer
```

| Field | Default | Description |
|-------|---------|-------------|
| `MaxQueueSize` | 256 | Enqueue channel capacity |
| `ConfirmPollInterval` | 2s | Confirmer tick interval |
| `TxTimeout` | 60s | Wall-clock confirmation timeout |
| `LedgerBoundsOffset` | 50 | Ledger-based expiry window |
| `MaxRetries` | 3 | Retries for `TRY_AGAIN_LATER` / sequence conflicts |
| `FeeBuffer` | 10,000 | Stroops added to simulated fee |
| `AutoRestore` | true | Auto-restore expired persistent ledger entries |

## Error Handling

Sentinel errors in `errors.go` can be checked with `errors.Is`:

```go
err := t.Enqueue(ctx, req)
if errors.Is(err, txm.ErrQueueFull) {
    // back-pressure: queue is at capacity
}
if errors.Is(err, txm.ErrDuplicateTx) {
    // idempotency key collision
}
```

## Dependencies

- `ccv/client.RPCClient` — unified Stellar RPC interface
- `github.com/stellar/go-stellar-sdk` — Stellar Go SDK for transaction building, XDR, keypairs
- `github.com/rs/zerolog` — structured logging
