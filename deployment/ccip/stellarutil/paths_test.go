package stellarutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeRootGoMod writes a go.mod declaring the given module path in dir.
func writeRootGoMod(t *testing.T, dir, modulePath string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+"\n"), 0o644))
}

func TestFindStellarRoot_EnvOverrideReturnsValidRoot(t *testing.T) {
	// Not parallel: t.Setenv.
	root := t.TempDir()
	writeRootGoMod(t, root, "github.com/smartcontractkit/chainlink-stellar")
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)

	got, err := FindStellarRoot()
	require.NoError(t, err)
	require.Equal(t, root, got)
}

func TestFindStellarRoot_EnvOverrideRejectsOtherModule(t *testing.T) {
	// Not parallel: t.Setenv.
	root := t.TempDir()
	writeRootGoMod(t, root, "github.com/smartcontractkit/some-other-module")
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)

	_, err := FindStellarRoot()
	require.ErrorContains(t, err, "CHAINLINK_STELLAR_ROOT")
	require.ErrorContains(t, err, root)
}

func TestFindStellarRoot_EnvOverrideRejectsMissingGoMod(t *testing.T) {
	// Not parallel: t.Setenv.
	root := t.TempDir() // no go.mod
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)

	_, err := FindStellarRoot()
	require.ErrorContains(t, err, "CHAINLINK_STELLAR_ROOT")
}

func TestFindStellarRoot_EnvUnsetWalksUpToRepoRoot(t *testing.T) {
	// Not parallel: mutates the process environment.
	if prev, ok := os.LookupEnv("CHAINLINK_STELLAR_ROOT"); ok {
		require.NoError(t, os.Unsetenv("CHAINLINK_STELLAR_ROOT"))
		t.Cleanup(func() {
			require.NoError(t, os.Setenv("CHAINLINK_STELLAR_ROOT", prev))
		})
	}

	wd, err := os.Getwd()
	require.NoError(t, err)
	// This package lives at <root>/deployment/ccip/stellarutil.
	expected := filepath.Dir(filepath.Dir(filepath.Dir(wd)))

	got, err := FindStellarRoot()
	require.NoError(t, err)
	require.Equal(t, expected, got)
}
