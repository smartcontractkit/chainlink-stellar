package sequences

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
)

// fakeLedger answers ContractInstanceState from a map, standing in for the
// on-chain ledger in the skip layers.
type fakeLedger struct {
	instances map[string]xdr.Hash
}

func (f fakeLedger) ContractInstanceState(_ context.Context, contractID string) (bool, xdr.Hash, error) {
	h, ok := f.instances[contractID]
	return ok, h, nil
}

// componentTestSetup builds the fixture every component test shares: a temp
// wasm, a recording invoker whose owner() reads return None by default, a
// bundle with a memory reporter, and ComponentDeps over both fakes. It returns
// the reporter so tests can assert on the recorded op trace.
func componentTestSetup(t *testing.T, ledger InstanceStateReader) (
	wasmPath string, b cldf_ops.Bundle, inv *operationstest.RecordingInvoker,
	reporter *cldf_ops.MemoryReporter, deps ComponentDeps,
) {
	t.Helper()
	dir := t.TempDir()
	wasmPath = filepath.Join(dir, "contract.wasm")
	require.NoError(t, os.WriteFile(wasmPath, []byte("wasm-bytes"), 0o644))

	inv = operationstest.NewRecordingInvoker().
		WithSimulateResult(&xdr.ScVal{Type: xdr.ScValTypeScvVoid})
	reporter = cldf_ops.NewMemoryReporter()
	b = cldf_ops.NewBundle(func() context.Context { return t.Context() }, cldflogger.Nop(), reporter)
	deps = ComponentDeps{
		StellarDeps: stellardeps.StellarDeps{
			// characterizationDeployer returns valid per-wasm C… strkeys; the
			// refs the sequences record are hex-converted, which the checksum-less
			// operationstest placeholder fails.
			Deploy:  newCharacterizationDeployer(),
			Invoker: inv,
		},
		NetworkPassphrase: characterizationPassphrase,
		DeployerAddress:   characterizationKeypair().Address(),
		Ledger:            ledger,
	}
	return wasmPath, b, inv, reporter, deps
}

// requireOnlyFailedSequenceReports asserts that every recorded report is the
// component's sequence report from a failed call: CLDF records the sequence
// report even when the handler errors.
func requireOnlyFailedSequenceReports(t *testing.T, reporter *cldf_ops.MemoryReporter, sequenceID string) {
	t.Helper()
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	require.NotEmpty(t, reports)
	for _, r := range reports {
		require.Equal(t, sequenceID, r.Def.ID)
		require.NotNil(t, r.Err)
	}
}

// reportIDs returns the ordered report ids the bundle recorded.
func reportIDs(t *testing.T, reporter *cldf_ops.MemoryReporter) []string {
	t.Helper()
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	ids := make([]string, len(reports))
	for i, r := range reports {
		ids[i] = r.Def.ID
	}
	return ids
}

// predictedID computes the contract ID a salt label would deploy to with the
// characterization fixture's passphrase and deployer.
func predictedID(t *testing.T, saltLabel string) string {
	t.Helper()
	deployer := characterizationKeypair().Address()
	salt := stellardeployment.GenerateDeterministicSalt(deployer, saltLabel)
	id, err := stellardeployment.ComputeContractID(characterizationPassphrase, deployer, salt)
	require.NoError(t, err)
	return id
}

// localWasmHash hashes the temp wasm the way the ledger would.
func localWasmHash(t *testing.T, wasmPath string) xdr.Hash {
	t.Helper()
	b, err := os.ReadFile(wasmPath)
	require.NoError(t, err)
	h := sha256.Sum256(b)
	return xdr.Hash(h)
}

// foreignOwnerAddress is a second, distinct account: not the deployer.
func foreignOwnerAddress() string {
	seed := sha256.Sum256([]byte("stellar-ccip-component-foreign-owner"))
	kp, err := keypair.FromRawSeed(seed)
	if err != nil {
		panic(err)
	}
	return kp.Address()
}

