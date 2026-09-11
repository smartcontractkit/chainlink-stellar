package ccvchain

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stellar/go-stellar-sdk/strkey"
)

const testEVMSelector = uint64(3379446385462418246)

func TestNewChain(t *testing.T) {
	logger := zerolog.Nop()
	chain := New(logger, 1)
	require.NotNil(t, chain)
	assert.Equal(t, chainsel.FamilyStellar, chain.ChainFamily())
}

func TestGenerateAccountAddress(t *testing.T) {
	// Test that generateAccountAddress creates valid Stellar addresses
	addr, err := generateAccountAddress("test-seed")
	require.NoError(t, err)

	// Stellar account addresses start with 'G'
	assert.True(t, len(addr) == 56, "Stellar address should be 56 characters")
	assert.Equal(t, byte('G'), addr[0], "Stellar account address should start with G")

	// Test determinism - same seed should produce same address
	addr2, err := generateAccountAddress("test-seed")
	require.NoError(t, err)
	assert.Equal(t, addr, addr2)

	// Test that different seeds produce different addresses
	addr3, err := generateAccountAddress("other-seed")
	require.NoError(t, err)
	assert.NotEqual(t, addr, addr3)
}

func TestGetConnectionProfile(t *testing.T) {
	logger := zerolog.Nop()
	selector := uint64(12345)
	chain := New(logger, selector)

	chainDef, cvConfig, err := chain.GetConnectionProfile(nil, selector)
	require.NoError(t, err)

	t.Run("chain definition has correct selector and address length", func(t *testing.T) {
		assert.Equal(t, selector, chainDef.Selector)
		assert.Equal(t, uint8(stellarccip.StellarAddressByteLen), chainDef.AddressBytesLength)
	})

	t.Run("base execution gas cost is set", func(t *testing.T) {
		assert.Equal(t, uint32(100_000), chainDef.BaseExecutionGasCost)
	})

	t.Run("default inbound CCVs reference the VVR contract", func(t *testing.T) {
		require.Len(t, chainDef.DefaultInboundCCVs, 1)
		ref := chainDef.DefaultInboundCCVs[0]
		want := stellarccip.VVRDatastoreRef().LaneAddressRef(selector)
		assert.Equal(t, want, ref)
	})

	t.Run("default outbound CCVs reference the VVR contract", func(t *testing.T) {
		require.Len(t, chainDef.DefaultOutboundCCVs, 1)
		ref := chainDef.DefaultOutboundCCVs[0]
		want := stellarccip.VVRDatastoreRef().LaneAddressRef(selector)
		assert.Equal(t, want, ref)
	})

	t.Run("committee verifier config has verification gas set", func(t *testing.T) {
		assert.Equal(t, uint32(10_000), cvConfig.GasForVerification)
	})

	t.Run("uses the provided selector, not the chain's own", func(t *testing.T) {
		otherSelector := uint64(99999)
		def, _, err := chain.GetConnectionProfile(nil, otherSelector)
		require.NoError(t, err)
		assert.Equal(t, otherSelector, def.Selector)
		assert.Equal(t, otherSelector, def.DefaultInboundCCVs[0].ChainSelector)
		assert.Equal(t, otherSelector, def.DefaultOutboundCCVs[0].ChainSelector)
	})

	t.Run("FeeQuoter dest chain config override is set and produces valid config", func(t *testing.T) {
		require.NotNil(t, chainDef.FeeQuoterDestChainConfigOverrides,
			"FeeQuoterDestChainConfigOverrides must be set so remote EVM FeeQuoter accepts Stellar as a destination")

		var cfg lanes.FeeQuoterDestChainConfig
		(*chainDef.FeeQuoterDestChainConfigOverrides)(&cfg)

		assert.True(t, cfg.IsEnabled)
		assert.Equal(t, uint32(30_000), cfg.MaxDataBytes)
		assert.Equal(t, uint32(3_000_000), cfg.MaxPerMsgGasLimit)
		assert.Equal(t, uint32(300_000), cfg.DestGasOverhead)
		assert.Equal(t, uint32(200_000), cfg.DefaultTxGasLimit)
		assert.Equal(t, uint16(10), cfg.NetworkFeeUSDCents)
		// Stand-in EVM family selector (0x2812d52c) until Stellar has its own.
		assert.Equal(t, uint32(0x2812d52c), cfg.ChainFamilySelector)
		require.NotNil(t, cfg.V2Params)
		assert.EqualValues(t, 90, cfg.V2Params.LinkFeeMultiplierPercent)
	})
}

func TestPostConnect(t *testing.T) {
	logger := zerolog.Nop()
	chain := New(logger, 1)

	err := chain.PostConnect(nil, 1, []uint64{2, 3})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environment is nil")
}

