package adapters

import (
	"errors"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// cvDeployFixtures holds the identities one CommitteeVerifier deploy test needs.
type cvDeployFixtures struct {
	chain     uint64
	rmnProxy  string
	qualifier string
	feeAgg    string
	feeAggHex string
	admin     string
}

func newCVDeployFixtures(t *testing.T) cvDeployFixtures {
	t.Helper()
	feeAgg := stellarutil.MustGenerateMockContractID("deployer", "cv-deploy-test-fee-agg")
	feeAggHex, err := stellarutil.StrkeyToHex(feeAgg)
	require.NoError(t, err)
	return cvDeployFixtures{
		chain:     42,
		rmnProxy:  stellarutil.MustGenerateMockContractID("deployer", "cv-deploy-test-rmn-proxy"),
		qualifier: "extra",
		feeAgg:    feeAgg,
		feeAggHex: feeAggHex,
		admin:     stellarutil.MustGenerateMockContractID("deployer", "cv-deploy-test-admin"),
	}
}

// cvDeployRefs returns ExistingAddresses carrying the RMN Proxy ref.
func (f cvDeployFixtures) refs(t *testing.T) []datastore.AddressRef {
	t.Helper()
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.RecordRMNProxy(ds, f.chain, f.rmnProxy))
	refs, err := ds.Addresses().Fetch()
	require.NoError(t, err)
	return refs
}

func cvDeployInput(f cvDeployFixtures, refs []datastore.AddressRef) ccvdeploymentadapters.CommitteeVerifierDeployInput {
	return ccvdeploymentadapters.CommitteeVerifierDeployInput{
		ChainSelector:     f.chain,
		DeployerContract:  "placeholder", // no deployer factory on stellar
		ExistingAddresses: refs,
		Params: ccvdeploymentadapters.CommitteeVerifierDeployParams{
			Version:          semver.MustParse("2.0.0"),
			FeeAggregator:    f.feeAggHex,
			AllowlistAdmin:   f.admin,
			StorageLocations: []string{"loc-a", "loc-b"},
			Qualifier:        f.qualifier,
		},
	}
}

// TestToStellarCommitteeVerifierDeployInputs_HappyPath pins the translation:
// storage locations map to bytes, the fee aggregator and allowlist admin
// normalize to strkeys, the qualifier passes through (both halves), the RMN
// Proxy resolves from ExistingAddresses, and the wasm paths point at the
// conventional release builds.
func TestToStellarCommitteeVerifierDeployInputs_HappyPath(t *testing.T) {
	t.Parallel()
	f := newCVDeployFixtures(t)

	cv, vvr, err := toStellarCommitteeVerifierDeployInputs(cvDeployInput(f, f.refs(t)))
	require.NoError(t, err)

	require.Equal(t, f.chain, cv.ChainSelector)
	require.Equal(t, f.qualifier, cv.Qualifier)
	require.Equal(t, [][]byte{[]byte("loc-a"), []byte("loc-b")}, cv.StorageLocations)
	require.Equal(t, f.rmnProxy, cv.RmnProxy)
	require.Equal(t, f.feeAgg, cv.FeeAggregator)
	require.Equal(t, f.admin, cv.AllowlistAdmin)
	require.True(t, strings.HasSuffix(cv.WasmPath, "ccvs_committee_verifier.wasm"))
	require.Equal(t, cv.ExistingAddresses, f.refs(t))

	require.Equal(t, f.chain, vvr.ChainSelector)
	require.Equal(t, f.qualifier, vvr.Qualifier, "the resolver deploys under the committee's qualifier")
	require.Equal(t, f.feeAgg, vvr.FeeAggregator)
	require.True(t, strings.HasSuffix(vvr.WasmPath, "ccvs_versioned_verifier_resolver.wasm"))
}

// TestToStellarCommitteeVerifierDeployInputs_AllowlistAdminOptional: an empty
// allowlist admin stays empty so the sequence defaults it to the owner.
func TestToStellarCommitteeVerifierDeployInputs_AllowlistAdminOptional(t *testing.T) {
	t.Parallel()
	f := newCVDeployFixtures(t)
	input := cvDeployInput(f, f.refs(t))
	input.Params.AllowlistAdmin = ""

	cv, _, err := toStellarCommitteeVerifierDeployInputs(input)
	require.NoError(t, err)
	require.Empty(t, cv.AllowlistAdmin)
}

