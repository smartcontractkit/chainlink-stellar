package sequences

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
)

// fakeGovernanceReader returns injected values so verifyGovernance can be tested without a chain.
type fakeGovernanceReader struct {
	labels     map[string]string
	owners     map[string]string
	netIDs     map[string][32]byte
	roles      map[string]bool // key: addr|role|account
	minDelay   uint64
	ownerByRef map[string]string
}

func roleKey(tlAddr, role, account string) string { return tlAddr + "|" + role + "|" + account }

func (f *fakeGovernanceReader) mcmsInstanceLabel(_ context.Context, addr string) (string, error) {
	return f.labels[addr], nil
}
func (f *fakeGovernanceReader) mcmsOwner(_ context.Context, addr string) (string, error) {
	return f.owners[addr], nil
}
func (f *fakeGovernanceReader) mcmsChainNetworkID(_ context.Context, addr string) ([32]byte, error) {
	return f.netIDs[addr], nil
}
func (f *fakeGovernanceReader) timelockHasRole(_ context.Context, addr, role, account string) (bool, error) {
	return f.roles[roleKey(addr, role, account)], nil
}
func (f *fakeGovernanceReader) timelockMinDelay(_ context.Context, _ string) (uint64, error) {
	return f.minDelay, nil
}
func (f *fakeGovernanceReader) contractOwner(_ context.Context, ref datastore.AddressRef) (string, error) {
	owner, ok := f.ownerByRef[ref.Address]
	if !ok {
		return "", fmt.Errorf("no owner for %s", ref.Address)
	}
	return owner, nil
}

func goodTopology() governanceTopology {
	return governanceTopology{
		Proposer:  "CPROPOSER",
		Canceller: "CCANCELLER",
		Bypasser:  "CBYPASSER",
		Timelock:  "CTIMELOCK",
		Deployer:  "GDEPLOYER",
	}
}

func goodReader(t governanceTopology) *fakeGovernanceReader {
	var net [32]byte
	net[0] = 9
	return &fakeGovernanceReader{
		labels: map[string]string{t.Proposer: "PROPOSER", t.Canceller: "CANCELLER", t.Bypasser: "BYPASSER"},
		owners: map[string]string{t.Proposer: t.Timelock, t.Canceller: t.Timelock, t.Bypasser: t.Timelock},
		netIDs: map[string][32]byte{t.Proposer: net, t.Canceller: net, t.Bypasser: net},
		roles: map[string]bool{
			roleKey(t.Timelock, timelockRoleAdmin, t.Timelock):      true,
			roleKey(t.Timelock, timelockRoleProposer, t.Proposer):   true,
			roleKey(t.Timelock, timelockRoleCanceller, t.Proposer):  true,
			roleKey(t.Timelock, timelockRoleCanceller, t.Canceller): true,
			roleKey(t.Timelock, timelockRoleBypasser, t.Bypasser):   true,
		},
	}
}

func TestVerifyGovernance_pass(t *testing.T) {
	topo := goodTopology()
	require.NoError(t, verifyGovernance(context.Background(), goodReader(topo), topo, governanceExpectations{}))
}