// existingRefForID records contractID under the TAR ref, as a prior run would.
func existingRefForID(t *testing.T, contractID string) datastore.AddressRef {
	t.Helper()
	hexAddr, err := stellarutil.StrkeyToHex(contractID)
	require.NoError(t, err)
	return stellarccip.TokenAdminRegistryDatastoreRef().FullAddressRef(1, hexAddr)
}

func TestDeployTokenAdminRegistry_FreshDeploysAndInitializes(t *testing.T) {
	t.Parallel()
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, fakeLedger{})
	out, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.NoError(t, err)
	require.True(t, out.Deployed)
	require.True(t, out.Initialized)
	require.Len(t, out.Refs, 1)
	require.Equal(t, []string{"token-admin-registry:deploy", "token-admin-registry:initialize", "stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
	require.Equal(t, "initialize", inv.Last().Fn)
}

func TestDeployTokenAdminRegistry_RerunWithRefSkipsBoth(t *testing.T) {
	t.Parallel()
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, fakeLedger{instances: map[string]xdr.Hash{
		// A stale ledger entry must not matter: the ref wins (layer 1).
		predictedID(t, "token-admin-registry"): {0x01},
	}})
	id := predictedID(t, "token-admin-registry")
	// The ref proves an earlier run finished: owner() is set, so no initialize.
	configuredOwner := scval.AddressToScVal(deps.DeployerAddress)
	inv.WithSimulateResultForFn("owner", &configuredOwner)
	out, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath, ExistingAddresses: []datastore.AddressRef{existingRefForID(t, id)},
	})
	require.NoError(t, err)
	require.False(t, out.Deployed)
	require.False(t, out.Initialized)
	require.Empty(t, out.Refs)
	require.Equal(t, id, out.ContractID)
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
	// Layer 3 still reads owner() on a ref-backed contract; only the deploy and
	// initialize ops are skipped.
	records := inv.Records()
	require.Len(t, records, 1)
	require.Equal(t, "owner", records[0].Fn)
}

func TestDeployTokenAdminRegistry_RerunWithStrkeyFormRef(t *testing.T) {
	t.Parallel()
	// A ref recorded by hand may carry the C… strkey instead of the hex the
	// datastore Record* helpers write; layer 1 must accept both forms.
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, fakeLedger{})
	id := predictedID(t, "token-admin-registry")
	configuredOwner := scval.AddressToScVal(deps.DeployerAddress)
	inv.WithSimulateResultForFn("owner", &configuredOwner)
	strkeyRef := stellarccip.TokenAdminRegistryDatastoreRef().FullAddressRef(1, "")
	strkeyRef.Address = id
	out, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath, ExistingAddresses: []datastore.AddressRef{strkeyRef},
	})
	require.NoError(t, err)
	require.False(t, out.Deployed)
	require.False(t, out.Initialized)
	require.Equal(t, id, out.ContractID)
	// The strkey-form row is rewritten to hex in the returned refs, so the
	// datastore readers that expect hex keep working.
	hexAddr, err := stellarutil.StrkeyToHex(id)
	require.NoError(t, err)
	require.Len(t, out.Refs, 1)
	require.Equal(t, hexAddr, out.Refs[0].Address)
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
}

func TestDeployTokenAdminRegistry_StaleRefErrorsClearly(t *testing.T) {
	t.Parallel()
	// A ref whose contract is gone from the chain fails the owner read; the
	// error must point at the stale ref.
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, fakeLedger{})
	id := predictedID(t, "token-admin-registry")
	deps.Invoker = simulateErrorInvoker{inv}
	strkeyRef := stellarccip.TokenAdminRegistryDatastoreRef().FullAddressRef(1, "")
	strkeyRef.Address = id
	_, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath, ExistingAddresses: []datastore.AddressRef{strkeyRef},
	})
	require.ErrorContains(t, err, "datastore ref may be stale")
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
}

