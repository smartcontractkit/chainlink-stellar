# Stellar RMN curse governance via MCMS

How the RMN Remote curse surface is governed via the three qualifier-keyed MCMS/timelock stacks, and how `deployment/` tooling drives it. Mirrors EVM's RMN 2.1 design — `rmn_remote` already ships the curse-admin role, so this requires **no contract change and no audit**.

## Topology (one `DeployStellarMCMS` stack each: Proposer/Canceller/Bypasser multisigs + self-administered RBACTimelock)

| Stack | Qualifier (`chainlink-ccip/deployment/utils`) | RMN authority | Use |
|---|---|---|---|
| Admin ops | `CLLCCIP` (`CLLQualifier`) | none | application governance — no RMN role |
| Regular curse + ALL uncurse | `RMNMCMS` (`RMNTimelockQualifier`) | **owner** | deliberate curse and every uncurse; the ≤2-min path is the same proposal with `TimelockAction: bypass` via the **bypasser** instance |
| Ultra fast curse only | `UltraFastCurse` (`UltraFastCurseMCMSQualifier`) | **curse-admin only, never owner** | bypass, EM 1-of-N — "curse-only" is enforced by the contract, not tooling |

Auth matrix (`contracts/rmn_remote/src/lib.rs`): `curse(caller, subjects)` — owner ∪ curse-admin, caller argument **required** and must equal the invoker; `uncurse(subjects)` and `apply_curse_admin_updates(added, removed)` — owner only, no caller argument; `get_curse_admins()` — simulation read. `get_curse_admins` returns the **raw stored list**: the owner is never inserted into it and never filtered out — treat the owner as curse-authorized independent of the list, and never assume owner ∩ admins = ∅ (pinned by `TestRmnRemoteCurseAdmins`).

## Grants and routing

- Activation grants curse-admin to `[UltraFastCurse timelock + extras]` **only** — the RMNMCMS timelock's curse authority comes from ownership (EVM `DeployAndActivateRMN` precedent: the UFC timelock is always curse admin on a newly deployed RMN; RMNMCMS receives ownership instead).
- Uncurse always goes through RMNMCMS (`uncurse` is owner-only on chain and the RMNMCMS timelock is the owner); fast-uncurse is rejected at proposal-build time (Sui/EVM precedent).

## Direct (no-MCMS) runs

When the caller has no MCMS config, the sequence executes immediately and must not also produce a governance proposal. It marks the resulting write as already executed: a non-nil `ExecInfo` value (`stellar-direct-curse`, `stellar-direct-uncurse`, `stellar-direct-curse-admin-update` — the established `stellar-direct-transfer` convention; any non-nil value works, and the string never reaches chain). The changeset machinery only turns **unexecuted** writes into batch operations — CLDF's `NewBatchOperationFromWrites` skips writes whose `Executed()` is true — so a direct run yields no batch ops, and with no batch ops the output builder emits no proposal. This is why the no-MCMS e2e tests (`CurseChain`/`UncurseChain` in `tests/testutils/setup_testutils.go`) stay green as the sequences gain proposal support.

## Caller-coupling invariant

EVM's `RMN.curse(bytes16[])` takes no caller argument — `msg.sender` authorizes, so one payload runs correctly under whichever timelock executes it and the qualifier alone picks the route. Stellar's `curse(caller, subjects)` **bakes the executing timelock into the payload** at proposal-build time, so the shared input `fastcurse.CurseInput` carries `MCMSQualifier` (from `cfg.MCMS.Qualifier`); the Stellar sequence resolves the executing timelock deterministically and a mismatch **fails at proposal-build time** with an actionable message instead of on chain at execute time.

Consequences: **an explicit qualifier wins over the deployer-direct arm** — a run that carries a qualifier produces a governed proposal even when the deployer is still the owner or a curse admin, and an unauthorized pick (e.g. CLLCCIP) fails at proposal-build time instead of silently signing with the deployer key; one changeset invocation yields **one proposal under one qualifier**; an empty qualifier (direct/no-MCMS or pre-qualifier callers) with an unauthorized deployer falls back to RMNMCMS-then-UltraFastCurse with a loud warning naming the assumption; nothing authorized fails closed with an error naming the RMN contract, deployer, known timelocks, owner and admin list. The curse adapter's `Initialize` reads the RMN owner **mandatorily** — an RPC failure surfaces from `Initialize` (and thus from the changeset) instead of degrading to an empty owner that would later produce a misleading proposal-build error — while the admin-list and timelock reads stay best-effort.

## Hex/strkey boundary

Datastore CCIP refs (RMN Remote) are stored **hex-encoded**; MCMS/timelock refs are **strkeys**. Every `ownership.*` ref and every MCMS transaction `To` must be the strkey form — `strkey.Decode(VersionByteContract, …)` (`deployment/deployer.go`) rejects hex, and `scval.AddressToScVal` silently yields nil for it. No Stellar entry exists in CLDF's `deploy.GetAddressNormalizerRegistry()` (the only Stellar normalizer, `deployment/adapters/init.go`, registers into the separate `ccvshared` registry), and `StellarTransferOwnershipViaMCMS` passes `ref.Address` verbatim into the transaction target — safe only under this strkey requirement; the latent hex-ref hazard is documented here and tracked separately, do not fix silently. Build Soroban vectors only via `scval.VecToScVal`/`*SliceToScVal` — a hand-rolled `xdr.ScVal{Type: ScvVec}` with nil `Vec` traps the host unrecoverably (`*SliceToScVal(nil)` is safe).

## Proposal targeting vs build-time validation

The shared `OutputBuilder` resolves the proposal's `timelockAddresses[selector]` through the MCMS reader using `mcms.Input.Qualifier` — the per-qualifier timelock map injected by the Stellar curse adapter exists for **build-time authorization validation and actionable error messages** (which qualifier resolves to which timelock, and why it is or is not authorized), not to set the proposal target. Do not "simplify" it away, and do not duplicate the reader's job in it.
