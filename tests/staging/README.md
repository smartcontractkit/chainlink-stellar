# Staging tests

Go tests that send CCIP messages through an already-deployed testnet
environment, using the Stellar `Router.ccip_send` entry point. Nothing is
deployed and no devenv is started; all accounts and addresses come from
`STAGING_*` environment variables. When the required variables are unset the
tests skip, so `go test ./...` stays green elsewhere.

| Test | What it checks |
| --- | --- |
| `TestStagingStellarToEVMMessage` | One data-only message Stellar → EVM: `CCIPMessageSent` on the OnRamp (dest, sender, fee token, receipt count), Router pulls exactly the quoted fee, and optionally `ExecutionStateChanged == Success` on the EVM OffRamp |
| `TestStagingStellarToEVMSequentialMessages` | `STAGING_SEND_BATCH_SIZE` back-to-back sends: unique message IDs, strictly increasing sequence numbers, optional execution of every message |
| `TestStagingStellarToEVMUnsupportedDestination` | `get_fee` / `ccip_send` to an unknown selector fail and move no fee tokens (spends nothing) |
| `TestStagingEVMToStellarMessage` | One data-only message EVM → Stellar: `CCIPMessageSent` on the EVM OnRamp (dest, sender, offRamp field = the Stellar OffRamp's raw id), exact fee paid, `ExecutionStateChanged == Success` on the Stellar OffRamp, and the receiver's `last_message_id` |
| `TestStagingEVMToStellarSequentialMessages` | `STAGING_SEND_BATCH_SIZE` back-to-back EVM sends: unique message IDs, increasing message numbers, every message executes on Stellar |
| `TestStagingEVMToStellarUnsupportedDestination` | EVM `getFee` reverts with `UnsupportedDestinationChain` and `ccipSend` fails gas estimation for an unknown selector (nothing broadcast) |

## Setup

1. Fund a Stellar testnet account with XLM (network fees) and the fee token.
2. `cp staging/staging.env.example staging/staging.env` (from `tests/`) and fill
   it in. Required: `STAGING_STELLAR_SENDER_SECRET`, `STAGING_STELLAR_ROUTER`,
   `STAGING_STELLAR_FEE_TOKEN`, `STAGING_STELLAR_CCVS`, `STAGING_EVM_RECEIVER`.
   Set `STAGING_EVM_RPC_URL` and `STAGING_EVM_OFFRAMP` to also wait for
   execution on Sepolia.
3. `STAGING_EVM_GAS_LIMIT` (default 200000) is the `ccipReceive` gas on the
   destination. Use 0 only for EOA receivers. A contract receiver given 0 gas
   runs out of gas, and the message fails with `ReceiverError(0x)`. With
   `STAGING_EVM_RPC_URL` set, the tests refuse to send in that case.

If the deployed Router predates `bbc2d2b4`, its `ccip_send` still takes a
trailing `fee_token_amount` and the generated binding fails with
`MismatchingParameterLen`. Set `STAGING_ROUTER_LEGACY_FEE_ARG=true` to pass the
quoted fee as that argument. Check the Router with
`stellar contract info interface --id <router> --network testnet`.

Before sending anything, each test extends the TTL of every contract it touches
(Router, OnRamp, fee token, CCVs, executor; OffRamp and receiver on the EVM →
Stellar side) to the network maximum in a single `ExtendFootprintTtl`
transaction paid by the sender. Without this, an expired WASM code entry makes
the RPC node re-upload it into every simulation footprint, the assembled send
transaction blows the transaction resource limit, and the call fails with
`Error(WasmVm, UnexpectedSize)` (often alongside `MismatchingParameterLen`
diagnostic events). Extending is permissionless, restores archived entries
first, and is a no-op once everything is at the maximum TTL.

### EVM → Stellar

These tests need `STAGING_EVM_RPC_URL`, `STAGING_EVM_SENDER_PRIVATE_KEY`,
`STAGING_EVM_ROUTER`, `STAGING_STELLAR_ROUTER`, `STAGING_STELLAR_RECEIVER` and
`STAGING_STELLAR_SENDER_SECRET`. On this side, the Stellar key is only used to
simulate read-only calls. The fee is paid in native ETH unless
`STAGING_EVM_FEE_TOKEN` names an ERC-20, which the test approves for the quoted fee.

The Stellar receiver must be a Wasm contract implementing `ccip_receive`. The
OffRamp rejects plain accounts as receivers of data messages. The example
receiver also rejects source chains it hasn't enabled, and the tests check this
before sending. To enable the EVM chain, the receiver's owner runs:

```sh
stellar contract invoke --id <receiver> --source-account <owner> --network testnet -- \
  enable_remote_chain --caller <owner> --dest_chain_selector 16015286601757825753 \
  --extra_args 01 --allowed_finality_config 0
```

## Run

From `tests/`:

```sh
set -a; source staging/staging.env; set +a
go test -v -count=1 -timeout 45m ./staging/... -run TestStagingStellarToEVM   # Stellar → EVM
go test -v -count=1 -timeout 45m ./staging/... -run TestStagingEVMToStellar   # EVM → Stellar
```

Each send costs real testnet fees, so run a single test with `-run` when iterating.
