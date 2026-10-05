# Stellar chain fakes

In-process implementation of the Stellar chain capability
(`stellar:ChainSelector:<selector>@1.0.0`) used by cre-cli's
`cre workflow simulate`. 

This module is deliberately small (chainlink-common + go-stellar-sdk) so cre-cli
can import it without pulling in the relayer or deployment dependency trees.

## Reads

`ReadContract` and `GetLatestLedger` proxy to the configured Stellar RPC.
`ReadContract` runs the call through `simulateTransaction`; an empty
`source_account` uses the all-zero placeholder account.

## How `WriteReport` works

Production DON writes go through the CRE forwarder (`contracts/cre/forwarder`),
which verifies `f+1` ed25519 DON signatures and only accepts allowlisted
transmitters. A local simulation can produce neither, so the fake writes
through the **mock forwarder** (`contracts/cre/mock_forwarder`) instead: a
permissionless contract with the same `report(transmitter, receiver,
raw_report, report_context, signatures)` entrypoint, report layout, replay
protection, `on_report` dispatch and `forwarder_ReportProcessed` event, minus
signature and transmitter checks. The fake sends an empty signature vector.

Result mapping mirrors production:

| Outcome | `TxStatus` | `ReceiverContractExecutionStatus` |
|---|---|---|
| Forwarder rejects the call (malformed report, replay, archived state) | `REVERTED` | unset |
| Forwarder accepts; receiver `on_report` succeeds | `SUCCESS` | `SUCCESS` |
| Forwarder accepts; receiver `on_report` errors or traps | `SUCCESS` | `REVERTED` |
| Submission rejected by the network (broadcast only) | `FATAL` | unset |

The receiver outcome is read from the forwarder's `ReportProcessed.success`.

## Modes

- **Dry-run** (`Config.DryRun = true`): `report()` is run through
  `simulateTransaction` in record-auth mode. Nothing is submitted and no key is
  needed (a placeholder transmitter is used when `Transmitter` is nil). The
  returned `tx_hash` is the hash of the unsigned simulated transaction.
- **Broadcast**: the fake loads the transmitter account, preflights with
  `simulateTransaction`, attaches the Soroban footprint/resource fee (+20%
  headroom) and auth entries, signs with `Config.Transmitter`, submits with
  `sendTransaction` and polls `getTransaction` until confirmed or
  `Config.TxTimeout`. A preflight failure is returned as `REVERTED` without
  submitting. Writes are serialized per fake so sequence numbers never race.
  The transmitter account must exist and be funded.

## What receiver authors still need

Receivers see the **mock forwarder** as `sender` in `on_report`. A receiver
that gates on its trusted forwarder (e.g. the data-feeds cache's
`(sender, workflow_owner, workflow_name)` permissions) must be configured to
trust the mock forwarder for writes to take effect. Use a test receiver for
this; never configure a production receiver to accept the mock forwarder.

Receiver events are logged at info level, so permission rejections such as
`InvalidUpdatePermission` show up in simulation output.
