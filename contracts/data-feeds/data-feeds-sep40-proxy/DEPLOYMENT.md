# Deploy and configure the SEP-40 adapter

Deploy `DataFeedsSep40Proxy` against an existing Chainlink Data Feeds proxy on Stellar, map assets to feeds, and verify price reads. Commands use bash or zsh and target Testnet. Run every step in the same shell so variables persist.

## Summary

- **SEP-40** is Stellar's standard price-oracle interface.
- The **adapter** (this contract) exposes SEP-40 and reads prices from a **Chainlink Data Feeds proxy**, so SEP-40 consumers get Chainlink prices without code changes.
- Each Chainlink feed has a **`data_id`**. The adapter maps a SEP-40 asset to one.

```text
SEP-40 consumer → adapter (Asset → data_id) → Chainlink Proxy → Chainlink Cache
```

| Term | Meaning |
|---|---|
| `data_id` | 32-byte Chainlink feed identifier (64 hex characters). Not a contract address |
| Base (quote) currency | The currency prices are quoted in. A `USD` adapter returns BTC's price in USD |
| 18 decimals | Prices are integers scaled by `10^18`. Divide to get the decimal value |
| Readable | The proxy returns a round for the feed without error. This says nothing about freshness or whether it is the right pair |
| Stale | The timestamp is old. Each consumer decides its own maximum age |
| SAC | Stellar Asset Contract: the `C...` contract address of a classic asset, not its issuer's `G...` account |
| Ledger sequence | Stellar's block counter. Used for deadlines; it is not a time |
| Adapter ID / proxy ID | Two different `C...` contracts. Consumers use the adapter; the proxy is upstream |

## Before you start

- The adapter stores an `Asset → data_id` mapping. `add_feed` does not create or change anything upstream; the feed must already exist and be readable.
- Each adapter has **one base (quote) currency**, fixed at deploy. Only map feeds quoted in that base. The adapter cannot verify this. Use a second adapter for another currency.
- It always requests 18 decimals from the proxy.
- It does **not** filter stale or future-dated prices. Consumers must check timestamps.
- Admin is owner-only. Ownership renunciation is disabled.

Full behaviour: [README](README.md).

---

# Part 1: Deploy and Configure

## 1. Prerequisites

