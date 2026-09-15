package mcmsutil

import (
	"bytes"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
)

func TestMCMSRoleDeploySalt_deterministicAndDistinct(t *testing.T) {
	a1 := MCMSRoleDeploySalt(7, "q", RoleProposer)
	a2 := MCMSRoleDeploySalt(7, "q", RoleProposer)
	require.True(t, bytes.Equal(a1[:], a2[:]), "same inputs must be deterministic")

	// Distinct across roles, and distinct from the timelock salt.
	salts := map[string][32]byte{
		"proposer":  MCMSRoleDeploySalt(7, "q", RoleProposer),
		"canceller": MCMSRoleDeploySalt(7, "q", RoleCanceller),
		"bypasser":  MCMSRoleDeploySalt(7, "q", RoleBypasser),
		"timelock":  TimelockDeploySalt(7, "q"),
	}
	seen := map[[32]byte]string{}
	for name, s := range salts {
		if other, dup := seen[s]; dup {
			t.Fatalf("salt collision between %s and %s", name, other)
		}
		seen[s] = name
	}
}

func TestMCMSRole_DatastoreTypeAndLabel(t *testing.T) {
	cases := []struct {
		role  MCMSRole
		typ   datastore.ContractType
		label string
	}{
		{RoleProposer, datastore.ContractType(utils.ProposerManyChainMultisig), "PROPOSER"},
		{RoleCanceller, datastore.ContractType(utils.CancellerManyChainMultisig), "CANCELLER"},
		{RoleBypasser, datastore.ContractType(utils.BypasserManyChainMultisig), "BYPASSER"},
	}
	for _, c := range cases {
		ct, err := c.role.DatastoreType()
		require.NoError(t, err)
		require.Equal(t, c.typ, ct)
		require.Equal(t, c.label, c.role.InstanceLabel())
	}
	_, err := MCMSRole("executor").DatastoreType()
	require.Error(t, err)
}

func TestStellarMCMSRoleDatastoreRef(t *testing.T) {
	ref, err := StellarMCMSRoleDatastoreRef(3, "q", RoleCanceller, "CADDR")
	require.NoError(t, err)
	require.Equal(t, uint64(3), ref.ChainSelector)
	require.Equal(t, "q", ref.Qualifier)
	require.Equal(t, "CADDR", ref.Address)
	require.Equal(t, datastore.ContractType(utils.CancellerManyChainMultisig), ref.Type)
	require.True(t, ref.Version.Equal(deploy.MCMSVersion))
}

func TestFindExistingStellarMCMSByRole_exactMatchFailClosed(t *testing.T) {
	chainSel := uint64(999)
	qual := "qual-a"
	proposerAddr := "CPROPOSERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	cancellerAddr := "CCANCELLERAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	mk := func(role MCMSRole, addr string) datastore.AddressRef {
		ref, err := StellarMCMSRoleDatastoreRef(chainSel, qual, role, addr)
		require.NoError(t, err)
		return ref
	}
	refs := []datastore.AddressRef{
		mk(RoleProposer, proposerAddr),
		mk(RoleCanceller, cancellerAddr),
	}

	t.Run("resolves each role to its own address", func(t *testing.T) {
		got, ok, err := FindExistingStellarMCMSByRole(refs, chainSel, qual, RoleProposer)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, proposerAddr, got)

		got, ok, err = FindExistingStellarMCMSByRole(refs, chainSel, qual, RoleCanceller)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, cancellerAddr, got)
	})

	t.Run("missing role fails closed (no fallback to another role)", func(t *testing.T) {
		_, ok, err := FindExistingStellarMCMSByRole(refs, chainSel, qual, RoleBypasser)
		require.NoError(t, err)
		require.False(t, ok, "bypasser must not resolve to proposer/canceller")
	})

	t.Run("wrong chain / qualifier / version do not match", func(t *testing.T) {
		_, ok, _ := FindExistingStellarMCMSByRole(refs, chainSel+1, qual, RoleProposer)
		require.False(t, ok)
		_, ok, _ = FindExistingStellarMCMSByRole(refs, chainSel, "other", RoleProposer)
		require.False(t, ok)

		bad := mk(RoleProposer, proposerAddr)
		bad.Version = semver.MustParse("0.9.0")
		_, ok, _ = FindExistingStellarMCMSByRole([]datastore.AddressRef{bad}, chainSel, qual, RoleProposer)
		require.False(t, ok)
	})
}

func TestFindExistingStellarTimelock(t *testing.T) {
	chainSel := uint64(999)
	qual := "qual-a"
	tlAddr := "CTIMELOCKAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	refs := []datastore.AddressRef{StellarTimelockDatastoreRef(chainSel, qual, tlAddr)}
	got, ok := FindExistingStellarTimelock(refs, chainSel, qual)
	require.True(t, ok)
	require.Equal(t, tlAddr, got)

	_, ok = FindExistingStellarTimelock(refs, chainSel+1, qual)
	require.False(t, ok)
	_, ok = FindExistingStellarTimelock(refs, chainSel, "other")
	require.False(t, ok)

	wrongType := StellarTimelockDatastoreRef(chainSel, qual, tlAddr)
	wrongType.Type = datastore.ContractType(utils.ProposerManyChainMultisig)
	_, ok = FindExistingStellarTimelock([]datastore.AddressRef{wrongType}, chainSel, qual)
	require.False(t, ok)
}

func TestStellarTimelockDatastoreRef(t *testing.T) {
	ref := StellarTimelockDatastoreRef(3, "q", "CADDR")
	require.Equal(t, uint64(3), ref.ChainSelector)
	require.Equal(t, "q", ref.Qualifier)
	require.Equal(t, "CADDR", ref.Address)
	require.Equal(t, datastore.ContractType(utils.RBACTimelock), ref.Type)
	require.True(t, ref.Version.Equal(deploy.MCMSVersion))
}
