package sequences

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

// Timelock role symbols (match contracts/timelock/src/types.rs).
const (
	timelockRoleAdmin     = "ADMIN"
	timelockRoleProposer  = "PROPOSER"
	timelockRoleCanceller = "CANCELLER"
	timelockRoleBypasser  = "BYPASSER"
)

// governanceReader abstracts the on-chain reads a verifier performs, so the verification
// logic is unit-testable with a fake reader.
type governanceReader interface {
	mcmsInstanceLabel(ctx context.Context, addr string) (string, error)
	mcmsOwner(ctx context.Context, addr string) (string, error)
	mcmsChainNetworkID(ctx context.Context, addr string) ([32]byte, error)
	timelockHasRole(ctx context.Context, addr, role, account string) (bool, error)
	timelockMinDelay(ctx context.Context, addr string) (uint64, error)
	contractOwner(ctx context.Context, ref datastore.AddressRef) (string, error)
}

// governanceTopology is the resolved set of addresses a verifier asserts over.
type governanceTopology struct {
	Proposer  string
	Canceller string
	Bypasser  string
	Timelock  string
	Deployer  string
}

// governanceExpectations are optional external values to assert against; zero values skip the check.
type governanceExpectations struct {
	MinDelay          *uint64
	GovernedContracts []datastore.AddressRef
}

// VerifyStellarMCMSGovernance resolves the three role instances + timelock from refs and asserts
// the post-deployment authority invariants (distinct addresses, correct labels, timelock ownership,
// exact role matrix, no residual deployer authority). It fails closed on any missing ref.
func VerifyStellarMCMSGovernance(ctx context.Context, deps stellardeps.StellarDeps, refs []datastore.AddressRef, chainSelector uint64, qual, deployerAddr string) error {
	topo, err := resolveGovernanceTopology(refs, chainSelector, qual, deployerAddr)
	if err != nil {
		return err
	}
	return verifyGovernance(ctx, &bindingGovernanceReader{deps: deps}, topo, governanceExpectations{})
}

// VerifyStellarMCMSGovernanceWithExpectations additionally asserts the timelock min delay and that
// the timelock owns each supplied governed contract.
func VerifyStellarMCMSGovernanceWithExpectations(ctx context.Context, deps stellardeps.StellarDeps, refs []datastore.AddressRef, chainSelector uint64, qual, deployerAddr string, exp governanceExpectations) error {
	topo, err := resolveGovernanceTopology(refs, chainSelector, qual, deployerAddr)
	if err != nil {
		return err
	}
	return verifyGovernance(ctx, &bindingGovernanceReader{deps: deps}, topo, exp)
}

func resolveGovernanceTopology(refs []datastore.AddressRef, chainSelector uint64, qual, deployerAddr string) (governanceTopology, error) {
	find := func(role mcmsutil.MCMSRole) (string, error) {
		addr, ok, err := mcmsutil.FindExistingStellarMCMSByRole(refs, chainSelector, qual, role)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("missing %s MCMS ref for chain %d qualifier %q", role, chainSelector, qual)
		}
		return addr, nil
	}
	proposer, err := find(mcmsutil.RoleProposer)
	if err != nil {
		return governanceTopology{}, err
	}
	canceller, err := find(mcmsutil.RoleCanceller)
	if err != nil {
		return governanceTopology{}, err
	}
	bypasser, err := find(mcmsutil.RoleBypasser)
	if err != nil {
		return governanceTopology{}, err
	}
	tlID, ok := mcmsutil.FindExistingStellarTimelock(refs, chainSelector, qual)
	if !ok {
		return governanceTopology{}, fmt.Errorf("missing RBACTimelock ref for chain %d qualifier %q", chainSelector, qual)
	}
	return governanceTopology{
		Proposer:  proposer,
		Canceller: canceller,
		Bypasser:  bypasser,
		Timelock:  tlID,
		Deployer:  deployerAddr,
	}, nil
}

