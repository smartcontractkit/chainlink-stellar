package sequences

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// mockBnmHost is a minimal CCIPDevenvHost for unit-testing the BnM onboarding
// sequence without a live network. Only the surface the sequence reads is
// populated (Router/RampRegistry/RmnProxy IDs, a real DeployerKeypair for the
// deterministic salt, a no-op logger, and a SetTokenPool recorder); every other
// method is a zero-value stub. Deployer() returns nil because the refactored
// sequence deploys through the injected deps, not the host's Deployer.
type mockBnmHost struct {
	log        zerolog.Logger
	kp         *keypair.Full
	routerID   string
	rampRegID  string
	rmnProxyID string

	setPoolIDs     []string
	setPoolClients []*tokenpoolbindings.TokenPoolClient
}

func newMockBnmHost(routerID, rampRegID, rmnProxyID string) *mockBnmHost {
	return &mockBnmHost{
		log:        zerolog.Nop(),
		kp:         keypair.MustRandom(),
		routerID:   routerID,
		rampRegID:  rampRegID,
		rmnProxyID: rmnProxyID,
	}
}

func (m *mockBnmHost) Logger() *zerolog.Logger { return &m.log }

func (m *mockBnmHost) Deployer() *stellardeployment.Deployer { return nil }

func (m *mockBnmHost) DeployerKeypair() *keypair.Full { return m.kp }

func (m *mockBnmHost) NetworkPassphrase() string { return "" }

func (m *mockBnmHost) FriendbotURL() string { return "" }

func (m *mockBnmHost) SetOnRamp(string, *onrampbindings.OnRampClient) {}

func (m *mockBnmHost) OnRampClient() *onrampbindings.OnRampClient { return nil }

func (m *mockBnmHost) FeeQuoterClient() *fqbindings.FeeQuoterClient { return nil }

func (m *mockBnmHost) SetFeeQuoter(*fqbindings.FeeQuoterClient) {}

func (m *mockBnmHost) SetTokenAdminRegistry(string, *tarbindings.TokenAdminRegistryClient) {}

func (m *mockBnmHost) TokenAdminRegistryClient() *tarbindings.TokenAdminRegistryClient {
	return nil
}

func (m *mockBnmHost) SetTokenPool(contractID string, client *tokenpoolbindings.TokenPoolClient) {
	m.setPoolIDs = append(m.setPoolIDs, contractID)
	m.setPoolClients = append(m.setPoolClients, client)
}

func (m *mockBnmHost) SetLegacyLockReleasePool(string) {}

func (m *mockBnmHost) SetTokenLockBox(string) {}

func (m *mockBnmHost) LatestLedgerSequence(context.Context) (uint32, error) { return 0, nil }

func (m *mockBnmHost) SetTestToken(string) {}

func (m *mockBnmHost) TestTokenContractID() string { return "" }

func (m *mockBnmHost) SetOffRamp(string, *offrampbindings.OffRampClient) {}

func (m *mockBnmHost) OffRampClient() *offrampbindings.OffRampClient { return nil }

func (m *mockBnmHost) SetRouter(string, *routerbindings.RouterClient) {}

func (m *mockBnmHost) RouterContractID() string { return m.routerID }

func (m *mockBnmHost) SetRampRegistry(string) {}

func (m *mockBnmHost) RampRegistryContractID() string { return m.rampRegID }

func (m *mockBnmHost) SetRmnProxy(string) {}

func (m *mockBnmHost) RmnProxyContractID() string { return m.rmnProxyID }

func (m *mockBnmHost) SetVVR(string) {}

func (m *mockBnmHost) SetCV(string) {}

func (m *mockBnmHost) SetReceiver(string) {}

func (m *mockBnmHost) SetFeeToken(string) {}

func (m *mockBnmHost) FeeTokenContractID() string { return "" }

func (m *mockBnmHost) CreateFeeToken(context.Context, string) (string, error) { return "", nil }

func (m *mockBnmHost) CreateTestToken(context.Context, string) (string, error) { return "", nil }

// validDeps builds a StellarDeps backed by the operationstest recording harness.
func validDeps(inv *operationstest.RecordingInvoker) stellardeps.StellarDeps {
	return stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
}

// seqFakeDeployer returns a fresh valid Stellar account strkey per deploy call
// (distinct bnm vs pool IDs) so every Address arg the sequence builds decodes
// cleanly — unlike operationstest.MockContractID, which is an invalid strkey.
// The returned IDs are recorded on Deployed for assertions.
type seqFakeDeployer struct {
	Deployed []string
}

func (d *seqFakeDeployer) DeployContract(_ context.Context, _ string, _ [32]byte) (string, error) {
	id := keypair.MustRandom().Address()
	d.Deployed = append(d.Deployed, id)
	return id, nil
}

// stagingRoot creates a temp dir that FindStellarRoot accepts (a go.mod with the
// chainlink-stellar module path) and writes empty release WASMs so the
// sequence's statReleaseWasm checks pass without a real build. Returns the dir.
func stagingRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte("module github.com/smartcontractkit/chainlink-stellar\n\ngo 1.24\n"),
		0o644,
	))
	relDir := filepath.Join(root, "target", "wasm32v1-none", "release")
	require.NoError(t, os.MkdirAll(relDir, 0o755))
	for _, w := range []string{"bnm_token.wasm", "pools_burn_mint_pool.wasm"} {
		require.NoError(t, os.WriteFile(filepath.Join(relDir, w), []byte{0x00, 0x61, 0x73, 0x6d}, 0o644))
	}
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)
	return root
}

