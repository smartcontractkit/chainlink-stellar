package adapters

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	sequences "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

// StellarTokenAdminRegistryManager implements
// tokens.TokenAdminRegistryManager (reader + writer) for Stellar chains: reads
// the pool a token's registry entry points at, and unregisters a token by
// clearing that entry (set_pool with the None option, the on-chain delist
// convention — EVM uses the zero address, Solana the zero pubkey).
type StellarTokenAdminRegistryManager struct{}

var _ tokens.TokenAdminRegistryManager = (*StellarTokenAdminRegistryManager)(nil)

// GetTokenAdminRegistryRef resolves the TokenAdminRegistry ref for the chain
// from the datastore.
func (m *StellarTokenAdminRegistryManager) GetTokenAdminRegistryRef(e deployment.Environment, chainSelector uint64) (datastore.AddressRef, error) {
	return stellarccip.TokenAdminRegistryDatastoreRef().LookupAddressRef(e.DataStore, chainSelector)
}

// GetActivePool returns the pool currently registered for tokenRef as raw 32
// bytes — the same form StellarTokenAdapter.AddressRefToBytes returns for the
// resolved pool ref, so callers compare chain-agnostically with bytes.Equal. A
// token with no registry entry or no pool set is empty bytes with no error;
// any other read failure is an error, never empty bytes.
func (m *StellarTokenAdminRegistryManager) GetActivePool(e deployment.Environment, chainSelector uint64, tokenRef datastore.AddressRef, overrides ...datastore.AddressRef) ([]byte, error) {
	tokenStrkey, err := resolveTARTokenStrkey(e.DataStore, chainSelector, tokenRef)
	if err != nil {
		return nil, err
	}
	tarStrkey, err := m.registryStrkey(e.DataStore, chainSelector, overrides)
	if err != nil {
		return nil, err
	}
	deps, err := stellarReadDeps(e.BlockChains, chainSelector)
	if err != nil {
		return nil, err
	}
	return activePoolBytes(e.OperationsBundle, deps, tarStrkey, tokenStrkey, chainSelector)
}

// activePoolBytes reads the token's registered pool as raw 32 bytes (core,
// unit-tested with a recording invoker). No entry or no pool set is empty
// bytes with no error; any other read failure is an error, never empty bytes.
func activePoolBytes(b cldf_ops.Bundle, deps stellardeps.StellarDeps, tarStrkey, tokenStrkey string, chainSelector uint64) ([]byte, error) {
	report, err := cldf_ops.ExecuteOperation(b, tarops.GetTokenConfig, deps, tarops.GetTokenConfigInput{
		ContractID: tarStrkey, LocalToken: tokenStrkey,
	}, cldf_ops.WithForceExecute[tarops.GetTokenConfigInput, stellardeps.StellarDeps]())
	if err != nil {
		return nil, fmt.Errorf("read token config for %s from TAR %s on chain %d: %w", tokenStrkey, tarStrkey, chainSelector, err)
	}
	if report.Output == nil || report.Output.TokenPool == nil {
		return nil, nil
	}
	raw, err := strkey.Decode(strkey.VersionByteContract, *report.Output.TokenPool)
	if err != nil {
		return nil, fmt.Errorf("decode pool strkey %s: %w", *report.Output.TokenPool, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("pool %s decodes to %d bytes, want 32", *report.Output.TokenPool, len(raw))
	}
	return raw, nil
}

// registryStrkey resolves the registry strkey: the first override that
// resolves from the datastore (an override that does not resolve is silently
// skipped, EVM/Solana parity), else the datastore default.
func (m *StellarTokenAdminRegistryManager) registryStrkey(ds datastore.DataStore, chainSelector uint64, overrides []datastore.AddressRef) (string, error) {
	for _, o := range overrides {
		if o.Address != "" {
			if cid, err := ownership.NormalizeContractAddress(o.Address); err == nil {
				return cid, nil
			}
			continue
		}
		if o.Type != "" {
			if full, err := stellarccip.LookupAddressRef(ds, chainSelector, o.Type, o.Version, o.Qualifier); err == nil {
				if cid, err := ownership.NormalizeContractAddress(full.Address); err == nil {
					return cid, nil
				}
			}
		}
	}
	return stellarccip.TokenAdminRegistryDatastoreRef().LookupStrkey(ds, chainSelector)
}

// UnregisterToken clears the token's registry pool (set_pool with the None
// option). The unregister is unconditional w.r.t. the current pool — the
// caller owns the read-check guard (tokens.TokenAdminRegistryWriter).
func (m *StellarTokenAdminRegistryManager) UnregisterToken() *cldf_ops.Sequence[tokens.UnregisterTokenSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:unregister-token",
		stellarops.ContractDeploymentVersion,
		"Unregisters a token from the TokenAdminRegistry on a Stellar chain by clearing its pool",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.UnregisterTokenSequenceInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.Selector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.Selector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.Selector, err)
			}
			return unregisterToken(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), input)
		},
	)
}

