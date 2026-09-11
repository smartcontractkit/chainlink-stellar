package main

import (
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cli"

	ccvchain "github.com/smartcontractkit/chainlink-stellar/tests/ccv/chain"
)

func init() {
	ccvchain.RegisterStellarComponents()
}

// main is the entry point for the Stellar devenv CLI.
//
// Run with:
//
//	CTF_CONFIGS=tests/env/env-stellar-evm.toml go run ./tests/testutils/cmd/devenv
//
// This spins up the full devenv (blockchains, Chainlink nodes, committee verifier, aggregator,
// executor, indexer) configured for the Stellar-to-EVM lane.
func main() {
	cli.RunCLI()
}
