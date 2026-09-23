package ownership

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// contractStrkeyLength is the encoded length of a Soroban contract strkey (C…).
const contractStrkeyLength = 56

// NormalizeContractAddress accepts a datastore ref address in either recorded
// form — a contract strkey, or the hex encoding the datastore Record* helpers
// store deployed contract IDs as — and returns the strkey the Soroban ops and
// the MCMS transaction targets require. Anything else is rejected: guessing an
// address form here would build a proposal that fails on chain at execute time
// instead of failing at build time with the actual input named.
func NormalizeContractAddress(addr string) (string, error) {
	if strings.HasPrefix(addr, "C") && len(addr) == contractStrkeyLength {
		// Shape alone is not enough: a wrong-checksum strkey would pass through
		// and fail much later (the bindings do not validate the contract ID, and
		// in the propose arm it would land in the MCMS transaction target).
		if _, err := strkey.Decode(strkey.VersionByteContract, addr); err != nil {
			return "", fmt.Errorf("contract address %q is not a valid contract strkey: %w", addr, err)
		}
		return addr, nil
	}
	trimmed := strings.TrimPrefix(addr, "0x")
	if len(trimmed) == 64 {
		if _, err := hex.DecodeString(trimmed); err == nil {
			return scval.HexToContractStrkey(addr)
		}
	}
	return "", fmt.Errorf("contract address %q is neither a contract strkey nor the hex form the datastore records", addr)
}
