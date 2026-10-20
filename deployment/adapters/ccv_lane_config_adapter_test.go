package adapters

import (
	"bytes"
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"

	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

var (
	stLaneLocalSel  = chainsel.STELLAR_TESTNET.Selector
	stLaneRemoteSel = chainsel.STELLAR_MAINNET.Selector
)

// stLaneContracts are the local-chain strkeys the seeded refs carry.
type stLaneContracts struct {
	router, onRamp, offRamp, feeQuoter, executor, verifier, resolver string
}

// stLaneSeed seeds the local chain's refs via the stellarccip record helpers
// and fetches them back, so the refs carry exactly the shapes deployment
// records produce.
func stLaneSeed(t *testing.T, sel uint64) (stLaneContracts, []datastore.AddressRef) {
	t.Helper()
	c := stLaneContracts{
		router:    stellarutil.MustGenerateMockContractID("deployer", "router-lane-config-test"),
		onRamp:    stellarutil.MustGenerateMockContractID("deployer", "onramp-lane-config-test"),
		offRamp:   stellarutil.MustGenerateMockContractID("deployer", "offramp-lane-config-test"),
		feeQuoter: stellarutil.MustGenerateMockContractID("deployer", "fee-quoter-lane-config-test"),
		executor:  stellarutil.MustGenerateMockContractID("deployer", "executor-lane-config-test"),
		verifier:  stellarutil.MustGenerateMockContractID("deployer", "verifier-lane-config-test"),
		resolver:  stellarutil.MustGenerateMockContractID("deployer", "resolver-lane-config-test"),
	}
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, stellarccip.RecordRouter(ds, sel, c.router))
	require.NoError(t, stellarccip.RecordOnRamp(ds, sel, c.onRamp))
	require.NoError(t, stellarccip.RecordOffRamp(ds, sel, c.offRamp))
	require.NoError(t, stellarccip.RecordFeeQuoter(ds, sel, c.feeQuoter))
	require.NoError(t, stellarccip.RecordCommitteeVerifier(ds, sel, c.verifier))
	require.NoError(t, stellarccip.RecordVVR(ds, sel, c.resolver))
	require.NoError(t, stellarccip.ExecutorProxyDatastoreRef(defaultQualifier).UpsertDeployedStrKey(ds, sel, c.executor))
	refs, err := ds.Addresses().Fetch()
	require.NoError(t, err)
	return c, refs
}

// stLaneHex returns the datastore hex form of a strkey.
func stLaneHex(t *testing.T, cid string) string {
	t.Helper()
	hexAddr, err := stellarutil.StrkeyToHex(cid)
	require.NoError(t, err)
	return hexAddr
}

// stLaneRaw returns the raw 32-byte contract ID of a strkey.
func stLaneRaw(t *testing.T, cid string) []byte {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, cid)
	require.NoError(t, err)
	require.Len(t, raw, 32)
	return raw
}

// stFakeRemoteFamily overrides the remote-side default providers the
// translation consults, embedding the stellar adapter for the rest, so tests
// can pin which family the remote defaults come from.
type stFakeRemoteFamily struct {
	*StellarChainFamilyAdapter
	selector [4]byte
}

func (f *stFakeRemoteFamily) GetChainFamilySelector() [4]byte { return f.selector }

func (f *stFakeRemoteFamily) GetAddressBytesLength() uint8 { return 20 }

func (f *stFakeRemoteFamily) GetDefaultRemoteChainConfig(_, _ uint64) ccvadapters.RemoteChainDefaults {
	return ccvadapters.RemoteChainDefaults{
		AllowTrafficFrom:          false,
		ExecutorDestChainConfig:   ccvadapters.ExecutorDestChainConfig{USDCentsFee: 77, Enabled: false},
		SkipExecutorConfig:        true,
		BaseExecutionGasCost:      4242,
		TokenReceiverAllowed:      true,
		MessageNetworkFeeUSDCents: 4321,
		TokenNetworkFeeUSDCents:   8765,
	}
}

func (f *stFakeRemoteFamily) GetDefaultFeeQuoterDestChainConfig(_, _ uint64, _ [4]byte) ccvadapters.FeeQuoterDestChainConfigOverrides {
	return ccvadapters.FeeQuoterDestChainConfigOverrides{
		IsEnabled:           ptr(false),
		MaxDataBytes:        ptr(uint32(9999)),
		ChainFamilySelector: f.selector,
	}
}

