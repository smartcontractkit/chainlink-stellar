# Stellar Transaction Manager - Requirements

## Context

When a Chainlink node needs to send a transaction on-chain, it should not talk to Stellar/Soroban RPC directly from product code. It hands the transaction intent to a **Transaction Manager (TXM)**.

The Stellar TXM is responsible for:

- Building the transaction
- Simulating it, which is mandatory for Soroban invoke transactions
- Restoring archived Soroban state when a write requires it
- Assembling the final transaction with footprint, auth, and resource fee data
- Signing it through the Chainlink keystore
- Submitting it to the network
- Tracking whether it landed on-chain
- Retrying safely when the failure is transient
- Managing Stellar account sequence numbers
- Bumping only the inclusion-fee portion when the network is congested

Every supported Chainlink chain has its own TXM. The Stellar implementation follows the same broad pattern as Aptos - accept a transaction, broadcast it, confirm it, retry if needed - but it has Stellar-specific behavior around mandatory simulation, `RestoreFootprint`, ledger-bound expiry, and two-part Soroban fees.

## Goal of this document

This document describes what the Stellar TXM does now and why the implementation differs from the original pre-implementation plan in a few places.

The main intentional differences are:

- `TxRequest` is operation-based (`[]txnbuild.Operation`) instead of hardcoding contract ID/function/args into the TXM API. `InvokerAdapter` builds the `InvokeHostFunction` operation for generated bindings. This keeps TXM generic and lets CRE/CCIP use the same lifecycle.
- Submit retries re-simulate, re-assemble, and re-sign instead of reusing the same signed XDR. This is better for Soroban because ledger bounds, footprints, auth, and resource estimates can become stale.
- Read-only simulations do not auto-restore archived state. If a read returns `RestorePreamble`, `InvokerAdapter.SimulateContract` fails cleanly. Auto-restore for reads is a future product policy, not a TXM completeness blocker.
- On-chain `FAILED` does not become `Finalized`. Stellar TXM marks deterministic contract failures as `Failed` and only retries resource/archival/fee-capacity style failures.

## Components

### RPCClient

**File:** `ccv/client/client.go`

The Stellar SDK's `*rpcclient.Client` provides the raw Soroban RPC methods. The repo wraps it in `ccv/client.Client` for caching, rate limiting, polling, and per-RPC latency metrics.

For testability, Stellar components share this interface:

```go
type RPCClient interface {
    SimulateTransaction(ctx, req) (SimulateTransactionResponse, error)
    SendTransaction(ctx, req)     (SendTransactionResponse, error)
    GetTransaction(ctx, req)      (GetTransactionResponse, error)
    GetLedgerEntries(ctx, req)    (GetLedgerEntriesResponse, error)
    GetEvents(ctx, req)           (GetEventsResponse, error)
    GetLatestLedger(ctx)          (GetLatestLedgerResponse, error)
    GetLedgers(ctx, req)          (GetLedgersResponse, error)
    GetFeeStats(ctx)              (GetFeeStatsResponse, error)
}
```

`GetFeeStats` is important for TXM because Stellar has a live inclusion-fee market. The TXM uses Soroban inclusion-fee percentiles to seed and bump fees.

Multi-node rotation is handled by `ClientFactory` in `ccv/client/factory.go`. The TXM receives a `getClient func() (*ccvclient.Client, error)` callback, so it can use the current healthy RPC node without owning node selection itself.

### Shared Client Wrapper

**File:** `ccv/client/client.go`

`ccv/client.Client` adds:

- **Latest-ledger TTL cache**: `LatestLedger()` coalesces repeated `GetLatestLedger` calls. Default TTL is 3 seconds.
- **Token-bucket rate limiter**: forwarded RPC methods call `WaitRateLimit` before hitting RPC. Default is 10 req/s with burst 20.
- **`PollTransaction` utility**: used by restore handling to wait for the restore transaction to reach SUCCESS/FAILED.
- **RPC latency histogram**: `stellar_rpc_call_latency` labelled by chain ID, node URL, RPC call name, and success.

The TXM uses the forwarded methods (`SimulateTransaction`, `SendTransaction`, `GetTransaction`, `GetLedgerEntries`, `LatestLedger`, `GetFeeStats`) so rate limiting and metrics stay centralized.

### Keystore

**File:** `relayer/txm/broadcast.go`

The TXM signs through the shared `loop.Keystore` interface from `chainlink-common`. If a `TxRequest` does not specify `FromAddress`, the TXM uses the first keystore account.

Signing flow:

