package sequences

import (
	"context"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	onramp "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func TestStellarUpgradeContractsViaMCMS_sequenceMetadata(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-seq-upgrade-contracts-via-mcms", StellarUpgradeContractsViaMCMS.ID())
	require.Equal(t, deploy.MCMSVersion.String(), StellarUpgradeContractsViaMCMS.Version())
}

func TestStellarUpgradeContractsViaMCMS_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	in := StellarUpgradeContractsInput{
		ChainSelector:  sel,
		ContractRef:    nil,
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarUpgradeContractsViaMCMS, chains, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarUpgradeContractsViaMCMS_EmptyContractRefsReturnsNoBatchOps(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420040)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarUpgradeContractsInput{
		ChainSelector:  sel,
		ContractRef:    nil,
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	out, err := cldf_ops.ExecuteSequence(b, StellarUpgradeContractsViaMCMS, chains, in)
	require.NoError(t, err)
	require.Empty(t, out.Output.BatchOps)
}

// TestStellarUpgradeContractsViaMCMS_NormalizesHexContractRef verifies the hex
// form a datastore records is normalized to the strkey before anything else
// sees it. The unsupported type makes ResolveUpgradeWasmPath fail right after
// normalization, so the error names the strkey form (never the hex form).
func TestStellarUpgradeContractsViaMCMS_NormalizesHexContractRef(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420041)
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            testStellarSigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})

	strkeyAddr := stellarutil.MustGenerateMockContractID("deployer", "upgrade-hex-ref-test")
	hexForm, err := stellarutil.StrkeyToHex(strkeyAddr)
	require.NoError(t, err)

	in := StellarUpgradeContractsInput{
		ChainSelector:  sel,
		ContractRef:    []datastore.AddressRef{{Address: hexForm, Type: "UnsupportedType"}},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	_, err = cldf_ops.ExecuteSequence(b, StellarUpgradeContractsViaMCMS, chains, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "upgrade "+strkeyAddr,
		"the hex form must be normalized to the strkey before anything else sees it")
	require.NotContains(t, err.Error(), hexForm)
}

// stubUploader is a wasmUploader that returns a fixed hash and records the path.
type stubUploader struct {
	hash     xdr.Hash
	wasmPath string
}

func (s *stubUploader) UploadContractWASM(_ context.Context, wasmPath string) (xdr.Hash, error) {
	s.wasmPath = wasmPath
	return s.hash, nil
}

