package changesets

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	focr "github.com/smartcontractkit/chainlink-deployments-framework/offchain/ocr"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	chainsel "github.com/smartcontractkit/chain-selectors"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// testEnvironment builds a minimal CLDF environment: a Stellar chain for
// chainSelector when withChain is set, and a sealed memory datastore holding
// refs. Only BlockChains and DataStore are populated — the changesets touch
// nothing else.
func testEnvironment(t *testing.T, chainSelector uint64, withChain bool, refs ...datastore.AddressRef) cldf.Environment {
	t.Helper()
	var chains []cldfchain.BlockChain
	if withChain {
		chains = append(chains, &cldfstellar.Chain{ChainMetadata: cldfstellar.ChainMetadata{Selector: chainSelector}})
	}
	ms := datastore.NewMemoryDataStore()
	for _, r := range refs {
		require.NoError(t, ms.AddressRefStore.Upsert(r))
	}
	env := cldf.NewEnvironment(
		"changesets-unit",
		cldflogger.Nop(),
		nil,
		ms.Seal(),
		nil,
		nil,
		func() context.Context { return context.Background() },
		focr.XXXGenerateTestOCRSecrets(),
		cldfchain.NewBlockChainsFromSlice(chains),
	)
	return *env
}

// testRef seeds a datastore row for ref at chainSelector with a valid hex
// contract address.
func testRef(ref stellarccip.DatastoreSorobanContractRef, chainSelector uint64) datastore.AddressRef {
	return ref.FullAddressRef(chainSelector, "0x"+strings.Repeat("ab", 32))
}

func TestVerifyPreconditions_MissingChain(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := testEnvironment(t, sel, false)

	err := DeployOnRamp{}.VerifyPreconditions(env, DeployOnRampConfig{ChainSelector: sel})
	require.ErrorContains(t, err, "stellar chain")
}

func TestVerifyPreconditions_MissingDependency(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := testEnvironment(t, sel, true)

	for _, tt := range []struct {
		name     string
		verify   func(cldf.Environment) error
		contains string
	}{
		{"rmn proxy without rmn remote", func(e cldf.Environment) error {
			return DeployRMNProxy{}.VerifyPreconditions(e, DeployRMNProxyConfig{ChainSelector: sel})
		}, "deploy RMN Remote first"},
		{"onramp without tar", func(e cldf.Environment) error {
			return DeployOnRamp{}.VerifyPreconditions(e, DeployOnRampConfig{ChainSelector: sel})
		}, "deploy TokenAdminRegistry first"},
		{"offramp without rmn proxy", func(e cldf.Environment) error {
			return DeployOffRamp{}.VerifyPreconditions(e, DeployOffRampConfig{ChainSelector: sel})
		}, "deploy RMN Proxy first"},
		{"router without rmn proxy", func(e cldf.Environment) error {
			return DeployRouter{}.VerifyPreconditions(e, DeployRouterConfig{ChainSelector: sel})
		}, "deploy RMN Proxy first"},
		{"committee verifier without rmn proxy", func(e cldf.Environment) error {
			return DeployCommitteeVerifier{}.VerifyPreconditions(e, DeployCommitteeVerifierConfig{
				ChainSelector:    sel,
				StorageLocations: [][]byte{{1}},
			})
		}, "deploy RMN Proxy first"},
		{"receiver without router", func(e cldf.Environment) error {
			return DeployCCIPReceiver{}.VerifyPreconditions(e, DeployCCIPReceiverConfig{ChainSelector: sel})
		}, "deploy Router first"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.verify(env)
			require.ErrorContains(t, err, tt.contains)
		})
	}
}

func TestVerifyPreconditions_DependenciesPresent(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := testEnvironment(t, sel, true,
		testRef(stellarccip.RMNRemoteDatastoreRef(), sel),
		testRef(stellarccip.TokenAdminRegistryDatastoreRef(), sel),
		testRef(stellarccip.RMNProxyDatastoreRef(), sel),
		testRef(stellarccip.FeeQuoterDatastoreRef(), sel),
		testRef(stellarccip.RouterDatastoreRef(), sel),
	)

	require.NoError(t, DeployRMNProxy{}.VerifyPreconditions(env, DeployRMNProxyConfig{ChainSelector: sel}))
	require.NoError(t, DeployOnRamp{}.VerifyPreconditions(env, DeployOnRampConfig{ChainSelector: sel}))
	require.NoError(t, DeployOffRamp{}.VerifyPreconditions(env, DeployOffRampConfig{ChainSelector: sel}))
	require.NoError(t, DeployRouter{}.VerifyPreconditions(env, DeployRouterConfig{ChainSelector: sel}))
	require.NoError(t, DeployCommitteeVerifier{}.VerifyPreconditions(env, DeployCommitteeVerifierConfig{
		ChainSelector:    sel,
		StorageLocations: [][]byte{{1}},
	}))
	require.NoError(t, DeployCCIPReceiver{}.VerifyPreconditions(env, DeployCCIPReceiverConfig{ChainSelector: sel}))
}

func TestVerifyPreconditions_DeployOnlyComponents(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := testEnvironment(t, sel, true)

	// The tier-0/1 components without datastore dependencies validate on an
	// empty datastore (FeeQuoter needs its config params instead).
	require.NoError(t, DeployRMNRemote{}.VerifyPreconditions(env, DeployRMNRemoteConfig{ChainSelector: sel}))
	require.NoError(t, DeployTokenAdminRegistry{}.VerifyPreconditions(env, DeployTokenAdminRegistryConfig{ChainSelector: sel}))
	require.NoError(t, DeployRampRegistry{}.VerifyPreconditions(env, DeployRampRegistryConfig{ChainSelector: sel}))
	require.NoError(t, DeployVVR{}.VerifyPreconditions(env, DeployVVRConfig{ChainSelector: sel}))
	require.NoError(t, DeployExecutor{}.VerifyPreconditions(env, DeployExecutorConfig{ChainSelector: sel}))
}

func TestVerifyPreconditions_FeeQuoterParams(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := testEnvironment(t, sel, true)

	err := DeployFeeQuoter{}.VerifyPreconditions(env, DeployFeeQuoterConfig{ChainSelector: sel})
	require.ErrorContains(t, err, "feeToken is required")

	err = DeployFeeQuoter{}.VerifyPreconditions(env, DeployFeeQuoterConfig{
		ChainSelector:     sel,
		FeeToken:          "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		MaxFeeJuelsPerMsg: big.NewInt(-1),
	})
	require.ErrorContains(t, err, "maxFeeJuelsPerMsg must not be negative")

	require.NoError(t, DeployFeeQuoter{}.VerifyPreconditions(env, DeployFeeQuoterConfig{
		ChainSelector: sel,
		FeeToken:      "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
	}))
}

func TestVerifyPreconditions_CommitteeVerifierParams(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	env := testEnvironment(t, sel, true, testRef(stellarccip.RMNProxyDatastoreRef(), sel))

	err := DeployCommitteeVerifier{}.VerifyPreconditions(env, DeployCommitteeVerifierConfig{ChainSelector: sel})
	require.ErrorContains(t, err, "storageLocations is required")
}

func TestResolveComponentWasmPath(t *testing.T) {
	got, err := resolveComponentWasmPath("/tmp/custom.wasm", "onramp.wasm")
	require.NoError(t, err)
	require.Equal(t, "/tmp/custom.wasm", got)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/smartcontractkit/chainlink-stellar\n"), 0o644))
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)

	got, err = resolveComponentWasmPath("", "onramp.wasm")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "target", "wasm32v1-none", "release", "onramp.wasm"), got)
}
