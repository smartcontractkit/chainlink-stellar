package sequences

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	executorbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/executor"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// lanesFixture holds the deterministic contract IDs and remote address
// encodings shared by the StellarConfigureChainForLanes tests. The remote
// chain is EVM (Sepolia): its OnRamp travels abi-encoded (32 bytes) and its
// OffRamp native (20 bytes).
type lanesFixture struct {
	routerID      string
	onRampID      string
	offRampID     string
	feeQuoterID   string
	executorID    string
	cvID          string
	remoteOnRamp  []byte
	remoteOffRamp []byte
}

func newLanesFixture(t *testing.T) lanesFixture {
	t.Helper()
	f := lanesFixture{
		routerID:    contractStrkey(t, 0x01),
		onRampID:    contractStrkey(t, 0x02),
		offRampID:   contractStrkey(t, 0x03),
		feeQuoterID: contractStrkey(t, 0x04),
		executorID:  contractStrkey(t, 0x05),
		cvID:        contractStrkey(t, 0x06),
	}
	f.remoteOnRamp = make([]byte, 32)
	for i := range f.remoteOnRamp {
		f.remoteOnRamp[i] = byte(0xa0 + i)
	}
	f.remoteOffRamp = make([]byte, 20)
	for i := range f.remoteOffRamp {
		f.remoteOffRamp[i] = byte(0xb0 + i)
	}
	return f
}

func lanesRawID(t *testing.T, strkeyID string) []byte {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, strkeyID)
	require.NoError(t, err)
	return raw
}

func lanesHexID(t *testing.T, strkeyID string) string {
	t.Helper()
	return "0x" + hex.EncodeToString(lanesRawID(t, strkeyID))
}

func lanesBoolPtr(v bool) *bool    { return &v }
func lanesU32Ptr(v uint32) *uint32 { return &v }
func lanesU16Ptr(v uint16) *uint16 { return &v }
func lanesU8Ptr(v uint8) *uint8    { return &v }

// fqOverrides mirrors the stellar adapter's GetDefaultFeeQuoterDestChainConfig
// (all pointer fields set), i.e. what the changeset hands the sequence after
// coalescing the YAML overrides over the remote adapter's defaults.
func (f lanesFixture) fqOverrides() ccvadapters.FeeQuoterDestChainConfigOverrides {
	return ccvadapters.FeeQuoterDestChainConfigOverrides{
		IsEnabled:                   lanesBoolPtr(true),
		MaxDataBytes:                lanesU32Ptr(50_000),
		MaxPerMsgGasLimit:           lanesU32Ptr(4_000_000),
		DestGasOverhead:             lanesU32Ptr(350_000),
		DestGasPerPayloadByteBase:   lanesU8Ptr(16),
		DefaultTokenFeeUSDCents:     lanesU16Ptr(50),
		DefaultTokenDestGasOverhead: lanesU32Ptr(50_000),
		DefaultTxGasLimit:           lanesU32Ptr(200_000),
		NetworkFeeUSDCents:          lanesU16Ptr(100),
		LinkFeeMultiplierPercent:    lanesU8Ptr(90),
	}
}

// input builds the sequence input for one Stellar→Sepolia lane. Inbound CCVs
// arrive 0x-hex (datastore form) and outbound CCVs as strkeys, covering both
// localContractStrkey forms; DefaultExecutor arrives 0x-hex.
func (f lanesFixture) input(t *testing.T, mutate func(in *ccvadapters.ConfigureChainForLanesInput)) ccvadapters.ConfigureChainForLanesInput {
	t.Helper()
	input := ccvadapters.ConfigureChainForLanesInput{
		ChainSelector: characterizationSelector,
		Router:        lanesRawID(t, f.routerID),
		OnRamp:        lanesRawID(t, f.onRampID),
		OffRamp:       lanesRawID(t, f.offRampID),
		FeeQuoter:     lanesRawID(t, f.feeQuoterID),
		RemoteChains: map[uint64]ccvadapters.RemoteChainConfig[[]byte, string]{
			characterizationRemoteSel: {
				AllowTrafficFrom:          lanesBoolPtr(true),
				OnRamps:                   [][]byte{f.remoteOnRamp},
				OffRamp:                   f.remoteOffRamp,
				DefaultInboundCCVs:        []string{lanesHexID(t, f.cvID)},
				DefaultOutboundCCVs:       []string{f.cvID},
				DefaultExecutor:           lanesHexID(t, f.executorID),
				FeeQuoterDestChainConfig:  f.fqOverrides(),
				ExecutorDestChainConfig:   ccvadapters.ExecutorDestChainConfig{USDCentsFee: 5, Enabled: true},
				AddressBytesLength:        20,
				BaseExecutionGasCost:      175_000,
				TokenReceiverAllowed:      lanesBoolPtr(false),
				MessageNetworkFeeUSDCents: 10,
				TokenNetworkFeeUSDCents:   25,
			},
		},
		CommitteeVerifiers: []ccvadapters.CommitteeVerifierConfig[datastore.AddressRef]{{
			CommitteeVerifier: []datastore.AddressRef{{
				Type:          "CommitteeVerifier",
				Address:       lanesHexID(t, f.cvID),
				ChainSelector: characterizationSelector,
			}},
			RemoteChains: map[uint64]ccvadapters.CommitteeVerifierRemoteChainConfig{
				characterizationRemoteSel: {
					SignatureConfig: ccvadapters.CommitteeVerifierSignatureQuorumConfig{
						Signers: []string{
							"0x1111111111111111111111111111111111111111",
							"0x2222222222222222222222222222222222222222",
						},
						Threshold: 2,
					},
				},
			},
		}},
	}
	if mutate != nil {
		mutate(&input)
	}
	return input
}

