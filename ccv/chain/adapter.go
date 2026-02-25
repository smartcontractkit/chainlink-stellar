package ccvchain

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	adapters "github.com/smartcontractkit/chainlink-ccip/deployment/v1_7_0/adapters"
	"github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/operations"
)

var _ adapters.ChainFamily = &StellarAdapter{}

// StellarAdapter is an implementation of the ChainFamily interface for Stellar.
type StellarAdapter struct {
	base adapters.ChainFamily
}

// NewChainFamilyAdapter creates a new Stellar chain family adapter.
// A "base" adapter needs to be passed in, currently assumed to be the EVM chain family adapter,
// in order to achieve all functionality.
// TODO: this needs to be fully implemented for Stellar.
func NewChainFamilyAdapter(base adapters.ChainFamily) *StellarAdapter {
	return &StellarAdapter{
		base: base,
	}
}

// AddressRefToBytes implements adapters.ChainFamily.
// Decodes a strkey-encoded Stellar address (C... for contracts, G... for accounts)
// into its raw 32-byte key.
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

// ConfigureChainForLanes implements adapters.ChainFamily.
// TODO: implement Stellar-specific chain lane configuration.
func (s *StellarAdapter) ConfigureChainForLanes() *operations.Sequence[adapters.ConfigureChainForLanesInput, sequences.OnChainOutput, chain.BlockChains] {
	return s.base.ConfigureChainForLanes()
}