1. Hash the assembled transaction with the configured network passphrase.
2. Call `keystore.Sign(ctx, fromAddress, hash[:])`.
3. Build the Stellar `xdr.DecoratedSignature` using the signer hint.
4. Attach the signature to the transaction envelope.

There is no separate `Signer` interface in the current implementation. That is intentional for CRE, where signing should go through the Chainlink keystore. If a future CCIP runtime needs raw keypair signing, the right shape is a small keystore-compatible adapter rather than a second signing path inside TXM.

### Enqueue / EnqueueAndWait / Simulate

**File:** `relayer/txm/txm.go`

The TXM exposes three main entry points:

#### `Enqueue(ctx, TxRequest) (string, error)`

Async. Stores a `StellarTx` with `Pending` status, assigns a UUID if the request ID is empty, and pushes the tx ID onto `broadcastChan`.

`TxRequest` is intentionally operation-based:

```go
type TxRequest struct {
    ID                 string
    FromAddress        string
    Operations         []txnbuild.Operation
    LedgerBoundsOffset uint32
    Metadata           *commontypes.TxMeta
}
```

The current TXM supports exactly one operation per transaction. Generated contract clients do not need to construct operations directly; `InvokerAdapter` converts contract ID/function/args into an `InvokeHostFunction` operation.

#### Backpressure behavior

The broadcast channel is bounded by `BroadcastChanSize` (default 100).

When the channel is full:

1. TXM drains the FIFO head from the channel.
2. The drained oldest tx is marked `Failed`.
3. Its `ResultCode` is set to `channel_full_oldest_evicted`.
4. Its `Done` channel is closed.
5. `stellar_txm_tx_dropped` is incremented.
6. The new tx is enqueued into the freed slot.

If a concurrent enqueue refills the slot between eviction and the new send, the new tx is dropped with `channel_full_new_rejected`.

This intentionally differs from Aptos, which drops the new tx. Stellar drops the oldest queued tx because queued Soroban transactions have stale ledger bounds and stale simulation assumptions.

#### `EnqueueAndWait(ctx, TxRequest) (*TxResult, error)`

Sync. Calls `Enqueue`, then waits for `tx.Done` until the tx reaches `Finalized` or `Failed`, the context is cancelled, or the TXM stops.

`TxResult` includes:

```go
type TxResult struct {
    ID            string
    Hash          string
    Status        commontypes.TransactionStatus
    Fee           *big.Int
    ResultXDR     string
    ResultMetaXDR string
    Error         error
}
```

`InvokerAdapter.InvokeContract` uses this path.

#### `Simulate(ctx, TxRequest) (SimulateTransactionResponse, error)`

Read-only. Builds a preliminary transaction with dummy sequence `0`, calls `SimulateTransaction`, and returns the raw simulation response.

It does not:

- enqueue
- allocate a sequence
- sign
- submit
- pay fees
- restore archived state

`InvokerAdapter.SimulateContract` uses this path. If the simulation returns `RestorePreamble`, the adapter returns a clean error because read-only calls should not silently submit a paid restore transaction.

### Broadcast Loop

**File:** `relayer/txm/txm.go`

`broadcastLoop` starts with the TXM service and stops on `Close`.

It:

- drains all currently available tx IDs from `broadcastChan`
- resolves IDs to tracked `StellarTx` objects
- sorts by original enqueue timestamp
- processes each tx through `simulateAssembleSignAndSend`

Timestamp ordering naturally prioritizes lifecycle retries because a retried tx keeps its original timestamp and sorts ahead of newer work.

### Soroban Transaction Pipeline

**Files:** `relayer/txm/txm.go`, `relayer/txm/broadcast.go`

`simulateAssembleSignAndSend` owns the write lifecycle:

```
1. Get healthy RPC client
2. Get or create TxStore for FromAddress
3. On outer retry, resync sequence from chain
4. Seed inclusion fee from fee stats or geometric fallback
5. Allocate seq = txStore.GetNextSequence()
6. Inner submit loop:
   a. Fetch latest ledger
   b. Compute MaxLedger = latestLedger + LedgerBoundsOffset
   c. Build preliminary tx with TimeBounds and LedgerBounds
   d. Simulate with MaxSimulateAttempts retry budget
   e. If RestorePreamble exists, submit RestoreFootprint, resync, and re-simulate
   f. Assemble final tx with Soroban data and auth
   g. Sign via keystore
   h. SendTransaction
   i. Handle PENDING / DUPLICATE / TRY_AGAIN_LATER / ERROR
7. If accepted, mark Unconfirmed and hand off to confirm loop
8. If all attempts exhaust, Release(seq), mark Failed, close Done
```

