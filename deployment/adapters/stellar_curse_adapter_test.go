package adapters

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

func TestStellarCurseAdapter_InterfaceCompliance(t *testing.T) {
	a := NewStellarCurseAdapter()
	var _ fastcurse.CurseAdapter = a
	var _ fastcurse.CurseSubjectAdapter = a
}

func TestStellarCurseAdapter_SelectorToSubject(t *testing.T) {
	a := NewStellarCurseAdapter()
	sel := uint64(12345)
	subject := a.SelectorToSubject(sel)
	got := fastcurse.GenericSelectorToSubject(sel)
	require.Equal(t, got, subject)
}

func TestStellarCurseAdapter_SubjectToSelector(t *testing.T) {
	a := NewStellarCurseAdapter()

	t.Run("normal", func(t *testing.T) {
		sel := uint64(99)
		subject := fastcurse.GenericSelectorToSubject(sel)
		got, err := a.SubjectToSelector(subject)
		require.NoError(t, err)
		require.Equal(t, sel, got)
	})

	t.Run("global_returns_zero", func(t *testing.T) {
		got, err := a.SubjectToSelector(fastcurse.GlobalCurseSubject())
		require.NoError(t, err)
		require.Equal(t, uint64(0), got)
	})
}

func TestStellarCurseAdapter_DeriveCurseAdapterVersion(t *testing.T) {
	a := NewStellarCurseAdapter()
	env := envWithDatastore(newSealedDatastore())
	v, err := a.DeriveCurseAdapterVersion(env, 0)
	require.NoError(t, err)
	require.True(t, v.Equal(stellarops.ContractDeploymentVersion))
}

func TestStellarCurseAdapter_IsCurseEnabled_beforeInit(t *testing.T) {
	a := NewStellarCurseAdapter()
	env := envWithDatastore(newSealedDatastore())
	ok, err := a.IsCurseEnabledForChain(env, 42)
	require.NoError(t, err)
	require.False(t, ok, "should be false before Initialize")
}

func TestStellarCurseAdapter_CurseUncurse_sequenceNonNil(t *testing.T) {
	a := NewStellarCurseAdapter()
	require.NotNil(t, a.Curse())
	require.NotNil(t, a.Uncurse())
}

func TestStellarCurseAdapter_SubjectRoundTrip(t *testing.T) {
	a := NewStellarCurseAdapter()
	for _, sel := range []uint64{0, 1, 100, 1<<63 - 1} {
		subject := a.SelectorToSubject(sel)
		got, err := a.SubjectToSelector(subject)
		require.NoError(t, err)
		require.Equal(t, sel, got, "roundtrip failed for selector %d", sel)
	}
}

func TestStellarCurseAdapter_Registration(t *testing.T) {
	reg := fastcurse.GetCurseRegistry()
	v := stellarops.ContractDeploymentVersion

	_, ok := reg.GetCurseAdapter("stellar", v)
	require.True(t, ok, "StellarCurseAdapter should be registered from init()")

	_, ok = reg.GetCurseSubjectAdapter("stellar")
	require.True(t, ok, "StellarCurseSubjectAdapter should be registered from init()")
}

func TestStellarContractIDOnChain_emptyDatastore(t *testing.T) {
	ds := newSealedDatastore()
	env := envWithDatastore(ds)
	_, err := stellarContractIDOnChain(env, 42, stellarccip.RouterDatastoreRef())
	require.Error(t, err)
}

func TestStellarContractIDOnChain_routerResolvesToStrkey(t *testing.T) {
	ds := datastore.NewMemoryDataStore()
	sel := uint64(7)
	routerStrkey := stellarutil.MustGenerateMockContractID("deployer", "router-curse-adapter-test")
	require.NoError(t, stellarccip.RecordRouter(ds, sel, routerStrkey))
	env := envWithDatastore(ds.Seal())
	got, err := stellarContractIDOnChain(env, sel, stellarccip.RouterDatastoreRef())
	require.NoError(t, err)
	require.Equal(t, routerStrkey, got)
}

