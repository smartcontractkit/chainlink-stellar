# Stellar RMN curse governance via MCMS

**Scope:** how the Stellar RMN Remote's curse surface is governed through the three qualifier-keyed MCMS/timelock stacks, the authorization model behind `curse`/`uncurse`/`apply_curse_admin_updates`, and how deployment tooling (`deployment/ownership`, `deployment/operations/rmn_remote`, `deployment/sequences`) drives them.

**Audience:** engineers building or reviewing curse/uncurse changesets and proposals for Stellar; operators running fast-curse drills.

---

## 1. Topology — three MCMS stacks per chain

Stellar mirrors EVM's RMN 2.1 governance design. Each stack is one qualifier-keyed `DeployStellarMCMS` deployment (Proposer/Canceller/Bypasser multisigs + one self-administered RBACTimelock):

| Stack | Qualifier (`chainlink-ccip/deployment/utils`) | RMN Remote authority | Use |
|---|---|---|---|
| Regular admin ops | `CLLCCIP` (`CLLQualifier`) | none | application governance (router/pools/lanes) — **no RMN role; nothing to wire here** |
| Regular curse + ALL uncurse | `RMNMCMS` (`RMNTimelockQualifier`) | **owner** | deliberate curse and every uncurse. The ≤2-minute in-wiring path is the same proposal with `TimelockAction: bypass` via the **bypasser** instance |
| Ultra fast curse only | `UltraFastCurse` (`UltraFastCurseMCMSQualifier`) | **curse-admin only, never owner** | bypass, EM 1-of-N — "curse-only" is enforced by the contract, not tooling |

On-chain elegance: `rmn_remote` already ships the curse-admin role (`contracts/rmn_remote/src/lib.rs`), so this topology requires **no contract change and no audit** — unlike Aptos/Sui/Ton/Solana, which needed contract work first.

### Authorization matrix (`contracts/rmn_remote/src/lib.rs`)

| Entry point | Who | Caller argument |
|---|---|---|
| `curse(caller, subjects)` (:201, via `require_can_curse` :79) | owner ∪ curse-admin | **required** — must equal the invoking address (`caller.require_auth()`) |
| `uncurse(subjects)` (:236) | owner only | none |
| `apply_curse_admin_updates(added, removed)` (:147) | owner only | none |
| `get_curse_admins()` (:187) | read (simulation) | none |

`get_curse_admins` returns the **raw stored list**: the owner is never inserted into it and nothing filters the owner out — treat the owner as curse-authorized independently of the list, and never assume owner ∩ admins = ∅. Explicitly adding the owner as an admin succeeds and the owner is then listed (pinned by `TestRmnRemoteCurseAdmins`).

## 2. Owner-implicit grants (EVM precedent)

Activation grants curse-admin to `[UltraFastCurse timelock + extras]` **only**. The RMNMCMS timelock's curse authority comes from *ownership*; it is never listed in `get_curse_admins`. This mirrors EVM's `DeployAndActivateRMN` ("The Ultra Fast Curse MCMS timelock is always the curse admin on a newly deployed RMN"; the RMNMCMS timelock receives ownership instead).

Uncurse always goes through the RMNMCMS stack — `uncurse` is owner-only on chain and the RMNMCMS timelock *is* the owner. Fast-uncurse is rejected at proposal-build time, matching the Sui and EVM (`Uncurse0` = `OnlyOwner`) precedents.

## 3. Direct-path preservation

Sequences mark no-MCMS executions with a non-nil `ExecInfo` hash (`stellar-direct-curse`, `stellar-direct-uncurse`, `stellar-direct-curse-admin-update`; the established convention is `stellar-direct-transfer`/`stellar-direct-accept`). `NewBatchOperationFromWrites` (CLDF) skips writes whose `Executed()` is true (`ExecInfo != nil`), so a direct execution produces **zero batch ops** and the shared changeset's `OutputBuilder` emits no proposal. The hash string never reaches chain — it is only a non-nil filter marker. This is why no-MCMS tests (`CurseChain`/`UncurseChain` in `tests/testutils/setup_testutils.go`) keep passing when sequences gain proposal support.

## 4. The caller-coupling invariant (design note)