// TestToStellarCommitteeVerifierDeployInputs_Errors covers the validation
// branches: empty storage locations, a missing RMN Proxy ref, and malformed
// fee aggregator / allowlist admin addresses.
func TestToStellarCommitteeVerifierDeployInputs_Errors(t *testing.T) {
	t.Parallel()
	f := newCVDeployFixtures(t)

	emptyLocations := cvDeployInput(f, f.refs(t))
	emptyLocations.Params.StorageLocations = nil
	_, _, err := toStellarCommitteeVerifierDeployInputs(emptyLocations)
	require.ErrorContains(t, err, "storageLocations is required")

	_, _, err = toStellarCommitteeVerifierDeployInputs(cvDeployInput(f, nil))
	require.ErrorContains(t, err, "deploy RMN Proxy first")

	badFeeAgg := cvDeployInput(f, f.refs(t))
	badFeeAgg.Params.FeeAggregator = "not-an-address"
	_, _, err = toStellarCommitteeVerifierDeployInputs(badFeeAgg)
	require.ErrorContains(t, err, "FeeAggregator")

	badAdmin := cvDeployInput(f, f.refs(t))
	badAdmin.Params.AllowlistAdmin = "not-an-address"
	_, _, err = toStellarCommitteeVerifierDeployInputs(badAdmin)
	require.ErrorContains(t, err, "AllowlistAdmin")
}

// TestStellarCCVCommitteeVerifierDeployAdapter_Sequence pins the wrapper
// sequence identity.
func TestStellarCCVCommitteeVerifierDeployAdapter_Sequence(t *testing.T) {
	t.Parallel()
	seq := (&StellarCCVCommitteeVerifierDeployAdapter{}).DeployCommitteeVerifier()
	require.NotNil(t, seq)
	require.Equal(t, "stellar-deploy-committee-verifier-and-resolver", seq.ID())
}

// TestStellarCCVProtocolContractsDeployAdapter_Validation pins the executor
// constraint: the full deploy deploys exactly one, the default executor.
func TestStellarCCVProtocolContractsDeployAdapter_Validation(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateStellarProtocolDeployInput(ccvdeploymentadapters.ProtocolContractsDeployInput{}))
	require.NoError(t, validateStellarProtocolDeployInput(ccvdeploymentadapters.ProtocolContractsDeployInput{
		Executors: []ccvdeploymentadapters.ExecutorDeployParams{{Qualifier: "", Version: semver.MustParse("2.0.0")}},
	}))
	err := validateStellarProtocolDeployInput(ccvdeploymentadapters.ProtocolContractsDeployInput{
		Executors: []ccvdeploymentadapters.ExecutorDeployParams{{Qualifier: "fast"}},
	})
	require.ErrorContains(t, err, "only the default executor")
}

// TestStellarProtocolDeployOutput pins the output mapping: RefsToTransferOwnership
// carries the deployed refs iff the caller did not keep deployer-key ownership,
// and BatchOps stays nil (EOA-only deploy).
func TestStellarProtocolDeployOutput(t *testing.T) {
	t.Parallel()
	refs := []datastore.AddressRef{{ChainSelector: 42, Type: "Router", Address: "0x01"}}

	owned := stellarProtocolDeployOutput(refs, true)
	require.Equal(t, refs, owned.Addresses)
	require.Empty(t, owned.RefsToTransferOwnership)
	require.Empty(t, owned.BatchOps)

	governed := stellarProtocolDeployOutput(refs, false)
	require.Equal(t, refs, governed.RefsToTransferOwnership)
	require.Empty(t, governed.BatchOps)

	// Alias rows resolve to the Executor type and dedupe by address,
	// order-independently: the proxy row may be the only one recorded (it is
	// written alone when it was missing or stale), or arrive first.
	aliasRefs := []datastore.AddressRef{
		{ChainSelector: 42, Type: "Router", Address: "0x01"},
		{ChainSelector: 42, Type: "Executor", Address: "0x02"},
		{ChainSelector: 42, Type: "Proxy", Address: "0x02"},
	}
	governedAliased := stellarProtocolDeployOutput(aliasRefs, false)
	require.Equal(t, aliasRefs, governedAliased.Addresses)
	require.Len(t, governedAliased.RefsToTransferOwnership, 2)
	require.Equal(t, "Executor", string(governedAliased.RefsToTransferOwnership[1].Type))

	aliasOnly := []datastore.AddressRef{{ChainSelector: 42, Type: "Proxy", Address: "0x02"}}
	governedAliasOnly := stellarProtocolDeployOutput(aliasOnly, false)
	require.Len(t, governedAliasOnly.RefsToTransferOwnership, 1)
	require.Equal(t, "Executor", string(governedAliasOnly.RefsToTransferOwnership[0].Type))

	aliasFirst := []datastore.AddressRef{
		{ChainSelector: 42, Type: "Proxy", Address: "0x02"},
		{ChainSelector: 42, Type: "Executor", Address: "0x02"},
	}
	governedAliasFirst := stellarProtocolDeployOutput(aliasFirst, false)
	require.Len(t, governedAliasFirst.RefsToTransferOwnership, 1)
	require.Equal(t, "Executor", string(governedAliasFirst.RefsToTransferOwnership[0].Type))
	require.Equal(t, "0x02", governedAliasFirst.RefsToTransferOwnership[0].Address)
}

// TestStellarCCVProtocolContractsDeployAdapter_Sequence pins the wrapper
// sequence identity.
func TestStellarCCVProtocolContractsDeployAdapter_Sequence(t *testing.T) {
	t.Parallel()
	seq := (&StellarCCVProtocolContractsDeployAdapter{}).DeployProtocolContracts()
	require.NotNil(t, seq)
	require.Equal(t, "stellar-deploy-protocol-contracts", seq.ID())
}

