package adapters

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stellar/go-stellar-sdk/strkey"

	deployapi "github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
)

var _ deployapi.AddressNormalizer = (*StellarAddressNormalizer)(nil)

// StellarAddressNormalizer provides the 0x-hex form the stellar datastore
// records for datastore-aligned lookups, mirroring EVM's EIP-55 normalizer.
// Stellar addresses are 32-byte contract or account IDs, carried either as
// C…/G… strkeys or as 0x-hex; both recorded forms normalize to the same
// lowercase hex so refs built from different sources compare equal.
type StellarAddressNormalizer struct{}

// NormalizeAddress canonicalizes a stellar address in either recorded form —
// a contract/account strkey or 0x-hex — to the lowercase 0x-hex the datastore
// records.
func (StellarAddressNormalizer) NormalizeAddress(addr string) (string, error) {
	raw, err := stellarAddressStringToBytes(addr)
	if err != nil {
		return "", err
	}
	return hexutil.Encode(raw), nil
}

// BytesToString converts raw on-chain address bytes — a 32-byte contract or
// account ID — to the 0x-hex form the datastore records.
func (StellarAddressNormalizer) BytesToString(addr []byte) (string, error) {
	if len(addr) != 32 {
		return "", fmt.Errorf("stellar address bytes must be 32, got %d", len(addr))
	}
	return hexutil.Encode(addr), nil
}

// StringToBytes converts a contract/account strkey or a 0x-hex address to its
// raw 32 bytes.
func (StellarAddressNormalizer) StringToBytes(addr string) ([]byte, error) {
	return stellarAddressStringToBytes(addr)
}

// stellarAddressStringToBytes decodes a stellar address in either recorded
// form to its raw 32 bytes. Anything else errors rather than guessing: a
// wrong-form address would silently become a different ref and miss every
// lookup keyed on the canonical form.
func stellarAddressStringToBytes(addr string) ([]byte, error) {
	switch {
	case strings.HasPrefix(addr, "C"), strings.HasPrefix(addr, "G"):
		vb := strkey.VersionByteAccountID
		if strings.HasPrefix(addr, "C") {
			vb = strkey.VersionByteContract
		}
		raw, err := strkey.Decode(vb, addr)
		if err != nil {
			return nil, fmt.Errorf("address %q is not a valid strkey: %w", addr, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("address %q decodes to %d bytes, want 32", addr, len(raw))
		}
		return raw, nil
	case strings.HasPrefix(addr, "0x"):
		raw, err := hexutil.Decode(addr)
		if err != nil {
			return nil, fmt.Errorf("address %q is not valid hex: %w", addr, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("address %q decodes to %d bytes, want 32", addr, len(raw))
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("address %q is neither a contract/account strkey nor 0x-hex", addr)
	}
}
