// Package datafeeds exposes the Data Feeds contract WASM artifacts produced by
// `stellar contract build` in the contracts/data-feeds nested cargo workspace
// (into target/wasm32v1-none/release/).
//
// The Data Feeds contracts live in their own package, separate from
// deployment/cre, because they are a product on top of the CRE platform: the
// cache is the production receiver behind the CRE forwarder, and the proxy is
// the stable consumer-facing address over cache upgrades.
//
// Each filename constant must match the corresponding Cargo package name with
// '-' replaced by '_'; that invariant is enforced by artifacts_test.go. The
// compiled WASM for each constant is committed under artifacts/ and exposed
// via Artifact, so Go consumers can deploy the pinned bytecode without a Rust
// toolchain.
package datafeeds

const (
	// DataFeedsCacheWasm is the Data Feeds cache (contracts/data-feeds/data-feeds-cache).
	DataFeedsCacheWasm = "data_feeds_cache.wasm"

	// DataFeedsProxyWasm is the Data Feeds proxy (contracts/data-feeds/data-feeds-proxy).
	DataFeedsProxyWasm = "data_feeds_proxy.wasm"
)