// stLaneAdapter builds the adapter with the remote-family seam returning remote.
func stLaneAdapter(remote ccvadapters.ChainFamily) *StellarCCVLaneConfigAdapter {
	return &StellarCCVLaneConfigAdapter{remoteChainFamily: func(string) (ccvadapters.ChainFamily, bool) {
		return remote, true
	}}
}

func stLaneInput(refs []datastore.AddressRef, mutate func(*ccvdeploymentadapters.LaneConfigInput)) ccvdeploymentadapters.LaneConfigInput {
	in := ccvdeploymentadapters.LaneConfigInput{
		ChainSelector:     stLaneLocalSel,
		ExistingAddresses: refs,
		RemoteChains: map[uint64]ccvdeploymentadapters.RemoteLaneConfig{
			stLaneRemoteSel: {
				ExecutorQualifier:     defaultQualifier,
				InboundCCVQualifiers:  []string{defaultQualifier},
				OutboundCCVQualifiers: []string{defaultQualifier},
				// Remote ramps are supplied pre-resolved by the changeset; the
				// translation threads them through unchanged.
				RemoteOnRamps: [][]byte{bytes.Repeat([]byte{0xA1}, 40)},
				RemoteOffRamp: bytes.Repeat([]byte{0xB2}, 20),
			},
		},
	}
	if mutate != nil {
		mutate(&in)
	}
	return in
}

// TestStellarLaneConfigAdapter_HappyPath proves the topology-free
// LaneConfigInput is resolved into the stellar ConfigureChainForLanesInput:
// local contracts by chain (OnRamp in its 40-byte wire envelope, the rest as
// raw 32-byte IDs), executor and committee-verifier (resolver) addresses by
// qualifier, the pre-resolved remote ramps threaded through, and the
// remote-side defaults coming from the remote chain family's adapter.
func TestStellarLaneConfigAdapter_HappyPath(t *testing.T) {
	t.Parallel()
	c, refs := stLaneSeed(t, stLaneLocalSel)
	fake := &stFakeRemoteFamily{StellarChainFamilyAdapter: &StellarChainFamilyAdapter{}, selector: [4]byte{0x0A, 0x0B, 0x0C, 0x0D}}
	a := stLaneAdapter(fake)

	onRampWire, err := scval.ContractAddressWireBytes(stLaneRaw(t, c.onRamp))
	require.NoError(t, err)

	in := stLaneInput(refs, nil)
	out, err := a.toConfigureChainForLanesInput(in)
	require.NoError(t, err)

	require.Equal(t, stLaneRaw(t, c.router), out.Router)
	require.Equal(t, onRampWire, out.OnRamp)
	require.Len(t, out.OnRamp, 40)
	require.Equal(t, stLaneRaw(t, c.offRamp), out.OffRamp)
	require.Equal(t, stLaneRaw(t, c.feeQuoter), out.FeeQuoter)
	require.False(t, out.AllowOnrampOverride)

	rc, ok := out.RemoteChains[stLaneRemoteSel]
	require.True(t, ok)
	require.Equal(t, c.executor, rc.DefaultExecutor)
	require.Equal(t, []string{stLaneHex(t, c.resolver)}, rc.DefaultInboundCCVs)
	require.Equal(t, []string{stLaneHex(t, c.resolver)}, rc.DefaultOutboundCCVs)
	require.Equal(t, in.RemoteChains[stLaneRemoteSel].RemoteOnRamps, rc.OnRamps)
	require.Equal(t, in.RemoteChains[stLaneRemoteSel].RemoteOffRamp, rc.OffRamp)
	// Remote-side defaults come from the remote family's adapter.
	require.Equal(t, fake.selector, rc.FeeQuoterDestChainConfig.ChainFamilySelector)
	require.Equal(t, uint8(20), rc.AddressBytesLength)
	require.True(t, rc.SkipExecutorConfig)
	require.Equal(t, uint32(4242), rc.BaseExecutionGasCost)
	require.Equal(t, ccvadapters.ExecutorDestChainConfig{USDCentsFee: 77, Enabled: false}, rc.ExecutorDestChainConfig)
	require.NotNil(t, rc.AllowTrafficFrom)
	require.False(t, *rc.AllowTrafficFrom)
	require.NotNil(t, rc.TokenReceiverAllowed)
	require.True(t, *rc.TokenReceiverAllowed)
	require.Equal(t, uint16(4321), rc.MessageNetworkFeeUSDCents)
	require.Equal(t, uint16(8765), rc.TokenNetworkFeeUSDCents)
	require.NotNil(t, rc.FeeQuoterDestChainConfig.IsEnabled)
	require.False(t, *rc.FeeQuoterDestChainConfig.IsEnabled)
	require.Equal(t, uint32(9999), *rc.FeeQuoterDestChainConfig.MaxDataBytes)

	require.Len(t, out.CommitteeVerifiers, 1)
	cv := out.CommitteeVerifiers[0]
	require.Len(t, cv.CommitteeVerifier, 2) // [verifier, resolver]
	require.NotZero(t, cv.AllowedFinalityConfig, "the family default finality config must reach the sequence's allowed-finality write")
	cvRemote, ok := cv.RemoteChains[stLaneRemoteSel]
	require.True(t, ok)
	require.Empty(t, cvRemote.SignatureConfig.Signers, "no signers supplied → quorum untouched")
	require.Zero(t, cvRemote.SignatureConfig.Threshold)
	require.Equal(t, laneChainFamily.GetDefaultCommitteeVerifierRemoteChainConfig().GasForVerification, cvRemote.GasForVerification)
}

