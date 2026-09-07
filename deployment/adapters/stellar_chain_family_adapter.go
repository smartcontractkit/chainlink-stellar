package adapters

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	datastore_utils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// StellarChainFamilyAdapter implements ccvadapters.ChainFamily for CCIP 2.0.
// Stellar does not register deployment/lanes.LaneAdapter: lane wiring is done
// via DeployContractsForSelector / ConfigureChainsForLanesFromTopology (no-op
// ConfigureChainForLanes here). Legacy lanes.ConnectChains is unsupported for
// Stellar unless a LaneAdapter is reintroduced.
type StellarChainFamilyAdapter struct{}

var _ ccvadapters.ChainFamily = (*StellarChainFamilyAdapter)(nil)

// stellarFeeQuoterChainFamilySelector is bytes4(keccak256("CCIP ChainFamilySelector EVM")).
// It is used as a stand-in for Stellar until a Stellar-specific selector is registered
// in the EVM FeeQuoter contract (see ccv/chain/chain.go).
var stellarFeeQuoterChainFamilySelector = [4]byte{0x28, 0x12, 0xd5, 0x2c}

func toStellarAddressBytes(ref datastore.AddressRef) ([]byte, error) {
	addr := strings.TrimPrefix(ref.Address, "0x")
	b, err := hex.DecodeString(addr)
	if err != nil {
		return nil, fmt.Errorf("decode stellar hex address %q: %w", ref.Address, err)
	}
	return b, nil
}

func (a *StellarChainFamilyAdapter) GetOnRampAddress(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return datastore_utils.FindAndFormatRef(ds, stellarccip.OnRampDatastoreRef().PartialAddressRef(), chainSelector, toStellarAddressBytes)
}

func (a *StellarChainFamilyAdapter) GetOffRampAddress(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return datastore_utils.FindAndFormatRef(ds, stellarccip.OffRampDatastoreRef().PartialAddressRef(), chainSelector, toStellarAddressBytes)
}

func (a *StellarChainFamilyAdapter) GetRouterAddress(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return datastore_utils.FindAndFormatRef(ds, stellarccip.RouterDatastoreRef().PartialAddressRef(), chainSelector, toStellarAddressBytes)
}

func (a *StellarChainFamilyAdapter) GetFQAddress(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return datastore_utils.FindAndFormatRef(ds, stellarccip.FeeQuoterDatastoreRef().PartialAddressRef(), chainSelector, toStellarAddressBytes)
}

// GetFeeQuoterDestChainConfig returns defaults for the legacy lanes package (e.g. connection profile helpers).
func (a *StellarChainFamilyAdapter) GetFeeQuoterDestChainConfig() lanes.FeeQuoterDestChainConfig {
	return lanes.FeeQuoterDestChainConfig{
		IsEnabled:                   true,
		MaxDataBytes:                50_000,
		MaxPerMsgGasLimit:           4_000_000,
		DestGasOverhead:             350_000,
		DestGasPerPayloadByteBase:   16,
		ChainFamilySelector:         binary.BigEndian.Uint32(stellarFeeQuoterChainFamilySelector[:]),
		DefaultTokenFeeUSDCents:     50,
		DefaultTokenDestGasOverhead: 50_000,
		DefaultTxGasLimit:           200_000,
		NetworkFeeUSDCents:          100,
		V1Params: &lanes.FeeQuoterV1Params{
			MaxNumberOfTokensPerMsg:           10,
			DestGasPerPayloadByteHigh:         40,
			DestGasPerPayloadByteThreshold:    3000,
			DestDataAvailabilityOverheadGas:   100,
			DestGasPerDataAvailabilityByte:    16,
			DestDataAvailabilityMultiplierBps: 1,
			GasMultiplierWeiPerEth:            11e17,
		},
		V2Params: &lanes.FeeQuoterV2Params{
			LinkFeeMultiplierPercent: 90,
			USDPerUnitGas:            big.NewInt(1e6),
		},
	}
}

func (a *StellarChainFamilyAdapter) GetDefaultGasPrice() *big.Int {
	return big.NewInt(1e9)
}

var stellarNoOpConfigureChainForLanes = cldf_ops.NewSequence(
	"StellarConfigureChainForLanes",
	semver.MustParse("2.0.0"),
	"No-op: Stellar lane config is applied during contract deployment",
	func(_ cldf_ops.Bundle, _ cldf_chain.BlockChains, _ ccvadapters.ConfigureChainForLanesInput) (seq_core.OnChainOutput, error) {
		return seq_core.OnChainOutput{}, nil
	},
)

