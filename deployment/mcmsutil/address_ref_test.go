package mcmsutil

import (
	"testing"

	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
)

func TestMCMSRefTypeForAction(t *testing.T) {
	cases := []struct {
		action mcmstypes.TimelockAction
		want   cldf.ContractType
	}{
		{mcmstypes.TimelockActionSchedule, cldf.ContractType(utils.ProposerManyChainMultisig)},
		{mcmstypes.TimelockActionCancel, cldf.ContractType(utils.CancellerManyChainMultisig)},
		{mcmstypes.TimelockActionBypass, cldf.ContractType(utils.BypasserManyChainMultisig)},
	}
	for _, c := range cases {
		got, err := MCMSRefTypeForAction(c.action)
		require.NoError(t, err)
		require.Equal(t, c.want, got)
	}
	_, err := MCMSRefTypeForAction(mcmstypes.TimelockAction("nope"))
	require.Error(t, err)
}

func TestFindStellarMCMSAddressRef_failClosed(t *testing.T) {
	chainSel := uint64(42)
	qual := "q1"
	proposerAddr := "CPROPOSERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	ds := datastore.NewMemoryDataStore()
	ref, err := StellarMCMSRoleDatastoreRef(chainSel, qual, RoleProposer, proposerAddr)
	require.NoError(t, err)
	require.NoError(t, ds.Addresses().Upsert(ref))
	env := cldf.Environment{DataStore: ds.Seal()}

	scheduleInput := mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule}
	got, err := FindStellarMCMSAddressRef(env, chainSel, scheduleInput)
	require.NoError(t, err)
	require.Equal(t, proposerAddr, got.Address)

	// Only the proposer ref exists: a bypass action must fail closed, not reuse the proposer.
	bypassInput := mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionBypass}
	_, err = FindStellarMCMSAddressRef(env, chainSel, bypassInput)
	require.Error(t, err)

	// Empty datastore fails closed too.
	emptyEnv := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	_, err = FindStellarMCMSAddressRef(emptyEnv, chainSel, scheduleInput)
	require.Error(t, err)
}

func TestFindStellarTimelockAddressRef_noMCMSFallback(t *testing.T) {
	chainSel := uint64(42)
	qual := "q1"
	proposerAddr := "CPROPOSERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tlAddr := "CTLOCKAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAY"
	input := mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule}

	t.Run("resolves RBACTimelock", func(t *testing.T) {
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, ds.Addresses().Upsert(StellarTimelockDatastoreRef(chainSel, qual, tlAddr)))
		env := cldf.Environment{DataStore: ds.Seal()}
		ref, err := FindStellarTimelockAddressRef(env, chainSel, input)
		require.NoError(t, err)
		require.Equal(t, tlAddr, ref.Address)
	})

	t.Run("no timelock ref fails closed even when an MCMS ref exists", func(t *testing.T) {
		ds := datastore.NewMemoryDataStore()
		ref, err := StellarMCMSRoleDatastoreRef(chainSel, qual, RoleProposer, proposerAddr)
		require.NoError(t, err)
		require.NoError(t, ds.Addresses().Upsert(ref))
		env := cldf.Environment{DataStore: ds.Seal()}
		_, err = FindStellarTimelockAddressRef(env, chainSel, input)
		require.Error(t, err, "must not fall back to the MCMS contract")
	})
}

func TestLookupStellarTimelockAddressRef_emptyOnMiss(t *testing.T) {
	chainSel := uint64(42)
	qual := "q1"
	tlAddr := "CTLOCKAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAY"

	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(StellarTimelockDatastoreRef(chainSel, qual, tlAddr)))
	env := cldf.Environment{DataStore: ds.Seal()}

	t.Run("resolves the timelock under its exact qualifier", func(t *testing.T) {
		got := LookupStellarTimelockAddressRef(env, chainSel, mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule})
		require.Equal(t, tlAddr, got.Address)
	})

	t.Run("empty qualifier is qualifier-agnostic", func(t *testing.T) {
		// The shared GetAddressRef semantics: an empty qualifier matches any
		// qualifier, so a governance-stack-qualified timelock still resolves —
		// a qualifier mismatch can never be the reason for an empty result.
		got := LookupStellarTimelockAddressRef(env, chainSel, mcmsutils.Input{Qualifier: "", TimelockAction: mcmstypes.TimelockActionSchedule})
		require.Equal(t, tlAddr, got.Address)
	})

	t.Run("no timelock deployed returns an empty ref, not an error", func(t *testing.T) {
		// EOA lanes deploy no RBACTimelock; the miss must surface as an empty
		// ref (the EVM/Solana reader contract), never as a deploy failure —
		// and an MCMS role ref alone must not be fallen back to either.
		noTL := datastore.NewMemoryDataStore()
		ref, err := StellarMCMSRoleDatastoreRef(chainSel, qual, RoleProposer, "CPROPOSERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		require.NoError(t, err)
		require.NoError(t, noTL.Addresses().Upsert(ref))
		emptyEnv := cldf.Environment{DataStore: noTL.Seal()}
		got := LookupStellarTimelockAddressRef(emptyEnv, chainSel, mcmsutils.Input{Qualifier: qual, TimelockAction: mcmstypes.TimelockActionSchedule})
		require.Empty(t, got.Address)
	})
}
