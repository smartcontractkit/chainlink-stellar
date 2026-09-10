# chainlink-ccv upstream delta — catch-up summary (2026-09-01)

Review of `chainlink-ccv` `CHANGELOG.md` (releases `v0.1.0` → `v0.6.0`) and its
`changelog/` entries, scoped to what affects how `chainlink-stellar` builds and runs.

> **Status update (2026-09-08): the bump is done and `make up` brings the environment up.**
> The pin is now `v0.6.1-0.20260901122814-abf56d76c31b` (MVS resolves the root module to
> `v0.9.0`). Everything in §1–§3 landed; §7 records what the original analysis missed.
> Remaining work is listed in §5 ("Remaining"). Sections §1–§4 are the original analysis,
> kept as-is with status annotations.

## Baseline

The last stellar-side alignment record is `changelog/2026-05-05_ccv_keystore_devenv_alignment.md`.
The dependency pin at the time of this review was:

```
github.com/smartcontractkit/chainlink-ccv          v0.0.2-0.20260608205628-b1fb1b311772
github.com/smartcontractkit/chainlink-ccv/build/devenv    (same)
github.com/smartcontractkit/chainlink-ccv/deployment      (same)
```

`b1fb1b31` is ccv `upgrade cl-ccip (#1154)`, **2026-06-08**. The pin was last touched in
stellar commit `1802473` (2026-08-25, "regen artifacts and tidy"), which carried it forward
unchanged from `1ac90ff`; before that it was `4f70eba1dfd2` (2026-05-18).

So the gap covered here is **ccv 2026-06-08 → 2026-09-01** — 32 upstream changelog entries and
six tagged releases.

---

## 1. Compile-breaking for stellar

### 1.1 `chainaccess.GenericConfig` gutted — `blockchain_infos` removal ✅ done
*Upstream:* `2026-07-14_blockchain_infos_removal.md`, release v0.2.0 (`#1271`, breaking).

`blockchain_infos` is gone from job/application config. Removed APIs include
`bootstrap.JobSpec.GetGenericConfig`, `chainaccess.GenericConfig.ChainConfig`,
`GenericConfig.GetConcreteConfig`, `GenericConfig.GetAllConcreteConfig`,
`executor.ConfigWithBlockchainInfo`, `executor.LoadConfigWithBlockchainInfos`.

**Resolution:** `ccv/accessors/factory_constructor.go` was rewritten off the removed surface:
reader configs come solely from the bind-mounted Stellar TOML; `applyOnRampRMNHexOverrides`
still fills OnRamp/RMN contract IDs from `GenericConfig.OnRampAddresses` /
`RMNRemoteAddresses` (both still exist on the slimmer `GenericConfig`), and
`buildStellarDestConfigs` overlays `genericConfig.ChainConfiguration[selector].OffRampAddress
/ RmnAddress` (also still present). `factory_constructor_test.go` was rewritten accordingly.
The RMN on-chain derivation (§4) was **not** adopted — the address is still plumbed through
config; see §5 "Remaining".

### 1.2 `CommitteeVerifierOnchainAdapter` gained two methods ✅ done
*Upstream:* `2026-07-01_lane_expansion_mcms_and_committee_onchain_products.md`.

`SetAllowedFinalityConfig` and `ApplyAllowlistUpdates` were added to
`StellarCCVCommitteeVerifierOnchainAdapter` (finality config is a logged no-op — the Soroban
contract has no finality gate; allowlist updates are wired to the contract). No
`LaneConfigAdapter` was added: devenv configures Stellar lanes through the ccip ChainFamily
adapter; `GetLaneConfigRegistry` is deliberately skipped (see the TODO in
`deployment/adapters/init.go`).

