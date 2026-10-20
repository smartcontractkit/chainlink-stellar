package sequences

import (
	"context"
	"fmt"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// StellarSetTokenTransferFeeInput is the sequence input for applying per-token transfer fees.
type StellarSetTokenTransferFeeInput struct {
	fees.SetTokenTransferFeeSequenceInput
	FQContractID string
}

// StellarSetTokenTransferFee applies per-token transfer fee configs on the Stellar FeeQuoter.
var StellarSetTokenTransferFee = cldfops.NewSequence(
	"stellar-set-token-transfer-fee",
	stellarops.ContractDeploymentVersion,
	"Set per-token transfer fees on Stellar FeeQuoter",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarSetTokenTransferFeeInput) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.Selector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.Selector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)

		var cfgArgs []fqbindings.TokenFeeConfigArgs
		for destSel, tokenFees := range in.Settings {
			for token, feeArgs := range tokenFees {
				if feeArgs == nil {
					continue
				}
				cfgArgs = append(cfgArgs, fqbindings.TokenFeeConfigArgs{
					DestChainSelector: destSel,
					Token:             token,
					Config: fqbindings.TokenTransferFeeConfig{
						DestBytesOverhead: feeArgs.DestBytesOverhead,
						DestGasOverhead:   feeArgs.DestGasOverhead,
						FeeUsdCents:       feeArgs.MinFeeUSDCents,
						IsEnabled:         feeArgs.IsEnabled,
					},
				})
			}
		}
		if len(cfgArgs) == 0 {
			return seqcore.OnChainOutput{}, nil
		}
		_, err = cldfops.ExecuteOperation(b, fqops.ApplyTokenFeeConfigs, deps, fqops.ApplyTokenFeeConfigsInput{
			ContractID: in.FQContractID,
			AddConfigs: cfgArgs,
		})
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("apply token fee configs on chain %d: %w", in.Selector, err)
		}
		return seqcore.OnChainOutput{}, nil
	},
)

// StellarApplyDestChainConfigInput is the sequence input for applying dest chain configs.
type StellarApplyDestChainConfigInput struct {
	fees.ApplyDestChainConfigSequenceInput
	FQContractID string
}

// StellarApplyDestChainConfig applies destination chain configs on the Stellar FeeQuoter.
var StellarApplyDestChainConfig = cldfops.NewSequence(
	"stellar-apply-dest-chain-config",
	stellarops.ContractDeploymentVersion,
	"Apply dest chain config updates on Stellar FeeQuoter",
	func(b cldfops.Bundle, chains cldfchain.BlockChains, in StellarApplyDestChainConfigInput) (seqcore.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.Selector]
		if !ok {
			return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.Selector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		deps := stellardeps.FromDeployer(dep)

		var cfgArgs []fqbindings.DestChainConfigArgs
		for destSel, cfg := range in.Settings {
			cfgArgs = append(cfgArgs, fqbindings.DestChainConfigArgs{
				DestChainSelector: destSel,
				Config: fqbindings.DestChainConfig{
					IsEnabled:             cfg.IsEnabled,
					MaxDataBytes:          cfg.MaxDataBytes,
					MaxPerMsgGasLimit:     cfg.MaxPerMsgGasLimit,
					DestGasOverhead:       cfg.DestGasOverhead,
					DestGasPerPayloadByte: uint32(cfg.DestGasPerPayloadByteBase),
					DefaultTokenFeeUsd:    uint32(cfg.DefaultTokenFeeUSDCents),
					DefaultTokenDestGas:   cfg.DefaultTokenDestGasOverhead,
					DefaultTxGasLimit:     cfg.DefaultTxGasLimit,
					NetworkFeeUsdCents:    uint32(cfg.NetworkFeeUSDCents),
					LinkPremiumPercent:    uint32(cfg.V2Params.LinkFeeMultiplierPercent),
				},
			})
		}
		if len(cfgArgs) == 0 {
			return seqcore.OnChainOutput{}, nil
		}
		_, err = cldfops.ExecuteOperation(b, fqops.ApplyDestChainConfigs, deps, fqops.ApplyDestChainConfigsInput{
			ContractID: in.FQContractID,
			Configs:    cfgArgs,
		})
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("apply dest chain configs on chain %d: %w", in.Selector, err)
		}
		return seqcore.OnChainOutput{}, nil
	},
)

