package adapters

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
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

func TestStellarTokenAdapter_DeployTokenVerify(t *testing.T) {
	t.Parallel()
	a := &StellarTokenAdapter{}

	t.Run("fresh symbol passes", func(t *testing.T) {
		t.Parallel()
		// The framework runs verify before the deploy that creates the BnmToken
		// row, so a missing row must not fail a fresh deploy.
		require.NoError(t, a.DeployTokenVerify(envWithDatastore(newSealedDatastore()), tokens.DeployTokenInput{
			ChainSelector: 42, Symbol: "stgBnM",
		}))
	})

	t.Run("already-deployed symbol passes", func(t *testing.T) {
		t.Parallel()
		cid := stellarutil.MustGenerateMockContractID("deployer", "deploy-token-verify-bnm")
		ds := datastore.NewMemoryDataStore()
		tokenRef := stellarccip.BnmTokenDatastoreRef()
		tokenRef.Qualifier = "stgBnM"
		require.NoError(t, tokenRef.UpsertDeployedStrKey(ds, 42, cid))
		require.NoError(t, a.DeployTokenVerify(envWithDatastore(ds.Seal()), tokens.DeployTokenInput{
			ChainSelector: 42, Symbol: "stgBnM",
		}))
	})

	t.Run("empty symbol errors", func(t *testing.T) {
		t.Parallel()
		err := a.DeployTokenVerify(envWithDatastore(newSealedDatastore()), tokens.DeployTokenInput{ChainSelector: 42})
		require.ErrorContains(t, err, "symbol is required")
	})
}
