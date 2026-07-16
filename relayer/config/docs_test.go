package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-common/pkg/config/configtest"
)

// TestDefaults_fieldsNotNil asserts that Defaults() (loaded from docs.toml at init
// and merged via SetFrom) leaves no pointer field nil. This catches docs.toml drift:
// if a field is added to a config struct but not to docs.toml, Defaults() will leave
// it nil and this test fails.
func TestDefaults_fieldsNotNil(t *testing.T) {
	configtest.AssertFieldsNotNil(t, Defaults())
}

// TestDocsTOMLComplete asserts that every field in TOMLConfig has a corresponding
// entry in docs.toml. This catches struct drift: if a field is added to TOMLConfig
// but not documented in docs.toml, this test fails.
func TestDocsTOMLComplete(t *testing.T) {
	configtest.AssertDocsTOMLComplete[TOMLConfig](t, docsTOML)
}

// TestGenerateDocs ensures GenerateDocs() produces non-empty, error-free output.
// It does not assert exact content (that would duplicate docs.toml); it just guards
// against the embed or configdoc pipeline breaking silently.
func TestGenerateDocs(t *testing.T) {
	out, err := GenerateDocs()
	require.NoError(t, err)
	require.NotEmpty(t, out)
}
