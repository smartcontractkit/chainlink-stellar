package adapters

import (
	"errors"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// tarManagerFixtures holds the identities one TAR-manager test needs.
type tarManagerFixtures struct {
	tar         string
	tarHex      string
	token       string
	tokenHex    string
	pool        string
	poolHex     string
	deployer    string
	timelock    string
	third       string
	otherTar    string
	otherTarHex string
}

func newTARManagerFixtures(t *testing.T) tarManagerFixtures {
	t.Helper()
	hexOf := func(cid string) string {
		h, err := stellarutil.StrkeyToHex(cid)
		require.NoError(t, err)
		return h
	}
	f := tarManagerFixtures{
		tar:      stellarutil.MustGenerateMockContractID("deployer", "tar-manager-tar"),
		token:    stellarutil.MustGenerateMockContractID("deployer", "tar-manager-token"),
		pool:     stellarutil.MustGenerateMockContractID("deployer", "tar-manager-pool"),
		deployer: stellarutil.MustGenerateMockContractID("deployer", "tar-manager-deployer"),
		timelock: stellarutil.MustGenerateMockContractID("deployer", "tar-manager-timelock"),
		third:    stellarutil.MustGenerateMockContractID("deployer", "tar-manager-third-party"),
		otherTar: stellarutil.MustGenerateMockContractID("deployer", "tar-manager-other-tar"),
	}
	f.tarHex = hexOf(f.tar)
	f.tokenHex = hexOf(f.token)
	f.poolHex = hexOf(f.pool)
	f.otherTarHex = hexOf(f.otherTar)
	return f
}

// tarDS returns a datastore holding the default TokenAdminRegistry ref.
func tarManagerDS(t *testing.T, f tarManagerFixtures) datastore.DataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
	return ds.Seal()
}

// tarAndTimelockDS returns a datastore holding both the default
// TokenAdminRegistry ref and the CLL timelock ref.
func tarManagerAndTimelockDS(t *testing.T, f tarManagerFixtures) datastore.DataStore {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.TokenAdminRegistryDatastoreRef().UpsertDeployedStrKey(ds, 42, f.tar))
	require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
		ChainSelector: 42,
		Type:          datastore.ContractType(cciputils.RBACTimelock),
		Version:       deploy.MCMSVersion,
		Qualifier:     cciputils.CLLQualifier,
		Address:       f.timelock,
	}))
	return ds.Seal()
}

// tokenConfigStub stubs get_token_config on one registry contract.
func tokenConfigStub(t *testing.T, inv *operationstest.RecordingInvoker, contractID string, cfg tarbindings.TokenConfig) *operationstest.RecordingInvoker {
	t.Helper()
	v, err := cfg.ToScVal()
	require.NoError(t, err)
	return inv.WithSimulateResultForContract(contractID, "get_token_config", &v)
}

func setPoolRecords(inv *operationstest.RecordingInvoker) []operationstest.InvokeRecord {
	var out []operationstest.InvokeRecord
	for _, r := range inv.Records() {
		if r.Fn == "set_pool" {
			out = append(out, r)
		}
	}
	return out
}

func TestStellarTokenAdminRegistryManager_GetTokenAdminRegistryRef(t *testing.T) {
	t.Parallel()
	f := newTARManagerFixtures(t)

	ref, err := (&StellarTokenAdminRegistryManager{}).GetTokenAdminRegistryRef(
		cldf.Environment{DataStore: tarManagerDS(t, f)}, 42)
	require.NoError(t, err)
	require.Equal(t, f.tarHex, ref.Address)

	_, err = (&StellarTokenAdminRegistryManager{}).GetTokenAdminRegistryRef(
		cldf.Environment{DataStore: newSealedDatastore()}, 42)
	require.Error(t, err)
}