EVM's `RMN.curse(bytes16[])` takes **no caller argument** — authorization is `msg.sender`, so one and the same payload executes correctly under whichever of the three timelocks runs it, and the MCMS qualifier alone picks the route.

Stellar's `curse(caller, subjects)` **bakes the executing timelock's address into the payload** at proposal-build time, because on chain the caller argument must equal the invoking address. The shared changeset input (`chainlink-ccip/deployment/fastcurse.CurseInput`) therefore carries `MCMSQualifier` (populated from `cfg.MCMS.Qualifier`), so the Stellar sequence can resolve the executing timelock deterministically and a mismatch **fails at proposal-build time** with an actionable message instead of on chain at execute time.

Consequences:

- One changeset invocation still yields **one proposal under one qualifier** — a run cannot curse through two stacks at once.
- With an empty qualifier (direct/no-MCMS or pre-qualifier callers), the sequence falls back to a documented order (RMNMCMS timelock if authorized, then UltraFastCurse) and logs a loud warning naming the assumption.
- Nothing authorized fails closed with an error naming the RMN contract, the deployer, the known timelocks, the on-chain owner and the current admin list.

## 5. Hex/strkey boundary

CCIP contract refs (RMN Remote) are stored in the datastore **hex-encoded**; MCMS/timelock refs are **strkeys**. Every `ownership.*` ref and every MCMS transaction `To` must be the **strkey** form: `strkey.Decode(strkey.VersionByteContract, …)` (`deployment/deployer.go`) rejects hex, and `scval.AddressToScVal` silently yields a nil `ScVal` for hex.

Callers must supply strkey refs — a hex ref fails early with a contract-ID decode error (reached via `ownership.ContractOwner` → `Deployer.SimulateContract`), rather than corrupting the MCMS transaction target. No Stellar entry is registered in CLDF's `deploy.GetAddressNormalizerRegistry()`; the only Stellar normalizer (`deployment/adapters/init.go`) registers into the separate `ccvshared` registry. `StellarTransferOwnershipViaMCMS` passes `ref.Address` verbatim into the transaction target, which is safe only under this strkey-ref requirement — documented here, tracked separately, do not fix silently.

Also: build Soroban vectors only via `scval.VecToScVal`/`*SliceToScVal` — a hand-rolled `xdr.ScVal{Type: ScvVec}` with a nil `Vec` traps the host unrecoverably (`deployment/mcmsutil/invoke_payload.go`). `*SliceToScVal(nil)` is safe.

## 6. Proposal targeting vs build-time validation

The shared `OutputBuilder` already resolves the proposal's `timelockAddresses[selector]` through the MCMS reader using `mcms.Input.Qualifier` — the per-qualifier timelock map injected by the Stellar curse adapter exists for **build-time authorization validation and actionable error messages** (naming which qualifier resolves to which timelock and why it is or is not authorized), not to set the proposal target. Do not "simplify" it away, and do not duplicate the reader's job in it.

## 7. Tooling map

| Piece | Location |
|---|---|
| Curse/uncurse ops | `deployment/operations/rmn_remote/rmn_remote.go` (`rmn-remote:curse`, `rmn-remote:uncurse`) |
| Curse-admin ops | `rmn-remote:apply-curse-admin-updates`, `rmn-remote:get-curse-admins` (same file) |
| Ownership reads | `deployment/ownership/contract_owner.go` (`ContractOwner`, `CurseAdmins`) |
| Ownership execution | `deployment/ownership/exec.go` (`ExecuteTransferOwnership`, `ExecuteAcceptOwnership`, `ExecuteApplyCurseAdminUpdates`) |
| MCMS deploy (per qualifier) | `deployment/sequences/mcms.go` (`DeployStellarMCMS`) |
| Curse sequences | `deployment/sequences/curse.go` (`StellarCurse`, `StellarUncurse`) |
| Shared changeset adapter | `deployment/adapters/stellar_curse_adapter.go` (registered `adapters/init.go`) |
| Encode/decode Soroban payloads | `deployment/mcmsutil/invoke_payload.go` |
| Integration test (auth matrix + raw-list pin) | `tests/integration/rmn_remote_test.go` `TestRmnRemoteCurseAdmins` |
