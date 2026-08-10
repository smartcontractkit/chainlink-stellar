package datafeeds

import (
	"embed"
	"fmt"
)

// artifactsFS holds the compiled Data Feeds contract WASM committed under
// artifacts/. The blobs are the build of this commit's contracts/data-feeds
// sources with the workspace's pinned toolchain (rust-toolchain.toml).
//
//go:embed artifacts/*.wasm
var artifactsFS embed.FS

// Artifact returns the compiled WASM for one of the artifact filename
// constants above, e.g. Artifact(DataFeedsCacheWasm).
func Artifact(name string) ([]byte, error) {
	switch name {
	case DataFeedsCacheWasm, DataFeedsProxyWasm:
	default:
		return nil, fmt.Errorf("unknown Data Feeds artifact %q", name)
	}
	wasm, err := artifactsFS.ReadFile("artifacts/" + name)
	if err != nil {
		return nil, fmt.Errorf("embedded Data Feeds artifact %q: %w", name, err)
	}
	return wasm, nil
}