func TestStellarTokenAdminRegistryManager_GetActivePool(t *testing.T) {
	t.Parallel()
	f := newTARManagerFixtures(t)

	t.Run("registered pool returns the raw 32-byte AddressRefToBytes form", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{
			Administrator: &f.deployer,
			TokenPool:     &f.pool,
		})
		active, err := activePoolBytes(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.tar, f.token, 42)
		require.NoError(t, err)
		want, err := (&StellarTokenAdapter{}).AddressRefToBytes(datastore.AddressRef{Address: f.poolHex})
		require.NoError(t, err)
		require.Equal(t, want, active)
	})

	t.Run("no token entry is empty bytes without error", func(t *testing.T) {
		t.Parallel()
		// An unregistered token reads back as a zero config, never a nil one.
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{})
		active, err := activePoolBytes(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.tar, f.token, 42)
		require.NoError(t, err)
		require.Empty(t, active)
	})

	t.Run("no pool set is empty bytes without error", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{
			Administrator: &f.deployer,
		})
		active, err := activePoolBytes(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.tar, f.token, 42)
		require.NoError(t, err)
		require.Empty(t, active)
	})

	t.Run("read failure is an error, never empty bytes", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().WithSimulateErrorForContract(f.tar, "get_token_config", errors.New("boom"))
		_, err := activePoolBytes(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.tar, f.token, 42)
		require.ErrorContains(t, err, "read token config")
	})
}

func TestStellarTokenAdminRegistryManager_registryStrkey(t *testing.T) {
	t.Parallel()
	f := newTARManagerFixtures(t)
	m := &StellarTokenAdminRegistryManager{}

	t.Run("no overrides resolves the datastore default", func(t *testing.T) {
		t.Parallel()
		cid, err := m.registryStrkey(tarManagerDS(t, f), 42, nil)
		require.NoError(t, err)
		require.Equal(t, f.tar, cid)
	})

	t.Run("override address wins", func(t *testing.T) {
		t.Parallel()
		cid, err := m.registryStrkey(tarManagerDS(t, f), 42, []datastore.AddressRef{{Address: f.otherTarHex}})
		require.NoError(t, err)
		require.Equal(t, f.otherTar, cid)
	})

	t.Run("override type resolves from the datastore", func(t *testing.T) {
		t.Parallel()
		ds := datastore.NewMemoryDataStore()
		require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
			ChainSelector: 42,
			Type:          "OtherRegistry",
			Version:       semver.MustParse("2.0.0"),
			Address:       f.otherTarHex,
		}))
		cid, err := m.registryStrkey(ds.Seal(), 42, []datastore.AddressRef{{Type: "OtherRegistry", Version: semver.MustParse("2.0.0")}})
		require.NoError(t, err)
		require.Equal(t, f.otherTar, cid)
	})

	t.Run("an override that does not resolve is skipped", func(t *testing.T) {
		t.Parallel()
		cid, err := m.registryStrkey(tarManagerDS(t, f), 42, []datastore.AddressRef{{Type: "MissingRegistry"}})
		require.NoError(t, err)
		require.Equal(t, f.tar, cid)
	})
}

