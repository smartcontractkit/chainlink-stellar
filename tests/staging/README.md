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

## Setup

1. Fund a Stellar testnet account with XLM (network fees) and the fee token.
2. `cp staging/staging.env.example staging/staging.env` (from `tests/`) and fill
   it in. Required: `STAGING_STELLAR_SENDER_SECRET`, `STAGING_STELLAR_ROUTER`,
   `STAGING_STELLAR_FEE_TOKEN`, `STAGING_STELLAR_CCVS`, `STAGING_EVM_RECEIVER`.
   Set `STAGING_EVM_RPC_URL` and `STAGING_EVM_OFFRAMP` to also wait for
   execution on Sepolia.

If the deployed Router predates `bbc2d2b4`, its `ccip_send` still takes a
trailing `fee_token_amount` and the generated binding fails with
`MismatchingParameterLen`. Set `STAGING_ROUTER_LEGACY_FEE_ARG=true` to pass the
quoted fee as that argument. Check the Router with
`stellar contract info interface --id <router> --network testnet`.

## Run

From `tests/`:

```sh
set -a; source staging/staging.env; set +a
go test -v -count=1 -timeout 45m ./staging/... -run TestStagingStellarToEVM
```

Each send costs real testnet fees, so run a single test with `-run` when iterating.