func (a *StellarChainFamilyAdapter) ConfigureChainForLanes() *cldf_ops.Sequence[ccvadapters.ConfigureChainForLanesInput, seq_core.OnChainOutput, cldf_chain.BlockChains] {
	return stellarNoOpConfigureChainForLanes
}

func (a *StellarChainFamilyAdapter) AddressRefToBytes(ref datastore.AddressRef) ([]byte, error) {
	return toStellarAddressBytes(ref)
}

func (a *StellarChainFamilyAdapter) GetTestRouter(ds datastore.DataStore, chainSelector uint64) ([]byte, error) {
	return a.GetRouterAddress(ds, chainSelector)
}

func (a *StellarChainFamilyAdapter) ResolveExecutor(ds datastore.DataStore, chainSelector uint64, qualifier string) (string, error) {
	toAddress := func(ref datastore.AddressRef) (string, error) { return ref.Address, nil }
	return datastore_utils.FindAndFormatRef(ds, stellarccip.ExecutorProxyDatastoreRef(qualifier).PartialAddressRef(), chainSelector, toAddress)
}

func (a *StellarChainFamilyAdapter) GetAddressBytesLength() uint8 {
	// Stellar contract IDs are 32 bytes on-chain / in cross-chain payloads.
	return 32
}

func (a *StellarChainFamilyAdapter) GetChainFamilySelector() [4]byte {
	return stellarFeeQuoterChainFamilySelector
}

func (a *StellarChainFamilyAdapter) GetDefaultFeeQuoterDestChainConfig(
	_, _ uint64, chainFamilySelector [4]byte,
) ccvadapters.FeeQuoterDestChainConfigOverrides {
	// FeeQuoterDestChainConfigOverrides uses pointer fields so a nil means "leave the
	// existing on-chain value alone". These are Stellar's defaults, so every field is set.
	// The caller passes the destination's family selector; fall back to Stellar's own when
	// it is unset so a zero-value call still produces a usable config.
	if chainFamilySelector == ([4]byte{}) {
		chainFamilySelector = stellarFeeQuoterChainFamilySelector
	}
	return ccvadapters.FeeQuoterDestChainConfigOverrides{
		IsEnabled:                   ptr(true),
		MaxDataBytes:                ptr(uint32(50_000)),
		MaxPerMsgGasLimit:           ptr(uint32(4_000_000)),
		DestGasOverhead:             ptr(uint32(350_000)),
		DestGasPerPayloadByteBase:   ptr(uint8(16)),
		ChainFamilySelector:         chainFamilySelector,
		DefaultTokenFeeUSDCents:     ptr(uint16(50)),
		DefaultTokenDestGasOverhead: ptr(uint32(50_000)),
		DefaultTxGasLimit:           ptr(uint32(200_000)),
		NetworkFeeUSDCents:          ptr(uint16(100)),
		LinkFeeMultiplierPercent:    ptr(uint8(90)),
		USDPerUnitGas:               big.NewInt(1e6),
	}
}

func (a *StellarChainFamilyAdapter) GetDefaultRemoteChainConfig(_, _ uint64) ccvadapters.RemoteChainDefaults {
	return ccvadapters.RemoteChainDefaults{
		AllowTrafficFrom:          true,
		ExecutorDestChainConfig:   ccvadapters.ExecutorDestChainConfig{USDCentsFee: 0, Enabled: true},
		BaseExecutionGasCost:      175_000,
		TokenReceiverAllowed:      false,
		MessageNetworkFeeUSDCents: 10,
		TokenNetworkFeeUSDCents:   25,
	}
}

func (a *StellarChainFamilyAdapter) GetDefaultCommitteeVerifierRemoteChainConfig() ccvadapters.CommitteeVerifierRemoteChainDefaults {
	return ccvadapters.CommitteeVerifierRemoteChainDefaults{
		AllowlistEnabled:   false,
		FeeUSDCents:        0,
		GasForVerification: 60_000,
		PayloadSizeBytes:   390,
	}
}

func (a *StellarChainFamilyAdapter) GetDefaultFinalityConfig() finality.Config {
	return finality.Config{
		WaitForFinality: true,
		WaitForSafe:     true,
		BlockDepth:      1,
	}
}

// ValidateNOPsTopology accepts any NOP count for Stellar.
//
// The EVM adapter enforces a production floor (minProductionChainNOPs). Stellar has no such
// requirement agreed yet, and the devenv runs far fewer NOPs than EVM's floor, so copying that
// constant would fail every local and CI environment. Revisit when Stellar has a production
// committee-size policy.
func (a *StellarChainFamilyAdapter) ValidateNOPsTopology(_ string, _ int) error {
	return nil
}

// ptr returns a pointer to v, for the pointer-valued override fields above.
func ptr[T any](v T) *T { return &v }