- [Rust via rustup](https://www.rust-lang.org/tools/install). The toolchain is pinned in [`../rust-toolchain.toml`](../rust-toolchain.toml).
- [Stellar CLI](https://developers.stellar.org/docs/tools/cli/install-cli). Commands were checked against `30.0.1`.

Run from the root of your checkout of this repository (the directory containing `contracts/`).

```sh
cd contracts/data-feeds
ls data-feeds-sep40-proxy        # should list Cargo.toml and src/
stellar --version
rustup show active-toolchain

# New Testnet identity only (skip if you already have a funded one)
stellar keys generate sep40-admin --network testnet --fund
```

The identity you sign with becomes the adapter owner in step 5.

## 2. Set variables

Get the proxy ID and feed ID for your network from the [Chainlink Stellar docs](https://docs.chain.link/data-feeds/stellar). A `data_id` is not a contract address. Record the pair and quote currency beside each ID.

```sh
SEP40_NETWORK="testnet"
SEP40_SIGNER="sep40-admin"
SEP40_OWNER=$(stellar keys address "$SEP40_SIGNER")
CHAINLINK_PROXY_ID="<upstream-proxy-contract-id>"
SEP40_BASE_SYMBOL="USD"            # letters, digits, _ ; max 32 chars
BTC_USD_DATA_ID="<64-hex-chars-no-0x>"
SEP40_ASSET='{"Other":"BTC"}'

# Optional format check (does not prove the feed exists)
echo "$BTC_USD_DATA_ID" | grep -Eq '^[0-9a-fA-F]{64}$' && echo ok || echo "bad data_id"
```

Two shell helpers (not CLI features) keep later commands short. `sep40` only works after you set `SEP40_ADAPTER_ID` in step 5.

```sh
upstream() { stellar contract invoke --id "$CHAINLINK_PROXY_ID" --network "$SEP40_NETWORK" --source-account "$SEP40_SIGNER" "$@"; }
sep40()    { stellar contract invoke --id "$SEP40_ADAPTER_ID"   --network "$SEP40_NETWORK" --source-account "$SEP40_SIGNER" "$@"; }
```

For a custom RPC, add a named network and use its name as `SEP40_NETWORK`:

```sh
stellar network add sep40-target --rpc-url "<rpc-url>" --network-passphrase "<passphrase>"
```

## 3. Build

```sh
cargo test -p data-feeds-sep40-proxy
stellar contract build --package data-feeds-sep40-proxy --locked
SEP40_WASM="target/wasm32v1-none/release/data_feeds_sep40_proxy.wasm"

git rev-parse HEAD
shasum -a 256 "$SEP40_WASM"
stellar contract info interface --wasm "$SEP40_WASM"
```

- The interface must include constructor args `owner`, `proxy`, `base_symbol`, plus `add_feed`, `get_feed`, `lastprice`.
- If `CARGO_TARGET_DIR` is set, the WASM is under that directory.

Check the CLI can resolve the constructor types (prints help, submits nothing):

```sh
stellar contract deploy \
  --wasm "$SEP40_WASM" \
  --source-account "$SEP40_SIGNER" \
  --network "$SEP40_NETWORK" \
  --build-only -- --help
```

Stop on `Missing Entry DataId`; see [Troubleshooting](#troubleshooting).

## 4. Check the upstream feed

Simulations only (`--send=no`).

```sh
upstream --send=no -- description --data_id "$BTC_USD_DATA_ID"
upstream --send=no -- latest_round --data_id "$BTC_USD_DATA_ID" --decimals 18
```

Confirm:
- The description is the intended pair and its quote currency matches `SEP40_BASE_SYMBOL`.
- The timestamp is acceptable. A successful call can still be stale.

`latest_round` returns `round_id`, `answer` (signed, scaled by `10^18`) and `timestamp` (Unix seconds). Repeat for every feed you will map.

## 5. Deploy

```sh
stellar contract deploy \
  --wasm "$SEP40_WASM" \
  --source-account "$SEP40_SIGNER" \
  --network "$SEP40_NETWORK" \
  -- \
  --owner "$SEP40_OWNER" \
  --proxy "$CHAINLINK_PROXY_ID" \
  --base_symbol "$SEP40_BASE_SYMBOL"
```

- The command prints the new adapter's contract ID: 56 characters starting with `C`.
- The constructor stores the proxy without probing it and starts with no assets, so success does not prove any feed works.
- Save the contract ID and the transaction hash.

```sh
SEP40_ADAPTER_ID="<returned-adapter-contract-id>"
```

Consumers use `SEP40_ADAPTER_ID`. `CHAINLINK_PROXY_ID` is a different contract.

## 6. Add a feed

```sh
sep40 --send=yes -- add_feed --asset "$SEP40_ASSET" --data_id "$BTC_USD_DATA_ID"
```

Emits `FeedAdded`. The adapter checks:
- Owner authorization.
- The asset is not the base, and the ID is not all zeros.
- The asset and ID are not already mapped.
- The feed is readable at 18 decimals (zero and negative prices count). No timestamp-age check.

Repeat steps 4 and 6 for more pairs.

**Stellar token asset** (when consumers identify the asset by token contract address):

```sh
SEP40_TOKEN_ASSET="{\"Stellar\":\"<token-contract-id>\"}"
sep40 --send=yes -- add_feed --asset "$SEP40_TOKEN_ASSET" --data_id "<token-usd-data-id>"
```

- For a classic asset, use its Stellar Asset Contract address, not the issuer's `G...` account.
- `Other("BTC")` and `Stellar(<id>)` are different assets. Consumers must request the exact variant.
- They cannot share a data ID. Remove one before adding the other.

## 7. Verify

```sh
for m in get_owner get_proxy base decimals resolution assets type_and_version; do
  printf '\n%s\n' "$m"
  sep40 --send=no -- "$m" || break
done
```

| Method | Expected |
|---|---|
| `get_owner` | `SEP40_OWNER` |
| `get_proxy` | `CHAINLINK_PROXY_ID` |
| `base` | `Other(SEP40_BASE_SYMBOL)` |
| `decimals` / `resolution` | `18` / `1` |
| `assets` | Mapped assets (empty before step 6) |
| `type_and_version` | `DataFeedsSep40Proxy 1.0.0` |

Check the deployed code matches your build (hashes should agree), then the mapping and price:

```sh
stellar contract info hash --wasm "$SEP40_WASM"
stellar contract info hash --id "$SEP40_ADAPTER_ID" --network "$SEP40_NETWORK"

sep40 --send=no -- get_feed --asset "$SEP40_ASSET"
sep40 --send=no -- lastprice --asset "$SEP40_ASSET"
```

`get_feed` returns your data ID. Compare `lastprice` with the upstream `latest_round` from step 4, allowing for new rounds in between.

Illustrative `lastprice` result (formatting varies by CLI version):

```json
{"price": "67000000000000000000000", "timestamp": 1735689600}
```

Here `price` is `67000` in the base currency and `timestamp` is Unix seconds. No value means `None`.

| Value | Meaning |
|---|---|
| `price` | Signed integer, 18 decimals. `67000000000000000000000` is `67000` |
| `timestamp` | Upstream Unix seconds, unchanged |
| `None` | Unmapped, upstream error, frozen feed, or answer outside `i128` |
| `price()` / `prices()` | Always `None` in v1 |

Convert with integer or arbitrary-precision arithmetic, not floats or JavaScript `Number`.

> **Freshness.** A readable price is not a fresh price. Consumers must reject future timestamps and enforce a maximum age on every read. There is no `max_age` setting.

---

# Part 2: Operations

Owner-only calls use `sep40 --send=yes`. Use `--send=no` to simulate first.

## Update or remove a feed

Verify the replacement upstream first. Its ID must not belong to another asset.

```sh
sep40 --send=yes -- update_feed --asset "$SEP40_ASSET" --data_id "<replacement-data-id>"
sep40 --send=yes -- remove_feed --asset "$SEP40_ASSET"
```

- `update_feed` needs an existing mapping and a readable replacement (even if the ID is unchanged). It emits `FeedUpdated`, frees the old ID, and keeps asset order.
- `remove_feed` emits `FeedRemoved` and frees the asset and ID. The upstream feed is unaffected.
- Afterward, check `get_feed`, `lastprice` and `assets`.

## Replace the upstream proxy

First read each mapped feed through the candidate. The `upstream` helper still points at the current proxy, so call the CLI directly:

```sh
CANDIDATE_PROXY_ID="<candidate-proxy-id>"

stellar contract invoke \
  --id "$CANDIDATE_PROXY_ID" \
  --network "$SEP40_NETWORK" \
  --source-account "$SEP40_SIGNER" \
  --send=no -- latest_round --data_id "<mapped-data-id>" --decimals 18
```

Repeat for every mapped `data_id`, then switch:

```sh
sep40 --send=yes -- set_proxy --proxy "$CANDIDATE_PROXY_ID"
```

- Every mapped feed is read through the candidate at 18 decimals first. On any failure it returns `FeedUnreadable`, keeps the current proxy, and emits no `ProxySet`.
- Cost grows with the number of mapped assets. With none mapped there are no checks, so an incompatible address can be stored.
- Readability does not prove the pair or quote currency match.
- After success: check `get_proxy` and `lastprice` for every asset, then set `CHAINLINK_PROXY_ID="$CANDIDATE_PROXY_ID"` so `upstream` follows the new proxy.
- Rollback is the same call with the old address, and it must pass the same checks.

## Transfer ownership

Two transactions: the owner proposes, the new owner accepts. `live_until_ledger` is a **future ledger sequence number**, not Unix time. Ledgers close roughly every 5 seconds, so pick a value comfortably above the current one.

```sh
# Current ledger: read "sequence" from the response
curl -s -X POST "<rpc-url>" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"getLatestLedger"}'

SEP40_NEXT_SIGNER="<new-owner-cli-identity>"
SEP40_NEXT_OWNER=$(stellar keys address "$SEP40_NEXT_SIGNER")

sep40 --send=yes -- transfer_ownership \
  --new_owner "$SEP40_NEXT_OWNER" \
  --live_until_ledger "<future-ledger-sequence>"

stellar contract invoke \
  --id "$SEP40_ADAPTER_ID" \
  --network "$SEP40_NETWORK" \
  --source-account "$SEP40_NEXT_SIGNER" \
  --send=yes -- accept_ownership
```

- The original owner stays in control until acceptance.
- To cancel, call `transfer_ownership` again with the same `new_owner` and `live_until_ledger` of `0`.
- Afterward, check `get_owner` and switch `SEP40_SIGNER` to the new identity.
- The commands above assume the new owner is a plain account whose key is in your CLI. A contract or multi-signer owner must authorize `accept_ownership` through its own process.

---

# Part 3: Reference

## Troubleshooting

Codes are the adapter's `Sep40ProxyError` values.

| Error | Next step |
|---|---|
| `AssetAlreadyMapped` (200) | Inspect with `get_feed`; use `update_feed` to change it |
| `AssetNotMapped` (201) | Check the exact asset variant and value. During `set_proxy`, suspect an inconsistency or archived state |
| `DataIdInUse` (202) | Another asset uses this ID; check `assets` and each `get_feed` |
| `InvalidDataId` (203) | The ID is all zeros |
| `FeedUnreadable` (204) | Query the feed upstream at 18 decimals; check ID, missing data, frozen state, answer range |
| `AssetIsBase` (205) | Map a different asset quoted in the base |
| `OwnershipRenunciationDisabled` (206) | Intentional; use two-step transfer |
| Auth failure | Check `get_owner` and the signer identity |
| Invalid hex / JSON / address | Check 64-char ID, enum capitalisation, quoting, full strkeys |
| `Missing Entry DataId` | CLI can't resolve a custom type in the WASM spec. Rebuild from the intended revision; if it persists, the contract metadata needs fixing |
| `lastprice` is `None` | Check `get_feed`, then query upstream. Upstream errors and `i128` overflow both give `None` |

## Links

- [Adapter README](README.md)
- [Chainlink Data Feeds on Stellar](https://docs.chain.link/data-feeds/stellar)
- [Read upstream feeds with the CLI](https://docs.chain.link/data-feeds/stellar/using-data-feeds-off-chain)
- [Stellar CLI manual](https://developers.stellar.org/docs/tools/cli/stellar-cli)
- [CLI argument types](https://developers.stellar.org/docs/tools/cli/cookbook/contract-invoke-arguments)
