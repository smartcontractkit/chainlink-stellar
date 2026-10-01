package stellarutil

// NoExecutionExecutorStrkey is the Stellar contract strkey of the no-execution
// sentinel executor: the 0xEBa517d2… EVM NoExecution address, strkey-encoded.
//
// It mirrors the Rust NO_EXECUTION_STRKEY constant
// (contracts/common/message/src/lib.rs) so the shared lane sequence can apply
// the EVM SkipExecutorConfig semantics without a Rust dependency. Writing this
// value as an OnRamp dest-chain DefaultExecutor means "the destination chain
// does not support execution" — the onramp must not route to a real executor.
//
// Keep in sync with the Rust constant; executor_sentinel_test.go pins the
// encoding so a drift fails tests rather than silently diverging.
const NoExecutionExecutorStrkey = "CDV2KF6SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEJ6"
