package accessors

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/pkg/chainaccess"
	"github.com/smartcontractkit/chainlink-common/pkg/logger"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/ccv/common"
	contracttransmitter "github.com/smartcontractkit/chainlink-stellar/ccv/contract_transmitter"
	destinationreader "github.com/smartcontractkit/chainlink-stellar/ccv/destination_reader"
	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validContractHex = "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func TestStellarConfigPath(t *testing.T) {
	t.Run("uses env when set", func(t *testing.T) {
		t.Setenv(StellarConfigPathEnv, "/custom/stellar.toml")
		assert.Equal(t, "/custom/stellar.toml", stellarConfigPath())
	})

	t.Run("uses default when env unset", func(t *testing.T) {
		require.NoError(t, os.Unsetenv(StellarConfigPathEnv))
		assert.Equal(t, common.DefaultStellarConfigPath, stellarConfigPath())
	})
}

func TestApplyOnRampRMNHexOverrides(t *testing.T) {
	sel := strconv.FormatUint(chainsel.STELLAR_LOCALNET.Selector, 10)
	wantStrkey, err := scval.HexToContractStrkey(validContractHex)
	require.NoError(t, err)

	t.Run("fills empty ids from hex maps", func(t *testing.T) {
		rc := map[string]sourcereader.ReaderConfig{
			sel: {SorobanRPCURL: "http://x"},
		}
		onRamp := map[string]string{sel: validContractHex}
		rmn := map[string]string{sel: validContractHex}
		require.NoError(t, applyOnRampRMNHexOverrides(rc, onRamp, rmn))
		assert.Equal(t, wantStrkey, rc[sel].OnRampContractID)
		assert.Equal(t, wantStrkey, rc[sel].RMNRemoteContractID)
	})

	t.Run("skips when strkey already set", func(t *testing.T) {
		rc := map[string]sourcereader.ReaderConfig{
			sel: {OnRampContractID: "already-set", SorobanRPCURL: "http://x"},
		}
		onRamp := map[string]string{sel: validContractHex}
		require.NoError(t, applyOnRampRMNHexOverrides(rc, onRamp, nil))
		assert.Equal(t, "already-set", rc[sel].OnRampContractID)
	})

	t.Run("invalid onramp hex returns error", func(t *testing.T) {
		rc := map[string]sourcereader.ReaderConfig{sel: {}}
		onRamp := map[string]string{sel: "0xZZZZ"}
		err := applyOnRampRMNHexOverrides(rc, onRamp, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "convert OnRamp hex")
	})
}

func TestBuildStellarDestConfigs(t *testing.T) {
	sel := strconv.FormatUint(chainsel.STELLAR_LOCALNET.Selector, 10)
	wantStrkey, err := scval.HexToContractStrkey(validContractHex)
	require.NoError(t, err)

	t.Run("no destination config yields nil", func(t *testing.T) {
		out, err := buildStellarDestConfigs(&common.Config{}, chainaccess.GenericConfig{})
		require.NoError(t, err)
		assert.Nil(t, out)
	})

	t.Run("seeds from file transmitter config", func(t *testing.T) {
		fileCfg := &common.Config{
			TransmitterConfigs: map[string]contracttransmitter.ContractTransmitterConfig{
				sel: {
					OffRampContractID:     "offramp-file",
					RMNRemoteAddress:      "rmn-file",
					CCIPStateChangedTopic: "topic",
				},
			},
		}
		out, err := buildStellarDestConfigs(fileCfg, chainaccess.GenericConfig{})
		require.NoError(t, err)
		require.Contains(t, out, sel)
		assert.Equal(t, "offramp-file", out[sel].offRampContractID)
		assert.Equal(t, "rmn-file", out[sel].rmnRemoteContractID)
		assert.Equal(t, common.StellarTransmitterKeyName, out[sel].keyName)
	})

	t.Run("overlays hex addresses from ChainConfiguration", func(t *testing.T) {
		fileCfg := &common.Config{
			TransmitterConfigs: map[string]contracttransmitter.ContractTransmitterConfig{
				sel: {CCIPStateChangedTopic: "topic"},
			},
		}
		var gc chainaccess.GenericConfig
		gc.ChainConfiguration = map[string]chainaccess.DestinationChainConfig{
			sel: {OffRampAddress: validContractHex, RmnAddress: validContractHex},
		}
		out, err := buildStellarDestConfigs(fileCfg, gc)
		require.NoError(t, err)
		require.Contains(t, out, sel)
		assert.Equal(t, wantStrkey, out[sel].offRampContractID)
		assert.Equal(t, wantStrkey, out[sel].rmnRemoteContractID)
	})

	t.Run("transmitter key name override", func(t *testing.T) {
		fileCfg := &common.Config{
			TransmitterConfigs: map[string]contracttransmitter.ContractTransmitterConfig{
				sel: {OffRampContractID: "offramp-file", CCIPStateChangedTopic: "topic"},
			},
		}
		var gc chainaccess.GenericConfig
		gc.ChainConfiguration = map[string]chainaccess.DestinationChainConfig{
			sel: {TransmitterKeyName: "custom-key"},
		}
		out, err := buildStellarDestConfigs(fileCfg, gc)
		require.NoError(t, err)
		require.Contains(t, out, sel)
		assert.Equal(t, "custom-key", out[sel].keyName)
	})

	t.Run("invalid offramp hex returns error", func(t *testing.T) {
		fileCfg := &common.Config{
			TransmitterConfigs: map[string]contracttransmitter.ContractTransmitterConfig{
				sel: {CCIPStateChangedTopic: "topic"},
			},
		}
		var gc chainaccess.GenericConfig
		gc.ChainConfiguration = map[string]chainaccess.DestinationChainConfig{
			sel: {OffRampAddress: "0xZZZZ"},
		}
		_, err := buildStellarDestConfigs(fileCfg, gc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "convert OffRamp hex")
	})

	t.Run("drops entries missing offramp or topic", func(t *testing.T) {
		fileCfg := &common.Config{
			DestinationReaderConfigs: map[string]destinationreader.Config{
				sel: {OffRampContractID: "offramp-file"},
			},
		}
		out, err := buildStellarDestConfigs(fileCfg, chainaccess.GenericConfig{})
		require.NoError(t, err)
		assert.Nil(t, out, "entry without stateChangedTopic must be dropped")
	})
}

func TestCreateStellarAccessorFactory(t *testing.T) {
	sel := strconv.FormatUint(chainsel.STELLAR_LOCALNET.Selector, 10)
	wantStrkey, err := scval.HexToContractStrkey(validContractHex)
	require.NoError(t, err)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "stellar.toml")
	fileContents := `
[reader_configs.` + sel + `]
network_passphrase = "file-pass"
soroban_rpc_url = "http://file-rpc"
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(fileContents), 0o600))
	t.Setenv(StellarConfigPathEnv, cfgPath)

	var gc chainaccess.GenericConfig
	gc.OnRampAddresses = map[string]string{sel: validContractHex}

	accessorFactory, err := CreateStellarAccessorFactory(logger.Test(t), gc)
	require.NoError(t, err)
	require.NotNil(t, accessorFactory)

	// The on-ramp hex map fills the reader's OnRampContractID via strkey conversion.
	f, ok := accessorFactory.(*factory)
	require.True(t, ok)
	require.Contains(t, f.readerConfig, sel)
	assert.Equal(t, "file-pass", f.readerConfig[sel].NetworkPassphrase)
	assert.Equal(t, wantStrkey, f.readerConfig[sel].OnRampContractID)
}
