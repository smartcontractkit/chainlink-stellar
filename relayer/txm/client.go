package txm

import (
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
)

// RPCClient is the test-mocking interface for Stellar RPC calls. Re-exported
// from ccv/client so TXM consumers don't need a separate import. Production
// code uses *rpcclient.Client which satisfies this interface directly.
type RPCClient = ccvclient.RPCClient

// Client is the shared optimization layer over *rpcclient.Client (cache +
// rate limiter). Re-exported from ccv/client.
type Client = ccvclient.Client

// ClientConfig is the configuration for the shared Client.
type ClientConfig = ccvclient.ClientConfig

// ClientFactory manages a pool of Stellar RPC nodes with random selection
// and per-URL caching. Re-exported from ccv/client.
type ClientFactory = ccvclient.ClientFactory

// FactoryNodeConfig is the per-node settings consumed by ClientFactory.
// Re-exported from ccv/client (distinct from txm.NodeConfig which has TOML tags).
type FactoryNodeConfig = ccvclient.NodeConfig
