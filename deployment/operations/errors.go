package operations

import (
	"fmt"
	"strings"
)

// Soroban contract error codes (contracts/common/error/src/lib.rs CCIPError)
// that read ops treat as "lane not configured". Reads must keep this list
// narrow: only the code the target contract's getter actually defines.
const (
	// DestinationChainNotEnabledCode is FeeQuoter/Executor get_dest_chain_config
	// for a destination chain with no config.
	DestinationChainNotEnabledCode uint32 = 25
	// DestinationChainNotSupportedCode is OnRamp get_dest_chain_config for a
	// destination chain with no config.
	DestinationChainNotSupportedCode uint32 = 37
	// UnsupportedDestinationChainCode is Router get_onramp for a destination
	// chain with no OnRamp routed.
	UnsupportedDestinationChainCode uint32 = 63
	// SourceChainNotEnabledCode is OffRamp get_source_chain_config for a source
	// chain with no config.
	SourceChainNotEnabledCode uint32 = 100
	// SourceSignersNotConfiguredCode is CommitteeVerifier get_signature_config
	// for a source chain with no signature quorum configured.
	SourceSignersNotConfiguredCode uint32 = 19
)

// IsContractErrorCode reports whether err carries the Soroban host's rendering
// of the given contract error code. The RPC client surfaces simulation
// diagnostics as "Error(Contract, #<code>)" inside the returned error's
// message; there is no typed error to unwrap, so a substring match on the
// rendered code is the established pattern (see
// adapters/ccv_committee_verifier_onchain.go, sourceSignersNotConfiguredCode).
//
// Callers must keep the match narrow: pass the exact code and only treat a
// match as "not configured" for the contract/error pair that actually defines
// that code.
func IsContractErrorCode(err error, code uint32) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), fmt.Sprintf("Error(Contract, #%d)", code))
}