#### Why re-simulate on every submit attempt

For Stellar/Soroban, reusing signed XDR is weaker because:

- Ledger bounds may be closer to expiry.
- Footprints can become stale.
- Auth entries come from simulation.
- Resource fees can drift with ledger state.

The current implementation re-fetches the ledger, re-simulates, re-assembles, and re-signs on each inner submit attempt. That is more conservative and better aligned with Soroban.

#### Assembly details

`assembleTransaction`:

- decodes `TransactionDataXDR` into `xdr.SorobanTransactionData`
- writes `ResourceFee = MinResourceFee + ResourceFeeBuffer`
- attaches Soroban data to `InvokeHostFunction.Ext`
- decodes `AuthXDR` entries into `xdr.SorobanAuthorizationEntry`
- rebuilds the transaction with `BaseFee = inclusionFee`

Important: `BaseFee` is only the inclusion fee. Resource fee belongs inside Soroban transaction data.

### Restore Handler

**File:** `relayer/txm/restore.go`

Triggered when write-path simulation returns `RestorePreamble`.

Flow:

```
1. Decode preamble.TransactionDataXDR into SorobanTransactionData
2. Build txnbuild.RestoreFootprint
3. Build restore transaction using the currently allocated sequence
4. Fee = preamble.MinResourceFee + RestoreFeeBuffer
5. Sign restore transaction
6. Submit restore transaction
7. Poll with PollTransaction until terminal status
8. If SUCCESS:
   - increment restore success metric
   - resync sequence because restore consumed one sequence
   - original write is re-simulated and submitted with a new sequence
9. If FAILED/ERROR/exhausted attempts:
   - mark original tx Failed
10. If the original simulation still returns RestorePreamble after one restore:
   - abort as Failed
```

Restore is only automatic for state-changing `InvokeContract` writes. Read-only `SimulateContract` returns an explicit restore-required error.

### Confirm Loop

**File:** `relayer/txm/txm.go`

`confirmLoop` runs with `ConfirmPollInterval` plus jitter.

For each unconfirmed tx, it calls `GetTransaction(hash)`:

```
SUCCESS:
  - Confirm sequence as consumed
  - Store ResultXDR
  - Decode FeeCharged from ResultXDR and replace estimated fee
  - Store ResultMetaXDR
  - Mark Finalized
  - Close Done

FAILED:
  - Confirm sequence as consumed
  - Store ResultXDR
  - Decode/classify failure
  - Retry only if classification is retryable
  - Otherwise mark Failed and close Done

NOT_FOUND or transient RPC issue:
  - Fetch latest ledger
  - If latestLedger > MaxLedger, expire
  - If wall-clock age > TxTimeoutSecs, expire as fallback
  - If expired, recycle sequence and maybeRetry
  - Otherwise keep pending
```

Stellar has deterministic finality. Once `GetTransaction` returns `SUCCESS` or `FAILED`, there is no reorg waiting period.

#### FAILED classification

`failed_result.go` decodes `ResultXDR`.

Retryable:

- `InvokeHostFunctionResourceLimitExceeded`
- `InvokeHostFunctionEntryArchived`
- `InvokeHostFunctionInsufficientRefundableFee`
- `OperationResultCodeOpExceededWorkLimit`

Terminal:

- contract trap/panic
- malformed invocation
- bad auth
- decode failures
- other deterministic application errors

This prevents contract bugs from burning the full retry budget.

### Sequence Store (AccountStore + TxStore)

**File:** `relayer/txm/txstore.go`

`AccountStore` maps sender address to a per-account `TxStore`.

Each `TxStore` tracks:

- `nextSequence`
- `unconfirmedSequences`
- `failedSequences`
- `lastOnchainSequence`

Important methods:

- `GetNextSequence()`: returns `min(nextSequence, min(failedSequences))`
- `AddUnconfirmed(seq, hash, maxLedger, tx)`: records accepted network submissions
- `Confirm(seq, hash, failed)`: removes from unconfirmed; if `failed=true`, recycles the sequence when safe
- `Release(seq)`: recycles a locally allocated but never accepted sequence
- `ResyncNonce(nextExpectedSequence)`: moves local state forward after reading chain sequence

Stellar nuance:

```go
// On-chain account.SeqNum is the last used sequence.
// TxStore wants next expected sequence.
txStore.ResyncNonce(onchainSeq + 1)
```

Resync happens:

