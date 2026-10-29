//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccvadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	timelockbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/timelock"
	_ "github.com/smartcontractkit/chainlink-stellar/deployment/adapters" // registers the adapter under test
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/changesets"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

// TestCommitteeVerifierBatchOps drives the registered CommitteeVerifierOnchain
// adapter against a live verifier: deployer-owned writes execute directly and
// timelock-owned writes come back as one decoded MCMS proposal that executes.
func TestCommitteeVerifierBatchOps(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, passphrase, _ := GetIsolatedTestEnv(ctx, t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	srcSel := chainsel.ETHEREUM_MAINNET.Selector
	nextSrcSel := chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector
	allowKey := chainsel.STELLAR_MAINNET.Selector
	qual := ccip.DefaultCommitteeVerifierQualifier

	e2eEnv := &helpers.E2ETestEnv{
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: passphrase,
		StellarRoot:       projectRoot,
	}
	newBundle := func() cldfops.Bundle {
		return cldfops.NewBundle(func() context.Context { return ctx }, cldflogger.Test(t), cldfops.NewMemoryReporter())
	}
	ds := datastore.NewMemoryDataStore()
	cldfEnv := cldf.Environment{
		Logger:     cldflogger.Test(t),
		GetContext: func() context.Context { return ctx },
		DataStore:  ds.Seal(),
		BlockChains: cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: cldf_stellar.Chain{
			ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
			Signer:            bindings.NewStellarKeypairSigner(deployerKP),
			Client:            rpcClient,
			NetworkPassphrase: passphrase,
		}}),
		OperationsBundle: newBundle(),
	}
	apply := func(name string, apply func() (datastore.MutableDataStore, error)) {
		t.Helper()
		out, err := apply()
		require.NoError(t, err, "%s: apply failed", name)
		require.NoError(t, ds.Merge(out.Seal()), "%s: merge output", name)
		cldfEnv.DataStore = ds.Seal()
	}

	apply("RMN Remote", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNRemote{}.Apply(cldfEnv, changesets.DeployRMNRemoteConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("RMN Proxy", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployRMNProxy{}.Apply(cldfEnv, changesets.DeployRMNProxyConfig{ChainSelector: sel})
		return out.DataStore, err
	})
	apply("CommitteeVerifier", func() (datastore.MutableDataStore, error) {
		out, err := changesets.DeployCommitteeVerifier{}.Apply(cldfEnv, changesets.DeployCommitteeVerifierConfig{
			ChainSelector:    sel,
			Owner:            deployerKP.Address(),
			StorageLocations: [][]byte{[]byte("location-1")},
		})
		return out.DataStore, err
	})
	verifierID, err := ccip.CommitteeVerifierDatastoreRef().LookupStrkey(cldfEnv.DataStore, sel)
	require.NoError(t, err)
	verifier := cvbindings.NewCommitteeVerifierClient(deployer, verifierID)

	adapter, err := ccvadapters.GetCommitteeVerifierOnchainRegistry().Get(sel)
	require.NoError(t, err)

	lowSigner := common.HexToAddress("0x0000000000000000000000000000000000000011").Hex()
	highSigner := common.HexToAddress("0x00000000000000000000000000000000000000aa").Hex()

	requireProposal := func(t *testing.T, ops []mcmstypes.BatchOperation, fn string) (mcmstypes.Transaction, []xdr.ScVal) {
		t.Helper()
		require.Len(t, ops, 1)
		op := ops[0]
		require.Equal(t, mcmstypes.ChainSelector(sel), op.ChainSelector)
		require.Len(t, op.Transactions, 1)
		tx := op.Transactions[0]
		require.Equal(t, verifierID, tx.To)
		require.JSONEq(t, `{"family":"stellar","encodingVersion":1}`, string(tx.AdditionalFields))
		gotFn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
		require.NoError(t, err)
		require.Equal(t, fn, gotFn)
		return tx, args
	}

	var gov *helpers.MCMSGovernanceStack
	execute := func(t *testing.T, tx mcmstypes.Transaction, nonce byte) {
		t.Helper()
		calls, err := helpers.TimelockCallsFromProposalTx(tx)
		require.NoError(t, err)
		var pred, salt [32]byte
		salt[31] = nonce
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, calls, pred, salt)
	}

	t.Run("deployer-owned writes execute directly", func(t *testing.T) {
		ops, err := adapter.ApplySignatureConfigs(ctx, cldfEnv, sel, qual, ccvadapters.SignatureConfigChange{
			NewConfigs: []ccvadapters.SignatureConfig{{
				SourceChainSelector: srcSel,
				Signers:             []string{highSigner, lowSigner},
				Threshold:           2,
			}},
		})
		require.NoError(t, err)
		require.Empty(t, ops, "deployer-owned writes must be submitted directly")

		cfg, err := verifier.GetSignatureConfig(ctx, srcSel)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		require.Equal(t, uint32(2), cfg.Threshold)
		require.Len(t, cfg.Signers, 2)
		require.Equal(t, common.HexToAddress(lowSigner), common.BytesToAddress(cfg.Signers[0][12:]))
		require.Equal(t, common.HexToAddress(highSigner), common.BytesToAddress(cfg.Signers[1][12:]))
		for _, signer := range cfg.Signers {
			require.Equal(t, [12]byte{}, [12]byte(signer[:12]), "signers are left-padded to 32 bytes")
		}

		ops, err = adapter.SetAllowedFinalityConfig(ctx, cldfEnv, sel, qual, false, true, 7)
		require.NoError(t, err)
		require.Empty(t, ops)

		finality, err := verifier.GetAllowedFinalityConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, uint32(1<<16|7), finality)
	})

	t.Run("foreign owner is rejected", func(t *testing.T) {
		foreignQualifier := "foreign-owner"
		foreignOwner := helpers.GenerateMockContractID(t, deployerKP.Address(), "cv-foreign-owner")
		apply("CommitteeVerifier foreign-owner", func() (datastore.MutableDataStore, error) {
			out, err := changesets.DeployCommitteeVerifier{}.Apply(cldfEnv, changesets.DeployCommitteeVerifierConfig{
				ChainSelector:    sel,
				Qualifier:        foreignQualifier,
				Owner:            foreignOwner,
				StorageLocations: [][]byte{[]byte("location-foreign")},
			})
			return out.DataStore, err
		})

		ops, err := adapter.ApplySignatureConfigs(ctx, cldfEnv, sel, foreignQualifier, ccvadapters.SignatureConfigChange{})
		require.ErrorContains(t, err, foreignOwner)
		require.ErrorContains(t, err, "no operation emitted")
		require.Empty(t, ops)
	})

	t.Run("timelock-owned write returns a proposal and submits nothing", func(t *testing.T) {
		gov = helpers.DeployMCMSStackWithRoles(t, ctx, e2eEnv, sel, cciputils.CLLQualifier, helpers.DefaultMCMSTimelockMinDelaySec)
		for _, r := range gov.Refs {
			require.NoError(t, ds.Addresses().Upsert(r))
		}
		cldfEnv.DataStore = ds.Seal()

		require.NoError(t, verifier.TransferOwnership(ctx, gov.TimelockID))
		acceptArgs, err := mcmsutil.EncodeSorobanInvokeArgs(nil)
		require.NoError(t, err)
		var acceptSalt [32]byte
		acceptSalt[31] = 1
		callsAccept := timelockbindings.Calls{
			Inner: []timelockbindings.Call{{Target: verifierID, Function: "accept_ownership", ArgsXdr: acceptArgs}},
		}
		helpers.MCMSTimelockScheduleAndExecute(t, ctx, e2eEnv, gov, callsAccept, [32]byte{}, acceptSalt)

		owner, err := verifier.Owner(ctx)
		require.NoError(t, err)
		require.NotNil(t, owner)
		require.Equal(t, gov.TimelockID, *owner)

		beforeFinality, err := verifier.GetAllowedFinalityConfig(ctx)
		require.NoError(t, err)

		ops, err := adapter.SetAllowedFinalityConfig(ctx, cldfEnv, sel, qual, false, false, 3)
		require.NoError(t, err)
		_, args := requireProposal(t, ops, "set_allowed_finality_config")
		require.Len(t, args, 1)
		finality, ok := args[0].GetU32()
		require.True(t, ok)
		require.Equal(t, uint32(3), uint32(finality))

		afterFinality, err := verifier.GetAllowedFinalityConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, beforeFinality, afterFinality, "an unexecuted proposal must not change on-chain state")
	})

	t.Run("signature config proposal executes through the timelock", func(t *testing.T) {
		ops, err := adapter.ApplySignatureConfigs(ctx, cldfEnv, sel, qual, ccvadapters.SignatureConfigChange{
			RemovedSourceChainSelectors: []uint64{srcSel},
			NewConfigs: []ccvadapters.SignatureConfig{{
				SourceChainSelector: nextSrcSel,
				Signers:             []string{highSigner, lowSigner},
				Threshold:           1,
			}},
		})
		require.NoError(t, err)
		tx, args := requireProposal(t, ops, "apply_signature_configs")
		require.Len(t, args, 2)
		require.Equal(t, []uint64{srcSel}, requireU64Vec(t, args[0]))
		configs := requireSignatureConfigsVec(t, args[1])
		require.Len(t, configs, 1)
		require.Equal(t, nextSrcSel, configs[0].SourceChainSelector)
		require.Equal(t, uint32(1), configs[0].Threshold)
		require.Len(t, configs[0].Signers, 2)
		require.Equal(t, common.HexToAddress(lowSigner), common.BytesToAddress(configs[0].Signers[0][12:]))

		execute(t, tx, 2)

		cfg, err := verifier.GetSignatureConfig(ctx, nextSrcSel)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		require.Equal(t, uint32(1), cfg.Threshold)
		_, err = verifier.GetSignatureConfig(ctx, srcSel)
		require.ErrorContains(t, err, "Error(Contract, #19)", "the removed config must be gone")
	})

	t.Run("finality proposal executes through the timelock", func(t *testing.T) {
		ops, err := adapter.SetAllowedFinalityConfig(ctx, cldfEnv, sel, qual, false, false, 5)
		require.NoError(t, err)
		tx, args := requireProposal(t, ops, "set_allowed_finality_config")
		require.Len(t, args, 1)
		finality, ok := args[0].GetU32()
		require.True(t, ok)
		require.Equal(t, uint32(5), uint32(finality))

		execute(t, tx, 3)

		got, err := verifier.GetAllowedFinalityConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, uint32(5), got)
	})

	t.Run("allowlist proposal executes through the timelock", func(t *testing.T) {
		added := []string{
			helpers.GenerateMockContractID(t, deployerKP.Address(), "cv-allowlist-a"),
			helpers.GenerateMockContractID(t, deployerKP.Address(), "cv-allowlist-b"),
		}
		ops, err := adapter.ApplyAllowlistUpdates(ctx, cldfEnv, sel, qual, allowKey, true, added, nil)
		require.NoError(t, err)
		tx, args := requireProposal(t, ops, "apply_allowlist_updates")
		require.Len(t, args, 1)
		updates := requireAllowListUpdatesVec(t, args[0])
		require.Len(t, updates, 1)
		require.Equal(t, allowKey, updates[0].DestChainSelector)
		require.True(t, updates[0].AllowlistEnabled)
		require.Equal(t, added, updates[0].AddedAllowlistedSenders)
		require.Empty(t, updates[0].RemovedAllowlistedSenders)

		execute(t, tx, 4)

		entry, err := verifier.GetAllowlistEntry(ctx, allowKey)
		require.NoError(t, err)
		require.NotNil(t, entry)
		require.True(t, entry.AllowlistEnabled)
		require.ElementsMatch(t, added, entry.Allowlist)
	})
}

