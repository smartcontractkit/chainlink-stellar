package cre

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestArtifactEmbedsEveryConstant guards against an artifact constant existing
// without its wasm committed under artifacts/ (go:embed only fails the build
// when the glob matches nothing at all).
func TestArtifactEmbedsEveryConstant(t *testing.T) {
	wasmMagic := []byte{0x00, 'a', 's', 'm'}

	for _, name := range []string{ReadFixtureWasm, ForwarderWasm, ReceiverWasm, RejectingReceiverWasm} {
		wasm, err := Artifact(name)
		require.NoErrorf(t, err, "artifact %s is not embedded — run 'make update-cre-artifacts'", name)
		require.Greaterf(t, len(wasm), len(wasmMagic), "embedded artifact %s is truncated", name)
		require.Equalf(t, wasmMagic, wasm[:4], "embedded artifact %s is not a wasm module", name)
	}
}

func TestArtifactUnknownName(t *testing.T) {
	_, err := Artifact("nonexistent.wasm")
	require.Error(t, err)
}

// TestEmbeddedSetIsExactlyTheConstants guards against a stray .wasm being
// committed under artifacts/ and silently embedded (the go:embed glob accepts
// anything matching *.wasm).
func TestEmbeddedSetIsExactlyTheConstants(t *testing.T) {
	entries, err := artifactsFS.ReadDir("artifacts")
	require.NoError(t, err)

	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	require.ElementsMatch(t,
		[]string{ForwarderWasm, ReceiverWasm, RejectingReceiverWasm, ReadFixtureWasm},
		got, "embedded artifacts/ must contain exactly the documented artifact set")
}