func TestDeployRMNRemote_FastCurseTimelockPrependsCurseAdmin(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	timelockID := predictedID(t, "fast-curse-timelock")
	otherAdmin := foreignOwnerAddress()
	timelockRef := mcmsutil.StellarTimelockDatastoreRef(1, cciputils.UltraFastCurseMCMSQualifier, timelockID)
	out, err := execComponentSequence(b, deps, DeployRMNRemote, DeployRMNRemoteInput{
		ChainSelector: 1, WasmPath: wasmPath, EnableFastCurse: true,
		CurseAdmins:       []string{otherAdmin},
		ExistingAddresses: []datastore.AddressRef{timelockRef},
	})
	require.NoError(t, err)
	require.True(t, out.Deployed)
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	var gotCurseAdmins []string
	for _, r := range reports {
		if r.Def.ID == "rmn-remote:initialize" {
			gotCurseAdmins = r.Input.(rmnremoteops.InitializeInput).CurseAdmins
		}
	}
	// The fast-curse timelock goes first; configured admins follow (EVM precedent).
	require.Equal(t, []string{timelockID, otherAdmin}, gotCurseAdmins)
}

func TestDeployRMNRemote_FastCurseWithoutTimelockErrors(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	_, err := execComponentSequence(b, deps, DeployRMNRemote, DeployRMNRemoteInput{
		ChainSelector: 1, WasmPath: wasmPath, EnableFastCurse: true,
	})
	require.ErrorContains(t, err, "no RBACTimelock deployed")
	require.Equal(t, []string{"stellar-deploy-rmn-remote"}, reportIDs(t, reporter))
}

func TestDeployTokenAdminRegistry_AdoptsMatchingWasmAndInitializes(t *testing.T) {
	t.Parallel()
	wasmPath, _, _, _, deps := componentTestSetup(t, fakeLedger{})
	id := predictedID(t, "token-admin-registry")
	ledger := fakeLedger{instances: map[string]xdr.Hash{id: localWasmHash(t, wasmPath)}}
	wasmPath, b, _, reporter, deps := componentTestSetup(t, ledger)
	out, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.NoError(t, err)
	require.False(t, out.Deployed)
	require.True(t, out.Initialized)
	require.Equal(t, id, out.ContractID)
	require.Len(t, out.Refs, 1)
	require.Equal(t, []string{"token-admin-registry:initialize", "stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
}

func TestDeployTokenAdminRegistry_WasmMismatchErrors(t *testing.T) {
	t.Parallel()
	wasmPath, _, _, _, deps := componentTestSetup(t, fakeLedger{})
	id := predictedID(t, "token-admin-registry")
	ledger := fakeLedger{instances: map[string]xdr.Hash{id: {0x01, 0x02}}}
	wasmPath, b, _, reporter, deps := componentTestSetup(t, ledger)
	_, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "runs wasm")
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
}

func TestDeployTokenAdminRegistry_AdoptedForeignOwnerErrors(t *testing.T) {
	t.Parallel()
	id := predictedID(t, "token-admin-registry")
	wasmPath, _, _, _, _ := componentTestSetup(t, fakeLedger{})
	ledger := fakeLedger{instances: map[string]xdr.Hash{id: localWasmHash(t, wasmPath)}}
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, ledger)
	foreignOwner := scval.AddressToScVal(foreignOwnerAddress())
	inv.WithSimulateResultForFn("owner", &foreignOwner)
	out, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "was initialized by")
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
	require.Empty(t, out.ContractID)
}