Note: `ApplySignatureConfigs` there also gained ascending signer sorting — the contract
requires it (CCIPError #66/#67); see §7.4.

### 1.3 `bootstrap.WithLogLevel` removed ✅ done
*Upstream:* `2026-07-13_bootstrap_prune_functional_options.md`, release v0.1.0 (`#1265`, breaking).

Calls dropped from `cmd/committee-verifier/main.go` and `cmd/executor/main.go`; the log level
is set via `LogLevel = "info"` in `[environment_topology.monitoring]` (feeds
`Bootstrap.Monitoring`) in `tests/env/env-stellar-evm.toml`.

### 1.4 `executor.DefaultEVMTransmitterKeyName` removed ✅ done — and it is load-bearing
*Upstream:* `2026-06-11_executor_transmitter_key_registry.md`.

The constant moved to `contracttransmitter.DefaultKeyName`
(`integration/pkg/contracttransmitter`). **Caution learned the hard way:** that ECDSA key
declaration is not only a placeholder for devenv's `GetKeys` POST — the bootstrapper only
publishes chain configs / signing keys to JD (`UpdateNode` on connect) when at least one
ECDSA_S256 key is declared. Commenting it out instead of swapping the constant caused the
executor chain-support validation failure in §7.5. Keep the declaration.

### 1.5 `cciptestinterfaces.ConfirmExecOnDest` returns `ExecEnvelope` ✅ done
*Upstream:* `2026-07-23_tcapi_run_result_tx_ids.md` (breaking).

`ccv/chain/chain.go` `ConfirmExecOnDest` returns `ExecEnvelope` (TxID left empty — the OffRamp
event waiter does not expose a transaction hash). E2E call sites updated to
`.Event.State` / `.Event.ReturnData`. `tcapi.SendV3Message` / `TestCase.Run` are unused in
this repo, so the other two bullets needed no work.

### 1.6 Narrower V3 interfaces ✅ no-op
*Upstream:* `2026-07-21_v3_source_destination_factories.md`.

No stellar code called the widened interfaces; nothing to simplify.

---

## 2. Required to boot at all (non-EVM binaries)

### 2.1 `evmconfig` package split — fixes a Stellar verifier boot failure ✅ avoided
*Upstream:* `2026-08-25_evm_config_driver_split.md`, release v0.5.0 (`#1375`).

The pin jumped straight to 2026-09-01 (`abf56d76`), past the `#1369`–`#1375` window, so the
deadly eager-EVM-factory init never shipped in a stellar build. No code imports the EVM
accessor package.

### 2.2 Stellar signing-key sync to JD ✅ resolved upstream at this pin
*Upstream:* `2026-06-26_signing_key_sync_to_jd.md`.

The 06-26 entry said "Stellar and Canton require a JD proto update first" — that update has
landed: `chainlink-protos/job-distributor v0.20.0` has `CHAIN_TYPE_STELLAR`, the ccv
bootstrapper accepts `type = "stellar"` in `[[chains]]`, and devenv's
`familiesSupportingJDKeySync` includes Stellar. The `ImplFactory.DefaultSignerKey` ECDSA
workaround stays (the on-chain committee_verifier stores 20-byte ETH-style identities).

**Operational caveat:** the JD *server* image must be new enough to round-trip
`CHAIN_TYPE_STELLAR`. A `job-distributor:local` built before the proto update persists the
chain type as `UNSPECIFIED`, and `fetch_node_chain_support` skips it — rebuild the image
(`docker rmi job-distributor:local && just build-jd-docker` in
`chainlink-ccv/build/devenv`; the recipe skips when the image exists). See §7.6.

`SigningIdentityReader` is registered for `FamilyStellar` in **both** registries (address-class
reader, matching the 20-byte ETH-style on-chain identities): ccv's
`deployment/shared` and ccip's `v2_0_0/offchain/shared`. The ccip-side registration was nearly
missed — its `fetch_signing_keys` only indexes `RegisteredSigningIdentityFamilies()`.

---

## 3. Config surface changes (bootstrap / app / devenv TOML)

| Change | Upstream | Status |
|---|---|---|
| Monitoring moves to operator bootstrap config | `2026-06-24`, `2026-08-13` | ✅ `[environment_topology.monitoring]` updated: `Enabled`/`Type` removed, `LogLevel = "info"` added, `Beholder.Enabled = true`; same for `[indexer.indexer_config.Monitoring]` |
| Bootstrap owns Beholder + logger; `MetricViews()` | `2026-06-30` | ✅ satisfied upstream (stellar uses upstream service factories) |
| `bootstrap.Config` split `NonSecretConfig` + `Secrets` | `2026-07-07` | ✅ no keyed literals in stellar |
| `app_config_mode` | `2026-07-09` | available; not used by the stellar devenv (JD mode) |
| Verifier secrets file | `2026-07-07` | not adopted; env vars still work |
| `[key_import]` | `2026-07-29` | EVM-only; no action |
| `[protocol_contracts.deploy]`; Phase 2 → Phase 3 move | `2026-06-17` | ✅ no TOML change needed at this pin (section optional) |
| `use_legacy_configure_lane` removed | `2026-06-26` | ✅ input TOML clean; the stale `env-stellar-evm-out.toml` is regenerated by `make up` (strict legacy decode of the old file fails — delete it if a resume/verify path reads it first) |
| `chainreg.ExecutorInfo` registration | `2026-06-11` | ✅ `ImplFactory` implements `ExecutorTransmitterKeyName` (`common.StellarTransmitterKeyName`) and `ExecutorTransmitterAddress` (hex of the Ed25519 pubkey — the account address); registered in `RegisterStellarDevenvComponents`. Funding confirmed in the bring-up logs |
| Aggregator `api_clients.description` removed | (schema strictness) | ✅ dropped from `tests/env/env-stellar-evm.toml` |
| Executor pool `nop_aliases` must be family-scoped | (JD chain-support validation) | ✅ EVM chains → `evm-executor-1`, stellar chain → `stellar-executor-1`; see §7.5 |
| Verifier `node_index` unique per committee | (devenv validation) | ✅ stellar verifiers 0–1, EVM verifiers 2–3; mixed-family committee is the intended model (per-source-chain NOP scoping) |
| Standalone EVM node config | `2026-07-27` | EVM-only; no stellar action |

---

## 4. Behavior changes worth knowing (little or no stellar code change)

- **RMN Remote derived from on-chain ramp static config** (`2026-08-14`, v0.5.0 `#1357`).
  `rmn_remote_addresses` / `rmn_address` are deprecated upstream; readers derive the address
  at construction and fail startup if the read fails. **Stellar has not adopted this** — the
  Stellar reader/transmitter still take the address from config (`applyOnRampRMNHexOverrides`,
  `rmnRemoteContractID`). Tracked in §5 "Remaining".

- **Multi-aggregator committee verifier** (`2026-06-19`, `2026-06-22`). Additive; no stellar
  call sites.

- **Distributed tracing** (`2026-07-28`, v0.3.0 `#1297`). `traceparent NOT NULL` migration:
  verifier DB volumes must be recreated on first `make up` with the new images.

- **JD lifecycle hardening** (`2026-06-26` ×2, `2026-07-14`). No stellar JD test doubles; no
  action.

- **Signal-driven job queue** (`2026-08-31`). Internal; no stellar call sites.

- **Executor / verifier observability hooks** (`2026-08-11` ×2, `2026-08-13`).
  `chainaccess.CriticalSourceInvariantCallbackSetter` / `ExecutorMonitoringSetter` remain
  **unimplemented** in `ccv/accessors` — optional metric parity, still open (§5).

- **Verifier poll timing unified** to 2s (`2026-08-13`). No action.

- **No stellar action:** standalone EVM production chain services, KMS, CSA key modes,
  migration tooling, batched chain status / block header fetches, Lombard Solana, indexer
  HMAC, `RemoveRemotePool`/`TokenPoolOnchainAdapter`.

---

## 5. Order of work — outcome and what remains

Completed: pin bump past the hazard window (1), mechanical cmd fixes (2 — with the §1.4
correction), `factory_constructor.go` rewrite (3 — without the RMN derivation), committee
onchain adapter methods (4), `ExecEnvelope` + e2e updates (5), `ExecutorInfo` +
`SigningIdentityReader` registrations (6 — both registries), devenv TOML config surface (7).

**Remaining:**

1. **Adopt RMN Remote on-chain derivation** (§4) in the Stellar source/destination readers and
   contract transmitter, then drop the config plumbing.
2. **Recreate verifier DB volumes** for the `traceparent` migration on first clean `make up`.
3. **Optional metric parity:** implement `CriticalSourceInvariantCallbackSetter` /
   `ExecutorMonitoringSetter` in `ccv/accessors`.
4. **E2E validation:** run `make test-e2e` against a fresh environment; the first full pass
   after this bump has not completed yet.
5. Consider deleting the now-unused `stellarutil.ResolveSignersFromOffchainTopology` (only its
   test references it; signers are resolved at lane-config time now).

## 6. Open question — resolved

`ccvadapters.VerifierContractAddresses.ExecutorProxyAddress`: chainlink-ccip did mirror the
consolidation. The ccip-side offchain-config adapters/registries (verifier, executor, indexer,
token verifier, aggregator) were **deleted upstream**; stellar's implementations moved to the
`StellarCCVDeployment*` types registered with chainlink-ccv's per-concern `FamilyRegistry`s
(`deployment/adapters/ccv_deployment_adapters.go`, registrations in `init.go`), and the old
`*_config_adapter.go` files were deleted.

`stellarccip.ExecutorProxyDatastoreRef` was **not** retired: it is still the resolution path
for `ChainFamily.ResolveExecutor` (the lane changeset uses it to wire executor addresses on
remote configs). Retiring it needs a replacement lookup, not just deletion.

---

## 7. Issues discovered during the bring-up (not in the original delta)

These only surfaced once the build was green and `make up` was attempted. All are fixed unless
noted.

### 7.1 chainlink-ccip `DeployChainContractsAdapter` redesigned (biggest hidden item)
The ccip/deployment bump (2026-08-17) replaced the two sequence-returning methods with a
4-step pipeline: `GetDefaultDeployContractParams` → `ResolveDeployAddresses` →
`BuildDeployContractParams` → `DeployChainContracts`, with topology-derived committee
verifiers and per-chain overrides. `StellarDeployChainContractsAdapter` implements all four;
the Stellar deploy sequence still sources its params from the pre-deploy topology stash
(params/DeployerContract are threaded for changeset parity, not consumed). The orphaned
`sequences.StellarImportConfigForDeployContracts` was deleted.

### 7.2 Module-graph conflicts with `otel/log v0.22.0`
The `chainlink-common` bump selected `otel/log v0.22.0`, which removed API that
`otelzap v0.10.0` and `wasp v1.52.0` compile against — breaking **all test binaries**
transitively. Fixed by requiring `otelzap v0.20.1` and `wasp v1.53.0` in `go.mod`.

### 7.3 Bindings regen: MCMS/timelock wire format changed
The 2026-08-21 bindings carry new contract interfaces:
`timelock.Call{Target, Function, ArgsXdr}` and `mcms.StellarOp{NetworkId, Multisig, Target,
Function, ArgsXdr, EncodingVersion, Nonce}` (strkey addresses, function name split out,
args-only `Vec<Val>` XDR). Merkle leaf hashing follows `contracts/mcms/src/encoding.rs`
(encoding-version prefix, u64 counts, `config_version`) — `tests/testutils/mcms_helpers.go`
rewritten to match. `execute_batch` is now permissionless (no executor role); timelock
`initialize` dropped admin/executors; MCMS `initialize` applies the signer config atomically
(config_version 1). `mcmsops.Initialize` (a no-op stub) was restored with the new signature —
without it `set_config` reverts on uninitialized instances, blocking the MCMS e2e path.
`mcmsutil.EncodeSorobanInvokeArgs` added for args-only encoding; the legacy symbol-prefixed
`EncodeSorobanMCMSInvokePayload` is retained for the MCMS proposal-transformer path
(`transfer_ownership.go`).

### 7.4 Committee signer quorums moved to lane-configuration time
Deploy-time `ApplySignatureConfigs` in `RunStellarCCIPFullDeploy` was removed: since the
signing-key-sync cutover, topology enrichment skips JD-sync families, so signers are absent at
deploy time. The upstream flow resolves them from JD (topology fallback) inside
`ConfigureChainsForLanesFromTopology` and calls the family `ConfigureChainForLanes` hook —
implemented as `sequences.StellarConfigureChainForLanes` (previously a no-op). Two latent bugs
fixed there and in the onchain adapter: signers must be **sorted ascending** (CCIPError #67
`InvalidSignerOrder`).

### 7.5 Executor JD chain-support validation
`ApplyExecutorConfig` validates each executor NOP's JD chain configs against the pool's
required chains (`fetch_node_chain_support`). Three learnings:
- The bootstrapper pushes chain configs only when an **ECDSA_S256 key is declared**
  (§1.4) — the `WithKey(contracttransmitter.DefaultKeyName, …)` line is required.
- Bootstrap `[[chains]]` are single-family, so cross-family `nop_aliases` in
  `executor_pools` can never validate — scope them per family in the env TOML.
- The `stellarexecutor:dev` / `stellarcommittee-verifier:dev` images bake the binary at image
  build time — after any `cmd/` change, rebuild with `make docker-executor docker-verifier`
  before `make up`.

### 7.6 Infrastructure image ages
- `stellar/quickstart` must support the contract protocol: soroban-sdk 26 builds protocol-26
  WASMs. Both the devenv `[[blockchains]]` entry and `testutils.StellarQuickstartImage` are
  pinned by digest to the 2026-09-04 build (stellar-core v28.0.1, protocol 28).
- `job-distributor:local` must postdate the `CHAIN_TYPE_STELLAR` proto addition (§2.2).

### 7.7 `bootstrap.KeystoreSetter` signature change (runtime-silent)
`SetKeystore(ks)` → `SetKeystore(ctx, ks) error`. The old signature still compiled, so the
accessor silently stopped satisfying the interface: keystore never injected, no transmitter,
job rejected (`contract transmitters must support at least one chain`), surfaced as a `/ready`
timeout. Fixed, and `var _ bootstrap.KeystoreSetter = (*accessor)(nil)` added so future drift
is a compile error. The Stellar accessor keeps its soft-fail design (errors stored on the
accessor's `*Err` fields, `nil` returned) so one chain's keystore failure can't abort the
job's other chains — a deliberate divergence from the EVM accessor's hard-fail.
