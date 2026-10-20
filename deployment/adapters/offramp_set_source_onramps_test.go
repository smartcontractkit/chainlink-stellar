package adapters

import (
	"context"
	"strings"
	"testing"

	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// offRampOnRampsTest carries the fixed facts one setter/reader core test needs.
type offRampOnRampsTest struct {
	inv       *operationstest.RecordingInvoker
	offRampID string
	deployer  string
	router    string
	cv        string
	timelock  string
	localSel  uint64
	sourceSel uint64
}

func newOffRampOnRampsTest(t *testing.T) offRampOnRampsTest {
	t.Helper()
	return offRampOnRampsTest{
		inv:       operationstest.NewRecordingInvoker(),
		offRampID: stellarutil.MustGenerateMockContractID("deployer", "offramp-set-onramps-test"),
		deployer:  stellarutil.MustGenerateMockContractID("deployer", "deployer-set-onramps-test"),
		router:    stellarutil.MustGenerateMockContractID("deployer", "router-set-onramps-test"),
		cv:        stellarutil.MustGenerateMockContractID("deployer", "cv-set-onramps-test"),
		timelock:  stellarutil.MustGenerateMockContractID("deployer", "timelock-set-onramps-test"),
		localSel:  chainsel.STELLAR_TESTNET.Selector,
		sourceSel: chainsel.STELLAR_MAINNET.Selector,
	}
}

// withCurrent stubs the get_source_chain_config read with cur.
func (o offRampOnRampsTest) withCurrent(t *testing.T, cur offrampbindings.SourceChainConfig) {
	t.Helper()
	val, err := cur.ToScVal()
	require.NoError(t, err)
	o.inv.WithSimulateResultForContract(o.offRampID, "get_source_chain_config", &val)
}

// withUnconfigured stubs the get_source_chain_config read as lane-not-configured.
func (o offRampOnRampsTest) withUnconfigured() {
	o.inv.WithSimulateErrorForContract(o.offRampID, "get_source_chain_config",
		operationstest.ContractErrorCode(stellarops.SourceChainNotEnabledCode))
}

// fixedReadOwner returns a readOwner fake that always reports owner.
func fixedReadOwner(owner string) func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
	return func(context.Context, stellardeps.StellarDeps, datastore.AddressRef) (string, error) {
		return owner, nil
	}
}

// set drives the setter core with the given owner read, refs, and onramp hex strings.
func (o offRampOnRampsTest) set(
	t *testing.T,
	owner string,
	refs []datastore.AddressRef,
	onRamps []string,
) (*mcmstypes.BatchOperation, bool, error) {
	t.Helper()
	return setOffRampSourceOnRamps(
		operationstest.NewBundle(t),
		stellardeps.StellarDeps{Invoker: o.inv},
		o.deployer,
		refs,
		fixedReadOwner(owner),
		o.offRampID,
		ccvadapters.OffRampSetSourceOnRampsEntry{
			LocalChainSelector:  o.localSel,
			SourceChainSelector: o.sourceSel,
			OnRamps:             onRamps,
		},
	)
}

// appliedArgs returns the decoded entry of the captured
// apply_source_chain_cfg_updates invoke, or nil when none was recorded. The
// client passes the entries as one ScVal vec, matching the contract's
// Vec<SourceChainConfigArgs> parameter.
func (o offRampOnRampsTest) appliedArgs(t *testing.T) *offrampbindings.SourceChainConfigArgs {
	t.Helper()
	for _, rec := range o.inv.Records() {
		if rec.ContractID != o.offRampID || rec.Fn != "apply_source_chain_cfg_updates" {
			continue
		}
		return decodeSourceChainConfigArgsVec(t, rec.Args[0])
	}
	return nil
}

// decodeSourceChainConfigArgsVec decodes one ScVal vec of SourceChainConfigArgs.
func decodeSourceChainConfigArgsVec(t *testing.T, v xdr.ScVal) *offrampbindings.SourceChainConfigArgs {
	t.Helper()
	vec, ok := v.GetVec()
	require.True(t, ok)
	require.NotNil(t, vec)
	require.Len(t, *vec, 1)
	args, err := offrampbindings.SourceChainConfigArgsFromScVal((*vec)[0])
	require.NoError(t, err)
	return args
}

