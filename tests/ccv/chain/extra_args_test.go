package ccvchain

import (
	"testing"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stellar/go-stellar-sdk/keypair"
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