// sdkOnlySigner reports an address without a keypair, so contract reads against
// a client-less chain fail: Initialize's mandatory owner read surfaces the
// failure, while its advisory admin read degrades.
type sdkOnlySigner struct{ addr string }

func (sdkOnlySigner) Sign([]byte) ([]byte, error) { return nil, nil }
func (sdkOnlySigner) SignDecorated([]byte) (xdr.DecoratedSignature, error) {
	return xdr.DecoratedSignature{}, nil
}
func (s sdkOnlySigner) Address() string          { return s.addr }
func (sdkOnlySigner) KeypairFull() *keypair.Full { return nil }

func adapterTestEnv(t *testing.T, sel uint64, seedHexRMN bool, quals ...string) cldf.Environment {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	if seedHexRMN {
		rmnStrkey := stellarutil.MustGenerateMockContractID("deployer", "rmn-curse-adapter-test")
		require.NoError(t, stellarccip.RecordRMNRemote(ds, sel, rmnStrkey))
	}
	routerStrkey := stellarutil.MustGenerateMockContractID("deployer", "router-curse-adapter-test")
	require.NoError(t, stellarccip.RecordRouter(ds, sel, routerStrkey))
	for i, qual := range quals {
		tl := stellarutil.MustGenerateMockContractID("deployer", fmt.Sprintf("timelock-%d", i))
		ref := mcmsutil.StellarTimelockDatastoreRef(sel, qual, tl)
		require.NoError(t, ds.Addresses().Upsert(ref))
	}
	// A dead RPC endpoint makes the owner/admin reads fail with a transport
	// error (not a panic), exercising Initialize's best-effort degradation.
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            sdkOnlySigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
		Client:            rpcclient.NewClient("http://127.0.0.1:1", &http.Client{Timeout: 2 * time.Second}),
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	return cldf.Environment{
		Logger:      cldflogger.Test(t),
		GetContext:  func() context.Context { return context.Background() },
		DataStore:   ds.Seal(),
		BlockChains: cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch}),
	}
}

func TestStellarCurseAdapter_InitializeResolvesRoutingFacts(t *testing.T) {
	sel := uint64(424242420101)
	// adapterTestEnv seeds qualifiers in call order: CLL(0), RMNMCMS(1), UFC(2).
	cclTL := stellarutil.MustGenerateMockContractID("deployer", "timelock-0")
	govTL := stellarutil.MustGenerateMockContractID("deployer", "timelock-1")
	fastTL := stellarutil.MustGenerateMockContractID("deployer", "timelock-2")

	env := adapterTestEnv(t, sel, true,
		utils.CLLQualifier, utils.RMNTimelockQualifier, utils.UltraFastCurseMCMSQualifier)

	rmnStrkey := stellarutil.MustGenerateMockContractID("deployer", "rmn-curse-adapter-test")
	a := NewStellarCurseAdapter()

	// The owner read is mandatory: against the dead RPC endpoint it fails loudly
	// instead of silently degrading to an empty Owner (which would surface much
	// later as a misleading proposal-build error).
	err := a.Initialize(env, sel)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read RMN Remote owner")

	// Everything resolved from the datastore before the mandatory read is cached.
	// RMN ref is stored hex and cached as the strkey form the ownership helpers need.
	require.Equal(t, rmnStrkey, a.rmnContractID[sel])

	// All three qualifiers resolve, including CLLCCIP (diagnostics only).
	require.Equal(t, govTL, a.timelocks[sel][utils.RMNTimelockQualifier])
	require.Equal(t, fastTL, a.timelocks[sel][utils.UltraFastCurseMCMSQualifier])
	require.Equal(t, cclTL, a.timelocks[sel][utils.CLLQualifier])

	// Neither governance fact was stored: the owner read failed first, and the
	// admin read never runs after it.
	require.Empty(t, a.owners[sel])
	require.Empty(t, a.curseAdmins[sel])
}