func notConfiguredErr(code uint32) error {
	// The exact rendering the Soroban host surfaces through the RPC client,
	// which stellarops.IsContractErrorCode matches on.
	return errors.New(fmt.Sprintf("Error(Contract, #%d)", code))
}

func lanesScVal(t *testing.T, v xdr.ScVal, err error) *xdr.ScVal {
	t.Helper()
	require.NoError(t, err)
	return &v
}

// lanesStructVal stubs a ToScVal-style binding value as a simulate result.
// It exists because Go forbids mixing ordinary args with a multi-value
// expansion (lanesScVal(t, cfg.ToScVal()) does not compile).
func lanesStructVal[T interface{ ToScVal() (xdr.ScVal, error) }](t *testing.T, s T) *xdr.ScVal {
	t.Helper()
	v, err := s.ToScVal()
	require.NoError(t, err)
	return &v
}

// stubLanesNotConfigured stubs every lane read as not-configured (per
// contract+fn — OnRamp, FeeQuoter and Executor share the get_dest_chain_config
// fn name but render different codes), so every write fires.
func stubLanesNotConfigured(t *testing.T, inv *operationstest.RecordingInvoker, f lanesFixture) {
	t.Helper()
	inv.
		WithSimulateErrorForContract(f.routerID, "get_onramp", notConfiguredErr(stellarops.UnsupportedDestinationChainCode)).
		WithSimulateResultForContract(f.routerID, "get_offramps", lanesScVal(t, scval.StructSliceToScVal([]routerbindings.OffRampEntry{}), nil)).
		WithSimulateErrorForContract(f.offRampID, "get_source_chain_config", notConfiguredErr(stellarops.SourceChainNotEnabledCode)).
		WithSimulateErrorForContract(f.onRampID, "get_dest_chain_config", notConfiguredErr(stellarops.DestinationChainNotSupportedCode)).
		WithSimulateErrorForContract(f.feeQuoterID, "get_dest_chain_config", notConfiguredErr(stellarops.DestinationChainNotEnabledCode)).
		WithSimulateErrorForContract(f.executorID, "get_dest_chain_config", notConfiguredErr(stellarops.DestinationChainNotEnabledCode)).
		WithSimulateErrorForContract(f.cvID, "get_signature_config", notConfiguredErr(stellarops.SourceSignersNotConfiguredCode))
}

// lanesDesired returns the on-chain state the sequence is expected to leave
// for the fixture input, i.e. what a configured chain reads back.
func (f lanesFixture) lanesDesired(t *testing.T) (offrampbindings.SourceChainConfig, onrampbindings.DestChainConfig, executorbindings.RemoteChainConfig, fqbindings.DestChainConfig) {
	t.Helper()
	return offrampbindings.SourceChainConfig{
			DefaultCcvs:      []string{f.cvID},
			IsEnabled:        true,
			LaneMandatedCcvs: []string{},
			OnRamps:          [][]byte{f.remoteOnRamp},
			Router:           f.routerID,
		},
		onrampbindings.DestChainConfig{
			AddressBytesLength:        20,
			BaseExecutionGasCost:      175_000,
			DefaultCcvs:               []string{f.cvID},
			DefaultExecutor:           f.executorID,
			ExecutionFeeUsdCents:      5,
			LaneMandatedCcvs:          []string{},
			MessageNetworkFeeUsdCents: 10,
			OffRamp:                   f.remoteOffRamp,
			Router:                    f.routerID,
			TokenNetworkFeeUsdCents:   25,
			TokenReceiverAllowed:      false,
		},
		executorbindings.RemoteChainConfig{Enabled: true, UsdCentsFee: 5},
		fqbindings.DestChainConfig{
			IsEnabled:             true,
			MaxDataBytes:          50_000,
			MaxPerMsgGasLimit:     4_000_000,
			DestGasOverhead:       350_000,
			DestGasPerPayloadByte: 16,
			DefaultTokenFeeUsd:    50,
			DefaultTokenDestGas:   50_000,
			DefaultTxGasLimit:     200_000,
			NetworkFeeUsdCents:    100,
			LinkPremiumPercent:    90,
		}
}