// TestStellarLaneConfigAdapter_DefaultSeamUsesRegistry proves the nil seam
// resolves the remote family's adapter from the ccip ChainFamily registry,
// which this package's init populated with the stellar adapter.
func TestStellarLaneConfigAdapter_DefaultSeamUsesRegistry(t *testing.T) {
	t.Parallel()
	c, refs := stLaneSeed(t, stLaneLocalSel)
	a := &StellarCCVLaneConfigAdapter{}
	out, err := a.toConfigureChainForLanesInput(stLaneInput(refs, nil))
	require.NoError(t, err)
	rc := out.RemoteChains[stLaneRemoteSel]
	require.Equal(t, laneChainFamily.GetChainFamilySelector(), rc.FeeQuoterDestChainConfig.ChainFamilySelector)
	require.Equal(t, laneChainFamily.GetAddressBytesLength(), rc.AddressBytesLength)
	require.Equal(t, laneChainFamily.GetDefaultRemoteChainConfig(stLaneLocalSel, stLaneRemoteSel).BaseExecutionGasCost, rc.BaseExecutionGasCost)
	require.Equal(t, c.executor, rc.DefaultExecutor)
}

// TestStellarLaneConfigAdapter_UseTestRouter proves the test and production
// routers resolve to the same address on Stellar (there is no separate
// TestRouter), so a test→prod promotion re-run is idempotent.
func TestStellarLaneConfigAdapter_UseTestRouter(t *testing.T) {
	t.Parallel()
	_, refs := stLaneSeed(t, stLaneLocalSel)
	a := stLaneAdapter(&stFakeRemoteFamily{StellarChainFamilyAdapter: &StellarChainFamilyAdapter{}})

	prod, err := a.toConfigureChainForLanesInput(stLaneInput(refs, nil))
	require.NoError(t, err)
	test, err := a.toConfigureChainForLanesInput(stLaneInput(refs, func(in *ccvdeploymentadapters.LaneConfigInput) {
		in.UseTestRouter = true
	}))
	require.NoError(t, err)
	require.Equal(t, prod.Router, test.Router)
}

// TestStellarLaneConfigAdapter_OverridesCoalesce proves caller overrides on
// the RemoteLaneConfig take precedence over the remote family defaults.
func TestStellarLaneConfigAdapter_OverridesCoalesce(t *testing.T) {
	t.Parallel()
	_, refs := stLaneSeed(t, stLaneLocalSel)
	a := stLaneAdapter(&stFakeRemoteFamily{StellarChainFamilyAdapter: &StellarChainFamilyAdapter{}})

	allow, gas := true, uint32(123456)
	out, err := a.toConfigureChainForLanesInput(stLaneInput(refs, func(in *ccvdeploymentadapters.LaneConfigInput) {
		rc := in.RemoteChains[stLaneRemoteSel]
		rc.AllowTrafficFrom = &allow
		rc.BaseExecutionGasCost = &gas
		in.RemoteChains[stLaneRemoteSel] = rc
	}))
	require.NoError(t, err)
	rc := out.RemoteChains[stLaneRemoteSel]
	require.NotNil(t, rc.AllowTrafficFrom)
	require.True(t, *rc.AllowTrafficFrom)
	require.Equal(t, gas, rc.BaseExecutionGasCost)
}

