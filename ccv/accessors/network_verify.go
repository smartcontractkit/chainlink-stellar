package accessors

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/network"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/protocol"

	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
	"github.com/smartcontractkit/chainlink-stellar/deployment/rpclimit"
)

// networkGetter abstracts the single RPC call the endpoint identity check
// needs, so tests can exercise it without an HTTP server. *rpcclient.Client
// satisfies it.
type networkGetter interface {
	GetNetwork(ctx context.Context) (protocolrpc.GetNetworkResponse, error)
}

// networkVerifyFunc is the factory/accessor seam for the endpoint identity
// check. NewFactory installs the real verifier; hand-crafted test factories
// leave it nil, which skips the check.
type networkVerifyFunc func(ctx context.Context, cfg sourcereader.ReaderConfig, chainSelector protocol.ChainSelector, zlggr zerolog.Logger) error

const (
	// networkVerifyAttempts bounds retries when the endpoint is unreachable;
	// a definitive mismatch fails on the first GetNetwork response.
	networkVerifyAttempts = 3
	// networkVerifyTimeout caps each GetNetwork call.
	networkVerifyTimeout = 10 * time.Second
)

// networkVerifyBackoff spaces unreachable-endpoint retries; a var so tests
// can shorten it.
var networkVerifyBackoff = 1 * time.Second

// defaultNetworkVerify builds a throwaway rpclimit-wrapped client (the same
// wrapper the reader/transmitter clients in factory.go use) and runs the
// identity check against it.
func defaultNetworkVerify(ctx context.Context, cfg sourcereader.ReaderConfig, chainSelector protocol.ChainSelector, zlggr zerolog.Logger) error {
	client := rpcclient.NewClient(cfg.SorobanRPCURL, rpclimit.NewClient(networkVerifyTimeout))
	return verifyNetworkIdentity(ctx, client, cfg.NetworkPassphrase, chainSelector, zlggr)
}

// verifyNetworkIdentity checks that the Soroban RPC endpoint serves the
// network the accessor is configured for. Two invariants, in order:
//
//  1. The endpoint's passphrase equals the configured NetworkPassphrase.
//     Without this, a wrong URL paired with the right passphrase is silent
//     on the reader path (read-only RPC calls never touch the passphrase)
//     and only surfaces as per-transaction signature rejections on the
//     transmitter path.
//
//  2. The endpoint's network id (SHA-256 of its passphrase, the same form
//     chain-selectors uses as ChainID) equals the canonical network of the
//     chain selector. Without this, URL and passphrase wrong together —
//     e.g. a whole TOML from another environment — satisfy invariant 1
//     while serving the wrong chain.
//
// An unreachable endpoint is retried networkVerifyAttempts times and then
// skipped with a warning: SetKeystore runs once per accessor at bootstrap,
// and a temporary outage must not permanently disable the lane. A definitive
// mismatch is immutable configuration and returns an error.
func verifyNetworkIdentity(
	ctx context.Context,
	client networkGetter,
	cfgPassphrase string,
	chainSelector protocol.ChainSelector,
	zlggr zerolog.Logger,
) error {
	var (
		netInfo protocolrpc.GetNetworkResponse
		lastErr error
	)
	for attempt := 1; attempt <= networkVerifyAttempts; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, networkVerifyTimeout)
		netInfo, lastErr = client.GetNetwork(callCtx)
		cancel()
		if lastErr == nil {
			break
		}
		zlggr.Warn().
			Err(lastErr).
			Int("attempt", attempt).
			Msg("stellar accessor: getNetwork failed during endpoint identity check")
		// The accessor is shutting down; treat like an unreachable endpoint
		// (skip) so shutdown stays fail-open.
		if ctx.Err() != nil || attempt == networkVerifyAttempts {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(networkVerifyBackoff):
		}
	}
	if lastErr != nil {
		zlggr.Warn().
			Err(lastErr).
			Msg("stellar accessor: soroban rpc unreachable, skipping network identity check")
		return nil
	}

	if netInfo.Passphrase != cfgPassphrase {
		return fmt.Errorf(
			"endpoint network passphrase %q does not match configured passphrase %q",
			netInfo.Passphrase, cfgPassphrase,
		)
	}

	chain, ok := chainsel.StellarChainBySelector(uint64(chainSelector))
	if !ok {
		// Custom network whose selector chain-selectors does not know; the
		// configured-passphrase check above is all that can be verified.
		zlggr.Debug().
			Uint64("chain_selector", uint64(chainSelector)).
			Msg("stellar accessor: selector unknown to chain-selectors, skipping selector-level identity check")
		return nil
	}
	id := network.ID(netInfo.Passphrase)
	if got, want := hex.EncodeToString(id[:]), chain.ChainID; got != want {
		return fmt.Errorf(
			"endpoint network id %s (passphrase %q) does not match chain selector %d (%s, network id %s)",
			got, netInfo.Passphrase, uint64(chainSelector), chain.Name, want,
		)
	}

	zlggr.Info().
		Str("network_passphrase", netInfo.Passphrase).
		Str("network_id", hex.EncodeToString(id[:])).
		Msg("stellar accessor: soroban rpc endpoint network identity verified")
	return nil
}
