package adapters

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var _ fees.FeeAggregatorAdapter = (*StellarFeeAggregatorAdapter)(nil)

// StellarFeeAggregatorAdapter implements fees.FeeAggregatorAdapter.
// SetFeeAggregator updates OnRamp dynamic config, VVR, and CommitteeVerifier (see [stellarsequences.ApplyStellarFeeAggregator]).
// WithdrawFeeTokens sweeps accumulated fee token balances to the configured fee aggregator
// on the same contracts (see [stellarsequences.ApplyStellarWithdrawFeeTokens]).
// GetFeeAggregator reads the canonical value from the Versioned Verifier Resolver.
type StellarFeeAggregatorAdapter struct{}

func (a *StellarFeeAggregatorAdapter) SetFeeAggregator(e cldf.Environment) *cldf_ops.Sequence[fees.FeeAggregatorForChain, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		stellarsequences.StellarSetFeeAggregatorSequenceID,
		stellarops.ContractDeploymentVersion,
		"Sets fee aggregator on Stellar OnRamp, VVR, and CommitteeVerifier",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in fees.FeeAggregatorForChain) (seqcore.OnChainOutput, error) {
			return stellarsequences.ApplyStellarFeeAggregator(b, chains, e, in)
		},
	)
}

func (a *StellarFeeAggregatorAdapter) WithdrawFeeTokens(e cldf.Environment) *cldf_ops.Sequence[fees.WithdrawFeeTokensForChain, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		stellarsequences.StellarWithdrawFeeTokensSequenceID,
		stellarops.ContractDeploymentVersion,
		"Withdraws accumulated fee token balances to the fee aggregator on Stellar OnRamp, VVR, and CommitteeVerifier",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in fees.WithdrawFeeTokensForChain) (seqcore.OnChainOutput, error) {
			return stellarsequences.ApplyStellarWithdrawFeeTokens(b, chains, e, in)
		},
	)
}

func (a *StellarFeeAggregatorAdapter) GetFeeAggregator(e cldf.Environment, chainSelector uint64) (string, error) {
	if e.DataStore == nil {
		return "", fmt.Errorf("environment DataStore is nil")
	}
	ch, ok := e.BlockChains.StellarChains()[chainSelector]
	if !ok {
		return "", fmt.Errorf("stellar chain %d not found in environment", chainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return "", fmt.Errorf("stellar deployer from chain: %w", err)
	}
	vvrID, err := stellarccip.GetVVRStrkey(e.DataStore, chainSelector)
	if err != nil {
		return "", fmt.Errorf("resolve VVR for chain %d: %w", chainSelector, err)
	}
	ctx := context.Background()
	if e.GetContext != nil {
		ctx = e.GetContext()
	}
	client := vvrbindings.NewVersionedVerifierResolverClient(dep, vvrID)
	return client.GetFeeAggregator(ctx)
}