func TestParseOffRampSourceOnRampAddresses(t *testing.T) {
	t.Parallel()

	t.Run("bytes are taken as given", func(t *testing.T) {
		t.Parallel()
		// A stellar source's onramp is the 40-byte Address::to_xdr envelope;
		// an EVM source's is abi.encode(address). Neither is reshaped.
		envelope := "0x" + strings.Repeat("ab", 40)
		evmPadded := "0x" + strings.Repeat("00", 12) + strings.Repeat("5e", 20)
		out, err := parseOffRampSourceOnRampAddresses([]string{envelope, evmPadded})
		require.NoError(t, err)
		require.Len(t, out, 2)
		require.Len(t, out[0], 40)
		require.Len(t, out[1], 32)
	})

	t.Run("raw contract ID is not wrapped into the envelope", func(t *testing.T) {
		t.Parallel()
		raw := "0x" + strings.Repeat("a1", 32)
		out, err := parseOffRampSourceOnRampAddresses([]string{raw})
		require.NoError(t, err)
		require.Len(t, out, 1)
		require.Len(t, out[0], 32, "a 32-byte input must not be widened to the 40-byte envelope")
	})

	t.Run("dedupes", func(t *testing.T) {
		t.Parallel()
		addr := "0x" + strings.Repeat("ab", 40)
		out, err := parseOffRampSourceOnRampAddresses([]string{addr, addr})
		require.NoError(t, err)
		require.Len(t, out, 1)
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		for _, addr := range []string{"0x", "nope", "ababab", "0x" + strings.Repeat("ab", 256)} {
			_, err := parseOffRampSourceOnRampAddresses([]string{addr})
			require.Error(t, err, addr)
		}
	})
}

func TestSetOffRampSourceOnRamps_SkipsWhenMatching(t *testing.T) {
	t.Parallel()
	desired := [][]byte{onRampBytes(40, 0xAA), onRampBytes(40, 0xBB)}
	o := newOffRampOnRampsTest(t)
	o.withCurrent(t, offrampbindings.SourceChainConfig{Router: o.router, IsEnabled: true, OnRamps: desired})

	// Same set, same order.
	batchOp, skipped, err := o.set(t, o.deployer, nil, []string{"0x" + strings.Repeat("aa", 40), "0x" + strings.Repeat("bb", 40)})
	require.NoError(t, err)
	require.True(t, skipped)
	require.Nil(t, batchOp)

	// Same set, different order: the OffRamp whitelist is a set.
	o2 := newOffRampOnRampsTest(t)
	o2.withCurrent(t, offrampbindings.SourceChainConfig{Router: o2.router, IsEnabled: true, OnRamps: desired})
	batchOp, skipped, err = o2.set(t, o2.deployer, nil, []string{"0x" + strings.Repeat("bb", 40), "0x" + strings.Repeat("aa", 40)})
	require.NoError(t, err)
	require.True(t, skipped)
	require.Nil(t, batchOp)
}

func TestSetOffRampSourceOnRamps_DirectExecPreservesFields(t *testing.T) {
	t.Parallel()
	o := newOffRampOnRampsTest(t)
	o.withCurrent(t, offrampbindings.SourceChainConfig{
		Router:           o.router,
		IsEnabled:        true,
		DefaultCcvs:      []string{o.cv},
		LaneMandatedCcvs: []string{},
		OnRamps:          [][]byte{onRampBytes(40, 0x11)},
	})

	batchOp, skipped, err := o.set(t, o.deployer, nil, []string{"0x" + strings.Repeat("aa", 40)})
	require.NoError(t, err)
	require.False(t, skipped)
	require.NotNil(t, batchOp)
	// Direct-exec marker: non-nil op the changeset can dereference, zero
	// transactions so the shared OutputBuilder drops it.
	require.Empty(t, batchOp.Transactions)

	args := o.appliedArgs(t)
	require.NotNil(t, args, "the direct exec must invoke apply_source_chain_cfg_updates")
	require.Equal(t, o.router, args.Router)
	require.Equal(t, o.sourceSel, args.SourceChainSelector)
	require.True(t, args.IsEnabled)
	require.Equal(t, []string{o.cv}, args.DefaultCcvs)
	require.Empty(t, args.LaneMandatedCcvs)
	require.Equal(t, [][]byte{onRampBytes(40, 0xAA)}, args.OnRamps)
}

func TestSetOffRampSourceOnRamps_TimelockProposal(t *testing.T) {
	t.Parallel()
	o := newOffRampOnRampsTest(t)
	o.withCurrent(t, offrampbindings.SourceChainConfig{
		Router:           o.router,
		IsEnabled:        true,
		DefaultCcvs:      []string{o.cv},
		LaneMandatedCcvs: []string{},
		OnRamps:          [][]byte{onRampBytes(40, 0x11)},
	})
	refs := []datastore.AddressRef{mcmsutil.StellarTimelockDatastoreRef(o.localSel, cciputils.CLLQualifier, o.timelock)}

	batchOp, skipped, err := o.set(t, o.timelock, refs, []string{"0x" + strings.Repeat("aa", 40)})
	require.NoError(t, err)
	require.False(t, skipped)
	require.NotNil(t, batchOp)
	require.Len(t, batchOp.Transactions, 1)
	tx := batchOp.Transactions[0]
	require.Equal(t, o.offRampID, tx.To)
	require.Contains(t, string(tx.AdditionalFields), `"family":"stellar"`)
	require.Nil(t, o.appliedArgs(t), "a timelock-owned OffRamp must not be written directly")

	fn, scArgs, err := mcmsutil.DecodeSorobanMCMSInvokePayload(tx.Data)
	require.NoError(t, err)
	require.Equal(t, "apply_source_chain_cfg_updates", fn)
	require.Len(t, scArgs, 1)
	args := decodeSourceChainConfigArgsVec(t, scArgs[0])
	require.Equal(t, [][]byte{onRampBytes(40, 0xAA)}, args.OnRamps)
	require.Equal(t, o.router, args.Router)
}