// TestStellarLaneConfigAdapter_InboundSigners proves the committee verifier
// signature quorum is set when InboundSigners is supplied and left empty
// otherwise.
func TestStellarLaneConfigAdapter_InboundSigners(t *testing.T) {
	t.Parallel()
	_, refs := stLaneSeed(t, stLaneLocalSel)
	a := stLaneAdapter(&stFakeRemoteFamily{StellarChainFamilyAdapter: &StellarChainFamilyAdapter{}})
	signer := "0x00000000000000000000000000000000000000Cd"

	out, err := a.toConfigureChainForLanesInput(stLaneInput(refs, func(in *ccvdeploymentadapters.LaneConfigInput) {
		rc := in.RemoteChains[stLaneRemoteSel]
		rc.InboundSigners = []string{signer}
		rc.InboundThreshold = 1
		in.RemoteChains[stLaneRemoteSel] = rc
	}))
	require.NoError(t, err)
	sigCfg := out.CommitteeVerifiers[0].RemoteChains[stLaneRemoteSel].SignatureConfig
	require.Equal(t, []string{signer}, sigCfg.Signers)
	require.Equal(t, uint8(1), sigCfg.Threshold)

	// The adapter passes signers through unpadded and unsorted: the sequence
	// owns the 32-byte left padding and strictly-ascending order.
	def, err := a.toConfigureChainForLanesInput(stLaneInput(refs, nil))
	require.NoError(t, err)
	require.Empty(t, def.CommitteeVerifiers[0].RemoteChains[stLaneRemoteSel].SignatureConfig.Signers)
}