func verifyGovernance(ctx context.Context, r governanceReader, t governanceTopology, exp governanceExpectations) error {
	// 1. Four distinct addresses.
	seen := map[string]string{}
	for label, addr := range map[string]string{
		"proposer": t.Proposer, "canceller": t.Canceller, "bypasser": t.Bypasser, "timelock": t.Timelock,
	} {
		if addr == "" {
			return fmt.Errorf("%s address is empty", label)
		}
		if other, dup := seen[addr]; dup {
			return fmt.Errorf("address %s is shared by %s and %s; the four governance contracts must be distinct", addr, other, label)
		}
		seen[addr] = label
	}

	// 2. Each MCMS instance carries its role label and is owned by the timelock.
	roleByAddr := []struct {
		addr  string
		label string
	}{
		{t.Proposer, mcmsutil.RoleProposer.InstanceLabel()},
		{t.Canceller, mcmsutil.RoleCanceller.InstanceLabel()},
		{t.Bypasser, mcmsutil.RoleBypasser.InstanceLabel()},
	}
	var netID *[32]byte
	for _, rc := range roleByAddr {
		label, err := r.mcmsInstanceLabel(ctx, rc.addr)
		if err != nil {
			return fmt.Errorf("read instance label %s: %w", rc.addr, err)
		}
		if label != rc.label {
			return fmt.Errorf("MCMS %s has instance label %q, want %q", rc.addr, label, rc.label)
		}
		owner, err := r.mcmsOwner(ctx, rc.addr)
		if err != nil {
			return fmt.Errorf("read owner %s: %w", rc.addr, err)
		}
		if owner != t.Timelock {
			return fmt.Errorf("MCMS %s owner is %q, want timelock %q", rc.addr, owner, t.Timelock)
		}
		id, err := r.mcmsChainNetworkID(ctx, rc.addr)
		if err != nil {
			return fmt.Errorf("read chain_network_id %s: %w", rc.addr, err)
		}
		if netID == nil {
			netID = &id
		} else if *netID != id {
			return fmt.Errorf("MCMS %s chain_network_id differs from other instances", rc.addr)
		}
	}

	// 3. Timelock self-administers: ADMIN is the timelock itself.
	if err := requireRole(ctx, r, t.Timelock, timelockRoleAdmin, t.Timelock, true); err != nil {
		return err
	}

	// 4. Exact role matrix: proposer→PROPOSER+CANCELLER, canceller→CANCELLER, bypasser→BYPASSER.
	matrix := []struct {
		role, account string
		want          bool
	}{
		{timelockRoleProposer, t.Proposer, true},
		{timelockRoleCanceller, t.Proposer, true},
		{timelockRoleCanceller, t.Canceller, true},
		{timelockRoleBypasser, t.Bypasser, true},
	}
	for _, m := range matrix {
		if err := requireRole(ctx, r, t.Timelock, m.role, m.account, m.want); err != nil {
			return err
		}
	}

	// 5. No residual deployer authority.
	for _, role := range []string{timelockRoleAdmin, timelockRoleProposer, timelockRoleCanceller, timelockRoleBypasser} {
		if err := requireRole(ctx, r, t.Timelock, role, t.Deployer, false); err != nil {
			return err
		}
	}

	// 6. Optional: expected min delay.
	if exp.MinDelay != nil {
		got, err := r.timelockMinDelay(ctx, t.Timelock)
		if err != nil {
			return fmt.Errorf("read min delay: %w", err)
		}
		if got != *exp.MinDelay {
			return fmt.Errorf("timelock min delay is %d, want %d", got, *exp.MinDelay)
		}
	}

	// 7. Optional: timelock owns each governed contract.
	for _, ref := range exp.GovernedContracts {
		owner, err := r.contractOwner(ctx, ref)
		if err != nil {
			return fmt.Errorf("read owner of governed %s: %w", ref.Address, err)
		}
		if owner != t.Timelock {
			return fmt.Errorf("governed contract %s owner is %q, want timelock %q", ref.Address, owner, t.Timelock)
		}
	}
	return nil
}

func requireRole(ctx context.Context, r governanceReader, tlAddr, role, account string, want bool) error {
	has, err := r.timelockHasRole(ctx, tlAddr, role, account)
	if err != nil {
		return fmt.Errorf("read role %s for %s: %w", role, account, err)
	}
	if has != want {
		return fmt.Errorf("role %s for %s: has=%t, want=%t", role, account, has, want)
	}
	return nil
}

// bindingGovernanceReader reads live contract state through the binding clients.
type bindingGovernanceReader struct {
	deps stellardeps.StellarDeps
}

func (b *bindingGovernanceReader) mcmsInstanceLabel(ctx context.Context, addr string) (string, error) {
	return mcmsbindings.NewMcmsClient(b.deps.Invoker, addr).GetInstanceLabel(ctx)
}

func (b *bindingGovernanceReader) mcmsOwner(ctx context.Context, addr string) (string, error) {
	o, err := mcmsbindings.NewMcmsClient(b.deps.Invoker, addr).Owner(ctx)
	if err != nil {
		return "", err
	}
	if o == nil {
		return "", fmt.Errorf("MCMS %s returned no owner", addr)
	}
	return *o, nil
}

func (b *bindingGovernanceReader) mcmsChainNetworkID(ctx context.Context, addr string) ([32]byte, error) {
	return mcmsbindings.NewMcmsClient(b.deps.Invoker, addr).ChainNetworkId(ctx)
}

func (b *bindingGovernanceReader) timelockHasRole(ctx context.Context, addr, role, account string) (bool, error) {
	return timelockbindings.NewTimelockClient(b.deps.Invoker, addr).HasRole(ctx, role, account)
}

func (b *bindingGovernanceReader) timelockMinDelay(ctx context.Context, addr string) (uint64, error) {
	return timelockbindings.NewTimelockClient(b.deps.Invoker, addr).GetMinDelay(ctx)
}

func (b *bindingGovernanceReader) contractOwner(ctx context.Context, ref datastore.AddressRef) (string, error) {
	return ownership.ContractOwner(ctx, b.deps, ref)
}