// lanesDesiredQuorum returns the signature quorum the fixture input writes on
// the committee verifier for the remote chain (hex signers left-padded to 32,
// sorted ascending).
func (f lanesFixture) lanesDesiredQuorum() cvbindings.SignatureQuorumConfig {
	signers := make([][32]byte, 0, 2)
	for _, s := range []string{"0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222"} {
		var padded [32]byte
		copy(padded[12:], common.HexToAddress(s).Bytes())
		signers = append(signers, padded)
	}
	sort.Slice(signers, func(i, j int) bool { return bytes.Compare(signers[i][:], signers[j][:]) < 0 })
	return cvbindings.SignatureQuorumConfig{
		SourceChainSelector: characterizationRemoteSel,
		Threshold:           2,
		Signers:             signers,
	}
}

// stubLanesConfigured stubs every lane read with the fixture's desired state,
// i.e. an already-configured chain.
func stubLanesConfigured(t *testing.T, inv *operationstest.RecordingInvoker, f lanesFixture) {
	t.Helper()
	offRampCfg, onRampCfg, execCfg, fqCfg := f.lanesDesired(t)
	onrampVal := scval.AddressToScVal(f.onRampID)
	inv.
		WithSimulateResultForContract(f.routerID, "get_onramp", &onrampVal).
		WithSimulateResultForContract(f.routerID, "get_offramps", lanesScVal(t, scval.StructSliceToScVal([]routerbindings.OffRampEntry{
			{Offramp: f.offRampID, SourceChainSelector: characterizationRemoteSel},
		}), nil)).
		WithSimulateResultForContract(f.offRampID, "get_source_chain_config", lanesStructVal(t, offRampCfg)).
		WithSimulateResultForContract(f.onRampID, "get_dest_chain_config", lanesStructVal(t, onRampCfg)).
		WithSimulateResultForContract(f.executorID, "get_dest_chain_config", lanesStructVal(t, execCfg)).
		WithSimulateResultForContract(f.feeQuoterID, "get_dest_chain_config", lanesStructVal(t, fqCfg)).
		WithSimulateResultForContract(f.cvID, "get_signature_config", lanesStructVal(t, f.lanesDesiredQuorum()))
}

func lanesBundle(t *testing.T) (cldf_ops.Bundle, *cldf_ops.MemoryReporter) {
	t.Helper()
	reporter := cldf_ops.NewMemoryReporter()
	b := cldf_ops.NewBundle(
		func() context.Context { return t.Context() },
		cldflogger.Nop(),
		reporter,
	)
	return b, reporter
}

func lanesOpIDs(t *testing.T, reporter *cldf_ops.MemoryReporter) []string {
	t.Helper()
	reports, err := reporter.GetReports()
	require.NoError(t, err)
	ids := make([]string, 0, len(reports))
	for _, r := range reports {
		require.Nil(t, r.Err, "op %s reported an error", r.Def.ID)
		ids = append(ids, r.Def.ID)
	}
	return ids
}

func lanesHasOp(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}

// TestStellarConfigureChainForLanesOpTrace pins the ordered (op ID, canonical
// input JSON) trace of the sequence against an unconfigured chain: every read
// reports not-configured, so every write fires. The write order is load-bearing
// (infra first, Router last) — the golden pins it.
func TestStellarConfigureChainForLanesOpTrace(t *testing.T) {
	f := newLanesFixture(t)
	inv := operationstest.NewRecordingInvoker()
	stubLanesNotConfigured(t, inv, f)
	deps := stellardeps.StellarDeps{Invoker: inv}

	b, reporter := lanesBundle(t)
	_, err := runStellarConfigureChainForLanes(b, deps, f.input(t, nil))
	require.NoError(t, err)

	reports, err := reporter.GetReports()
	require.NoError(t, err)

	trace := make([]opTraceEntry, 0, len(reports))
	for _, r := range reports {
		inputJSON, err := json.Marshal(r.Input)
		require.NoError(t, err)
		trace = append(trace, opTraceEntry{ID: r.Def.ID, Input: inputJSON})
	}

	got, err := json.MarshalIndent(trace, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	goldenPath := filepath.Join("testdata", "stellar_configure_lanes_op_trace.golden.json")
	if *updateGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "golden file missing; run with -update to create it")
	require.Equal(t, string(want), string(got))
}

