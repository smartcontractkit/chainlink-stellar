# Test fixtures

`cache_self_upgrade.wasm` is the `data-feeds-cache` contract compiled from the
**current source tree**. The upgrade tests deploy the contract, then call
`upgrade()` with this wasm, so the fixture must be rebuilt whenever the
contract's storage layout (`DataKey`, stored value types) or interface changes
— otherwise the "upgrade" is really a downgrade to stale code and reads after
the upgrade silently miss.

Regenerate from `contracts/data-feeds/`:

```sh
stellar contract build --package data-feeds-cache
cp target/wasm32v1-none/release/data_feeds_cache.wasm \
   data-feeds-cache/test_fixtures/cache_self_upgrade.wasm
```

Requires stellar-cli **v25.x** (e.g. 25.2.0): soroban-sdk 26.1.0 (pulled in
via `stellar-access`) enables `experimental_spec_shaking_v2`, whose build gate
is satisfied by stellar-cli v25.2.0+. Newer major CLI versions (27+) no longer
export the gate variable and fail the same build, so pin v25.x rather than
latest.
