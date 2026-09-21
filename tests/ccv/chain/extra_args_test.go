package ccvchain

import (
	"testing"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeStellarSourceExtraArgsForOnRamp_rejectsEmptyVVRWhenNoCCVs(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	_, err := EncodeStellarSourceExtraArgsForOnRamp(kp.Address(), "", cciptestinterfaces.MessageOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "versioned verifier resolver contract id is empty")
}

func TestEncodeStellarSourceExtraArgsForOnRamp_usesVVRWhenNoCCVs(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	vvr := stellarutil.MustGenerateMockContractID(kp.Address(), "vvr-path")
	out, err := EncodeStellarSourceExtraArgsForOnRamp(kp.Address(), vvr, cciptestinterfaces.MessageOptions{
		ExecutionGasLimit: 10,
		FinalityConfig:    1,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, out)
}

func TestEncodeStellarSourceExtraArgsForOnRamp_withCCVs(t *testing.T) {
	t.Parallel()
	kp := keypair.MustRandom()
	ccvRaw := make(protocol.UnknownAddress, 32)
	for i := range ccvRaw {
		ccvRaw[i] = byte(i + 1)
	}
	out, err := EncodeStellarSourceExtraArgsForOnRamp(kp.Address(), "", cciptestinterfaces.MessageOptions{
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

	out, err := EncodeStellarSourceExtraArgsForOnRamp(kp.Address(), vvr, cciptestinterfaces.MessageOptions{
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