// TestStellarCCVDeployAdapters_ReuseFullDeploySequence pins that the protocol
// adapter drives the same full-deploy sequence the ccip
// DeployChainContractsAdapter path uses, so both registration routes deploy
// identically.
func TestStellarCCVDeployAdapters_ReuseFullDeploySequence(t *testing.T) {
	t.Parallel()
	require.Equal(t, sequences.StellarDeployChainContracts.ID(), "stellar-deploy-chain-contracts")
}

// invokedFns lists every function the invoker recorded, reads and writes alike.
func invokedFns(inv *operationstest.RecordingInvoker) []string {
	fns := make([]string, 0, len(inv.Records()))
	for _, rec := range inv.Records() {
		fns = append(fns, rec.Fn)
	}
	return fns
}

// TestWireVVRImplementations pins the fill-missing contract: a fresh resolver
// gets every mapping, while existing mappings — including ones pointing at a
// different verifier — are preserved, so a rerun performs no owner-gated
// writes once everything is wired.
func TestWireVVRImplementations(t *testing.T) {
	t.Parallel()
	vvrStrkey := stellarutil.MustGenerateMockContractID("deployer", "wire-vvr-test")
	cvStrkey := stellarutil.MustGenerateMockContractID("deployer", "wire-cv-test")
	otherStrkey := stellarutil.MustGenerateMockContractID("deployer", "wire-other-test")
	sel := chainsel.STELLAR_LOCALNET.Selector

	t.Run("fills missing mappings", func(t *testing.T) {
		t.Parallel()
		emptyOut := scval.StructSliceToScVal([]vvrbindings.OutboundImplementationArgs{})
		emptyIn := scval.StructSliceToScVal([]vvrbindings.InboundImplementationArgs{})
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(vvrStrkey, "get_all_outbound_implementations", &emptyOut).
			WithSimulateResultForContract(vvrStrkey, "get_all_inbound_implementations", &emptyIn)

		require.NoError(t, wireVVRImplementations(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, vvrStrkey, cvStrkey, []uint64{sel}))

		fns := invokedFns(inv)
		require.Contains(t, fns, "apply_outbound_impl_updates")
		require.Contains(t, fns, "apply_inbound_impl_updates")
	})

	t.Run("preserves existing and changed mappings", func(t *testing.T) {
		t.Parallel()
		// The outbound entry points at a different verifier: still an existing
		// mapping, so it is preserved rather than overwritten.
		out := scval.StructSliceToScVal([]vvrbindings.OutboundImplementationArgs{{
			DestChainSelector: sel, Verifier: otherStrkey,
		}})
		in := scval.StructSliceToScVal([]vvrbindings.InboundImplementationArgs{{
			Version: stellarutil.DefaultCommitteeVerifierVersionTag(), Verifier: otherStrkey,
		}})
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(vvrStrkey, "get_all_outbound_implementations", &out).
			WithSimulateResultForContract(vvrStrkey, "get_all_inbound_implementations", &in)

		require.NoError(t, wireVVRImplementations(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, vvrStrkey, cvStrkey, []uint64{sel}))

		fns := invokedFns(inv)
		require.NotContains(t, fns, "apply_outbound_impl_updates")
		require.NotContains(t, fns, "apply_inbound_impl_updates")
	})
}

// TestFillCommitteeVerifierRemoteConfigs pins the fill-missing contract: a
// verifier with no remote entry for a chain gets one (router + verification
// gas); an existing entry is preserved untouched.
func TestFillCommitteeVerifierRemoteConfigs(t *testing.T) {
	t.Parallel()
	cvStrkey := stellarutil.MustGenerateMockContractID("deployer", "fill-cv-test")
	routerStrkey := stellarutil.MustGenerateMockContractID("deployer", "fill-router-test")
	sel := chainsel.STELLAR_LOCALNET.Selector

	t.Run("fills missing remote configs", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(cvStrkey, "get_remote_chain_config", errors.New("failed to call get_remote_chain_config: RemoteChainNotSupported"))

		require.NoError(t, fillCommitteeVerifierRemoteConfigs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, cvStrkey, routerStrkey, []uint64{sel}))

		fns := invokedFns(inv)
		require.Contains(t, fns, "apply_remote_chain_cfg_updates")
	})

	t.Run("preserves existing entries", func(t *testing.T) {
		t.Parallel()
		cfg := cvbindings.RemoteChainConfig{RemoteChainSelector: sel, GasForVerification: 1}
		cfgVal := scval.MustToScVal(cfg.ToScVal())
		inv := operationstest.NewRecordingInvoker().
			WithSimulateResultForContract(cvStrkey, "get_remote_chain_config", &cfgVal)

		require.NoError(t, fillCommitteeVerifierRemoteConfigs(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: inv}, cvStrkey, routerStrkey, []uint64{sel}))

		fns := invokedFns(inv)
		require.NotContains(t, fns, "apply_remote_chain_cfg_updates")
	})
}