func TestBuildOnRampDestConfigs_UsesSelectorSpecificAddressLengths(t *testing.T) {
	chain := &Chain{
		vvrContractID:    "vvr",
		routerContractID: "router",
	}

	configs, err := chain.buildOnRampDestConfigs(nil, []uint64{testEVMSelector, chainsel.STELLAR_LOCALNET.Selector}, "executor", false)
	require.NoError(t, err)
	require.Len(t, configs, 2)

	assert.Equal(t, uint32(20), configs[0].AddressBytesLength)
	assert.Len(t, configs[0].OffRamp, 20)
	assert.Equal(t, uint32(stellarccip.StellarAddressByteLen), configs[1].AddressBytesLength)
	assert.Len(t, configs[1].OffRamp, stellarccip.StellarAddressByteLen)
}

func TestBuildOffRampSourceConfigs_UsesPlaceholderOnRampBytes(t *testing.T) {
	chain := &Chain{
		vvrContractID:    "vvr",
		routerContractID: "router",
	}

	configs, err := chain.buildOffRampSourceConfigs(nil, []uint64{testEVMSelector, chainsel.STELLAR_LOCALNET.Selector}, false)
	require.NoError(t, err)
	require.Len(t, configs, 2)

	require.Len(t, configs[0].OnRamps, 1)
	assert.Len(t, configs[0].OnRamps[0], 32)
	require.Len(t, configs[1].OnRamps, 1)
	assert.Len(t, configs[1].OnRamps[0], 32)
}

func TestBuildRemoteRampConfigs_ResolveDatastoreAddresses(t *testing.T) {
	ds := datastore.NewMemoryDataStore()
	require.NoError(t, ds.AddressRefStore.Add(stellarccip.OnRampDatastoreRef().FullAddressRef(testEVMSelector, "0x"+strings.Repeat("ab", 20))))
	require.NoError(t, ds.AddressRefStore.Add(stellarccip.OffRampDatastoreRef().FullAddressRef(testEVMSelector, "0x"+strings.Repeat("cd", 20))))

	chain := &Chain{
		vvrContractID:    "vvr",
		routerContractID: "router",
	}

	onRampConfigs, err := chain.buildOnRampDestConfigs(ds.Seal(), []uint64{testEVMSelector}, "executor", true)
	require.NoError(t, err)
	require.Len(t, onRampConfigs, 1)
	assert.Equal(t, []byte{0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd, 0xcd}, onRampConfigs[0].OffRamp)

	offRampConfigs, err := chain.buildOffRampSourceConfigs(ds.Seal(), []uint64{testEVMSelector}, true)
	require.NoError(t, err)
	require.Len(t, offRampConfigs, 1)
	require.Len(t, offRampConfigs[0].OnRamps, 1)
	assert.Equal(t,
		append(make([]byte, 12), []byte{0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab, 0xab}...),
		offRampConfigs[0].OnRamps[0],
	)
}

// decodeDatastoreAddressRefToStellarBytes decodes a Stellar contract (C...), account (G...),
// or 32-byte hex datastore address into raw bytes. Test-only helper (no production analogue in this package).
func decodeDatastoreAddressRefToStellarBytes(ref datastore.AddressRef) ([]byte, error) {
	if decoded, err := strkey.Decode(strkey.VersionByteContract, ref.Address); err == nil {
		return decoded, nil
	}
	if decoded, err := strkey.Decode(strkey.VersionByteAccountID, ref.Address); err == nil {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(strings.TrimPrefix(ref.Address, "0x")); err == nil {
		if len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("failed to decode Stellar address %q: not a valid contract (C...), account (G...), or hex address", ref.Address)
}

func TestDatastoreAddressRefToStellarBytes(t *testing.T) {
	t.Run("decodes contract address (C...)", func(t *testing.T) {
		contractAddr := "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
		ref := datastore.AddressRef{Address: contractAddr}
		decoded, err := decodeDatastoreAddressRefToStellarBytes(ref)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})

	t.Run("decodes account address (G...)", func(t *testing.T) {
		accountAddr := "GAAZI4TCR3TY5OJHCTJC2A4QSY6CJWJH5IAJTGKIN2ER7LBNVKOCCWN7"
		ref := datastore.AddressRef{Address: accountAddr}
		decoded, err := decodeDatastoreAddressRefToStellarBytes(ref)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})

	t.Run("decodes 32-byte hex address", func(t *testing.T) {
		hexAddr := "0x" + strings.Repeat("ab", 32)
		ref := datastore.AddressRef{Address: hexAddr}
		decoded, err := decodeDatastoreAddressRefToStellarBytes(ref)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})

	t.Run("decodes 32-byte hex address without 0x prefix", func(t *testing.T) {
		hexAddr := strings.Repeat("cd", 32)
		ref := datastore.AddressRef{Address: hexAddr}
		decoded, err := decodeDatastoreAddressRefToStellarBytes(ref)
		require.NoError(t, err)
		assert.Len(t, decoded, 32)
	})

	t.Run("fails on invalid address", func(t *testing.T) {
		ref := datastore.AddressRef{Address: "not-a-valid-address"}
		_, err := decodeDatastoreAddressRefToStellarBytes(ref)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode Stellar address")
	})

	t.Run("fails on short hex (not 32 bytes)", func(t *testing.T) {
		ref := datastore.AddressRef{Address: "0xabcdef"}
		_, err := decodeDatastoreAddressRefToStellarBytes(ref)
		require.Error(t, err)
	})
}
