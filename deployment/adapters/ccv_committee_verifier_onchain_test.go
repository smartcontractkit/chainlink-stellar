package adapters

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccvadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	ccvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// committeeVerifierWriteTest carries the fixed facts one write-core test needs.
type committeeVerifierWriteTest struct {
	verifier string
	deployer string
	timelock string
	sel      uint64
	destSel  uint64
}

func newCommitteeVerifierWriteTest(t *testing.T) committeeVerifierWriteTest {
	t.Helper()
	return committeeVerifierWriteTest{
		verifier: stellarutil.MustGenerateMockContractID("deployer", "cv-batch-ops-verifier"),
		deployer: stellarutil.MustGenerateMockContractID("deployer", "cv-batch-ops-deployer"),
		timelock: stellarutil.MustGenerateMockContractID("deployer", "cv-batch-ops-timelock"),
		sel:      chainsel.STELLAR_TESTNET.Selector,
		destSel:  chainsel.STELLAR_MAINNET.Selector,
	}
}

// write builds the core input with the given owner read and chain refs.
func (f committeeVerifierWriteTest) write(
	inv *operationstest.RecordingInvoker,
	owner string,
	chainRefs []datastore.AddressRef,
) committeeVerifierWrite {
	return committeeVerifierWrite{
		deps: stellardeps.StellarDeps{Invoker: inv},
		ref: datastore.AddressRef{
			ChainSelector: f.sel,
			Type:          datastore.ContractType(cvops.ContractType),
			Address:       f.verifier,
		},
		deployer:  f.deployer,
		chainRefs: chainRefs,
		readOwner: fixedReadOwner(owner),
	}
}

// timelockRefs returns the single RBACTimelock ref under qual, plus noise refs
// that must not be mistaken for the owner.
func (f committeeVerifierWriteTest) timelockRefs(qual, addr string) []datastore.AddressRef {
	return []datastore.AddressRef{
		mcmsutil.StellarTimelockDatastoreRef(f.sel, qual, addr),
		{
			ChainSelector: f.sel,
			Type:          datastore.ContractType(cvops.ContractType),
			Address:       f.verifier,
		},
	}
}

// requireCommitteeVerifierProposal asserts the returned batch op is the single
// MCMS transaction for fn against the verifier, and that nothing ran directly.
func requireCommitteeVerifierProposal(
	t *testing.T,
	f committeeVerifierWriteTest,
	inv *operationstest.RecordingInvoker,
	ops []mcmstypes.BatchOperation,
	fn string,
) []xdr.ScVal {
	t.Helper()
	require.Len(t, ops, 1)
	op := ops[0]
	require.Equal(t, mcmstypes.ChainSelector(f.sel), op.ChainSelector)
	require.Len(t, op.Transactions, 1)
	tx := op.Transactions[0]
	require.Equal(t, f.verifier, tx.To)
	require.Equal(t, cvops.ContractType, tx.OperationMetadata.ContractType)
	require.JSONEq(t, `{"family":"stellar","encodingVersion":1}`, string(tx.AdditionalFields))
	gotFn, args, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
	require.NoError(t, err)
	require.Equal(t, fn, gotFn)
	require.Empty(t, inv.Records(), "the proposal arm must not invoke the contract directly")
	return args
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

func requireSignatureConfigsVec(t *testing.T, v xdr.ScVal) []ccvbindings.SignatureQuorumConfig {
	t.Helper()
	vec, ok := v.GetVec()
	require.True(t, ok)
	require.NotNil(t, vec)
	out := make([]ccvbindings.SignatureQuorumConfig, len(*vec))
	for i, item := range *vec {
		cfg, err := ccvbindings.SignatureQuorumConfigFromScVal(item)
		require.NoError(t, err)
		out[i] = *cfg
	}
	return out
}

func requireAllowListUpdatesVec(t *testing.T, v xdr.ScVal) []ccvbindings.AllowListUpdate {
	t.Helper()
	vec, ok := v.GetVec()
	require.True(t, ok)
	require.NotNil(t, vec)
	out := make([]ccvbindings.AllowListUpdate, len(*vec))
	for i, item := range *vec {
		u, err := ccvbindings.AllowListUpdateFromScVal(item)
		require.NoError(t, err)
		out[i] = *u
	}
	return out
}

func TestApplySignatureConfigsProposesUnderTimelock(t *testing.T) {
	t.Parallel()
	f := newCommitteeVerifierWriteTest(t)
	lowSigner := common.HexToAddress("0x0000000000000000000000000000000000000011").Hex()
	highSigner := common.HexToAddress("0x00000000000000000000000000000000000000aa").Hex()
	change := ccvadapters.SignatureConfigChange{
		RemovedSourceChainSelectors: []uint64{chainsel.ETHEREUM_MAINNET.Selector},
		NewConfigs: []ccvadapters.SignatureConfig{{
			SourceChainSelector: chainsel.STELLAR_MAINNET.Selector,
			Signers:             []string{highSigner, lowSigner},
			Threshold:           2,
		}},
	}

	t.Run("timelock owner proposes", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		ops, err := applySignatureConfigsCore(
			context.Background(),
			f.write(inv, f.timelock, f.timelockRefs(cciputils.CLLQualifier, f.timelock)),
			change,
		)
		require.NoError(t, err)
		args := requireCommitteeVerifierProposal(t, f, inv, ops, "apply_signature_configs")
		require.Len(t, args, 2)
		require.Equal(t, change.RemovedSourceChainSelectors, requireU64Vec(t, args[0]))
		configs := requireSignatureConfigsVec(t, args[1])
		require.Len(t, configs, 1)
		require.Equal(t, change.NewConfigs[0].SourceChainSelector, configs[0].SourceChainSelector)
		require.Equal(t, uint32(2), configs[0].Threshold)
		require.Len(t, configs[0].Signers, 2)
		require.Equal(t, common.HexToAddress(lowSigner), common.BytesToAddress(configs[0].Signers[0][12:]))
		require.Equal(t, common.HexToAddress(highSigner), common.BytesToAddress(configs[0].Signers[1][12:]))
		require.Negative(t, bytes.Compare(configs[0].Signers[0][:], configs[0].Signers[1][:]),
			"the contract requires strictly ascending signer order")
		for _, signer := range configs[0].Signers {
			require.Equal(t, [12]byte{}, [12]byte(signer[:12]), "signers are left-padded to 32 bytes")
		}
	})

	t.Run("deployer owner executes directly", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		ops, err := applySignatureConfigsCore(context.Background(), f.write(inv, f.deployer, nil), change)
		require.NoError(t, err)
		require.Empty(t, ops)
		records := inv.Records()
		require.Len(t, records, 1)
		require.Equal(t, f.verifier, records[0].ContractID)
		require.Equal(t, "apply_signature_configs", records[0].Fn)
		require.Len(t, records[0].Args, 2)
		require.Equal(t, change.RemovedSourceChainSelectors, requireU64Vec(t, records[0].Args[0]))
		require.Len(t, requireSignatureConfigsVec(t, records[0].Args[1]), 1)
	})
}

