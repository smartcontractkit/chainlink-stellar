package mcmsutil

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

func TestResolveTimelockWasmPath_STELLAR_TIMELOCK_WASM(t *testing.T) {
	t.Setenv("STELLAR_TIMELOCK_WASM", "/abs/custom/timelock.wasm")
	t.Setenv("CHAINLINK_STELLAR_ROOT", "/should/be/ignored")

	got, err := ResolveTimelockWasmPath()
	require.NoError(t, err)
	require.Equal(t, "/abs/custom/timelock.wasm", got)
}

func TestResolveTimelockWasmPath_CHAINLINK_STELLAR_ROOT(t *testing.T) {
	t.Setenv("STELLAR_TIMELOCK_WASM", "")
	t.Setenv("CHAINLINK_STELLAR_ROOT", "/repo/root")

	got, err := ResolveTimelockWasmPath()
	require.NoError(t, err)
	want := filepath.Join("/repo/root", DefaultTimelockWasmRelative)
	require.Equal(t, want, got)
}

func TestResolveTimelockWasmPath_fallbackWalksUpToRepoRoot(t *testing.T) {
	t.Setenv("STELLAR_TIMELOCK_WASM", "")
	t.Setenv("CHAINLINK_STELLAR_ROOT", "")

	got, err := ResolveTimelockWasmPath()
	require.NoError(t, err)
	root, err := stellarutil.FindStellarRoot()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, DefaultTimelockWasmRelative), got)
}

func TestResolveMCMSWasmPath_fallbackWalksUpToRepoRoot(t *testing.T) {
	t.Setenv("STELLAR_MCMS_WASM", "")
	t.Setenv("CHAINLINK_STELLAR_ROOT", "")

	got, err := ResolveMCMSWasmPath()
	require.NoError(t, err)
	root, err := stellarutil.FindStellarRoot()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, DefaultMCMSWasmRelative), got)
}