// TestStellarConfigureChainForLanesIdempotent runs the sequence against an
// already-configured chain and expects only reads: no write op fires.
func TestStellarConfigureChainForLanesIdempotent(t *testing.T) {
	f := newLanesFixture(t)
	inv := operationstest.NewRecordingInvoker()
	stubLanesConfigured(t, inv, f)
	deps := stellardeps.StellarDeps{Invoker: inv}

	b, reporter := lanesBundle(t)
	// The CV signature quorum is now read-before-write too, so the full input
	// (verifiers included) must emit nothing but reads on a configured chain.
	_, err := runStellarConfigureChainForLanes(b, deps, f.input(t, nil))
	require.NoError(t, err)

	ids := lanesOpIDs(t, reporter)
	for _, id := range ids {
		require.NotContains(t, id, "apply", "configured chain must not emit a write, got op %s", id)
	}
}

// TestStellarConfigureChainForLanesGuards covers the sequence's fail-loud and
// sentinel behaviors, one stub scenario per guard.
func TestStellarConfigureChainForLanesGuards(t *testing.T) {
	f := newLanesFixture(t)

	t.Run("onramp override refused", func(t *testing.T) {
		inv := operationstest.NewRecordingInvoker()
		stubLanesNotConfigured(t, inv, f)
		// The router already routes this destination to a different OnRamp.
		current := scval.AddressToScVal(contractStrkey(t, 0x99))
		inv.WithSimulateResultForContract(f.routerID, "get_onramp", &current)

		b, _ := lanesBundle(t)
		_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, nil))
		require.ErrorContains(t, err, "refusing to overwrite")

		// With the override allowed the same state is accepted.
		b2, _ := lanesBundle(t)
		_, err = runStellarConfigureChainForLanes(b2, stellardeps.StellarDeps{Invoker: inv}, f.input(t, func(in *ccvadapters.ConfigureChainForLanesInput) {
			in.AllowOnrampOverride = true
		}))
		require.NoError(t, err)
	})

	t.Run("base execution gas cost lowering refused", func(t *testing.T) {
		inv := operationstest.NewRecordingInvoker()
		stubLanesNotConfigured(t, inv, f)
		// A valid current config (empty strkeys break the ScVal round-trip) with
		// a BaseExecutionGasCost above the desired 175_000.
		_, current, _, _ := f.lanesDesired(t)
		current.BaseExecutionGasCost = 200_000
		inv.WithSimulateResultForContract(f.onRampID, "get_dest_chain_config", lanesStructVal(t, current))

		b, _ := lanesBundle(t)
		_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, nil))
		require.ErrorIs(t, err, ErrBaseExecutionGasCostLowered)

		// With the lowering allowed the same state is accepted.
		b2, _ := lanesBundle(t)
		_, err = runStellarConfigureChainForLanes(b2, stellardeps.StellarDeps{Invoker: inv}, f.input(t, func(in *ccvadapters.ConfigureChainForLanesInput) {
			in.AllowLoweringBaseExecutionGasCost = true
		}))
		require.NoError(t, err)
	})

	t.Run("skip executor config writes sentinel and no executor ops", func(t *testing.T) {
		inv := operationstest.NewRecordingInvoker()
		stubLanesNotConfigured(t, inv, f)
		// Keep the executor stub in place anyway: the test asserts the
		// executor is never even read when SkipExecutorConfig is set.

		b, reporter := lanesBundle(t)
		_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, func(in *ccvadapters.ConfigureChainForLanesInput) {
			rc := in.RemoteChains[characterizationRemoteSel]
			rc.SkipExecutorConfig = true
			rc.DefaultExecutor = ""
			in.RemoteChains[characterizationRemoteSel] = rc
		}))
		require.NoError(t, err)

		ids := lanesOpIDs(t, reporter)
		require.False(t, lanesHasOp(ids, "executor:get-dest-chain-config"), "executor read must be skipped")
		require.False(t, lanesHasOp(ids, "executor:apply-dest-chain-updates"), "executor write must be skipped")

		reports, err := reporter.GetReports()
		require.NoError(t, err)
		for _, r := range reports {
			if r.Def.ID != "onramp:apply-dest-chain-config-updates" {
				continue
			}
			in, ok := r.Input.(onrampops.ApplyDestChainConfigUpdatesInput)
			require.True(t, ok, "unexpected input type %T", r.Input)
			require.Len(t, in.Updates, 1)
			require.Equal(t, stellarutil.NoExecutionExecutorStrkey, in.Updates[0].DefaultExecutor)
		}
	})

	t.Run("zero source onramps rejected", func(t *testing.T) {
		for name, onRamps := range map[string][][]byte{
			"empty element":  {{}},
			"all-zero bytes": {make([]byte, 32)},
		} {
			t.Run(name, func(t *testing.T) {
				inv := operationstest.NewRecordingInvoker()
				stubLanesNotConfigured(t, inv, f)

				b, _ := lanesBundle(t)
				_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, func(in *ccvadapters.ConfigureChainForLanesInput) {
					rc := in.RemoteChains[characterizationRemoteSel]
					rc.OnRamps = onRamps
					in.RemoteChains[characterizationRemoteSel] = rc
				}))
				require.ErrorIs(t, err, ErrZeroAddressNotAllowed)
			})
		}
	})

	t.Run("fee quoter config protected", func(t *testing.T) {
		inv := operationstest.NewRecordingInvoker()
		stubLanesNotConfigured(t, inv, f)
		// An already-enabled dest chain config on the FeeQuoter.
		_, _, _, fqCfg := f.lanesDesired(t)
		inv.WithSimulateResultForContract(f.feeQuoterID, "get_dest_chain_config", lanesStructVal(t, fqCfg))

		b, reporter := lanesBundle(t)
		_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, nil))
		require.NoError(t, err)

		ids := lanesOpIDs(t, reporter)
		require.False(t, lanesHasOp(ids, "fee-quoter:apply-dest-chain-configs"),
			"enabled fee quoter config must not be overwritten without OverrideExistingConfig")
	})

	t.Run("cv quorum written only on diff", func(t *testing.T) {
		inv := operationstest.NewRecordingInvoker()
		stubLanesNotConfigured(t, inv, f)
		// A different on-chain quorum (one signer, threshold 1) vs the desired
		// two signers, threshold 2 — the write must fire with the desired set.
		current := f.lanesDesiredQuorum()
		current.Signers = current.Signers[:1]
		current.Threshold = 1
		inv.WithSimulateResultForContract(f.cvID, "get_signature_config", lanesStructVal(t, current))

		b, reporter := lanesBundle(t)
		_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, nil))
		require.NoError(t, err)

		reports, err := reporter.GetReports()
		require.NoError(t, err)
		found := false
		for _, r := range reports {
			if r.Def.ID != "committee-verifier:apply-signature-configs" {
				continue
			}
			found = true
			in, ok := r.Input.(cvops.ApplySignatureConfigsInput)
			require.True(t, ok, "unexpected input type %T", r.Input)
			require.Empty(t, in.RemoveSelectors)
			require.Len(t, in.SignatureQuorumConfigs, 1)
			require.Equal(t, f.lanesDesiredQuorum(), in.SignatureQuorumConfigs[0])
		}
		require.True(t, found, "quorum diff must emit committee-verifier:apply-signature-configs")
	})

	t.Run("empty cv signature config skipped", func(t *testing.T) {
		inv := operationstest.NewRecordingInvoker()
		stubLanesNotConfigured(t, inv, f)
		// An empty SignatureConfig (no signers, zero threshold) means the caller
		// is not managing the quorum through this sequence — no CV ops at all.
		b, reporter := lanesBundle(t)
		_, err := runStellarConfigureChainForLanes(b, stellardeps.StellarDeps{Invoker: inv}, f.input(t, func(in *ccvadapters.ConfigureChainForLanesInput) {
			rc := in.CommitteeVerifiers[0].RemoteChains[characterizationRemoteSel]
			rc.SignatureConfig = ccvadapters.CommitteeVerifierSignatureQuorumConfig{}
			in.CommitteeVerifiers[0].RemoteChains[characterizationRemoteSel] = rc
		}))
		require.NoError(t, err)

		ids := lanesOpIDs(t, reporter)
		require.False(t, lanesHasOp(ids, "committee-verifier:get-signature-config"), "empty signature config must not read the CV quorum")
		require.False(t, lanesHasOp(ids, "committee-verifier:apply-signature-configs"), "empty signature config must not write the CV quorum")
	})
}
