package ccvchain

import (
	"testing"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	common "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeStellarSourceExtraArgsForOnRamp_rejectsEmptyVVRWhenNoCCVs(t *testing.T) {
	t.Parallel()
	_, err := EncodeStellarSourceExtraArgsForOnRamp("", cciptestinterfaces.MessageOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "versioned verifier resolver contract id is empty")
}

func TestEncodeStellarSourceExtraArgsForOnRamp_usesVVRWhenNoCCVs(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	vvr := stellarutil.MustGenerateMockContractID(kp.Address(), "vvr-path")
	out, err := EncodeStellarSourceExtraArgsForOnRamp(vvr, cciptestinterfaces.MessageOptions{
		ExecutionGasLimit: 10,
		FinalityConfig:    1,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out)
}

func TestEncodeStellarSourceExtraArgsForOnRamp_withCCVs(t *testing.T) {
	t.Parallel()
	ccvRaw := make(protocol.UnknownAddress, 32)
	for i := range ccvRaw {
		ccvRaw[i] = byte(i + 1)
	}
	out, err := EncodeStellarSourceExtraArgsForOnRamp("", cciptestinterfaces.MessageOptions{
		CCVs: []protocol.CCV{
			{CCVAddress: ccvRaw, Args: []byte{0x7, 0x8}},
		},
		ExecutionGasLimit: 5,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out)
}

// TestEncodeStellarSourceExtraArgsForOnRamp_roundTripsSentinelExecutor exercises
// the previously-untested opts.Executor branch (extra_args.go:61-67): a 32-byte
// executor sentinel supplied via MessageOptions.Executor must be strkey-encoded
// into the GenericExtraArgsV3.executor field and survive an XDR encode/decode
// round-trip. This is the off-chain wire entry point for the OnRamp's executor
// sentinels (M-5 use-default / M-7 no-execution) that the e2e sentinel tests
// rely on, so a round-trip here pins the Go→Soroban encoding without needing a
// live chain. The sentinel bytes match common/message NO_EXECUTION_TAG
// (0xeba517d2 + 28 zero bytes).
func TestEncodeStellarSourceExtraArgsForOnRamp_roundTripsSentinelExecutor(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	vvr := stellarutil.MustGenerateMockContractID(kp.Address(), "vvr")

	// 32-byte no-execution sentinel (tag 0xeba517d2 + 28 zero bytes).
	sentinel := append([]byte{0xeb, 0xa5, 0x17, 0xd2}, make([]byte, 28)...)
	require.Len(t, sentinel, 32)

	wantStrkey, err := strkey.Encode(strkey.VersionByteContract, sentinel)
	require.NoError(t, err)

	out, err := EncodeStellarSourceExtraArgsForOnRamp(vvr, cciptestinterfaces.MessageOptions{
		OutOfOrderExecution: true,
		Executor:            sentinel,
	})
	require.NoError(t, err)
	require.NotEmpty(t, out)

	// Decode the XDR back and assert the executor field round-trips the sentinel.
	var scVal xdr.ScVal
	require.NoError(t, xdr.SafeUnmarshal(out, &scVal))
	parsed, err := onrampbindings.GenericExtraArgsV3FromScVal(scVal)
	require.NoError(t, err)
	assert.Equal(t, wantStrkey, parsed.Executor,
		"sentinel executor must round-trip through XDR encode/decode")
}

// TestEncodeStellarSourceExtraArgsForOnRamp_defaultsToUseDefaultExecutorSentinel
// pins the default executor for normal sends (no opts.Executor): it must be the
// "use default executor" sentinel, which the OnRamp resolves to the lane's
// configured default_executor before Executor::get_fee. This is EVM address(0)
// parity and ensures the cross-contract get_fee call targets the deployed
// Executor contract instead of an uninitialized mock address.
func TestEncodeStellarSourceExtraArgsForOnRamp_defaultsToUseDefaultExecutorSentinel(t *testing.T) {
	t.Parallel()
	vvr := stellarutil.MustGenerateMockContractID(keypair.MustRandom().Address(), "vvr")
	out, err := EncodeStellarSourceExtraArgsForOnRamp(vvr, cciptestinterfaces.MessageOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, out)

	wantStrkey, err := common.ExecutorSentinelStrkey(common.UseDefaultExecutorAddressRaw)
	require.NoError(t, err)

	var scVal xdr.ScVal
	require.NoError(t, xdr.SafeUnmarshal(out, &scVal))
	parsed, err := onrampbindings.GenericExtraArgsV3FromScVal(scVal)
	require.NoError(t, err)
	assert.Equal(t, wantStrkey, parsed.Executor,
		"default executor must be the use-default sentinel so the OnRamp resolves it to the lane's deployed Executor")
}
