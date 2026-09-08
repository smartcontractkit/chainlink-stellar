package helpers

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
)

// ContractIDToBytes32 decodes a Soroban contract strkey into a 32-byte contract id.
func ContractIDToBytes32(contractID string) ([32]byte, error) {
	var out [32]byte
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return out, err
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("contract id raw length %d, want 32", len(raw))
	}
	copy(out[:], raw)
	return out, nil
}