- when first creating a store for an account
- when submit returns `tx_bad_seq`
- before outer lifecycle retries
- after a successful RestoreFootprint transaction

### Fee Strategy

**Files:** `relayer/txm/fee.go`, `relayer/txm/config.go`

Stellar/Soroban fees have two pieces:

- **Inclusion fee**: validator priority bid, non-refundable, bumped on retry
- **Resource fee**: simulation-derived Soroban resource cost, refundable when unused, not bumped as a market bid

Before the submit loop:

```
inclusionFee = max(geometric baseline, live network fee)

first outer attempt: feeStats.SorobanInclusionFee.P50
outer retry:         feeStats.SorobanInclusionFee.P99
fallback:            BaseInclusionFee * FeeBumpMultiplier^attempt
```

On `TRY_AGAIN_LATER`:

```
bumped = max(inclusionFee * FeeBumpMultiplier, feeStats.SorobanInclusionFee.P90)
bumped = min(bumped, MaxInclusionFee)
```

Resource fee:

```
resourceFee = sim.MinResourceFee + ResourceFeeBuffer
```

Restore fee:

```
restoreFee = preamble.MinResourceFee + RestoreFeeBuffer
```

This is better than the static-only formula because it reacts to live network fee percentiles while keeping a deterministic fallback.

### Retry Architecture

The implementation has three practical retry scopes:

#### Layer 1 - inner submit loop

Bounded by `MaxSubmitRetryAttempts`.

Handles:

- raw `SendTransaction` RPC errors
- `TRY_AGAIN_LATER`
- `ERROR` with `tx_bad_seq`
- other retryable submit responses

Each attempt re-fetches ledger, re-simulates, re-assembles, and re-signs.

#### Layer 2 - simulation retry helper

Bounded by `MaxSimulateAttempts`.

`prepareAndSimulateWithRetry` retries transient latest-ledger and simulation errors. It does not retry likely terminal contract errors such as contract traps, malformed calls, bad auth, unknown function, or no such contract.

#### Layer 3 - lifecycle retry

Bounded by `MaxTxRetryAttempts`.

Triggered by the confirm loop when:

- a tx expires by LedgerBounds or wall-clock fallback
- an on-chain `FAILED` result is classified as retryable
- client acquisition fails before broadcast and the tx still has retry budget

Layer 3 increments `tx.Attempt`, re-enqueues the same tx ID, and the next broadcast attempt uses higher-priority fee seeding.

### Transaction Expiry

The TXM does not have a separate field called "mempool TTL". It uses Stellar transaction preconditions:

- `LedgerBounds.MaxLedger = latestLedger + LedgerBoundsOffset`
- `TimeBounds = txnbuild.NewTimeout(TxTimeoutSecs)`

`LedgerBounds` are the primary expiry mechanism because they are enforced by the network. `TxTimeoutSecs` is also used as a wall-clock fallback in the confirm loop and by restore polling.

Default:

- `LedgerBoundsOffset = 50` ledgers, roughly 5 minutes
- `TxTimeoutSecs = 300`, 5 minutes

### Metrics

**File:** `relayer/txm/metrics.go`

Metrics are emitted to both Prometheus and Beholder/OpenTelemetry.

| Metric | Type | Labels |
| :--- | :--- | :--- |
| `stellar_txm_tx_broadcasted` | Counter | chainID |
| `stellar_txm_tx_success` | Counter | chainID |
| `stellar_txm_tx_finalized` | Counter | chainID |
| `stellar_txm_tx_pending` | Gauge | chainID |
| `stellar_txm_tx_error` | Counter | chainID, reason |
| `stellar_txm_tx_retry` | Counter | chainID, reason |
| `stellar_txm_tx_dropped` | Counter | chainID, reason |
| `stellar_txm_restore_total` | Counter | chainID |
| `stellar_txm_restore_success` | Counter | chainID |
| `stellar_txm_restore_failed` | Counter | chainID |
| `stellar_txm_simulation_duration_seconds` | Histogram | chainID |
| `stellar_txm_fee_inclusion_stroops` | Histogram | chainID |
| `stellar_txm_fee_resource_stroops` | Histogram | chainID |

RPC latency is tracked separately by the shared client as `stellar_rpc_call_latency`.

### Config

**File:** `relayer/txm/config.go`

All config fields are pointers so TOML can distinguish "unset" from explicit zero. `Resolve()` fills defaults.