func TestSetAllowedFinalityConfigProposesUnderTimelock(t *testing.T) {
	t.Parallel()
	f := newCommitteeVerifierWriteTest(t)

	t.Run("timelock owner proposes", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		// A non-CLL qualifier proves routing matches any deployed RBACTimelock.
		ops, err := setAllowedFinalityConfigCore(
			context.Background(),
			f.write(inv, f.timelock, f.timelockRefs("custom-mcms", f.timelock)),
			true,
			7,
		)
		require.NoError(t, err)
		args := requireCommitteeVerifierProposal(t, f, inv, ops, "set_allowed_finality_config")
		require.Len(t, args, 1)
		finality, ok := args[0].GetU32()
		require.True(t, ok)
		require.Equal(t, uint32(1<<16|7), uint32(finality))
	})

	t.Run("deployer owner executes directly", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		ops, err := setAllowedFinalityConfigCore(context.Background(), f.write(inv, f.deployer, nil), false, 3)
		require.NoError(t, err)
		require.Empty(t, ops)
		records := inv.Records()
		require.Len(t, records, 1)
		require.Equal(t, "set_allowed_finality_config", records[0].Fn)
		require.Len(t, records[0].Args, 1)
		finality, ok := records[0].Args[0].GetU32()
		require.True(t, ok)
		require.Equal(t, uint32(3), uint32(finality))
	})

	t.Run("hex-recorded timelock ref routes to proposal", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		hexAddr, err := stellarutil.StrkeyToHex(f.timelock)
		require.NoError(t, err)
		ops, err := setAllowedFinalityConfigCore(
			context.Background(),
			f.write(inv, f.timelock, f.timelockRefs(cciputils.CLLQualifier, hexAddr)),
			true,
			1,
		)
		require.NoError(t, err)
		args := requireCommitteeVerifierProposal(t, f, inv, ops, "set_allowed_finality_config")
		require.Len(t, args, 1)
	})

	t.Run("owner read error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		w := f.write(inv, f.timelock, nil)
		sentinel := errors.New("rpc unavailable")
		w.readOwner = func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
			return "", sentinel
		}
		ops, err := setAllowedFinalityConfigCore(context.Background(), w, false, 1)
		require.ErrorIs(t, err, sentinel)
		require.Empty(t, ops)
		require.Empty(t, inv.Records())
	})
}