func TestStellarTokenAdminRegistryManager_UnregisterToken(t *testing.T) {
	t.Parallel()
	f := newTARManagerFixtures(t)
	newInput := func(mutate func(*tokens.UnregisterTokenSequenceInput)) tokens.UnregisterTokenSequenceInput {
		input := tokens.UnregisterTokenSequenceInput{
			Selector:          42,
			TokenRef:          datastore.AddressRef{ChainSelector: 42, Address: f.tokenHex},
			ExistingDataStore: tarManagerAndTimelockDS(t, f),
		}
		if mutate != nil {
			mutate(&input)
		}
		return input
	}

	t.Run("deployer administrator clears the pool directly", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{
			Administrator: &f.deployer,
			TokenPool:     &f.pool,
		})
		out, err := unregisterToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)
		recs := setPoolRecords(inv)
		require.Len(t, recs, 1)
		require.Equal(t, f.tar, recs[0].ContractID)
		token, err := scval.AddressFromScVal(recs[0].Args[0])
		require.NoError(t, err)
		require.Equal(t, f.token, token)
		require.Equal(t, scval.VoidScVal(), recs[0].Args[1], "the None pool option marshals as void")
		// Direct-exec marker: one ExecInfo-marked empty batch op.
		require.Len(t, out.BatchOps, 1)
		require.Empty(t, out.BatchOps[0].Transactions)
	})

	t.Run("timelock administrator gets one set_pool proposal", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{
			Administrator: &f.timelock,
			TokenPool:     &f.pool,
		})
		out, err := unregisterToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)
		require.Empty(t, setPoolRecords(inv), "no direct invokes under the timelock administrator")
		require.Len(t, out.BatchOps, 1)
		require.Len(t, out.BatchOps[0].Transactions, 1)
		tx := out.BatchOps[0].Transactions[0]
		require.Equal(t, f.tar, tx.To)
		require.Contains(t, string(tx.AdditionalFields), `"family":"stellar"`)
		fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
		require.NoError(t, err)
		require.Equal(t, "set_pool", fn)
		token, err := scval.AddressFromScVal(args[0])
		require.NoError(t, err)
		require.Equal(t, f.token, token)
		require.Equal(t, scval.VoidScVal(), args[1])
	})

	t.Run("third-party administrator fails closed", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{
			Administrator: &f.third,
			TokenPool:     &f.pool,
		})
		_, err := unregisterToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.ErrorContains(t, err, "neither deployer")
	})

	t.Run("token with no registry entry skips", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{})
		out, err := unregisterToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.NoError(t, err)
		require.Empty(t, setPoolRecords(inv))
		require.Empty(t, out.BatchOps)
	})

	t.Run("entry without an administrator errors", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.tar, tarbindings.TokenConfig{
			TokenPool: &f.pool,
		})
		_, err := unregisterToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(nil))
		require.ErrorContains(t, err, "no administrator")
	})

	t.Run("registry override wins", func(t *testing.T) {
		t.Parallel()
		inv := tokenConfigStub(t, operationstest.NewRecordingInvoker(), f.otherTar, tarbindings.TokenConfig{
			Administrator: &f.deployer,
			TokenPool:     &f.pool,
		})
		_, err := unregisterToken(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, f.deployer, newInput(func(input *tokens.UnregisterTokenSequenceInput) {
			input.RegistryRef = datastore.AddressRef{Address: f.otherTarHex}
		}))
		require.NoError(t, err)
		recs := setPoolRecords(inv)
		require.Len(t, recs, 1)
		require.Equal(t, f.otherTar, recs[0].ContractID)
	})

	t.Run("sequence errors on missing chain", func(t *testing.T) {
		t.Parallel()
		m := &StellarTokenAdminRegistryManager{}
		_, err := cldf_ops.ExecuteSequence(operationstest.NewBundle(t), m.UnregisterToken(), cldf_chain.NewBlockChains(nil), newInput(nil))
		require.ErrorContains(t, err, "stellar chain 42 not found")
	})
}

func TestResolveTARTokenStrkey(t *testing.T) {
	t.Parallel()
	f := newTARManagerFixtures(t)

	cid, err := resolveTARTokenStrkey(tarManagerDS(t, f), 42, datastore.AddressRef{Address: f.tokenHex})
	require.NoError(t, err)
	require.Equal(t, f.token, cid)

	cid, err = resolveTARTokenStrkey(tarManagerDS(t, f), 42, datastore.AddressRef{Address: f.token})
	require.NoError(t, err)
	require.Equal(t, f.token, cid)

	// Empty address falls back to the datastore lookup by ref fields.
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.Addresses().Upsert(datastore.AddressRef{
		ChainSelector: 42,
		Type:          "BnmToken",
		Version:       semver.MustParse("2.0.0"),
		Qualifier:     "q",
		Address:       f.tokenHex,
	}))
	cid, err = resolveTARTokenStrkey(ds.Seal(), 42, datastore.AddressRef{Type: "BnmToken", Version: semver.MustParse("2.0.0"), Qualifier: "q"})
	require.NoError(t, err)
	require.Equal(t, f.token, cid)

	_, err = resolveTARTokenStrkey(tarManagerDS(t, f), 42, datastore.AddressRef{})
	require.ErrorContains(t, err, "neither an address nor a type")
}
