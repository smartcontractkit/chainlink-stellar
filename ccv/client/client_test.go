package ccvclient

import (
	"net/http"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Run("stores rpc client reference", func(t *testing.T) {
		rpc := rpcclient.NewClient("http://127.0.0.1:9", &http.Client{Timeout: time.Millisecond})
		c := NewClient(rpc)
		require.NotNil(t, c)
		assert.Same(t, rpc, c.RPC)
	})

	t.Run("allows nil client", func(t *testing.T) {
		c := NewClient(nil)
		require.NotNil(t, c)
		assert.Nil(t, c.RPC)
	})
}
