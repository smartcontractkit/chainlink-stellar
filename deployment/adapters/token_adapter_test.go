package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarutil "github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// The wrapped-token read backs DeriveTokenAddress when the qualifier-keyed
// BnmToken row is absent: the pool contract itself is authoritative (EVM parity).
func TestPoolWrappedTokenHex(t *testing.T) {
	t.Parallel()
	poolStrkey := stellarutil.MustGenerateMockContractID("derive-token-address", "pool")
	tokenStrkey := stellarutil.MustGenerateMockContractID("derive-token-address", "token")
	val := scval.AddressToScVal(tokenStrkey)
	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForContract(poolStrkey, "get_token", &val)

	got, err := poolWrappedTokenHex(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, poolStrkey)
	require.NoError(t, err)
	want, err := stellarutil.StrkeyToHex(tokenStrkey)
	require.NoError(t, err)
	require.Equal(t, want, got)
}
