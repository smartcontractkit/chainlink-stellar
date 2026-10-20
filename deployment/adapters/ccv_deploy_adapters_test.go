package adapters

import (
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/require"

	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
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
