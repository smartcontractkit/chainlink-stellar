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
| `TestStagingStellarToEVMTokenTransfer` | Both pools are linked to each other, then one BnM token transfer (with data) Stellar → EVM: the pool burns exactly the amount from the sender, the Router pulls exactly the quoted fee, and optionally `ExecutionStateChanged == Success` with the EVM receiver minted the decimal-scaled amount |
| `TestStagingStellarToEVMUnsupportedDestination` | `get_fee` / `ccip_send` to an unknown selector fail and move no fee tokens (spends nothing) |
| `TestStagingEVMToStellarMessage` | One data-only message EVM → Stellar: `CCIPMessageSent` on the EVM OnRamp (dest, sender, offRamp field = the Stellar OffRamp's raw id), exact fee paid, `ExecutionStateChanged == Success` on the Stellar OffRamp, and the receiver's `last_message_id` |
| `TestStagingEVMToStellarSequentialMessages` | `STAGING_SEND_BATCH_SIZE` back-to-back EVM sends: unique message IDs, increasing message numbers, every message executes on Stellar |
| `TestStagingEVMToStellarTokenTransfer` | Both pools are linked to each other, then one BnM token transfer (with data) EVM → Stellar: the pool burns exactly the amount from the sender, `ExecutionStateChanged == Success` on the Stellar OffRamp, the receiver's `last_message_id`, and the Stellar receiver minted exactly the amount |
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

### Token transfers

The two `*TokenTransfer` tests also need `STAGING_STELLAR_TOKEN`,
`STAGING_STELLAR_TOKEN_POOL`, `STAGING_EVM_TOKEN` and `STAGING_EVM_TOKEN_POOL`:
a burn-mint pool pair on each side, each pool registered as the other's remote
pool. Before sending, the tests check:

- each pool serves its token and supports the other chain;
- each pool names the other side's token and pool as its remote token and pool;
- the Stellar pool's ramp registry accepts the lane's OnRamp (Stellar → EVM) or
  OffRamp (EVM → Stellar).

`STAGING_TOKEN_TRANSFER_AMOUNT` (default 1000) is in the Stellar token's base
units. The EVM-side amount is scaled up by the difference in decimals read from
the pools: 10^11 for a 7-decimal BnM token and an 18-decimal ERC-20. This way
the pools' conversion is exact in both directions. If the sender holds less than
the amount, the test tops it up from the token's permissionless `drip` faucet,
where the token has one. Each drip gives 0.1 BnM on Stellar and 1 token on EVM.
On EVM, the test approves the Router for the transfer amount plus the fee, if
the fee is paid in an ERC-20.

The receiver balance checks are exact. They assume nothing else sends this token
to the same receiver while the test runs.

#### Prerequisites

These came up while bringing the tests up against the `stgBnM` pair. Each one
fails in a way that is hard to trace back to its cause.

1. **The Stellar pool's ramp registry must know the lane's ramps.** The pool
   checks its caller against its own ramp registry (`get_ramp_registry`), not
   the Router. With an empty registry, `ccip_send` fails in simulation with
   `Error(Contract, #63)` (`UnsupportedDestinationChain`) raised from the
   pool's `lock_or_burn`. On EVM → Stellar, the message is burned on Sepolia
   and then fails execution on Stellar. The registry owner registers both ramps:

   ```sh
   stellar contract invoke --id <registry> --network testnet --source-account <owner> -- \
     apply_onramp_updates --updates '[{"dest_chain_selector":16015286601757825753,"onramp":"<stellar onramp>"}]'
   stellar contract invoke --id <registry> --network testnet --source-account <owner> -- \
     apply_offramp_updates --updates '[{"source_chain_selector":16015286601757825753,"offramp":"<stellar offramp>","enabled":true}]'
   ```

2. **The Stellar FeeQuoter must budget enough EVM gas for the token pool.** The
   Stellar OnRamp sets a message's EVM gas budget to the sum of:
   - the CCV gas;
   - the pool gas, which is the FeeQuoter's `dest_gas_overhead` for the token;
   - `base_execution_gas_cost`;
   - the receiver gas limit.

   The executor sizes its transaction from that budget. Without a per-token
   config, the pool gas falls back to the chain default of 90000. That is too
   little on Sepolia since its Glamsterdam gas repricing: a transfer to a
   contract receiver needed about 675k gas but was sent with 571k. Execution
   then fails with `ExecutionStateChanged` state 3 and return data `0x37c3be29`
   (`NotEnoughGasForCall`). Data-only messages are unaffected. Raising
   `STAGING_EVM_GAS_LIMIT` does not help, because it raises the gas the
   receiver call requires by the same amount. The FeeQuoter owner sets a
   per-token value:

   ```sh
   stellar contract invoke --id <fee quoter> --network testnet --source-account <owner> -- \
     apply_token_fee_configs --remove_args '[]' --config_args \
     '[{"config":{"dest_bytes_overhead":32,"dest_gas_overhead":200000,"fee_usd_cents":0,"is_enabled":true},"dest_chain_selector":16015286601757825753,"token":"<stellar token>"}]'
   ```

   Local `cast` replays use the older gas schedule and wrongly show such
   messages succeeding. Simulate with the node instead (`eth_simulateV1`). A
   message in the Failure state can be retried by anyone. Resend the failed
   transaction's calldata to the OffRamp with a higher gas limit:

   ```sh
   cast send <evm offramp> "$(cast tx <failed tx> input --rpc-url $STAGING_EVM_RPC_URL)" \
     --gas-limit 750000 --private-key $STAGING_EVM_SENDER_PRIVATE_KEY --rpc-url $STAGING_EVM_RPC_URL
   ```

3. **The EVM sender must already hold the EVM token.** The Sepolia `stgBnM`
   token is a role-gated `BurnMintERC20` with no `drip`, despite its datastore
   type, and only its pool holds `MINTER_ROLE`. The test fails up front if the
   sender is short. To fund it, run the Stellar → EVM test once with
   `STAGING_EVM_RECEIVER` set to the EVM sender. An EOA receiver makes it a
   token-only transfer. A larger amount funds many runs:

   ```sh
   STAGING_EVM_RECEIVER=<evm sender> STAGING_TOKEN_TRANSFER_AMOUNT=100000 \
     go test -v -count=1 -timeout 45m ./staging/... -run TestStagingStellarToEVMTokenTransfer
   ```

4. **Each pool must be able to mint and burn its token.** On Stellar, the pool
   must be in the token's `get_minters`. On EVM, it needs `MINTER_ROLE` and
   `BURNER_ROLE`. Both are true for `stgBnM`; check them on any new pair.

EVM → Stellar transfers take about 15 minutes. The verifiers wait for Sepolia
finality (two epochs, about 13 minutes) before signing, and the default
`STAGING_STELLAR_EXEC_TIMEOUT` of 30m allows for that. Stellar → EVM is faster.

## Run

From `tests/`:

```sh
set -a; source staging/staging.env; set +a
go test -v -count=1 -timeout 45m ./staging/... -run TestStagingStellarToEVM   # Stellar → EVM
go test -v -count=1 -timeout 45m ./staging/... -run TestStagingEVMToStellar   # EVM → Stellar
```

Each send costs real testnet fees, so run a single test with `-run` when iterating.
