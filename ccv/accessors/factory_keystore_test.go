package accessors

import (
	"context"
	"strconv"
	"testing"
	"time"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/keystore"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
)

var stellarSelector = chainsel.STELLAR_LOCALNET.Selector

// fakeKeystoreSetter exists so we can confirm a non-Stellar accessor wouldn't
// be touched by the registry — not used directly here, but documents intent.

func newDestFactoryUnderTest(t *testing.T) (*factory, string) {
	t.Helper()
	stellarKey := strconv.FormatUint(stellarSelector, 10)
	f := &factory{
		lggr: logger.Test(t),
		readerConfig: map[string]sourcereader.ReaderConfig{
			stellarKey: {
				SorobanRPCURL:       "http://localhost:8000",
				NetworkPassphrase:   testNetworkPassphrase,
				OnRampContractID:    "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
				RMNRemoteContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
			},
		},
		destConfig: map[string]destinationConfig{
			stellarKey: {
				offRampContractID:   "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
				rmnRemoteContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
				stateChangedTopic:   "offramp_1_7_ExecStateChanged",
				keyName:             "stellar/tx/dest-test",
			},
		},
		attemptCacheExpiration: 5 * time.Minute,
	}
	return f, stellarKey
}

func TestAccessor_SetKeystore_NilKeystore(t *testing.T) {
	ctx := context.Background()
	f, _ := newDestFactoryUnderTest(t)
	accIface, err := f.GetAccessor(ctx, protocol.ChainSelector(stellarSelector))
	require.NoError(t, err)
	acc, ok := accIface.(*accessor)
	require.True(t, ok)

	// Calling SetKeystore(nil) must be a no-op (no panic, no state change). The
	// destination components should remain unbuilt and report errKeystoreNotInjected.
	require.NoError(t, acc.SetKeystore(ctx, nil))

	_, err = acc.DestinationReader()
	require.Error(t, err)
	assert.ErrorIs(t, err, errKeystoreNotInjected)

	_, err = acc.ContractTransmitter()
	require.Error(t, err)
	assert.ErrorIs(t, err, errKeystoreNotInjected)
}

func TestAccessor_SetKeystore_MissingKeyFailsGetters(t *testing.T) {
	ctx := context.Background()
	f, _ := newDestFactoryUnderTest(t)
	accIface, err := f.GetAccessor(ctx, protocol.ChainSelector(stellarSelector))
	require.NoError(t, err)
	acc := accIface.(*accessor)

	// Empty keystore: SetKeystore should record the load error against every
	// keystore-backed getter so callers see a consistent root cause.
	ks := newTestKeystore(t)
	require.NoError(t, acc.SetKeystore(ctx, ks))

	_, err = acc.DestinationReader()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load stellar keystore signer")
	_, err = acc.ContractTransmitter()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load stellar keystore signer")
	_, err = acc.SourceReader()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load stellar keystore signer")
}

func TestAccessor_SetKeystore_BuildsDestinationComponents(t *testing.T) {
	ctx := context.Background()
	f, _ := newDestFactoryUnderTest(t)
	accIface, err := f.GetAccessor(ctx, protocol.ChainSelector(stellarSelector))
	require.NoError(t, err)
	acc := accIface.(*accessor)

	ks := newTestKeystore(t)
	createKey(t, ks, "stellar/tx/dest-test", keystore.Ed25519)

	require.NoError(t, acc.SetKeystore(ctx, ks))

	// After the keystore arrives, the destination components must be wired up
	// and observable from both DestinationReader() and ContractTransmitter().
	dr, err := acc.DestinationReader()
	require.NoError(t, err)
	require.NotNil(t, dr)

	ct, err := acc.ContractTransmitter()
	require.NoError(t, err)
	require.NotNil(t, ct)

	// SourceReader should also be available with the same keystore signer.
	sr, err := acc.SourceReader()
	require.NoError(t, err)
	require.NotNil(t, sr)
}

func TestAccessor_SetKeystore_FallsBackToDefaultKeyName(t *testing.T) {
	ctx := context.Background()
	stellarKey := strconv.FormatUint(stellarSelector, 10)

	// No destConfig present — only a reader config — so SetKeystore should
	// fall back to common.StellarTransmitterKeyName when constructing the
	// SourceReader-only deployer.
	f := &factory{
		lggr: logger.Test(t),
		readerConfig: map[string]sourcereader.ReaderConfig{
			stellarKey: {
				SorobanRPCURL:       "http://localhost:8000",
				NetworkPassphrase:   testNetworkPassphrase,
				OnRampContractID:    "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
				RMNRemoteContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
			},
		},
	}
	accIface, err := f.GetAccessor(ctx, protocol.ChainSelector(stellarSelector))
	require.NoError(t, err)
	acc := accIface.(*accessor)

	ks := newTestKeystore(t)
	// Register the default Stellar transmitter key so the fallback succeeds.
	createKey(t, ks, "stellar/tx/stellar_transmitter_ed25519_key", keystore.Ed25519)

	require.NoError(t, acc.SetKeystore(ctx, ks))

	sr, err := acc.SourceReader()
	require.NoError(t, err)
	require.NotNil(t, sr)

	// Destination components remain disabled because the factory had no
	// destConfig for this chain.
	_, err = acc.DestinationReader()
	require.Error(t, err)
	assert.ErrorIs(t, err, errDestConfigMissing)
}