func TestApplyAllowlistUpdatesProposesUnderTimelock(t *testing.T) {
	t.Parallel()
	f := newCommitteeVerifierWriteTest(t)
	added := []string{stellarutil.MustGenerateMockContractID("deployer", "cv-allowlist-added")}
	removed := []string{stellarutil.MustGenerateMockContractID("deployer", "cv-allowlist-removed")}

	t.Run("timelock owner proposes", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		ops, err := applyAllowlistUpdatesCore(
			context.Background(),
			f.write(inv, f.timelock, f.timelockRefs(cciputils.CLLQualifier, f.timelock)),
			f.destSel,
			true,
			added,
			removed,
		)
		require.NoError(t, err)
		args := requireCommitteeVerifierProposal(t, f, inv, ops, "apply_allowlist_updates")
		require.Len(t, args, 1)
		updates := requireAllowListUpdatesVec(t, args[0])
		require.Len(t, updates, 1)
		require.Equal(t, f.destSel, updates[0].DestChainSelector)
		require.True(t, updates[0].AllowlistEnabled)
		require.Equal(t, added, updates[0].AddedAllowlistedSenders)
		require.Equal(t, removed, updates[0].RemovedAllowlistedSenders)
	})

	t.Run("deployer owner executes directly", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		ops, err := applyAllowlistUpdatesCore(context.Background(), f.write(inv, f.deployer, nil), f.destSel, false, removed, added)
		require.NoError(t, err)
		require.Empty(t, ops)
		records := inv.Records()
		require.Len(t, records, 1)
		require.Equal(t, "apply_allowlist_updates", records[0].Fn)
		require.Len(t, records[0].Args, 1)
		updates := requireAllowListUpdatesVec(t, records[0].Args[0])
		require.Len(t, updates, 1)
		require.False(t, updates[0].AllowlistEnabled)
		require.Equal(t, removed, updates[0].AddedAllowlistedSenders)
		require.Equal(t, added, updates[0].RemovedAllowlistedSenders)
	})
}

func TestWritesRejectForeignOwner(t *testing.T) {
	t.Parallel()
	f := newCommitteeVerifierWriteTest(t)
	foreign := stellarutil.MustGenerateMockContractID("deployer", "cv-batch-ops-foreign-owner")
	refs := f.timelockRefs(cciputils.CLLQualifier, f.timelock)

	cases := map[string]func(committeeVerifierWrite) ([]mcmstypes.BatchOperation, error){
		"apply signature configs": func(w committeeVerifierWrite) ([]mcmstypes.BatchOperation, error) {
			return applySignatureConfigsCore(context.Background(), w, ccvadapters.SignatureConfigChange{})
		},
		"set allowed finality config": func(w committeeVerifierWrite) ([]mcmstypes.BatchOperation, error) {
			return setAllowedFinalityConfigCore(context.Background(), w, true, 1)
		},
		"apply allowlist updates": func(w committeeVerifierWrite) ([]mcmstypes.BatchOperation, error) {
			return applyAllowlistUpdatesCore(context.Background(), w, f.destSel, true, nil, nil)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inv := operationstest.NewRecordingInvoker()
			ops, err := run(f.write(inv, foreign, refs))
			require.ErrorContains(t, err, f.verifier)
			require.ErrorContains(t, err, foreign)
			require.ErrorContains(t, err, f.deployer)
			require.Empty(t, ops)
			require.Empty(t, inv.Records())
		})
	}
}

func TestMustProposeFiltersTimelockRefs(t *testing.T) {
	t.Parallel()
	f := newCommitteeVerifierWriteTest(t)
	good := mcmsutil.StellarTimelockDatastoreRef(f.sel, cciputils.CLLQualifier, f.timelock)
	wrongVersion := good
	wrongVersion.Version = semver.MustParse("2.0.0")
	wrongType := good
	wrongType.Type = datastore.ContractType("SomeOtherContractType")
	noVersion := good
	noVersion.Version = nil

	cases := map[string]struct {
		refs    []datastore.AddressRef
		wantErr bool
	}{
		"matching timelock proposes":                {refs: []datastore.AddressRef{good}},
		"other contract version is not the owner":   {refs: []datastore.AddressRef{wrongVersion}, wantErr: true},
		"other contract type is not the owner":      {refs: []datastore.AddressRef{wrongType}, wantErr: true},
		"unversioned timelock ref is not the owner": {refs: []datastore.AddressRef{noVersion}, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inv := operationstest.NewRecordingInvoker()
			ops, err := setAllowedFinalityConfigCore(context.Background(), f.write(inv, f.timelock, tc.refs), true, 1)
			if tc.wantErr {
				require.ErrorContains(t, err, "neither the deployer")
				require.Empty(t, ops)
				require.Empty(t, inv.Records())
				return
			}
			require.NoError(t, err)
			require.Len(t, ops, 1)
		})
	}
}

func TestSignatureQuorumConfigsRejectsInvalidSigner(t *testing.T) {
	t.Parallel()
	valid := common.HexToAddress("0x0000000000000000000000000000000000000011").Hex()
	out, err := signatureQuorumConfigs([]ccvadapters.SignatureConfig{{
		SourceChainSelector: chainsel.STELLAR_MAINNET.Selector,
		Signers:             []string{valid},
		Threshold:           1,
	}})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Signers, 1)

	_, err = signatureQuorumConfigs([]ccvadapters.SignatureConfig{{
		SourceChainSelector: chainsel.STELLAR_MAINNET.Selector,
		Signers:             []string{"not-a-hex-address"},
		Threshold:           1,
	}})
	require.ErrorContains(t, err, "not-a-hex-address")
}