func TestVerifyGovernance_failures(t *testing.T) {
	t.Run("shared address", func(t *testing.T) {
		topo := goodTopology()
		topo.Bypasser = topo.Proposer // collision
		r := goodReader(topo)
		require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}), "distinct")
	})

	t.Run("wrong instance label", func(t *testing.T) {
		topo := goodTopology()
		r := goodReader(topo)
		r.labels[topo.Canceller] = "PROPOSER"
		require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}), "instance label")
	})

	t.Run("mcms not owned by timelock", func(t *testing.T) {
		topo := goodTopology()
		r := goodReader(topo)
		r.owners[topo.Bypasser] = topo.Deployer
		require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}), "owner")
	})

	t.Run("mismatched network id", func(t *testing.T) {
		topo := goodTopology()
		r := goodReader(topo)
		var other [32]byte
		other[0] = 7
		r.netIDs[topo.Bypasser] = other
		require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}), "chain_network_id")
	})

	t.Run("missing role in matrix", func(t *testing.T) {
		topo := goodTopology()
		r := goodReader(topo)
		delete(r.roles, roleKey(topo.Timelock, timelockRoleCanceller, topo.Proposer))
		require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}), "CANCELLER")
	})

	t.Run("deployer retains a role", func(t *testing.T) {
		topo := goodTopology()
		r := goodReader(topo)
		r.roles[roleKey(topo.Timelock, timelockRoleAdmin, topo.Deployer)] = true
		require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}), "ADMIN")
	})

	t.Run("admin is not the timelock itself", func(t *testing.T) {
		topo := goodTopology()
		r := goodReader(topo)
		r.roles[roleKey(topo.Timelock, timelockRoleAdmin, topo.Timelock)] = false
		require.Error(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{}))
	})
}

func TestVerifyGovernance_expectations(t *testing.T) {
	topo := goodTopology()
	r := goodReader(topo)
	r.minDelay = 100
	r.ownerByRef = map[string]string{"CGOVERNED": topo.Timelock}

	want := uint64(100)
	exp := governanceExpectations{
		MinDelay:          &want,
		GovernedContracts: []datastore.AddressRef{{Address: "CGOVERNED"}},
	}
	require.NoError(t, verifyGovernance(context.Background(), r, topo, exp))

	// Wrong min delay fails.
	bad := uint64(50)
	require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, governanceExpectations{MinDelay: &bad}), "min delay")

	// Governed contract not owned by timelock fails.
	r.ownerByRef["CGOVERNED"] = topo.Deployer
	require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, exp), "governed contract")
}

// CREForwarder refs flow through the same governed-contracts ownership check.
func TestVerifyGovernance_governedCREForwarder(t *testing.T) {
	topo := goodTopology()
	r := goodReader(topo)
	r.ownerByRef = map[string]string{"CFORWARDER": topo.Timelock}

	exp := governanceExpectations{
		GovernedContracts: []datastore.AddressRef{StellarCREForwarderDatastoreRef(5, "q", "CFORWARDER")},
	}
	require.NoError(t, verifyGovernance(context.Background(), r, topo, exp))

	// A pre-handoff (deployer-owned) forwarder fails the governed check.
	r.ownerByRef["CFORWARDER"] = topo.Deployer
	require.ErrorContains(t, verifyGovernance(context.Background(), r, topo, exp), "governed contract")
}

// Ensure the role-specific ref round-trips through the resolver used by the verifier.
func TestResolveGovernanceTopology(t *testing.T) {
	sel := uint64(5)
	qual := "q"
	refs := []datastore.AddressRef{}
	for _, rc := range []struct {
		role mcmsutil.MCMSRole
		addr string
	}{{mcmsutil.RoleProposer, "CP"}, {mcmsutil.RoleCanceller, "CC"}, {mcmsutil.RoleBypasser, "CB"}} {
		ref, err := mcmsutil.StellarMCMSRoleDatastoreRef(sel, qual, rc.role, rc.addr)
		require.NoError(t, err)
		refs = append(refs, ref)
	}
	refs = append(refs, mcmsutil.StellarTimelockDatastoreRef(sel, qual, "CT"))

	topo, err := resolveGovernanceTopology(refs, sel, qual, "GD")
	require.NoError(t, err)
	require.Equal(t, "CP", topo.Proposer)
	require.Equal(t, "CC", topo.Canceller)
	require.Equal(t, "CB", topo.Bypasser)
	require.Equal(t, "CT", topo.Timelock)

	// Missing bypasser ref → fail closed.
	_, err = resolveGovernanceTopology(refs[:2], sel, qual, "GD")
	require.Error(t, err)
}
