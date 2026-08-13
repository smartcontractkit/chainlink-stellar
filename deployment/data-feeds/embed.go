package datafeeds

import (
	"embed"
	"fmt"
)

//go:embed artifacts/*.wasm
var artifactsFS embed.FS

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