// StellarSetFeeAggregatorSequenceID is the pipeline sequence ID for SetFeeAggregator on Stellar
// (OnRamp, VVR, Committee Verifier). Use [StellarFeeAggregatorAdapter.SetFeeAggregator], which
// wraps [ApplyStellarFeeAggregator] with the [cldf.Environment] needed for datastore lookups.
const StellarSetFeeAggregatorSequenceID = "stellar-set-fee-aggregator"

// ApplyStellarFeeAggregator updates the fee aggregator on Stellar contracts that hold CCIP fee funds.
// When in.Contracts is empty, OnRamp, Versioned Verifier Resolver, and Committee Verifier are updated.
func ApplyStellarFeeAggregator(b cldfops.Bundle, chains cldfchain.BlockChains, env cldf.Environment, in fees.FeeAggregatorForChain) (seqcore.OnChainOutput, error) {
	if env.DataStore == nil {
		return seqcore.OnChainOutput{}, fmt.Errorf("environment DataStore is nil")
	}
	ctx := stellarFeeAggregatorContext(b, env)
	feeAgg, err := stellarutil.ParseFeeAggregatorAddress(in.FeeAggregator)
	if err != nil {
		return seqcore.OnChainOutput{}, err
	}
	ch, ok := chains.StellarChains()[in.ChainSelector]
	if !ok {
		return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
	if err != nil {
		return seqcore.OnChainOutput{}, fmt.Errorf("stellar deployer from chain: %w", err)
	}

	want := map[datastore.ContractType]struct{}{}
	if len(in.Contracts) == 0 {
		want[stellarccip.OnRampDatastoreRef().Type] = struct{}{}
		want[stellarccip.VVRDatastoreRef().Type] = struct{}{}
		want[stellarccip.CommitteeVerifierDatastoreRef().Type] = struct{}{}
	} else {
		for _, c := range in.Contracts {
			want[c.Type] = struct{}{}
		}
	}

	if _, ok := want[stellarccip.OnRampDatastoreRef().Type]; ok {
		onID, lerr := stellarccip.GetOnRampStrkey(env.DataStore, in.ChainSelector)
		if lerr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("resolve OnRamp: %w", lerr)
		}
		onClient := onrampbindings.NewOnRampClient(dep, onID)
		dyn, gerr := onClient.GetDynamicConfig(ctx)
		if gerr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("onramp get dynamic config: %w", gerr)
		}
		if dyn == nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("onramp dynamic config is nil")
		}
		next := *dyn
		next.FeeAggregator = feeAgg
		if serr := onClient.SetDynamicConfig(ctx, next); serr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("onramp set dynamic config: %w", serr)
		}
	}

	if _, ok := want[stellarccip.VVRDatastoreRef().Type]; ok {
		vvrID, lerr := stellarccip.GetVVRStrkey(env.DataStore, in.ChainSelector)
		if lerr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("resolve VVR: %w", lerr)
		}
		vvrClient := vvrbindings.NewVersionedVerifierResolverClient(dep, vvrID)
		if serr := vvrClient.SetFeeAggregator(ctx, feeAgg); serr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("vvr set_fee_aggregator: %w", serr)
		}
	}

	if _, ok := want[stellarccip.CommitteeVerifierDatastoreRef().Type]; ok {
		cvID, lerr := stellarccip.GetCommitteeVerifierStrkey(env.DataStore, in.ChainSelector)
		if lerr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("resolve CommitteeVerifier: %w", lerr)
		}
		cvClient := cvbindings.NewCommitteeVerifierClient(dep, cvID)
		dyn, gerr := cvClient.GetDynamicConfig(ctx)
		if gerr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("committee verifier get dynamic config: %w", gerr)
		}
		if dyn == nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("committee verifier dynamic config is nil")
		}
		next := *dyn
		next.FeeAggregator = &feeAgg
		if serr := cvClient.SetDynamicConfig(ctx, next); serr != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("committee verifier set dynamic config: %w", serr)
		}
	}

	return seqcore.OnChainOutput{}, nil
}

// stellarFeeAggregatorContext prefers the CLDF operations bundle context (cancellation/timeouts
// from ExecuteSequence), then [cldf.Environment.GetContext], then [context.Background].
func stellarFeeAggregatorContext(b cldfops.Bundle, env cldf.Environment) context.Context {
	if b.GetContext != nil {
		return b.GetContext()
	}
	if env.GetContext != nil {
		return env.GetContext()
	}
	return context.Background()
}
