package common

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
)

// Executor sentinel tags — must match contracts/common/message/src/lib.rs
// (GenericExtraArgsV3::USE_DEFAULT_TAG / NO_EXECUTION_TAG). Each is a 4-byte tag
// left-aligned in a 32-byte Soroban contract Address; the OnRamp recognises these
// instead of the EVM address(0)/NO_EXECUTION_ADDRESS sentinels (Soroban Address
// has no zero value), so no GenericExtraArgsV3 schema change is needed. Go cannot
// express array constants, so these are package vars (treat as read-only).
var (
	UseDefaultExecutorTag = [4]byte{0x72, 0x06, 0x8b, 0x37}
	NoExecutionTag        = [4]byte{0xeb, 0xa5, 0x17, 0xd2}
)

// UseDefaultExecutorAddressRaw is the 32-byte "use the lane's default_executor"
// sentinel Address (EVM address(0) parity). The OnRamp resolves it to
// dest_config.default_executor before compute_ccv_and_executor_hash and before
// Executor::get_fee, so a sender can request the default executor without knowing
// its concrete address. Use this as the default for the executor field when a
// caller supplies no explicit executor: the OnRamp then targets the deployed
// Executor contract instead of an uninitialized mock address (which would trap
// with Error(Storage, MissingValue) on the new cross-contract get_fee call).
var UseDefaultExecutorAddressRaw = append(UseDefaultExecutorTag[:], make([]byte, 28)...)

// NoExecutionAddressRaw is the 32-byte "no auto-execution" sentinel Address
// (EVM NO_EXECUTION_ADDRESS parity). The OnRamp leaves it in place and zeroes the
// executor flat fee + execution-gas cost while still emitting the executor
// receipt with the sentinel as issuer.
var NoExecutionAddressRaw = append(NoExecutionTag[:], make([]byte, 28)...)

// ExecutorSentinelStrkey returns the VersionByteContract strkey form of a 32-byte
// executor sentinel, i.e. the form placed in GenericExtraArgsV3.executor and
// emitted as the executor receipt Issuer.
func ExecutorSentinelStrkey(raw []byte) (string, error) {
	if len(raw) != 32 {
		return "", fmt.Errorf("executor sentinel must be 32 bytes, got %d", len(raw))
	}
	return strkey.Encode(strkey.VersionByteContract, raw)
}