// TestStellarLaneConfigAdapter_SignerValidation mirrors EVM's
// validateInboundSignatureQuorum error table.
func TestStellarLaneConfigAdapter_SignerValidation(t *testing.T) {
	t.Parallel()
	signer := "0x00000000000000000000000000000000000000Cd"
	tests := []struct {
		name      string
		signers   []string
		threshold uint8
		wantErr   string
	}{
		{name: "not hex", signers: []string{"not-a-hex"}, threshold: 1, wantErr: "is not a valid hex address"},
		{name: "zero threshold", signers: []string{signer}, threshold: 0, wantErr: "InboundThreshold must be greater than 0"},
		{name: "threshold exceeds signers", signers: []string{signer}, threshold: 2, wantErr: "exceeds the number of InboundSigners"},
		{name: "empty signers ignores threshold", signers: nil, threshold: 0, wantErr: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateInboundSignatureQuorum(tc.signers, tc.threshold)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

// TestStellarLaneConfigAdapter_DefaultQualifiers proves omitted CCV
// qualifiers auto-resolve the "default" committee verifier, and inbound and
// outbound qualifier lists dedupe into one CommitteeVerifiers entry.
func TestStellarLaneConfigAdapter_DefaultQualifiers(t *testing.T) {
	t.Parallel()
	c, refs := stLaneSeed(t, stLaneLocalSel)
	a := stLaneAdapter(&stFakeRemoteFamily{StellarChainFamilyAdapter: &StellarChainFamilyAdapter{}})

	out, err := a.toConfigureChainForLanesInput(stLaneInput(refs, func(in *ccvdeploymentadapters.LaneConfigInput) {
		rc := in.RemoteChains[stLaneRemoteSel]
		rc.InboundCCVQualifiers = nil
		rc.OutboundCCVQualifiers = nil
		in.RemoteChains[stLaneRemoteSel] = rc
	}))
	require.NoError(t, err)
	rc := out.RemoteChains[stLaneRemoteSel]
	require.Equal(t, []string{stLaneHex(t, c.resolver)}, rc.DefaultInboundCCVs)
	require.Equal(t, []string{stLaneHex(t, c.resolver)}, rc.DefaultOutboundCCVs)
	require.Len(t, out.CommitteeVerifiers, 1, "default committee verifier configured")
}

// TestStellarLaneConfigAdapter_MissingRemoteAdapter errors when the remote
// family has no registered adapter.
func TestStellarLaneConfigAdapter_MissingRemoteAdapter(t *testing.T) {
	t.Parallel()
	_, refs := stLaneSeed(t, stLaneLocalSel)
	a := &StellarCCVLaneConfigAdapter{remoteChainFamily: func(string) (ccvadapters.ChainFamily, bool) {
		return nil, false
	}}
	_, err := a.toConfigureChainForLanesInput(stLaneInput(refs, nil))
	require.ErrorContains(t, err, "no chain family adapter registered for remote chain family")
}

// TestStellarLaneConfigAdapter_Errors covers the resolution error branches.
func TestStellarLaneConfigAdapter_Errors(t *testing.T) {
	t.Parallel()
	c, refs := stLaneSeed(t, stLaneLocalSel)
	without := func(hexAddr string) []datastore.AddressRef {
		out := make([]datastore.AddressRef, 0, len(refs))
		for _, r := range refs {
			if r.Address == hexAddr {
				continue
			}
			out = append(out, r)
		}
		return out
	}
	tests := []struct {
		name       string
		input      ccvdeploymentadapters.LaneConfigInput
		wantErrSub string
	}{
		{name: "missing local Router", input: stLaneInput(without(stLaneHex(t, c.router)), nil), wantErrSub: "resolve local Router"},
		{name: "missing local OnRamp", input: stLaneInput(without(stLaneHex(t, c.onRamp)), nil), wantErrSub: "resolve local OnRamp"},
		{name: "missing local OffRamp", input: stLaneInput(without(stLaneHex(t, c.offRamp)), nil), wantErrSub: "resolve local OffRamp"},
		{name: "missing local FeeQuoter", input: stLaneInput(without(stLaneHex(t, c.feeQuoter)), nil), wantErrSub: "resolve local FeeQuoter"},
		{name: "missing executor", input: stLaneInput(without(stLaneHex(t, c.executor)), nil), wantErrSub: "resolve executor"},
		{name: "missing committee verifier resolver", input: stLaneInput(without(stLaneHex(t, c.resolver)), nil), wantErrSub: "resolver for qualifier"},
		{name: "missing committee verifier", input: stLaneInput(without(stLaneHex(t, c.verifier)), nil), wantErrSub: "resolve committee verifier"},
		{
			name: "invalid inbound signer",
			input: stLaneInput(refs, func(in *ccvdeploymentadapters.LaneConfigInput) {
				rc := in.RemoteChains[stLaneRemoteSel]
				rc.InboundSigners = []string{"not-a-hex"}
				in.RemoteChains[stLaneRemoteSel] = rc
			}),
			wantErrSub: "is not a valid hex address",
		},
	}
	a := stLaneAdapter(&stFakeRemoteFamily{StellarChainFamilyAdapter: &StellarChainFamilyAdapter{}})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := a.toConfigureChainForLanesInput(tc.input)
			require.ErrorContains(t, err, tc.wantErrSub)
		})
	}
}

// TestStellarLaneConfigAdapter_DirectExecBatchShape pins the ExecInfo-marked
// empty batch op the sequence returns for a direct-exec (deployer-key) run:
// zero transactions, so the shared OutputBuilder drops it (parity doc open
// question 7).
func TestStellarLaneConfigAdapter_DirectExecBatchShape(t *testing.T) {
	t.Parallel()
	c, _ := stLaneSeed(t, stLaneLocalSel)
	op, err := laneConfigDirectExecBatchOp(stLaneLocalSel, c.router)
	require.NoError(t, err)
	require.Empty(t, op.Transactions)
}

// TestStellarLaneConfigAdapter_Sequence pins the wrapper sequence identity.
func TestStellarLaneConfigAdapter_Sequence(t *testing.T) {
	t.Parallel()
	a := &StellarCCVLaneConfigAdapter{}
	seq := a.ConfigureLane()
	require.NotNil(t, seq)
	require.Equal(t, "stellar-configure-lane", seq.ID())
	_, err := a.GetOnRampAddress(datastore.NewMemoryDataStore().Seal(), 1)
	require.Error(t, err)
	_, err = a.GetOffRampAddress(datastore.NewMemoryDataStore().Seal(), 1)
	require.Error(t, err)
}
