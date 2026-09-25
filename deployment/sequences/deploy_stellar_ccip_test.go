package sequences

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestStellarDeployChainContractsSequenceID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-deploy-chain-contracts", StellarDeployChainContracts.ID())
}

func newTestBundle(t *testing.T) cldf_ops.Bundle {
	t.Helper()
	return cldf_ops.NewBundle(
		func() context.Context { return t.Context() },
		cldflogger.Nop(),
		cldf_ops.NewMemoryReporter(),
	)
}

type stubDeployer struct{}

func (stubDeployer) DeployContract(ctx context.Context, wasmPath string, salt [32]byte) (string, error) {
	_ = ctx
	_ = wasmPath
	_ = salt
	return "", nil
}

type stubInvoker struct{}

func (stubInvoker) InvokeContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	_ = contractID
	_ = functionName
	_ = args
	return nil, nil
}

func (stubInvoker) SimulateContract(ctx context.Context, contractID string, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	_ = ctx
	_ = contractID
	_ = functionName
	_ = args
	return nil, nil
}

func (stubInvoker) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	_ = ctx
	_ = contractID
	_ = startLedger
	_ = topics
	return nil, nil
}

func TestStellarDeployChainContracts_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	_, err := cldf_ops.ExecuteSequence(b, StellarDeployChainContracts, chains, ccvadapters.DeployChainContractsInput{
		ChainSelector: sel,
	})
	require.Error(t, err)
}

func TestStellarDeployChainContracts_RejectsNilChainSigner(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	// Unique selector so parallel tests do not collide on BlockChains maps.
	sel := uint64(424242420001)
	// Stellar chain entry without Signer: NewDeployerFromChain fails before any deploy.
	st := cldf_stellar.Chain{ChainMetadata: cldf_stellar.ChainMetadata{Selector: sel}}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: st})

	_, err := cldf_ops.ExecuteSequence(b, StellarDeployChainContracts, chains, ccvadapters.DeployChainContractsInput{
		ChainSelector: sel,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Signer is nil")
}

func TestRunStellarCCIPFullDeployForCCV_ErrorsWhenTopologyNil(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	ctx := context.Background()
	_, err := RunStellarCCIPFullDeployForCCV(ctx, b, stellardeps.StellarDeps{}, nil, []uint64{1}, 1, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "EnvironmentTopology is nil")
}

func TestStellarDeployChainContracts_RunsWithoutStashedTopology(t *testing.T) {
	// A missing stash entry must not fail the sequence: signer quorums are applied at
	// lane-configuration time, so the deploy runs on and stops at the first real
	// prerequisite — the release WASMs under the resolved chainlink-stellar root.
	// Not parallel: t.Setenv.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/smartcontractkit/chainlink-stellar\n\ngo 1.24\n"), 0o644))
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)

	b := newTestBundle(t)
	sel := uint64(424242420098)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	_, err := cldf_ops.ExecuteSequence(b, StellarDeployChainContracts, chains, ccvadapters.DeployChainContractsInput{
		ChainSelector: sel,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "OnRamp WASM not found at "+filepath.Join(root, "target", "wasm32v1-none", "release", "onramp.wasm"))
	require.NotContains(t, err.Error(), "topology")
}

func TestRunStellarCCIPFullDeploy_ErrorsWhenCCIPDevenvHostNil(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	deps := stellardeps.StellarDeps{
		Deploy:  stubDeployer{},
		Invoker: stubInvoker{},
	}
	_, err := RunStellarCCIPFullDeploy(b.GetContext(), b, deps, nil, nil, DeployStellarCCIPInnerInput{
		ChainSelector: sel,
		AllSelectors:  []uint64{sel, 123},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "CCIPDevenvHost")
}

func TestDeployStellarCCIPInnerInput_ZeroValuePreservesDefaultBehavior(t *testing.T) {
	t.Parallel()
	// The governance fields are additive: a zero-value input must keep the
	// pre-fast-curse deploy behavior (no curse admins, no timelock resolution).
	in := DeployStellarCCIPInnerInput{}
	require.Nil(t, in.CurseAdmins)
	require.False(t, in.EnableFastCurse)
	require.Empty(t, in.FastCurseQualifier)
	require.Empty(t, in.GovernanceQualifier)
}