func TestDeployTokenAdminRegistry_AdoptedOwnedByConfiguredOwnerSkipsInit(t *testing.T) {
	t.Parallel()
	id := predictedID(t, "token-admin-registry")
	wasmPath, _, _, _, _ := componentTestSetup(t, fakeLedger{})
	ledger := fakeLedger{instances: map[string]xdr.Hash{id: localWasmHash(t, wasmPath)}}
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, ledger)
	configuredOwner := scval.AddressToScVal(deps.DeployerAddress)
	inv.WithSimulateResultForFn("owner", &configuredOwner)
	out, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.NoError(t, err)
	require.False(t, out.Deployed)
	require.False(t, out.Initialized)
	require.Equal(t, []string{"stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
}

// simulateErrorInvoker fails every SimulateContract, as a stale ref would.
type simulateErrorInvoker struct {
	*operationstest.RecordingInvoker
}

func (simulateErrorInvoker) SimulateContract(context.Context, string, string, []xdr.ScVal) (*xdr.ScVal, error) {
	return nil, fmt.Errorf("simulate failed")
}

// bogusDeployer returns a valid but wrong contract ID, as a real deploy would
// if ComponentDeps did not match the signing key.
type bogusDeployer struct{ id string }

func (d bogusDeployer) DeployContract(context.Context, string, [32]byte) (string, error) {
	return d.id, nil
}

func TestDeployTokenAdminRegistry_DeployedIDMismatchErrors(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	deps.Deploy = bogusDeployer{id: predictedID(t, "not-the-tar-label")}
	_, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "the salt predicted")
	require.Equal(t, []string{"token-admin-registry:deploy", "stellar-deploy-token-admin-registry"}, reportIDs(t, reporter))
}

func TestDeployExecutor_WritesProxyRefWhenMissing(t *testing.T) {
	t.Parallel()
	// An executor ref recorded by hand but no proxy row: nothing deploys or
	// initializes, yet the missing proxy row is written.
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, fakeLedger{})
	id := predictedID(t, "executor")
	execHex, err := stellarutil.StrkeyToHex(id)
	require.NoError(t, err)
	configuredOwner := scval.AddressToScVal(deps.DeployerAddress)
	inv.WithSimulateResultForFn("owner", &configuredOwner)
	execRef := stellarccip.DefaultExecutorDatastoreRef().FullAddressRef(1, execHex)
	out, err := execComponentSequence(b, deps, DeployExecutor, DeployExecutorInput{
		ChainSelector: 1, WasmPath: wasmPath, ExistingAddresses: []datastore.AddressRef{execRef},
	})
	require.NoError(t, err)
	require.False(t, out.Deployed)
	require.False(t, out.Initialized)
	require.Equal(t, id, out.ContractID)
	require.Len(t, out.Refs, 1)
	require.Equal(t, stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier).Type, out.Refs[0].Type)
	require.Equal(t, []string{"stellar-deploy-executor"}, reportIDs(t, reporter))
}

func TestDeployExecutor_RewritesStaleProxyRef(t *testing.T) {
	t.Parallel()
	// A proxy row pointing at an old executor is realigned with the executor the
	// sequence resolved, so the OnRamp's default-executor lookup stays correct.
	wasmPath, b, inv, reporter, deps := componentTestSetup(t, fakeLedger{})
	id := predictedID(t, "executor")
	oldExecutor := predictedID(t, "old-executor")
	execHex, err := stellarutil.StrkeyToHex(id)
	require.NoError(t, err)
	configuredOwner := scval.AddressToScVal(deps.DeployerAddress)
	inv.WithSimulateResultForFn("owner", &configuredOwner)
	execRef := stellarccip.DefaultExecutorDatastoreRef().FullAddressRef(1, execHex)
	oldHex, err := stellarutil.StrkeyToHex(oldExecutor)
	require.NoError(t, err)
	staleProxyRef := stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier).FullAddressRef(1, oldHex)
	out, err := execComponentSequence(b, deps, DeployExecutor, DeployExecutorInput{
		ChainSelector: 1, WasmPath: wasmPath, ExistingAddresses: []datastore.AddressRef{execRef, staleProxyRef},
	})
	require.NoError(t, err)
	require.Equal(t, id, out.ContractID)
	require.Len(t, out.Refs, 1)
	require.Equal(t, stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier).Type, out.Refs[0].Type)
	require.Equal(t, execHex, out.Refs[0].Address)
	require.Equal(t, []string{"stellar-deploy-executor"}, reportIDs(t, reporter))
}

