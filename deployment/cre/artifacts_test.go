package cre

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/require"
)

// wasmNameFromCargo derives the built wasm filename from a crate's Cargo.toml
// package name (cargo replaces '-' with '_' in the produced artifact name).
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

// TestArtifactNamesMatchCargoPackages guards the exported wasm-name constants against
// drift from the Rust crate names. Paths are relative to this package dir (deployment/cre),
// so the repo root is two levels up.
func TestArtifactNamesMatchCargoPackages(t *testing.T) {
	cases := map[string]string{
		ReadFixtureWasm: filepath.Join("..", "..", "contracts", "cre", "test", "read_fixture", "Cargo.toml"),
		ForwarderWasm:   filepath.Join("..", "..", "contracts", "cre", "forwarder", "Cargo.toml"),
		ReceiverWasm:    filepath.Join("..", "..", "contracts", "cre", "test", "receiver", "Cargo.toml"),
	}
	for want, cargoPath := range cases {
		require.Equalf(t, want, wasmNameFromCargo(t, cargoPath),
			"artifact constant out of sync with %s (rename the const or the crate)", cargoPath)
	}
}
