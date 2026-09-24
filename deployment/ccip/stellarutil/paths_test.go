package stellarutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// realStellarRoot discovers the chainlink-stellar repo root via the CWD-walk fallback
// (CHAINLINK_STELLAR_ROOT unset). The stellarutil tests live under that checkout, so the
// walk reaches the root. Used to seed the override-valid assertion without hard-coding a
// host-specific absolute path.
func realStellarRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("CHAINLINK_STELLAR_ROOT", "")
	root, err := FindStellarRoot()
	require.NoError(t, err, "CWD-walk fallback should find the chainlink-stellar root from the test checkout")
	return root
}

// t.Setenv cannot be used in t.Parallel tests, so these are intentionally non-parallel.

func TestFindStellarRoot_OverrideValid(t *testing.T) {
	root := realStellarRoot(t)
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)

	got, err := FindStellarRoot()
	require.NoError(t, err)
	require.Equal(t, root, got)
}

func TestFindStellarRoot_OverrideNonexistentPath(t *testing.T) {
	t.Setenv("CHAINLINK_STELLAR_ROOT", "/this/path/does/not/exist/xyz")

	_, err := FindStellarRoot()
	require.Error(t, err)
}

func TestFindStellarRoot_OverrideWrongModule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/not-stellar\n\ngo 1.21\n"), 0o644))
	t.Setenv("CHAINLINK_STELLAR_ROOT", dir)

	_, err := FindStellarRoot()
	require.Error(t, err)
}

func TestFindStellarRoot_FallbackWalk(t *testing.T) {
	t.Setenv("CHAINLINK_STELLAR_ROOT", "")

	got, err := FindStellarRoot()
	require.NoError(t, err)
	mp, ok := goModModulePath(got)
	require.True(t, ok, "resolved root %q has no go.mod", got)
	require.Equal(t, stellarRootModule, mp)
}