// opInput returns the recorded input of the given op.
func opInput[IN any](t *testing.T, reporter *cldf_ops.MemoryReporter, opID string) IN {
	t.Helper()
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	for _, r := range reports {
		if r.Def.ID == opID {
			in, ok := r.Input.(IN)
			require.True(t, ok, "op %s input has unexpected type", opID)
			return in
		}
	}
	t.Fatalf("op %s not recorded", opID)
	var zero IN
	return zero
}

func TestComponentParams_FlowIntoInitializeInputs(t *testing.T) {
	t.Parallel()
	// L3 changesets will call the sequences with explicit params; the values
	// must reach the initialize op inputs verbatim.
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	owner := foreignOwnerAddress()
	feeAgg := predictedID(t, "fee-agg")

	_, err := execComponentSequence(b, deps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector: 1, WasmPath: wasmPath, Owner: owner,
	})
	require.NoError(t, err)
	require.Equal(t, owner, opInput[tarops.InitializeInput](t, reporter, "token-admin-registry:initialize").Owner)

	_, err = execComponentSequence(b, deps, DeployCommitteeVerifier, DeployCommitteeVerifierInput{
		ChainSelector: 1, WasmPath: wasmPath, Owner: owner, AllowlistAdmin: feeAgg, FeeAggregator: owner,
		StorageLocations: [][]byte{{0xAA}}, RmnProxy: predictedID(t, "rmn-proxy"),
	})
	require.NoError(t, err)
	cvIn := opInput[cvops.InitializeInput](t, reporter, "committee-verifier:initialize")
	require.Equal(t, owner, cvIn.Owner)
	require.Equal(t, feeAgg, *cvIn.DynamicConfig.AllowlistAdmin)
	require.Equal(t, owner, *cvIn.DynamicConfig.FeeAggregator)

	_, err = execComponentSequence(b, deps, DeployFeeQuoter, DeployFeeQuoterInput{
		ChainSelector: 1, WasmPath: wasmPath, Owner: owner, FeeToken: predictedID(t, "fee-token"),
		MaxFeeJuelsPerMsg: big.NewInt(42), AuthorizedCallers: []string{feeAgg},
	})
	require.NoError(t, err)
	fqIn := opInput[fqops.InitializeInput](t, reporter, "fee-quoter:initialize")
	require.Equal(t, owner, fqIn.Owner)
	require.Equal(t, int64(42), fqIn.StaticConfig.MaxFeeJuelsPerMsg.Int64())
	require.Equal(t, []string{feeAgg}, fqIn.AuthorizedCallers)
}

func TestDeployOnRamp_RequiresDependencies(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	dep := predictedID(t, "some-dependency")
	for name, in := range map[string]DeployOnRampInput{
		"tokenAdminRegistry": {ChainSelector: 1, WasmPath: wasmPath, RmnProxy: dep, FeeQuoter: dep},
		"rmnProxy":           {ChainSelector: 1, WasmPath: wasmPath, TokenAdminRegistry: dep, FeeQuoter: dep},
		"feeQuoter":          {ChainSelector: 1, WasmPath: wasmPath, TokenAdminRegistry: dep, RmnProxy: dep},
	} {
		_, err := execComponentSequence(b, deps, DeployOnRamp, in)
		require.ErrorContains(t, err, name+" is required", name)
	}
	requireOnlyFailedSequenceReports(t, reporter, "stellar-deploy-onramp")
}

func TestDeployOnRamp_FreshDeploysAndInitializes(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	tar, rmnProxy, fq := predictedID(t, "token-admin-registry"), predictedID(t, "rmn-proxy"), predictedID(t, "fee-quoter")
	out, err := execComponentSequence(b, deps, DeployOnRamp, DeployOnRampInput{
		ChainSelector: 1, WasmPath: wasmPath,
		TokenAdminRegistry: tar, RmnProxy: rmnProxy, FeeQuoter: fq,
	})
	require.NoError(t, err)
	require.True(t, out.Deployed)
	require.True(t, out.Initialized)
	require.Equal(t, []string{"onramp:deploy", "onramp:initialize", "stellar-deploy-onramp"}, reportIDs(t, reporter))
}

