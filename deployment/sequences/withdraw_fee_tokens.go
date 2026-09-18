package sequences

import (
	"fmt"
	"strings"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/stellar/go-stellar-sdk/strkey"

	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

// StellarWithdrawFeeTokensSequenceID is the pipeline sequence ID for WithdrawFeeTokens on
// Stellar (OnRamp, VVR, Committee Verifier). Use [StellarFeeAggregatorAdapter.WithdrawFeeTokens],
// which wraps [ApplyStellarWithdrawFeeTokens] with the [cldf.Environment] needed for datastore
// lookups.
const StellarWithdrawFeeTokensSequenceID = "stellar-withdraw-fee-tokens"

// ApplyStellarWithdrawFeeTokens sweeps accumulated fee token balances to the configured fee
// aggregator on the Stellar contracts that hold CCIP fee funds. When in.Contracts is empty,
// OnRamp, Versioned Verifier Resolver, and Committee Verifier are swept; otherwise only the
// referenced contracts are, resolved by full ref (Type + Version + Qualifier).
//
// The underlying withdraw_fee_tokens is permissionless (it only sends to the trusted fee
// aggregator), so the withdrawals are executed directly and no batch ops are returned.
func ApplyStellarWithdrawFeeTokens(b cldfops.Bundle, chains cldfchain.BlockChains, env cldf.Environment, in fees.WithdrawFeeTokensForChain) (seqcore.OnChainOutput, error) {
	if env.DataStore == nil {
		return seqcore.OnChainOutput{}, fmt.Errorf("environment DataStore is nil")
	}
	if len(in.FeeTokens) == 0 {
		return seqcore.OnChainOutput{}, fmt.Errorf("at least one fee token is required (chain=%d)", in.ChainSelector)
	}
	feeTokens := make([]string, 0, len(in.FeeTokens))
	for i, tok := range in.FeeTokens {
		if tok.Amount != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("amount is not supported at feeTokens[%d] (chain=%d, token=%s): the Stellar withdrawal always sweeps the full balance, omit amount",
				i, in.ChainSelector, tok.Token)
		}
		addr, err := parseStellarFeeTokenAddress(tok.Token)
		if err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("invalid fee token at feeTokens[%d] (chain=%d): %w", i, in.ChainSelector, err)
		}
		feeTokens = append(feeTokens, addr)
	}

	ch, ok := chains.StellarChains()[in.ChainSelector]
	if !ok {
		return seqcore.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", in.ChainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return seqcore.OnChainOutput{}, fmt.Errorf("stellar deployer from chain: %w", err)
	}
	deps := stellardeps.FromDeployer(dep)

	onRampType := stellarccip.OnRampDatastoreRef().Type
	vvrType := stellarccip.VVRDatastoreRef().Type
	cvType := stellarccip.CommitteeVerifierDatastoreRef().Type

	// explicit maps contract types from in.Contracts to their full refs; sweep is the set of
	// contract types to withdraw from (defaults to every fee-holding contract).
	explicit := map[datastore.ContractType]datastore.AddressRef{}
	sweep := map[datastore.ContractType]struct{}{}
	if len(in.Contracts) == 0 {
		sweep[onRampType] = struct{}{}
		sweep[vvrType] = struct{}{}
		sweep[cvType] = struct{}{}
	} else {
		for _, ref := range in.Contracts {
			explicit[ref.Type] = ref
			sweep[ref.Type] = struct{}{}
		}
	}

	// contractID resolves the strkey for a swept contract type: the explicit ref when one was
	// provided, else the default ref.
	contractID := func(typ datastore.ContractType, defaultRef stellarccip.DatastoreSorobanContractRef) (string, error) {
		if ref, ok := explicit[typ]; ok {
			id, err := stellarccip.LookupStellarContractStrkey(env.DataStore, in.ChainSelector, ref.Type, ref.Version, ref.Qualifier)
			if err != nil {
				return "", fmt.Errorf("resolve %s: %w", typ, err)
			}
			return id, nil
		}
		id, err := defaultRef.LookupStrkey(env.DataStore, in.ChainSelector)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", typ, err)
		}
		return id, nil
	}

	// Reject unsupported contract types before executing anything, so a mixed Contracts list
	// cannot withdraw from its supported entries and then fail.
	for typ := range sweep {
		if typ != onRampType && typ != vvrType && typ != cvType {
			return seqcore.OnChainOutput{}, fmt.Errorf("cannot withdraw fee tokens from contract type %s on chain %d: supported types are %s, %s, %s",
				typ, in.ChainSelector, onRampType, vvrType, cvType)
		}
	}

	if _, ok := sweep[onRampType]; ok {
		id, err := contractID(onRampType, stellarccip.OnRampDatastoreRef())
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		if _, err = cldfops.ExecuteOperation(b, onrampops.WithdrawFeeTokens, deps, onrampops.WithdrawFeeTokensInput{
			ContractID: id,
			FeeTokens:  feeTokens,
		}); err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("onramp withdraw fee tokens on chain %d: %w", in.ChainSelector, err)
		}
	}

	if _, ok := sweep[vvrType]; ok {
		id, err := contractID(vvrType, stellarccip.VVRDatastoreRef())
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		if _, err = cldfops.ExecuteOperation(b, vvrops.WithdrawFeeTokens, deps, vvrops.WithdrawFeeTokensInput{
			ContractID: id,
			FeeTokens:  feeTokens,
		}); err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("vvr withdraw fee tokens on chain %d: %w", in.ChainSelector, err)
		}
	}

	if _, ok := sweep[cvType]; ok {
		id, err := contractID(cvType, stellarccip.CommitteeVerifierDatastoreRef())
		if err != nil {
			return seqcore.OnChainOutput{}, err
		}
		if _, err = cldfops.ExecuteOperation(b, cvops.WithdrawFeeTokens, deps, cvops.WithdrawFeeTokensInput{
			ContractID: id,
			FeeTokens:  feeTokens,
		}); err != nil {
			return seqcore.OnChainOutput{}, fmt.Errorf("committee verifier withdraw fee tokens on chain %d: %w", in.ChainSelector, err)
		}
	}

	return seqcore.OnChainOutput{}, nil
}

// parseStellarFeeTokenAddress validates a fee token address for the Stellar withdrawal
// contracts. It accepts a Stellar strkey (G… account or C… contract) verbatim; other forms
// are rejected because the raw bytes of a hex address are ambiguous between account and
// contract strkeys.
func parseStellarFeeTokenAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("fee token address is empty")
	}
	switch s[0] {
	case 'G':
		if _, err := strkey.Decode(strkey.VersionByteAccountID, s); err != nil {
			return "", fmt.Errorf("decode account strkey: %w", err)
		}
		return s, nil
	case 'C':
		if _, err := strkey.Decode(strkey.VersionByteContract, s); err != nil {
			return "", fmt.Errorf("decode contract strkey: %w", err)
		}
		return s, nil
	default:
		return "", fmt.Errorf("fee token must be a Stellar G/C strkey, got %q", s)
	}
}
