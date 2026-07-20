package config

import (
	_ "embed"

	"github.com/smartcontractkit/chainlink-common/pkg/config/configdoc"
)

//go:embed docs.toml
var docsTOML string

//go:embed example.toml
var exampleConfig string

// GenerateDocs renders the operator-facing documentation for the Stellar TOML
// config from docs.toml. The output is meant to be checked into the operator
// docs site so that documented defaults stay in sync with code.
func GenerateDocs() (string, error) {
	return configdoc.Generate(docsTOML, `[//]: # (Documentation generated from docs.toml - DO NOT EDIT.)
This document describes the TOML format for configuration.`, exampleConfig, nil)
}
