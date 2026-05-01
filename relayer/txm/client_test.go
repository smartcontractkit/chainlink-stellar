package txm

import (
	"testing"

	"github.com/smartcontractkit/chainlink-common/pkg/config"
	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	"github.com/stretchr/testify/assert"
)

// TestTxm_ClientTypeAliases verifies that the type aliases in the txm package
// correctly map to the shared ccv/client package types. This ensures TXM
// consumers can use the re-exported types without a separate import.
func TestTxm_ClientTypeAliases(t *testing.T) {
	t.Parallel()

	// RPCClient alias matches the shared interface
	var _ RPCClient = (ccvclient.RPCClient)(nil)

	// ClientConfig alias matches the shared struct
	cfg := ClientConfig{
		LedgerCacheTTL:  config.MustNewDuration(0),
		RateLimitPerSec: ptr(0.0),
	}
	var sharedCfg ccvclient.ClientConfig = cfg
	assert.Equal(t, cfg, sharedCfg)
}