func TestStellarCurseAdapter_InitializeRefreshesGovernanceFacts(t *testing.T) {
	sel := uint64(424242420104)
	env1 := adapterTestEnv(t, sel, true, utils.RMNTimelockQualifier)
	env2 := adapterTestEnv(t, sel, true, utils.UltraFastCurseMCMSQualifier)

	deployerOwner := stellarutil.MustGenerateMockContractID("deployer", "owner-0")
	govTL := stellarutil.MustGenerateMockContractID("deployer", "timelock-0")
	fastTL := stellarutil.MustGenerateMockContractID("deployer", "timelock-1")

	// Activation moves the RMN Remote from the deployer to the governance
	// timelock between the two Initialize calls; an admin is granted in between.
	owners := []string{deployerOwner, govTL}
	admins := [][]string{nil, {fastTL}}
	calls := 0
	a := NewStellarCurseAdapter()
	a.readOwner = func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
		owner := owners[calls]
		return owner, nil
	}
	a.readAdmins = func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) ([]string, error) {
		return admins[calls], nil
	}

	require.NoError(t, a.Initialize(env1, sel))
	require.Equal(t, deployerOwner, a.owners[sel], "before activation the deployer owns")
	require.Empty(t, a.curseAdmins[sel])
	require.Contains(t, a.timelocks[sel], utils.RMNTimelockQualifier)
	require.NotContains(t, a.timelocks[sel], utils.UltraFastCurseMCMSQualifier)

	calls++
	require.NoError(t, a.Initialize(env2, sel))
	require.Equal(t, govTL, a.owners[sel], "a cached owner would still route as the deployer post-activation")
	require.Equal(t, []string{fastTL}, a.curseAdmins[sel], "a newly granted admin must be visible")
	require.Contains(t, a.timelocks[sel], utils.UltraFastCurseMCMSQualifier, "a stack deployed since the last run must be visible")
	require.NotContains(t, a.timelocks[sel], utils.RMNTimelockQualifier)
}

func TestStellarCurseAdapter_InitializeDropsStaleAdminsOnFailedRead(t *testing.T) {
	sel := uint64(424242420105)
	env := adapterTestEnv(t, sel, true, utils.RMNTimelockQualifier)
	fastTL := stellarutil.MustGenerateMockContractID("deployer", "admin-0")

	calls := 0
	a := NewStellarCurseAdapter()
	a.readOwner = func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
		return fastTL, nil
	}
	a.readAdmins = func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) ([]string, error) {
		if calls == 0 {
			return []string{fastTL}, nil
		}
		return nil, fmt.Errorf("read failure")
	}

	require.NoError(t, a.Initialize(env, sel))
	require.Equal(t, []string{fastTL}, a.curseAdmins[sel])

	calls++
	require.NoError(t, a.Initialize(env, sel), "the advisory admin read degrades, never errors")
	require.Empty(t, a.curseAdmins[sel], "a failed re-read must drop the stale list so this run never routes on it")
}

func TestStellarCurseAdapter_InitializeAbsentTimelocksDegrade(t *testing.T) {
	sel := uint64(424242420102)
	env := adapterTestEnv(t, sel, true, utils.RMNTimelockQualifier)
	a := NewStellarCurseAdapter()
	// The owner read fails against the dead RPC endpoint (mandatory, fatal)…
	require.Error(t, a.Initialize(env, sel))
	// …but the datastore-only timelock cache still degraded quietly: absent
	// qualifiers are simply missing keys, never an error.
	require.Contains(t, a.timelocks[sel], utils.RMNTimelockQualifier)
	require.NotContains(t, a.timelocks[sel], utils.UltraFastCurseMCMSQualifier)
	require.NotContains(t, a.timelocks[sel], utils.CLLQualifier)
}

func TestStellarCurseAdapter_InitializeFailsClosedWithoutRMN(t *testing.T) {
	sel := uint64(424242420103)
	env := adapterTestEnv(t, sel, false, utils.RMNTimelockQualifier)
	a := NewStellarCurseAdapter()
	err := a.Initialize(env, sel)
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve RMN Remote")
}