func TestRunDeployBnmToken_ErrorsWhenHostNil(t *testing.T) {
	t.Parallel()
	b := operationstest.NewBundle(t)
	_, err := RunDeployBnmToken(b.GetContext(), b, validDeps(operationstest.NewRecordingInvoker()), nil, DeployBnmTokenInput{
		TarContractID: operationstest.MockContractID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "CCIPDevenvHost")
}

func TestRunDeployBnmToken_ErrorsWhenDepsIncomplete(t *testing.T) {
	t.Parallel()
	b := operationstest.NewBundle(t)
	host := newMockBnmHost(operationstest.MockContractID, operationstest.MockContractID, operationstest.MockContractID)
	// Zero StellarDeps: Deploy and Invoker both nil.
	_, err := RunDeployBnmToken(b.GetContext(), b, stellardeps.StellarDeps{}, host, DeployBnmTokenInput{
		TarContractID: operationstest.MockContractID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "incomplete StellarDeps")
}

func TestRunDeployBnmToken_ErrorsWhenTarEmpty(t *testing.T) {
	t.Parallel()
	b := operationstest.NewBundle(t)
	host := newMockBnmHost(operationstest.MockContractID, operationstest.MockContractID, operationstest.MockContractID)
	_, err := RunDeployBnmToken(b.GetContext(), b, validDeps(operationstest.NewRecordingInvoker()), host, DeployBnmTokenInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tar_contract_id")
}

func TestRunDeployBnmToken_ErrorsWhenRmnProxyEmpty(t *testing.T) {
	t.Parallel()
	b := operationstest.NewBundle(t)
	// RMN proxy unset → must fail before any deploy (curse-check prerequisite).
	host := newMockBnmHost(operationstest.MockContractID, operationstest.MockContractID, "")
	_, err := RunDeployBnmToken(b.GetContext(), b, validDeps(operationstest.NewRecordingInvoker()), host, DeployBnmTokenInput{
		TarContractID: operationstest.MockContractID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rmn proxy")
}

// TestRunDeployBnmToken_HappyPath_RecordsOnboardingOrder drives the full
// onboarding through the recording harness and asserts the canonical op order:
// initialize(BnM) → set_admin(mint authority→pool) → initialize(pool) →
// propose_administrator → accept_admin_role → set_pool, plus that the pool is
// registered on the host via SetTokenPool. The two `deploy` calls go through
// FakeDeployer (not the recording invoker), so they are not in the record list.
func TestRunDeployBnmToken_HappyPath_RecordsOnboardingOrder(t *testing.T) {
	// Not parallel: t.Setenv (CHAINLINK_STELLAR_ROOT).
	stagingRoot(t)

	b := operationstest.NewBundle(t)
	inv := operationstest.NewRecordingInvoker()
	deployer := &seqFakeDeployer{}
	host := newMockBnmHost(operationstest.MockContractID, operationstest.MockContractID, operationstest.MockContractID)
	deployerAddr := host.DeployerKeypair().Address()
	deps := stellardeps.StellarDeps{Deploy: deployer, Invoker: inv}

	out, err := RunDeployBnmToken(b.GetContext(), b, deps, host, DeployBnmTokenInput{
		TarContractID: operationstest.MockContractID,
	})
	require.NoError(t, err)
	// Two deploys: first the BnM token, then the burn-mint pool — distinct IDs.
	require.Len(t, deployer.Deployed, 2)
	bnmID := deployer.Deployed[0]
	poolID := deployer.Deployed[1]
	require.NotEqual(t, bnmID, poolID)
	require.Equal(t, bnmID, out.BnmTokenID)
	require.Equal(t, poolID, out.PoolID)

	// The canonical onboarding order, recorded by the invoker (the two `deploy`
	// calls go through the fake deployer, so they are not in this list).
	recs := inv.Records()
	wantFns := []string{"initialize", "set_admin", "initialize", "propose_administrator", "accept_admin_role", "set_pool"}
	require.Len(t, recs, len(wantFns))
	for i, want := range wantFns {
		require.Equalf(t, want, recs[i].Fn, "op %d: expected %q, got %q", i, want, recs[i].Fn)
	}

	// The BnM initialize must seed the deployer as the initial admin (the address
	// that later hands mint authority to the pool via set_admin).
	require.Equal(t, "initialize", recs[0].Fn)
	require.Len(t, recs[0].Args, 4)
	gotAdmin, err := scval.AddressFromScVal(recs[0].Args[0])
	require.NoError(t, err)
	require.Equal(t, deployerAddr, gotAdmin)

	// The burn-mint mint-authority handoff: set_admin's single arg is the pool ID
	// (the address that will mint on inbound bridge messages).
	require.Equal(t, "set_admin", recs[1].Fn)
	require.Len(t, recs[1].Args, 1)
	gotNewAdmin, err := scval.AddressFromScVal(recs[1].Args[0])
	require.NoError(t, err)
	require.Equal(t, poolID, gotNewAdmin)

	// The burn-mint pool initialize is wired against the host's core CCIP stack.
	require.Equal(t, "initialize", recs[2].Fn)
	require.Len(t, recs[2].Args, 6) // owner, token, tokenDecimals, router, rampRegistry, rmnProxy

	// The pool is registered on the host exactly once, with the deployed pool ID.
	require.Equal(t, []string{poolID}, host.setPoolIDs)
	require.Len(t, host.setPoolClients, 1)
}
