package sequences

import (
	"fmt"
	"strings"

	cldfchain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

// StellarWithdrawFeeTokensSequenceID is the pipeline sequence ID for WithdrawFeeTokens on
// Stellar (OnRamp, VVR, Committee Verifier). Use [StellarFeeAggregatorAdapter.WithdrawFeeTokens],
// which wraps [ApplyStellarWithdrawFeeTokens] with the [cldf.Environment] needed for datastore
// lookups.
const StellarWithdrawFeeTokensSequenceID = "stellar-withdraw-fee-tokens"

// readContractOwner is a seam over ownership.ContractOwner so tests can drive the proposal arm
// of ApplyStellarWithdrawFeeTokens without a live chain; the direct arm still needs one to
// execute the withdrawal.
var readContractOwner = ownership.ContractOwner

// ApplyStellarWithdrawFeeTokens sweeps accumulated fee token balances to the configured fee
// aggregator on the Stellar contracts that hold CCIP fee funds. When in.Contracts is empty,
// OnRamp, Versioned Verifier Resolver, and Committee Verifier are swept; otherwise only the
// referenced contracts are, resolved by full ref (Type + Version + Qualifier).
//
// withdraw_fee_tokens is permissionless on chain (it only sends to the trusted fee aggregator),
// so MCMS adds no authorization here; each contract is instead routed the same way the EVM fee
// adapters route their allowed-caller check: when the deployer owns the contract the withdrawal
// executes directly and returns no batch ops, otherwise it is emitted as an unexecuted MCMS
// write so the shared changeset proposes it via Build(cfg.MCMS). Governed environments (the
// owner is a timelock) therefore get a proposal, dev environments (the deployer owns) execute
// immediately.
func ApplyStellarWithdrawFeeTokens(b cldfops.Bundle, chains cldfchain.BlockChains, env cldf.Environment, in fees.WithdrawFeeTokensForChain) (output seqcore.OnChainOutput, err error) {
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
	deployerAddr := dep.SignerAddress()
	ctx := b.GetContext()

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

	// resolveSwept resolves the strkey-addressed ref of a swept contract type: the explicit ref
	// when one was provided, else the default ref. The address must be a strkey for the owner
	// read and the MCMS transaction target.
	resolveSwept := func(typ datastore.ContractType, defaultRef stellarccip.DatastoreSorobanContractRef) (datastore.AddressRef, error) {
		if explicitRef, ok := explicit[typ]; ok {
			id, err := stellarccip.LookupStellarContractStrkey(env.DataStore, in.ChainSelector, explicitRef.Type, explicitRef.Version, explicitRef.Qualifier)
			if err != nil {
				return datastore.AddressRef{}, fmt.Errorf("resolve %s: %w", typ, err)
			}
			resolved := explicitRef
			resolved.Address = id
			return resolved, nil
		}
		id, err := defaultRef.LookupStrkey(env.DataStore, in.ChainSelector)
		if err != nil {
			return datastore.AddressRef{}, fmt.Errorf("resolve %s: %w", typ, err)
		}
		resolved := defaultRef.PartialAddressRef()
		resolved.Address = id
		return resolved, nil
	}

	// withdraw routes one swept contract: the withdrawal executes directly when the deployer
	// owns it, otherwise it is emitted as an unexecuted MCMS write for the shared changeset to
	// propose.
	withdraw := func(typ datastore.ContractType, defaultRef stellarccip.DatastoreSorobanContractRef, execute func(id string) error) error {
		ref, err := resolveSwept(typ, defaultRef)
		if err != nil {
			return err
		}
		owner, err := readContractOwner(ctx, deps, ref)
		if err != nil {
			return fmt.Errorf("read owner of %s %s: %w", typ, ref.Address, err)
		}
		if owner == deployerAddr {
			return execute(ref.Address)
		}
		wo, err := withdrawFeeTokensWrite(in.ChainSelector, typ, ref.Address, feeTokens)
		if err != nil {
			return err
		}
		batchOp, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{wo})
		if err != nil {
			return err
		}
		output.BatchOps = append(output.BatchOps, batchOp)
		return nil
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
		if err := withdraw(onRampType, stellarccip.OnRampDatastoreRef(), func(id string) error {
			_, err := cldfops.ExecuteOperation(b, onrampops.WithdrawFeeTokens, deps, onrampops.WithdrawFeeTokensInput{
				ContractID: id,
				FeeTokens:  feeTokens,
			})
			return err
		}); err != nil {
			return output, fmt.Errorf("onramp withdraw fee tokens on chain %d: %w", in.ChainSelector, err)
		}
	}

	if _, ok := sweep[vvrType]; ok {
		if err := withdraw(vvrType, stellarccip.VVRDatastoreRef(), func(id string) error {
			_, err := cldfops.ExecuteOperation(b, vvrops.WithdrawFeeTokens, deps, vvrops.WithdrawFeeTokensInput{
				ContractID: id,
				FeeTokens:  feeTokens,
			})
			return err
		}); err != nil {
			return output, fmt.Errorf("vvr withdraw fee tokens on chain %d: %w", in.ChainSelector, err)
		}
	}

	if _, ok := sweep[cvType]; ok {
		if err := withdraw(cvType, stellarccip.CommitteeVerifierDatastoreRef(), func(id string) error {
			_, err := cldfops.ExecuteOperation(b, cvops.WithdrawFeeTokens, deps, cvops.WithdrawFeeTokensInput{
				ContractID: id,
				FeeTokens:  feeTokens,
			})
			return err
		}); err != nil {
			return output, fmt.Errorf("committee verifier withdraw fee tokens on chain %d: %w", in.ChainSelector, err)
		}
	}

	return output, nil
}

// withdrawFeeTokensWrite builds the unexecuted MCMS write for a fee-token withdrawal on one
// contract. The Soroban invoke payload carries the exact argument shape the generated bindings
// send (a single Vec<Address> of fee tokens), and the write mirrors the transfer-ownership
// propose arm, so the shared changeset turns it into a timelock proposal via Build(cfg.MCMS).
func withdrawFeeTokensWrite(sel uint64, typ datastore.ContractType, contractID string, feeTokens []string) (evmcontract.WriteOutput, error) {
	data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("withdraw_fee_tokens", []xdr.ScVal{scval.AddressSliceToScVal(feeTokens)})
	if err != nil {
		return evmcontract.WriteOutput{}, fmt.Errorf("encode withdraw_fee_tokens payload for %s: %w", typ, err)
	}
	return evmcontract.WriteOutput{
		ChainSelector: sel,
		Tx: mcmstypes.Transaction{
			OperationMetadata: mcmstypes.OperationMetadata{
				ContractType: string(typ),
			},
			To:               contractID,
			Data:             data,
			AdditionalFields: stellarMCMSTxAdditionalFields(),
		},
	}, nil
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