func TestSetOffRampSourceOnRamps_FailsClosed(t *testing.T) {
	t.Parallel()
	o := newOffRampOnRampsTest(t)
	o.withCurrent(t, offrampbindings.SourceChainConfig{Router: o.router, IsEnabled: true, OnRamps: [][]byte{onRampBytes(40, 0x11)}})
	thirdParty := stellarutil.MustGenerateMockContractID("deployer", "third-party-set-onramps-test")
	refs := []datastore.AddressRef{mcmsutil.StellarTimelockDatastoreRef(o.localSel, cciputils.CLLQualifier, o.timelock)}

	_, _, err := o.set(t, thirdParty, refs, []string{"0x" + strings.Repeat("aa", 40)})
	require.ErrorContains(t, err, "is neither the deployer")
	require.Nil(t, o.appliedArgs(t))
}

func TestSetOffRampSourceOnRamps_UnconfiguredLane(t *testing.T) {
	t.Parallel()
	o := newOffRampOnRampsTest(t)
	o.withUnconfigured()

	_, _, err := o.set(t, o.deployer, nil, []string{"0x" + strings.Repeat("aa", 40)})
	require.ErrorContains(t, err, "configure the lane first")
	require.Nil(t, o.appliedArgs(t))
}

func TestSetOffRampSourceOnRamps_ReadErrorPropagates(t *testing.T) {
	t.Parallel()
	o := newOffRampOnRampsTest(t)
	o.inv.WithSimulateErrorForContract(o.offRampID, "get_source_chain_config", operationstest.ContractErrorCode(1))

	_, _, err := o.set(t, o.deployer, nil, []string{"0x" + strings.Repeat("aa", 40)})
	require.ErrorContains(t, err, "get source chain config")
}

func TestGetOffRampSourceOnRamps(t *testing.T) {
	t.Parallel()
	o := newOffRampOnRampsTest(t)

	t.Run("configured returns the whitelist", func(t *testing.T) {
		t.Parallel()
		want := [][]byte{onRampBytes(40, 0xAA), onRampBytes(40, 0xBB)}
		o.withCurrent(t, offrampbindings.SourceChainConfig{Router: o.router, OnRamps: want})
		got, err := getOffRampSourceOnRamps(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: o.inv}, o.offRampID, o.sourceSel)
		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("unconfigured reads as empty", func(t *testing.T) {
		t.Parallel()
		o2 := newOffRampOnRampsTest(t)
		o2.withUnconfigured()
		got, err := getOffRampSourceOnRamps(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: o2.inv}, o2.offRampID, o2.sourceSel)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("other error propagates", func(t *testing.T) {
		t.Parallel()
		o3 := newOffRampOnRampsTest(t)
		o3.inv.WithSimulateErrorForContract(o3.offRampID, "get_source_chain_config", operationstest.ContractErrorCode(1))
		_, err := getOffRampSourceOnRamps(operationstest.NewBundle(t), stellardeps.StellarDeps{Invoker: o3.inv}, o3.offRampID, o3.sourceSel)
		require.ErrorContains(t, err, "get source chain config")
	})
}

// TestStellarChainFamilyAdapter_OffRampSourceOnRampInterfaces pins the
// interface compliance and that the globally registered ChainFamily instance
// type-asserts to both, which is how the offramp_set_source_onramps changeset
// discovers it.
func TestStellarChainFamilyAdapter_OffRampSourceOnRampInterfaces(t *testing.T) {
	t.Parallel()
	var _ ccvadapters.OffRampSourceOnRampSetter = (*StellarChainFamilyAdapter)(nil)
	var _ ccvadapters.OffRampSourceOnRampReader = (*StellarChainFamilyAdapter)(nil)

	family, ok := ccvadapters.GetChainFamilyRegistry().GetChainFamily(chainsel.FamilyStellar)
	require.True(t, ok)
	_, ok = family.(ccvadapters.OffRampSourceOnRampSetter)
	require.True(t, ok, "registered ChainFamily must implement OffRampSourceOnRampSetter")
	_, ok = family.(ccvadapters.OffRampSourceOnRampReader)
	require.True(t, ok, "registered ChainFamily must implement OffRampSourceOnRampReader")

	// The nil seam must default to the real ownership read.
	a := &StellarChainFamilyAdapter{}
	require.NotNil(t, a.readOwnerFn())
}

// onRampBytes returns n bytes of value v — a stand-in onramp wire entry. The
// hex strings Repeat("XY", n) parse to exactly these bytes.
func onRampBytes(n int, v byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = v
	}
	return out
}