// TestUpgradeContractRef_governanceBranchEmitsMCMSPayload is the new value-add
// over the transfer_ownership tests: when the owner is the governance/timelock
// address, the upgrade is emitted as an MCMS proposal transaction whose payload
// decodes to ("upgrade", [hash]) — proving the Wasm hash threads through the
// encode step, with no on-chain execution.
func TestUpgradeContractRef_governanceBranchEmitsMCMSPayload(t *testing.T) {
	t.Parallel()
	deployerAddr := keypair.MustRandom().Address()
	governanceAddr := keypair.MustRandom().Address()

	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("owner", ptrScVal(scval.AddressToScVal(governanceAddr)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	var wantHash xdr.Hash
	wantHash[0] = 0xCD
	uploader := &stubUploader{hash: wantHash}

	cid := stellarutil.MustGenerateMockContractID("deployer", "upgrade-mcms-branch")
	wo, err := upgradeContractRef(context.Background(), operationstest.NewBundle(t), deps, uploader,
		deployerAddr, governanceAddr, 999, datastore.AddressRef{Address: cid, Type: onramp.ContractType})
	require.NoError(t, err)

	require.False(t, wo.Executed(), "governance branch must not claim direct execution")
	require.NotEmpty(t, wo.Tx.Data, "governance branch must emit an MCMS proposal payload")
	require.Equal(t, cid, wo.Tx.To)
	require.Equal(t, string(onramp.ContractType), wo.Tx.OperationMetadata.ContractType)

	fn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(wo.Tx.Data)
	require.NoError(t, err)
	require.Equal(t, "upgrade", fn)
	require.Len(t, args, 1)
	got, err := scval.Bytes32FromScVal(args[0])
	require.NoError(t, err)
	require.Equal(t, [32]byte(wantHash), got)

	// No on-chain upgrade call was made — only the owner simulation.
	for _, r := range inv.Records() {
		require.NotEqual(t, "upgrade", r.Fn, "governance branch must not execute the upgrade")
	}
}

// TestUpgradeContractRef_deployerBranchExecutesDirectly verifies the EOA-owner
// path runs ExecuteUpgrade on chain and reports a direct-execution WriteOutput.
func TestUpgradeContractRef_deployerBranchExecutesDirectly(t *testing.T) {
	t.Parallel()
	deployerAddr := keypair.MustRandom().Address()
	governanceAddr := keypair.MustRandom().Address()

	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("owner", ptrScVal(scval.AddressToScVal(deployerAddr)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	var wantHash xdr.Hash
	wantHash[0] = 0xCD
	uploader := &stubUploader{hash: wantHash}

	cid := stellarutil.MustGenerateMockContractID("deployer", "upgrade-eoa-branch")
	wo, err := upgradeContractRef(context.Background(), operationstest.NewBundle(t), deps, uploader,
		deployerAddr, governanceAddr, 999, datastore.AddressRef{Address: cid, Type: onramp.ContractType})
	require.NoError(t, err)

	require.True(t, wo.Executed(), "deployer branch must report direct execution")
	require.Empty(t, wo.Tx.Data, "deployer branch must not emit an MCMS proposal")
	require.NotNil(t, wo.ExecInfo)
	require.Equal(t, "stellar-direct-upgrade", wo.ExecInfo.Hash)

	// The upgrade was actually invoked on chain with the uploaded hash.
	rec := inv.Last()
	require.Equal(t, "upgrade", rec.Fn)
	require.Equal(t, cid, rec.ContractID)
	got, err := scval.Bytes32FromScVal(rec.Args[0])
	require.NoError(t, err)
	require.Equal(t, [32]byte(wantHash), got)
}

// TestUpgradeContractRef_rejectsUnknownOwner verifies a contract whose owner is
// neither the deployer nor governance errors with an explicit routing message.
func TestUpgradeContractRef_rejectsUnknownOwner(t *testing.T) {
	t.Parallel()
	deployerAddr := keypair.MustRandom().Address()
	governanceAddr := keypair.MustRandom().Address()
	thirdParty := keypair.MustRandom().Address()

	inv := operationstest.NewRecordingInvoker().
		WithSimulateResultForFn("owner", ptrScVal(scval.AddressToScVal(thirdParty)))
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}

	var wantHash xdr.Hash
	uploader := &stubUploader{hash: wantHash}

	cid := stellarutil.MustGenerateMockContractID("deployer", "upgrade-unknown-owner")
	_, err := upgradeContractRef(context.Background(), operationstest.NewBundle(t), deps, uploader,
		deployerAddr, governanceAddr, 999, datastore.AddressRef{Address: cid, Type: onramp.ContractType})
	require.Error(t, err)
	require.Contains(t, err.Error(), "neither deployer")
}

// TestUpgradeContractRef_rejectsUnmappedType verifies a contract type with no
// release-WASM mapping fails at the upload-resolution step before any I/O.
func TestUpgradeContractRef_rejectsUnmappedType(t *testing.T) {
	t.Parallel()
	inv := operationstest.NewRecordingInvoker()
	deps := stellardeps.StellarDeps{Deploy: &operationstest.FakeDeployer{}, Invoker: inv}
	uploader := &stubUploader{}

	cid := stellarutil.MustGenerateMockContractID("deployer", "upgrade-unmapped-type")
	_, err := upgradeContractRef(context.Background(), operationstest.NewBundle(t), deps, uploader,
		"GDEPLOYER", "GGOVERNANCE", 999, datastore.AddressRef{Address: cid, Type: "BogusType"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no upgrade WASM mapping")
	require.Empty(t, uploader.wasmPath, "uploader must not be called for an unmapped type")
}

// ptrScVal returns a pointer to v (helper for the stub invoker's simulate result).
func ptrScVal(v xdr.ScVal) *xdr.ScVal { return &v }

// Compile-time check that the ownership package's upgrade entrypoint is wired.
var _ = ownership.ExecuteUpgrade
