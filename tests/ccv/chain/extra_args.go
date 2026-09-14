package ccvchain

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	common "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
)

// EncodeStellarSourceExtraArgsForOnRamp maps cciptest MessageOptions into the Soroban
// GenericExtraArgsV3 XDR blob expected by the Stellar OnRamp extra_args field
// (see common.EncodeExtraArgsV3). This is not EVM ABI extraArgs (GenericExtraArgsV1/V2
// selectors): ABI bytes cause OnRamp::get_fee / forward_from_router to fail while
// parsing extra_args (host Error(Value, InvalidInput)).
//
// We do not register this as cciptestinterfaces.ExtraArgsSerializer(FamilyStellar)
// because:
//  1. ExtraArgsSerializer is func(MessageOptions) []byte with no chain context, but
//     sensible defaults require the deployer account (mock executor) and VVR contract.
//  2. That registry is keyed by destination family for EVM-style sends where the
//     wire format follows the *destination* executor; Stellar OnRamp always consumes
//     Soroban GenericExtraArgsV3 XDR regardless of destination, so dest-family lookup
//     is the wrong axis for Stellar-as-source.
//
// allowOutOfOrderExecution (MessageOptions.OutOfOrderExecution) is not represented on
// Soroban GenericExtraArgsV3 today; callers should still set it to true for parity with
// CCIP devenv policy — BuildChainMessage forces it before encoding.
//
// This helper depends on the chainlink-ccv/build/devenv test interfaces (MessageOptions),
// so it lives in the tests module rather than the root production module — keeping the
// root module free of the devenv (and transitively chainlink-testing-framework)
// dependency.
func EncodeStellarSourceExtraArgsForOnRamp(deployerGAddr, vvrContractID string, opts cciptestinterfaces.MessageOptions) ([]byte, error) {
	var ccvAddrs []string
	var ccvArgs [][]byte
	if len(opts.CCVs) > 0 {
		ccvAddrs = make([]string, 0, len(opts.CCVs))
		ccvArgs = make([][]byte, 0, len(opts.CCVs))
		for i := range opts.CCVs {
			ccv := opts.CCVs[i]
			addr, err := strkey.Encode(strkey.VersionByteContract, []byte(ccv.CCVAddress))
			if err != nil {
				return nil, fmt.Errorf("encode ccv address: %w", err)
			}
			ccvAddrs = append(ccvAddrs, addr)
			ccvArgs = append(ccvArgs, append([]byte(nil), ccv.Args...))
		}
	} else {
		if vvrContractID == "" {
			return nil, fmt.Errorf("versioned verifier resolver contract id is empty")
		}
		ccvAddrs = []string{vvrContractID}
		ccvArgs = [][]byte{{}}
	}

	executor := stellarutil.MustGenerateMockContractID(deployerGAddr, "executor")
	if len(opts.Executor) > 0 {
		ex, err := strkey.Encode(strkey.VersionByteContract, []byte(opts.Executor))
		if err != nil {
			return nil, fmt.Errorf("encode executor address: %w", err)
		}
		executor = ex
	}

	v3 := onrampbindings.GenericExtraArgsV3{
		BlockConfirmations: uint32(opts.FinalityConfig),
		CcvArgs:            ccvArgs,
		Ccvs:               ccvAddrs,
		Executor:           executor,
		ExecutorArgs:       append([]byte(nil), opts.ExecutorArgs...),
		GasLimit:           opts.ExecutionGasLimit,
		TokenArgs:          append([]byte(nil), opts.TokenArgs...),
		TokenReceiver:      nil,
	}
	return common.EncodeExtraArgsV3(v3)
}