func requireU64Vec(t *testing.T, v xdr.ScVal) []uint64 {
	t.Helper()
	vec, ok := v.GetVec()
	require.True(t, ok)
	require.NotNil(t, vec)
	out := make([]uint64, len(*vec))
	for i, item := range *vec {
		u, ok := item.GetU64()
		require.True(t, ok)
		out[i] = uint64(u)
	}
	return out
}

func requireSignatureConfigsVec(t *testing.T, v xdr.ScVal) []cvbindings.SignatureQuorumConfig {
	t.Helper()
	vec, ok := v.GetVec()
	require.True(t, ok)
	require.NotNil(t, vec)
	out := make([]cvbindings.SignatureQuorumConfig, len(*vec))
	for i, item := range *vec {
		cfg, err := cvbindings.SignatureQuorumConfigFromScVal(item)
		require.NoError(t, err)
		out[i] = *cfg
	}
	return out
}

func requireAllowListUpdatesVec(t *testing.T, v xdr.ScVal) []cvbindings.AllowListUpdate {
	t.Helper()
	vec, ok := v.GetVec()
	require.True(t, ok)
	require.NotNil(t, vec)
	out := make([]cvbindings.AllowListUpdate, len(*vec))
	for i, item := range *vec {
		u, err := cvbindings.AllowListUpdateFromScVal(item)
		require.NoError(t, err)
		out[i] = *u
	}
	return out
}
