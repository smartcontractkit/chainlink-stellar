package ccip

import (
	"fmt"

	"github.com/Masterminds/semver/v3"
	"github.com/ethereum/go-ethereum/common/hexutil"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
)

// StellarAddressByteLen is the canonical length (bytes) for Stellar contract/account IDs in lane configs.
const StellarAddressByteLen = 32

// AddressBytesLength returns the expected encoded address length for a CCIP chain selector.
func AddressBytesLength(selector uint64) (uint32, error) {
	family, err := chainsel.GetSelectorFamily(selector)
	if err != nil {
		return 0, fmt.Errorf("get selector family for %d: %w", selector, err)
	}
	if family == chainsel.FamilyStellar {
		return StellarAddressByteLen, nil
	}
	return 20, nil
}

// LookupAddressRef loads an address ref from the datastore.
func LookupAddressRef(ds datastore.DataStore, selector uint64, contractType datastore.ContractType, version *semver.Version, qualifier string) (datastore.AddressRef, error) {
	ref, err := ds.Addresses().Get(datastore.NewAddressRefKey(selector, contractType, version, qualifier))
	if err != nil {
		return datastore.AddressRef{}, err
	}
	return ref, nil
}

// LookupStellarContractStrkey resolves a datastore entry to a Soroban contract strkey.
func LookupStellarContractStrkey(ds datastore.DataStore, selector uint64, contractType datastore.ContractType, version *semver.Version, qualifier string) (string, error) {
	ref, err := LookupAddressRef(ds, selector, contractType, version, qualifier)
	if err != nil {
		return "", err
	}
	contractID, err := scval.HexToContractStrkey(ref.Address)
	if err != nil {
		return "", fmt.Errorf("convert %s address %s to contract strkey: %w", contractType, ref.Address, err)
	}
	return contractID, nil
}

// AddressBytesHex decodes a datastore address hex string to raw bytes and validates length for selector.
func AddressBytesHex(ref datastore.AddressRef, selector uint64) ([]byte, error) {
	raw, err := hexutil.Decode(ref.Address)
	if err != nil {
		return nil, fmt.Errorf("decode address %s: %w", ref.Address, err)
	}
	expectedLen, err := AddressBytesLength(selector)
	if err != nil {
		return nil, err
	}
	if len(raw) != int(expectedLen) {
		return nil, fmt.Errorf("address %s has %d bytes, expected %d for selector %d", ref.Address, len(raw), expectedLen, selector)
	}
	return raw, nil
}

// CanonicalSourceOnRampBytes returns on-ramp bytes for OffRamp source config; left-pads EVM addresses to 32 bytes.
func CanonicalSourceOnRampBytes(ref datastore.AddressRef, selector uint64) ([]byte, error) {
	raw, err := AddressBytesHex(ref, selector)
	if err != nil {
		return nil, err
	}

	family, err := chainsel.GetSelectorFamily(selector)
	if err != nil {
		return nil, fmt.Errorf("get selector family for %d: %w", selector, err)
	}
	if family != chainsel.FamilyEVM {
		return raw, nil
	}

	padded := make([]byte, 32)
	copy(padded[len(padded)-len(raw):], raw)
	return padded, nil
}

// BuildOnRampDestConfigs builds datastore-backed OnRamp destination chain configs.
// These builders serve the devenv path (ccvchain Chain.PostConnect); the shared
// CLD lane path builds its args in sequences.StellarConfigureChainForLanes.
// The remote chain's OffRamp must already be deployed and recorded: a
// zero-address placeholder is not a valid entry (the ramp contracts reject
// zero encodings, EVM `ZeroAddressNotAllowed` parity), so lane wiring is
// strictly a lookup-and-fail-loudly operation at lane-configuration time.
func BuildOnRampDestConfigs(
	ds datastore.DataStore,
	remoteSelectors []uint64,
	defaultExecutor string,
	vvrContractID string,
	routerContractID string,
) ([]onrampbindings.DestChainConfigArgs, error) {
	configs := make([]onrampbindings.DestChainConfigArgs, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		addressBytesLength, err := AddressBytesLength(rs)
		if err != nil {
			return nil, err
		}

		offRampRef, err := OffRampDatastoreRef().LookupAddressRef(ds, rs)
		if err != nil {
			return nil, fmt.Errorf("lookup remote offramp for %d: %w", rs, err)
		}
		offRampBytes, err := AddressBytesHex(offRampRef, rs)
		if err != nil {
			return nil, fmt.Errorf("resolve remote offramp bytes for %d: %w", rs, err)
		}

		configs = append(configs, onrampbindings.DestChainConfigArgs{
			DestChainSelector:         rs,
			AddressBytesLength:        addressBytesLength,
			BaseExecutionGasCost:      100_000,
			DefaultCcvs:               []string{vvrContractID},
			DefaultExecutor:           defaultExecutor,
			ExecutionFeeUsdCents:      0,
			LaneMandatedCcvs:          []string{},
			MessageNetworkFeeUsdCents: 100,
			OffRamp:                   offRampBytes,
			Router:                    routerContractID,
			TokenNetworkFeeUsdCents:   50,
			TokenReceiverAllowed:      true,
		})
	}
	return configs, nil
}

// BuildOffRampSourceConfigs builds datastore-backed OffRamp source configs.
// The remote chain's OnRamp must already be deployed and recorded: the
// OffRamp rejects an all-zero onramp entry with `ZeroAddressNotAllowed`
// (#808, EVM `OffRamp.applySourceChainConfigUpdates` parity), so there is
// deliberately no zero-address placeholder mode here — lane wiring fails
// loudly when a remote OnRamp is not yet in the datastore.
func BuildOffRampSourceConfigs(
	ds datastore.DataStore,
	remoteSelectors []uint64,
	vvrContractID string,
	routerContractID string,
) ([]offrampbindings.SourceChainConfigArgs, error) {
	configs := make([]offrampbindings.SourceChainConfigArgs, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		onRampRef, err := OnRampDatastoreRef().LookupAddressRef(ds, rs)
		if err != nil {
			return nil, fmt.Errorf("lookup remote onramp for %d: %w", rs, err)
		}
		onRampBytes, err := CanonicalSourceOnRampBytes(onRampRef, rs)
		if err != nil {
			return nil, fmt.Errorf("resolve remote onramp bytes for %d: %w", rs, err)
		}

		configs = append(configs, offrampbindings.SourceChainConfigArgs{
			SourceChainSelector: rs,
			IsEnabled:           true,
			DefaultCcvs:         []string{vvrContractID},
			LaneMandatedCcvs:    []string{},
			OnRamps:             [][]byte{onRampBytes},
			Router:              routerContractID,
		})
	}
	return configs, nil
}
