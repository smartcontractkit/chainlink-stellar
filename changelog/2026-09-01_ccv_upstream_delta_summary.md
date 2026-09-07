# chainlink-ccv upstream delta — catch-up summary (2026-09-01)

Review of `chainlink-ccv` `CHANGELOG.md` (releases `v0.1.0` → `v0.6.0`) and its
`changelog/` entries, scoped to what affects how `chainlink-stellar` builds and runs.

## Baseline

The last stellar-side alignment record is `changelog/2026-05-05_ccv_keystore_devenv_alignment.md`,
but the **actual dependency pin is newer**: `go.mod` holds

```
github.com/smartcontractkit/chainlink-ccv          v0.0.2-0.20260608205628-b1fb1b311772
github.com/smartcontractkit/chainlink-ccv/build/devenv    (same)
github.com/smartcontractkit/chainlink-ccv/deployment      (same)
```

`b1fb1b31` is ccv `upgrade cl-ccip (#1154)`, **2026-06-08**. The pin was last touched in
stellar commit `1802473` (2026-08-25, "regen artifacts and tidy"), which carried it forward
unchanged from `1ac90ff`; before that it was `4f70eba1dfd2` (2026-05-18).

So the real gap is **ccv 2026-06-08 → 2026-09-01** — 32 upstream changelog entries and
six tagged releases. Everything below is relative to that.

---

## 1. Compile-breaking for stellar today

These break `go build ./...` the moment the ccv pin is bumped. Ordered by rework cost.

### 1.1 `chainaccess.GenericConfig` gutted — `blockchain_infos` removal
*Upstream:* `2026-07-14_blockchain_infos_removal.md`, release v0.2.0 (`#1271`, breaking).

`blockchain_infos` is gone from job/application config. Removed APIs include
`bootstrap.JobSpec.GetGenericConfig`, `chainaccess.GenericConfig.ChainConfig`,
`GenericConfig.GetConcreteConfig`, `GenericConfig.GetAllConcreteConfig`,
`executor.ConfigWithBlockchainInfo`, `executor.LoadConfigWithBlockchainInfos`.
Chain selectors now come from application-owned typed maps; RPC endpoints and
family tuning come from operator-local config.

**This is the largest single item.** `ccv/accessors/factory_constructor.go` is built almost
entirely on the removed surface:

- `loadStellarJobReaderInfos(genericConfig chainaccess.GenericConfig)` — reads
  `blockchain_infos` (line ~109-115)
- `buildStellarReaderConfigs` merges job `blockchain_infos` over the Stellar file TOML (~117-131)
- `applyOnRampRMNHexOverrides(..., genericConfig.OnRampAddresses, genericConfig.RMNRemoteAddresses)`
- `buildStellarDestConfigs` overlays `genericConfig.ChainConfiguration[selector].OffRampAddress / RmnAddress` (~143-190)

The replacement shape is: `JobSpec.GetAppConfig` with the application's typed config, plus
the Stellar-local operator config file for anything endpoint/tuning shaped. Related consumer:
`ccv/chain/modifier/executor.go` (`ChainConfiguration`) and
`deployment/adapters/ccv_deployment_adapters.go`.

### 1.2 `CommitteeVerifierOnchainAdapter` gained two methods
*Upstream:* `2026-07-01_lane_expansion_mcms_and_committee_onchain_products.md` (explicitly
called out as breaking for non-EVM families implementing the interface outside the repo).

`ccvdeploymentadapters.CommitteeVerifierOnchainAdapter` now requires
`SetAllowedFinalityConfig` and `ApplyAllowlistUpdates`.
`deployment/adapters/ccv_committee_verifier_onchain.go` implements only `ScanCommitteeStates`
(line 25) and `ApplySignatureConfigs` (line 88), and is registered in
`deployment/adapters/init.go` — so the registration stops compiling.