func TestDeployOffRamp_RequiresDependencies(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	dep := predictedID(t, "some-dependency")
	_, err := execComponentSequence(b, deps, DeployOffRamp, DeployOffRampInput{
		ChainSelector: 1, WasmPath: wasmPath, TokenAdminRegistry: dep,
	})
	require.ErrorContains(t, err, "rmnProxy is required")
	_, err = execComponentSequence(b, deps, DeployOffRamp, DeployOffRampInput{
		ChainSelector: 1, WasmPath: wasmPath, RmnProxy: dep,
	})
	require.ErrorContains(t, err, "tokenAdminRegistry is required")
	requireOnlyFailedSequenceReports(t, reporter, "stellar-deploy-offramp")
}

func TestDeployRouter_RequiresRmnProxy(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	_, err := execComponentSequence(b, deps, DeployRouter, DeployRouterInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "rmnProxy is required")
	requireOnlyFailedSequenceReports(t, reporter, "stellar-deploy-router")
}

func TestDeployCommitteeVerifier_RequiresStorageLocationsAndRmnProxy(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	rmnProxy := predictedID(t, "rmn-proxy")
	_, err := execComponentSequence(b, deps, DeployCommitteeVerifier, DeployCommitteeVerifierInput{
		ChainSelector: 1, WasmPath: wasmPath, RmnProxy: rmnProxy,
	})
	require.ErrorContains(t, err, "storageLocations is required")
	_, err = execComponentSequence(b, deps, DeployCommitteeVerifier, DeployCommitteeVerifierInput{
		ChainSelector: 1, WasmPath: wasmPath, StorageLocations: [][]byte{{0x01}},
	})
	require.ErrorContains(t, err, "rmnProxy is required")
	requireOnlyFailedSequenceReports(t, reporter, "stellar-deploy-committee-verifier")
}

func TestDeployCCIPReceiver_RequiresRouter(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	_, err := execComponentSequence(b, deps, DeployCCIPReceiver, DeployCCIPReceiverInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "router is required")
	requireOnlyFailedSequenceReports(t, reporter, "stellar-deploy-ccip-receiver")
}

func TestDeployFeeQuoter_RequiresFeeToken(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	_, err := execComponentSequence(b, deps, DeployFeeQuoter, DeployFeeQuoterInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "feeToken is required")
	require.Equal(t, []string{"stellar-deploy-fee-quoter"}, reportIDs(t, reporter))
}

func TestDeployRMNProxy_RequiresRmnRemote(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	_, err := execComponentSequence(b, deps, DeployRMNProxy, DeployRMNProxyInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.ErrorContains(t, err, "rmnRemote is required")
	require.Equal(t, []string{"stellar-deploy-rmn-proxy"}, reportIDs(t, reporter))
}

func TestDeployExecutor_WritesExecutorAndProxyRefs(t *testing.T) {
	t.Parallel()
	wasmPath, b, _, reporter, deps := componentTestSetup(t, fakeLedger{})
	out, err := execComponentSequence(b, deps, DeployExecutor, DeployExecutorInput{
		ChainSelector: 1, WasmPath: wasmPath,
	})
	require.NoError(t, err)
	require.True(t, out.Deployed)
	require.Len(t, out.Refs, 2)
	require.Equal(t, stellarccip.DefaultExecutorDatastoreRef().Type, out.Refs[0].Type)
	require.Equal(t, stellarccip.ExecutorProxyDatastoreRef(stellarccip.DefaultExecutorQualifier).Type, out.Refs[1].Type)
	require.Equal(t, out.Refs[0].Address, out.Refs[1].Address)
	require.Equal(t, []string{"executor:deploy", "executor:initialize", "stellar-deploy-executor"}, reportIDs(t, reporter))
}
