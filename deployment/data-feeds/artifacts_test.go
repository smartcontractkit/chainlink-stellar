package datafeeds

import (
	"testing"
)

var allArtifacts = []string{DataFeedsCacheWasm, DataFeedsProxyWasm}

// TestArtifactEmbedsEveryConstant asserts every filename constant resolves to
// an embedded blob that deserializes as WASM.
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

// TestArtifactUnknownName asserts unknown names are rejected rather than
// passed through to the filesystem.
func TestArtifactUnknownName(t *testing.T) {
	if _, err := Artifact("nope.wasm"); err == nil {
		t.Fatal("expected error for unknown artifact name")
	}
}

// TestEmbeddedSetIsExactlyTheConstants asserts no stray blobs are embedded.
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