| Parameter | Default | What it controls |
| :--- | :--- | :--- |
| `BroadcastChanSize` | 100 | Broadcast queue capacity |
| `ConfirmPollInterval` | 5s | Confirm loop cadence |
| `BaseInclusionFee` | 100 stroops | Geometric fee baseline |
| `MaxInclusionFee` | 100,000 stroops | Inclusion fee cap |
| `FeeBumpMultiplier` | 1.5 | Inclusion fee multiplier |
| `ResourceFeeBuffer` | 15,000 stroops | Added to simulated resource fee |
| `RestoreFeeBuffer` | 10,000 stroops | Added to restore preamble fee |
| `MaxSimulateAttempts` | 3 | Simulation/latest-ledger retry budget |
| `MaxSubmitRetryAttempts` | 10 | Inner submit retry budget |
| `SubmitRetryDelay` | 3s | Delay between retry attempts |
| `TxTimeoutSecs` | 300 | Wall-clock fallback timeout |
| `LedgerBoundsOffset` | 50 | Network-enforced tx validity window |
| `MaxTxRetryAttempts` | 5 | Layer 3 lifecycle retry budget |
| `MaxRestoreAttempts` | 3 | Restore submission/poll retry budget |
| `PruneInterval` | 2h | Minimum time between pruning passes |
| `PruneTxExpiration` | 2h | Terminal tx retention before pruning |

### InvokerAdapter

**File:** `relayer/txm/invoker_adapter.go`

`InvokerAdapter` implements `bindings.Invoker`:

```go
type Invoker interface {
    InvokeContract(ctx, contractID, functionName, args) (*xdr.ScVal, error)
    SimulateContract(ctx, contractID, functionName, args) (*xdr.ScVal, error)
    GetEvents(ctx, contractID, startLedger, topics) ([]protocolrpc.EventInfo, error)
}
```

Behavior:

- `InvokeContract`: builds `txnbuild.InvokeHostFunction`, calls `txm.EnqueueAndWait`, extracts return value from `ResultMetaXDR`
- `SimulateContract`: builds `txnbuild.InvokeHostFunction`, calls `txm.Simulate`, extracts return value from simulation result
- `GetEvents`: delegates directly to RPC client; no TXM involvement

Return value extraction supports `TransactionMeta` V3 and V4.

### Query API

**File:** `relayer/txm/txm.go`

Available query methods:

- `GetStatus(txID)`: returns current `commontypes.TransactionStatus`
- `GetTransactionResult(txID)`: returns `TxResult` with hash, status, fee, `ResultXDR`, `ResultMetaXDR`, and error
- `GetTransactionFee(txID)`: returns confirmed fee for finalized txs
- `InflightCount()`: returns queued tx count and total unconfirmed tx count

`GetTransactionResult` is in-memory only. If a terminal transaction has been pruned, callers receive "no such transaction".

### Pruning

Terminal transactions (`Finalized`, `Failed`) are pruned during enqueue if enough time has passed since the previous prune.

Defaults:

- `PruneInterval = 2h`
- `PruneTxExpiration = 2h`

## Current non-blocking follow-ups

- **CRE registration/wiring**: CRE can use `InvokerAdapter`, but the product-facing write target/read target wiring still needs to select this implementation.
- **Read-only restore policy**: current behavior fails cleanly on `RestorePreamble` for reads. Auto-restore-before-read should be an explicit product policy if needed later.
- **Additional latency histograms**: enqueue-to-broadcast, broadcast-to-inclusion, and enqueue-to-finalized are useful observability improvements but not core TXM behavior.
- **Optional keypair adapter**: only needed if a runtime needs TXM signing without `loop.Keystore`.

## Reference implementations

| What | Where |
| :--- | :--- |
| Aptos TXM reference | `chainlink-aptos/relayer/txm/` |
| Stellar TXM | `relayer/txm/` |
| Shared client wrapper | `ccv/client/client.go` |
| Client factory | `ccv/client/factory.go` |
| Existing deployer helper | `deployment/deployer.go` |
| Invoker interface | `bindings/invoker.go` |
| Keystore interface | `chainlink-common/pkg/loop` |

## RPC calls required

| RPC Method | When it is called |
| :--- | :--- |
| `SimulateTransaction` | Before every submit attempt and for read-only `Simulate` |
| `SendTransaction` | To submit signed original and restore transactions |
| `GetTransaction` | Confirm loop and restore polling |
| `GetLedgerEntries` | Initial sequence fetch and resync |
| `GetLatestLedger` | Ledger bounds, expiry checks, client health |
| `GetFeeStats` | Inclusion fee seeding and `TRY_AGAIN_LATER` bumping |
| `GetEvents` | InvokerAdapter event reads |
