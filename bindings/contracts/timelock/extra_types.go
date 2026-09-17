// Package timelock extra_types.go contains hand-maintained constants that the
// stellar-bindings-generator does NOT emit.
//
// The timelock contract only declares the TimelockError enum (emitted in the
// generated types.go as TimelockError* constants). It does NOT re-declare the
// common CCIPError enum, so the generator never produces CCIPError* constants
// for this package — yet consumer code (tests/integration) references them
// (e.g. timelockbindings.CCIPErrorUnauthorized). They were hand-added to the
// generated types.go in the published binding and wiped on regeneration.
//
// This file is deliberately separate from the generated types.go so that
// `make generate-bindings` (which rewrites only types.go / client.go) does not
// drop them. Values mirror common/error/src/lib.rs CCIPError discriminants and
// match the CCIPError* constants every other contract binding emits.
package timelock

// CCIPError represents the common contract error codes.
const (
	CCIPErrorNotInitialized                      = 1
	CCIPErrorAlreadyInitialized                  = 2
	CCIPErrorUnauthorized                        = 3
	CCIPErrorNotOwner                            = 4
	CCIPErrorNoPendingOwner                      = 5
	CCIPErrorCallerNotAuthorized                 = 6
	CCIPErrorCallerAlreadyAuthorized             = 7
	CCIPErrorCallerNotFound                      = 8
	CCIPErrorRoleNotGranted                      = 9
	CCIPErrorFeatureNotEnabled                   = 10
	CCIPErrorRoleAlreadyGranted                  = 11
	CCIPErrorCannotRenounceRole                  = 12
	CCIPErrorInvalidVersionTag                   = 13
	CCIPErrorInvalidSignatureLength              = 14
	CCIPErrorInvalidSignature                    = 15
	CCIPErrorInvalidSignatureCount               = 16
	CCIPErrorInvalidSignatureThreshold           = 17
	CCIPErrorInvalidSignaturePubkey              = 18
	CCIPErrorSourceSignersNotConfigured          = 19
	CCIPErrorInvalidVerifierResults              = 20
	CCIPErrorReentrantCall                       = 21
	CCIPErrorTokenNotSupported                   = 22
	CCIPErrorFeeTokenNotSupported                = 23
	CCIPErrorNoGasPriceAvailable                 = 24
	CCIPErrorDestinationChainNotEnabled          = 25
	CCIPErrorInvalidExtraArgsTag                 = 26
	CCIPErrorInvalidExtraArgsData                = 27
	CCIPErrorMessageGasLimitTooHigh              = 28
	CCIPErrorMessageTooLarge                     = 29
	CCIPErrorUnsupportedNumberOfTokens           = 30
	CCIPErrorInvalidDestChainConfig              = 31
	CCIPErrorMessageFeeTooHigh                   = 32
	CCIPErrorInvalidStaticConfig                 = 33
	CCIPErrorInvalidTokenReceiver                = 34
	CCIPErrorSourceTokenDataTooLarge             = 35
	CCIPErrorInvalidDestBytesOverhead            = 36
	CCIPErrorDestinationChainNotSupported        = 37
	CCIPErrorMustBeCalledByRouter                = 38
	CCIPErrorRouterMustSetOriginalSender         = 39
	CCIPErrorCannotSendZeroTokens                = 40
	CCIPErrorCanOnlySendOneTokenPerMessage       = 41
	CCIPErrorUnsupportedToken                    = 42
	CCIPErrorInvalidDestChainAddress             = 43
	CCIPErrorFeeExceedsMaxAllowed                = 44
	CCIPErrorInsufficientFeeTokenAmount          = 45
	CCIPErrorTokenReceiverNotAllowed             = 46
	CCIPErrorCursedByRMN                         = 47
	CCIPErrorRemoteChainNotSupported             = 48
	CCIPErrorSenderNotAllowed                    = 49
	CCIPErrorInvalidTokenAmount                  = 50
	CCIPErrorInvalidReceiverAddress              = 51
	CCIPErrorInvalidConfig                       = 52
	CCIPErrorInvalidVerifierResultsLength        = 53
	CCIPErrorInboundImplementationNotFound       = 54
	CCIPErrorOutboundImplementationNotFound      = 55
	CCIPErrorInvalidAddress                      = 56
	CCIPErrorInvalidChainSelector                = 57
	CCIPErrorInvalidVersion                      = 58
	CCIPErrorInvalidCCVVersion                   = 59
	CCIPErrorOffRampAlreadyExists                = 60
	CCIPErrorOffRampMismatch                     = 61
	CCIPErrorBadRMNSignal                        = 62
	CCIPErrorUnsupportedDestinationChain         = 63
	CCIPErrorAlreadyCursed                       = 64
	CCIPErrorConfigNotSet                        = 65
	CCIPErrorDuplicateOnchainPublicKey           = 66
	CCIPErrorInvalidSignerOrder                  = 67
	CCIPErrorNotEnoughSigners                    = 68
	CCIPErrorNotCursed                           = 69
	CCIPErrorOutOfOrderSignatures                = 70
	CCIPErrorThresholdNotMet                     = 71
	CCIPErrorUnexpectedSigner                    = 72
	CCIPErrorZeroValueNotAllowed                 = 73
	CCIPErrorSourceChainNotEnabled               = 100
	CCIPErrorInvalidSourceChainConfig            = 101
	CCIPErrorInvalidOnRampAddress                = 102
	CCIPErrorInvalidOffRampAddress               = 103
	CCIPErrorInvalidMessageDestination           = 104
	CCIPErrorMessageAlreadyExecuted              = 105
	CCIPErrorInvalidExecutionState               = 106
	CCIPErrorCCVLengthMismatch                   = 107
	CCIPErrorCCVQuorumNotMet                     = 108
	CCIPErrorReceiverError                       = 109
	CCIPErrorGasLimitOverrideTooLow              = 110
	CCIPErrorInvalidReceiverLength               = 111
	CCIPErrorTokenHandlingError                  = 112
	CCIPErrorMessageDecodingError                = 113
	CCIPErrorReceiverDoesNotExist                = 114
	CCIPErrorReceiverNotWasmContract             = 115
	CCIPErrorRequiredCCVMissing                  = 116
	CCIPErrorOnlyRegistryModuleOrOwner           = 201
	CCIPErrorOnlyAdministrator                   = 202
	CCIPErrorOnlyPendingAdministrator            = 203
	CCIPErrorTokenAlreadyRegistered              = 204
	CCIPErrorInvalidTokenPoolToken               = 205
	CCIPErrorPoolTokenMismatch                   = 301
	CCIPErrorChainNotSupported                   = 302
	CCIPErrorCallerIsNotRamp                     = 303
	CCIPErrorInsufficientPoolLiquidity           = 304
	CCIPErrorInvalidRemotePoolAddress            = 305
	CCIPErrorInvalidRemoteChainConfig            = 306
	CCIPErrorInvalidRemoteChainDecimals          = 307
	CCIPErrorDecimalAmountOverflow               = 308
	CCIPErrorInvalidPoolTokenDecimals            = 309
	CCIPErrorBucketOverfilled                    = 310
	CCIPErrorTokenMaxCapacityExceeded            = 311
	CCIPErrorTokenRateLimitReached               = 312
	CCIPErrorInvalidRateLimitRate                = 313
	CCIPErrorDisabledNonZeroRateLimit            = 314
	CCIPErrorInvalidRequestedFinality            = 315
	CCIPErrorRequestedFinalityCanOnlyHaveOneMode = 316
	CCIPErrorInvalidChainForClient               = 317
	CCIPErrorRouterNotConfigured                 = 318
	CCIPErrorInvalidFeeCalculation               = 801
	CCIPErrorInvalidFeeTokenConversion           = 802
	CCIPErrorZeroFeeAggregatorNotAllowed         = 803
)
