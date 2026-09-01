package datafeeds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

var allArtifacts = []string{DataFeedsCacheWasm, DataFeedsProxyWasm}

func wasmNameFromCargo(t *testing.T, cargoPath string) string {
	t.Helper()
	data, err := os.ReadFile(cargoPath)
	require.NoError(t, err, "read %s", cargoPath)

	var m struct {
		Package struct {
			Name string `toml:"name"`
		} `toml:"package"`
	}
	require.NoError(t, toml.Unmarshal(data, &m), "parse %s", cargoPath)
	require.NotEmpty(t, m.Package.Name, "missing [package] name in %s", cargoPath)

	return strings.ReplaceAll(m.Package.Name, "-", "_") + ".wasm"
}

func TestArtifactNamesMatchCargoPackages(t *testing.T) {
	cases := map[string]string{
		DataFeedsCacheWasm: filepath.Join("..", "..", "contracts", "data-feeds", "data-feeds-cache", "Cargo.toml"),
		DataFeedsProxyWasm: filepath.Join("..", "..", "contracts", "data-feeds", "data-feeds-proxy", "Cargo.toml"),
	}
	for want, cargoPath := range cases {
		require.Equalf(t, want, wasmNameFromCargo(t, cargoPath),
			"artifact constant out of sync with %s (rename the const or the crate)", cargoPath)
	}
}

func TestArtifactEmbedsEveryConstant(t *testing.T) {
	for _, name := range allArtifacts {
		wasm, err := Artifact(name)
		if err != nil {
			t.Fatalf("Artifact(%q): %v", name, err)
		}
		if len(wasm) < 8 {
			t.Fatalf("Artifact(%q): implausibly small (%d bytes)", name, len(wasm))
		}
		if string(wasm[:4]) != "\x00asm" {
			t.Fatalf("Artifact(%q): not a WASM module (magic %x)", name, wasm[:4])
		}
	}
}

func TestArtifactUnknownName(t *testing.T) {
	if _, err := Artifact("nope.wasm"); err == nil {
		t.Fatal("expected error for unknown artifact name")
	}
}

func TestEmbeddedSetIsExactlyTheConstants(t *testing.T) {
	entries, err := artifactsFS.ReadDir("artifacts")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(allArtifacts) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("embedded %v, constants %v", names, allArtifacts)
	}
}