// unregisterToken is the unregister core (unit-tested with a recording
// invoker). set_pool authorizes the token's administrator, not the registry
// owner, so the write routes by the current administrator read from the
// registry: deployer → direct op plus the marker; CLL timelock → one MCMS
// proposal transaction; any other administrator → fail closed.
func unregisterToken(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	deployerAddr string,
	input tokens.UnregisterTokenSequenceInput,
) (sequences.OnChainOutput, error) {
	tokenStrkey, err := resolveTARTokenStrkey(input.ExistingDataStore, input.Selector, input.TokenRef)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	tarStrkey, err := resolveTARStrkey(input.ExistingDataStore, input.Selector, input.RegistryRef)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("resolve TokenAdminRegistry on chain %d: %w", input.Selector, err)
	}

	configReport, err := cldf_ops.ExecuteOperation(b, tarops.GetTokenConfig, deps, tarops.GetTokenConfigInput{
		ContractID: tarStrkey, LocalToken: tokenStrkey,
	}, cldf_ops.WithForceExecute[tarops.GetTokenConfigInput, stellardeps.StellarDeps]())
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read token config for %s from TAR %s on chain %d: %w", tokenStrkey, tarStrkey, input.Selector, err)
	}
	if configReport.Output == nil {
		// Unreachable from the binding (a parsed config is always non-nil on
		// success), kept as a guard for the op's nil contract.
		return sequences.OnChainOutput{}, fmt.Errorf("no token config returned for %s from TAR %s", tokenStrkey, tarStrkey)
	}
	admin := derefStr(configReport.Output.Administrator)
	if admin == "" {
		// A zero config (no administrator, no pending admin, no pool) is a
		// token with no registry entry: nothing to unregister (idempotent; the
		// caller's active-pool guard normally prevents reaching here).
		if configReport.Output.TokenPool == nil && configReport.Output.PendingAdministrator == nil {
			b.Logger.Warnf("skipping TAR unregister for token %s on chain %d: token is not registered", tokenStrkey, input.Selector)
			return sequences.OnChainOutput{}, nil
		}
		return sequences.OnChainOutput{}, fmt.Errorf("token %s on chain %d has no administrator; cannot clear its pool", tokenStrkey, input.Selector)
	}
	timelock, hasTimelock, err := cllTimelockStrkey(input.ExistingDataStore, input.Selector)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	tarType := datastore.ContractType(tarops.ContractType)
	switch {
	case admin == deployerAddr:
		if _, err := cldf_ops.ExecuteOperation(b, tarops.SetPool, deps, tarops.SetPoolInput{
			ContractID: tarStrkey, LocalToken: tokenStrkey, Pool: nil,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("clear pool for token %s in TAR %s: %w", tokenStrkey, tarStrkey, err)
		}
		return dynamicConfigOutput(input.Selector, tarType, tarStrkey, "stellar-unregister-token-direct-exec", true, nil)
	case hasTimelock && admin == timelock:
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("set_pool", []xdr.ScVal{
			scval.AddressToScVal(tokenStrkey), scval.OptionalAddressToScVal(nil),
		})
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		return dynamicConfigOutput(input.Selector, tarType, tarStrkey, "stellar-unregister-token-direct-exec", false,
			[]evmcontract.WriteOutput{poolProposalWrite(input.Selector, tarType, tarStrkey, data)})
	default:
		return sequences.OnChainOutput{}, fmt.Errorf(
			"cannot unregister token %s on chain %d: administrator %q is neither deployer %q nor timelock %q",
			tokenStrkey, input.Selector, admin, deployerAddr, timelock)
	}
}

// resolveTARStrkey resolves the TokenAdminRegistry strkey: an override ref
// wins (its address, else a datastore lookup by its fields — an override that
// does not resolve falls through to the default), then the datastore default.
func resolveTARStrkey(ds datastore.DataStore, chainSelector uint64, override datastore.AddressRef) (string, error) {
	if override.Address != "" {
		return ownership.NormalizeContractAddress(override.Address)
	}
	if override.Type != "" {
		if full, err := stellarccip.LookupAddressRef(ds, chainSelector, override.Type, override.Version, override.Qualifier); err == nil {
			if cid, err := ownership.NormalizeContractAddress(full.Address); err == nil {
				return cid, nil
			}
		}
	}
	return stellarccip.TokenAdminRegistryDatastoreRef().LookupStrkey(ds, chainSelector)
}

// resolveTARTokenStrkey resolves the token strkey for a TAR interaction: an
// explicit ref address wins, else a datastore lookup by the ref's
// (type, version, qualifier).
func resolveTARTokenStrkey(ds datastore.DataStore, chainSelector uint64, ref datastore.AddressRef) (string, error) {
	if ref.Address != "" {
		cid, err := normalizeFeeTokenAddress(ref.Address)
		if err != nil {
			return "", fmt.Errorf("normalize token address on chain %d: %w", chainSelector, err)
		}
		return cid, nil
	}
	if ref.Type == "" {
		return "", fmt.Errorf("token ref has neither an address nor a type to look up on chain %d", chainSelector)
	}
	full, err := stellarccip.LookupAddressRef(ds, chainSelector, ref.Type, ref.Version, ref.Qualifier)
	if err != nil {
		return "", fmt.Errorf("resolve token ref on chain %d: %w", chainSelector, err)
	}
	cid, err := normalizeFeeTokenAddress(full.Address)
	if err != nil {
		return "", fmt.Errorf("normalize resolved token address on chain %d: %w", chainSelector, err)
	}
	return cid, nil
}