Same entry adds two methods to `LaneConfigAdapter` (remote ramps now resolved via the *remote*
chain's own adapter for family-correct encoding). Stellar has no `LaneConfigAdapter` impl today;
if lane config for Stellar destinations is expected to work from an EVM source, one is now needed.

### 1.3 `bootstrap.WithLogLevel` removed
*Upstream:* `2026-07-13_bootstrap_prune_functional_options.md`, release v0.1.0 (`#1265`, breaking).

`WithLogLevel` / `WithLogLevelFromEnv` were no-ops and are removed; the deprecated default
signing-key set is also removed. Delete the calls and set the level in the bootstrap TOML:

```toml
[Monitoring]
LogLevel = "info"
```

Call sites: `cmd/committee-verifier/main.go:29`, `cmd/executor/main.go:35`.
(`bootstrap.WithKey` is unchanged — the 2026-05-05 key declarations stay.)

### 1.4 `executor.DefaultEVMTransmitterKeyName` removed
*Upstream:* `2026-06-11_executor_transmitter_key_registry.md`.

Relocated to `contracttransmitter.DefaultKeyName`. `services.BootstrapKeys.EVMTransmitterAddress`
is replaced by a generic `PublicKeys map[string]string` + `PublicKeyHex(keyName)`, and
`services/executor.New` changed signature.

Call site: `cmd/executor/main.go:36` (the placeholder ECDSA key declared so devenv's
`GetKeys` POST does not 500 — see the comment block at the top of that file).

### 1.5 `cciptestinterfaces.ConfirmExecOnDest` returns `ExecEnvelope`
*Upstream:* `2026-07-23_tcapi_run_result_tx_ids.md` (breaking).

- `Chain.ConfirmExecOnDest` returns `ExecEnvelope` (event **plus** opaque tx id), not a bare event
- `tcapi.TestCase.Run` returns `(RunResult, error)` instead of `error`
- `tcapi.SendV3Message` gains a `protocol.ByteSlice` (tx id) return

Call sites: `ccv/chain/chain.go:735`, plus `tests/e2e/{evm_to_stellar,stellar_to_evm}_exec_test.go`
and both `*_token_transfer_test.go`.

### 1.6 Narrower V3 interfaces — this one *reduces* stellar's surface
*Upstream:* `2026-07-21_v3_source_destination_factories.md`.

`tcapi.SendV3Message` now takes `cciptestinterfaces.V3Source` / `V3Destination` instead of a full
`CCIP17`, and derives the destination selector from `dst.ChainSelector()` (the separate
`destSelector` parameter is gone). `tcapi` no longer references `CCIP17` except for `ChainsMap`.

Net effect for `ccv/chain/chain.go`: far fewer methods must exist purely to satisfy type
assertions. Worth taking as a simplification pass alongside 1.5.

---

## 2. Required to boot at all (non-EVM binaries)

### 2.1 `evmconfig` package split — fixes a Stellar verifier boot failure
*Upstream:* `2026-08-25_evm_config_driver_split.md`, release v0.5.0 (`#1375`).

Between `#1369` and `#1375`, `cli/migrate` imported the EVM accessor package and `cmd/verifier`
imports `cli/migrate` — so **every** binary built on `cmd/verifier`, including a downstream
non-EVM verifier, ran the EVM driver's `init()` and registered the EVM factory.
`chainaccess.NewRegistry` constructs every registered factory eagerly, and
`CreateEVMAccessorFactory` fails with no EVM config mounted, killing the process at
`bootstrap.Run` / `StartJob` with `failed to construct accessor factory for family evm`.
The changelog names a downstream Solana verifier; `cmd/committee-verifier/main.go` has exactly
that shape.

**Consequences:** do not pin ccv into the `#1369`–`#1375` window. After the bump, stellar must
import `integration/pkg/accessors/evmconfig` if it ever needs the EVM config types, and never
`integration/pkg/accessors/evm`. (`evm.Config`, `evm.ChainConfig`, `evm.Node`, `evm.Info`,
`evm.Conversion`, `evm.NewConfigFromInfos`, `evm.EVMConfigPathEnv`, `evm.DefaultEVMConfigPath`
survive as aliases.)

### 2.2 Stellar cannot yet sync signing keys to JD
*Upstream:* `2026-06-26_signing_key_sync_to_jd.md`.

Verifier nodes now publish their onchain signing address to JD via `feedsmanager.UpdateNode`
on every connect, declared through a new optional `[[chains]]` block in the bootstrap config.
Accepted `type` values: `EVM`, `SOLANA`, `APTOS`, `STARKNET`, `TRON`, `TON`, `SUI` —
**"Stellar and Canton require a JD proto update first."**

So the workaround described in the 2026-05-05 record (`ImplFactory.DefaultSignerKey` returning
the ECDSA address to keep devenv topology enrichment from falling through to JD) remains
necessary. Track the JD proto update as the unblocker.

Related: `2026-07-14_signing_identity_reader.md` adds a `SigningIdentityReader` registry so each
family declares whether `fetch_signing_keys` reads `OnchainSigningAddress` (20-byte EVM) or
`OnchainSigningPubKey` (raw secp256k1). Stellar's `committee_verifier` expects ETH-style 20-byte
identities, so it is an address-class family and **must still register a reader** — in *both* the
CCV and CCIP registries — or `fetch_signing_keys` will not index the Stellar family key.

---

## 3. Config surface changes (bootstrap / app / devenv TOML)

| Change | Upstream | Stellar impact |
|---|---|---|
| Monitoring moves from JD app config to operator bootstrap config; `[monitoring]` required per app; app-config `Monitoring` deprecated-then-ignored | `2026-06-24`, `2026-08-13_cutover_parity_followups` | Add `[monitoring]` to each bootstrap TOML; drop app-config `Monitoring` |
| Bootstrap owns Beholder + logger; `ServiceFactory.MetricViews() []sdkmetric.View` required | `2026-06-30` | Low: stellar uses upstream `verifiercmd.NewCommitteeVerifierServiceFactory()` / `executorcmd.NewFactory()`, so this is satisfied upstream. Use `deps.Logger`. |
| `bootstrap.Config` split into embedded `NonSecretConfig` + `Secrets` | `2026-07-07_bootstrap_config_secret_split` | Reads unaffected; only keyed composite literals break — none found in stellar |
| `app_config_mode = "jd_app_config" \| "local_app_config"` | `2026-07-09` | Enables a JD-free Stellar devenv / local testing path |
| Verifier secrets file `COMMITTEE_VERIFIER_SECRETS_PATH` (default `/etc/committee-verifier/secrets.toml`); file wins over env | `2026-07-07_verifier_secrets_file` | Optional; env vars still work |
| `[key_import]` adopts a Chainlink-node-exported key instead of generating one | `2026-07-29`, v0.3.0 (`#1317`) | EVM migration story; no Stellar action |
| `[protocol_contracts.deploy]` TOML section; committee verifiers + mock receivers move from Phase 2 to Phase 3 | `2026-06-17` | `tests/env/env-stellar-evm.toml` needs the new section and phase expectations |
| `use_legacy_configure_lane` flag and `deploy.ConnectAllChainsLegacy` **removed**; changeset inputs now derived from live state (`*FromState`, `NOPIdentities`) | `2026-06-26_state_based_offchain_inputs` (breaking in `build/devenv`) | `tests/env/env-stellar-evm-out.toml` still carries `use_legacy_configure_lane` |
| `chainreg.ExecutorInfo` per-family registration (which key name, how to decode to an address) | `2026-06-11` | Stellar must register `ExecutorInfo` so devenv funds the Ed25519 transmitter without edits to shared devenv code — this is the clean home for `common.StellarTransmitterKeyName` |
| Standalone EVM node config: one `http_url` + optional `ws_url` per node; four-URL schema removed, loader is strict | `2026-07-27` | EVM-only, but the strict loader means stale mounted EVM config in the shared devenv fails hard |

---

## 4. Behavior changes worth knowing (little or no stellar code change)

- **RMN Remote derived from on-chain ramp static config** (`2026-08-14`, v0.5.0 `#1357`).
  Readers read RMN Remote from OnRamp/OffRamp static config at construction instead of trusting
  configured values. `rmn_remote_addresses` (verifier app config) and `rmn_address` (executor
  chain config) are **deprecated but still accepted**; a disagreeing configured value logs a
  warning and the derived address wins. Both reader constructors now **fail at startup** if the
  derivation read fails. Also, `vtypes.SourceConfig.RMNRemoteAddress` is removed (`2026-08-13`).
  → Directly relevant to the RMN overlay logic in `ccv/accessors/factory_constructor.go`
  (`applyOnRampRMNHexOverrides`, `rmnRemoteContractID`) and to `ccv/contract_transmitter`,
  `ccv/chain/modifier/{executor,committeeverifier}.go`. Stellar's readers should adopt the same
  derivation rather than continuing to plumb the address through config.

- **Multi-aggregator committee verifier** (`2026-06-19`, `2026-06-22`). A verifier can fan out to
  multiple aggregators from one job via `[[aggregators]]` / `commit.Config.Aggregators`;
  `AggregatorAddress` is deprecated. Breaking: `NewVerificationCoordinator` takes
  `map[string]*hmac.ClientConfig` keyed by `AggregatorConnection.SecretName` (legacy config uses
  the `""` key). Stellar does not call `NewVerificationCoordinator` directly — additive for us.

- **Distributed tracing** (`2026-07-28`, v0.3.0 `#1297`). `executor.NewCoordinator` gains a
  required `executorID string` parameter; `traceparent` added to `verifier_node_results` as a
  breaking DB migration (`NOT NULL DEFAULT ''`). Devenv DB volumes need recreating.

- **JD lifecycle hardening** (`2026-06-26` ×2, `2026-07-14_jd_replacement_validation`).
  Two-phase proposal persistence with crash recovery, fallback to the previous job on failed
  replacement, and pre-stop validation of replacement specs via an optional
  `ValidatingJobRunner`. Breaking: `client.ClientInterface` gained `UpdateNode` — matters only if
  stellar has its own JD test double.

- **Signal-driven job queue** (`2026-08-31`). `JobQueue[T].Consume` → `ConsumePending` /
  `ReclaimStale` / `Signals`; `taskverifier`/`storagewriter` `NewProcessorWithPollInterval`
  removed. Internal to the verifier; no stellar call sites.

- **Executor / verifier observability restored after cutover** (`2026-08-11` ×2, `2026-08-13`).
  Notably `chainaccess.CriticalSourceInvariantCallbackSetter` and
  `chainaccess.ExecutorMonitoringSetter` are new **optional** interfaces the accessor's readers
  and transmitters can implement — if `ccv/accessors` implements them, Stellar gets the same
  OffRamp read-latency / unrecoverable-transmit / critical-invariant metrics EVM has. Also, both
  binaries now accept `http_listen_port` in the app config and the executor serves `/health`.

- **Verifier poll timing unified** to a 2s interval with a dedicated head-fetch timeout
  (`2026-08-13`); standalone's 1s polling is gone.

- **No stellar action:** standalone EVM production chain services (`2026-07-22`), KMS support
  (v0.3.0 `#1301`), CSA key mode/backend-driven (`#1322`), migration tooling (`2026-08-20`),
  batched chain status updates (v0.5.0) and batched block header fetches (v0.6.0),
  Lombard Solana format / token verifier factory refactor, indexer HMAC fix,
  `RemoveRemotePool` + the new `TokenPoolOnchainAdapter` (only needed if Stellar token pools
  should support removal — no stellar impl exists).

---

## 5. Suggested order of work

1. **Bump the pin past `2026-08-25` (v0.5.0+)** — anything in the `#1369`–`#1375` window boots
   dead for a non-EVM verifier (§2.1).
2. **Mechanical compile fixes first:** drop `WithLogLevel` (§1.3), swap
   `executor.DefaultEVMTransmitterKeyName` → `contracttransmitter.DefaultKeyName` (§1.4).
3. **Rewrite `ccv/accessors/factory_constructor.go` off `GenericConfig`/`blockchain_infos`**
   (§1.1) — schedule this with the RMN on-chain derivation adoption (§4) since they touch the
   same address-overlay code.
4. **Add `SetAllowedFinalityConfig` + `ApplyAllowlistUpdates`** to
   `StellarCCVCommitteeVerifierOnchainAdapter` (§1.2).
5. **Update `cciptestinterfaces` impls and e2e tests** for `ExecEnvelope` / `RunResult`, and take
   the `V3Source`/`V3Destination` narrowing as a simplification (§1.5, §1.6).
6. **Register `chainreg.ExecutorInfo` and a `SigningIdentityReader` for `FamilyStellar`** (§3, §2.2).
7. **Devenv TOML:** `[monitoring]` in bootstrap configs, `[protocol_contracts.deploy]`, drop
   `use_legacy_configure_lane`, recreate verifier DB volumes for the `traceparent` migration.
8. **Optional but cheap wins:** implement `CriticalSourceInvariantCallbackSetter` /
   `ExecutorMonitoringSetter` in `ccv/accessors` for metric parity with EVM.
9. Re-run `make docker-ccv-dev` before `make up` (carried over from the 2026-05-05 record).

## 6. Open question

Whether `ccvadapters.VerifierContractAddresses.ExecutorProxyAddress` still exists.
`2026-07-06_remove_executor_proxy_from_public_api.md` removes it from **chainlink-ccv**'s
`deployment/adapters/verifier_config.go` and adds `ExecutorConfigAdapter.ResolveExecutorAddress`.
`deployment/adapters/verifier_config_adapter.go:9` imports the **chainlink-ccip**
`deployment/v2_0_0/adapters` package, and line 59 sets `ExecutorProxyAddress` there. Confirm
whether chainlink-ccip mirrored the removal at the ccip ref the new ccv pin pulls in; if so,
`deployment/adapters/{verifier,executor}_config_adapter.go` need the same consolidation, and the
second `ExecutorProxy` datastore contract type registered purely to satisfy the verifier adapter
(`stellarccip.ExecutorProxyDatastoreRef`) can be retired.
