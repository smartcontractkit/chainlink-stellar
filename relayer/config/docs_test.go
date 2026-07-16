package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/config/configtest"
)

// TestDefaults_fieldsNotNil catches docs.toml drift: a field in the struct but
// missing from docs.toml leaves a nil pointer after Defaults().
func TestDefaults_fieldsNotNil(t *testing.T) {
	configtest.AssertFieldsNotNil(t, Defaults())
}

// TestDocsTOMLComplete catches struct drift: a field in TOMLConfig but missing
// from docs.toml fails this test.
func TestDocsTOMLComplete(t *testing.T) {
	configtest.AssertDocsTOMLComplete[TOMLConfig](t, docsTOML)
}

func TestGenerateDocs(t *testing.T) {
	out, err := GenerateDocs()
	require.NoError(t, err)
	require.NotEmpty(t, out)
}

// TestDefaults_CopyIsolation ensures SetFrom deep-copies pointer fields so
// mutating a Defaults() copy cannot corrupt the package-level defaults.
func TestDefaults_CopyIsolation(t *testing.T) {
	d1 := Defaults()
	original := *d1.TxManager.BroadcastChanSize
	*d1.TxManager.BroadcastChanSize = 999
	d2 := Defaults()
	require.Equal(t, original, *d2.TxManager.BroadcastChanSize,
		"mutating a Defaults() copy must not corrupt the package-level defaults")
}
