# data-feeds-sep40-proxy

A [SEP-40](https://github.com/stellar/stellar-protocol/blob/master/ecosystem/sep-0040.md) price-feed adapter over the Chainlink Data Feeds proxy on Stellar. Existing SEP-40 consumers can read Chainlink prices without code changes.

## Scope (v1)

| SEP-40 method | Behaviour |
|---|---|
| `base()` | `Asset::Other(<base_symbol>)`, for example `Other("USD")` or `Other("GBP")` |
| `assets()` | Mapped assets, in insertion order |
| `decimals()` | `18` (constant; the proxy is always asked for 18) |
| `resolution()` | `1` |
| `lastprice(asset)` | Latest round from the proxy, or `None` |
| `price(asset, ts)` / `prices(asset, n)` | `None` (history is not supported in v1) |

`lastprice` returns `None` rather than failing when the asset is unmapped, the proxy returns an error, the feed is frozen, or the answer is not in `1..=i128::MAX`. The timestamp is the proxy's, unchanged. Each call also extends the TTL of the instance and of the persistent entries it read, so no public `extend_ttl` is exposed.

## Deployment

The constructor is `__constructor(owner: Address, proxy: Address, base_symbol: Symbol)`. `base_symbol` is the quote currency, passed as a plain symbol such as `USD`, `GBP` or `EUR` (letters, digits and `_`, at most 32 characters). It is stored as `Asset::Other(base_symbol)` and cannot be changed after deployment. One adapter has one base, so a deployment that needs several quote currencies needs several adapters.

The adapter cannot check what currency a feed is quoted in. Only map feeds quoted in the base: a `GBP` adapter should only be given `X/GBP` feeds.

## Admin (owner only)

`add_feed`, `update_feed`, `remove_feed`, `set_proxy`, plus two-step ownership and `upgrade`. A data id can be mapped to at most one asset. `add_feed` and `update_feed` require the feed to be readable at call time.

## Tests

```sh
cd contracts/data-feeds
cargo test -p data-feeds-sep40-proxy
```

`src/tests/conformance.rs` pins the XDR encoding of `Asset` and `PriceData`. The SEP-40 types are declared locally because the Script3 `sep-40-oracle` crate pins a different `soroban-sdk` major version.

## Maintenance

If this adapter is taken over by a customer, the customer is responsible for its maintenance, including upgrades and the feed mapping.
