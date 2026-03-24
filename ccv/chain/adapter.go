package ccvchain

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"

	ccvevmadapters "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/adapters"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
)

var _ lanes.LaneAdapter = (*StellarAdapter)(nil)

// StellarAdapter wraps the EVM CCIP lane adapter and overrides address decoding for Stellar strkeys.
type StellarAdapter struct {
	*ccvevmadapters.ChainFamilyAdapter
}

// NewChainFamilyAdapter returns a Stellar lane adapter that delegates lane operations to the EVM
// adapter while decoding Stellar contract/account addresses in AddressRefToBytes.
func NewChainFamilyAdapter(base *ccvevmadapters.ChainFamilyAdapter) *StellarAdapter {
	if base == nil {
		panic("NewChainFamilyAdapter: base adapter is nil")
	}
	return &StellarAdapter{ChainFamilyAdapter: base}
}

// AddressRefToBytes decodes a strkey-encoded Stellar address (C... contracts, G... accounts)
// or a 32-byte hex string into raw bytes.
func (s *StellarAdapter) AddressRefToBytes(ref datastore.AddressRef) ([]byte, error) {
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
