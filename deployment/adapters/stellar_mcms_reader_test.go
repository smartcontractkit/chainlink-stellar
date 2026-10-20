package adapters

import (
	"testing"

	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"

	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
)

// The reader must honor the shared-pipeline MCMSReader contract: GetTimelockRef
// never errors — it returns an empty ref so callers such as token expansion can
// fall back to non-timelock admins — while GetMCMSRef stays fail-closed (role
// refs are only resolved when actually building proposals).
func TestStellarMCMSReader_GetTimelockRef_neverErrors(t *testing.T) {
	t.Parallel()
	chainSel := uint64(42)
	qual := "q1"
	tlAddr := "CTLOCKAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAY"

	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(mcmsutil.StellarTimelockDatastoreRef(chainSel, qual, tlAddr)))
	env := cldf.Environment{DataStore: ds.Seal()}
	reader := StellarMCMSReader{}

	t.Run("resolves the timelock under its exact qualifier", func(t *testing.T) {
		t.Parallel()
		ref, err := reader.GetTimelockRef(env, chainSel, mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule})
		require.NoError(t, err)
		require.Equal(t, tlAddr, ref.Address)
	})

	t.Run("no timelock deployed returns an empty ref, not an error", func(t *testing.T) {
		t.Parallel()
		// Regression: the devenv's shared token pipeline resolves the timelock
		// unconditionally via the family reader; an EOA stellar lane deploys
		// no RBACTimelock, so the miss must surface as an empty ref (the
		// EVM/Solana reader contract), not as a deploy failure.
		emptyEnv := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
		ref, err := reader.GetTimelockRef(emptyEnv, chainSel, mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule})
		require.NoError(t, err)
		require.Empty(t, ref.Address)
	})

	t.Run("GetMCMSRef still fails closed on miss", func(t *testing.T) {
		t.Parallel()
		_, err := reader.GetMCMSRef(env, chainSel, mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule})
		require.Error(t, err)
	})
}
