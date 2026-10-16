package adapters

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	mcmstypes "github.com/smartcontractkit/mcms/types"
	"github.com/stellar/go-stellar-sdk/strkey"

	ccvdeploymentadapters "github.com/smartcontractkit/chainlink-ccv/deployment/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
)

// StellarCCVTokenPoolOnchainAdapter implements
// ccvdeploymentadapters.TokenPoolOnchainAdapter for Stellar chains, backing
// the chain-agnostic RemoveRemotePool changeset. Deployer-key owned pools are
// reconfigured directly; the returned batch op is ExecInfo-marked and empty
// (dropped by the shared OutputBuilder, mirroring the ownership sequences).
type StellarCCVTokenPoolOnchainAdapter struct {
	// newTarClient and newPoolClient are seams for unit tests; nil uses the
	// generated bindings clients.
	newTarClient  func(invoker bindings.Invoker, contractID string) tarPoolResolver
	newPoolClient func(invoker bindings.Invoker, contractID string) remotePoolClient
}

var _ ccvdeploymentadapters.TokenPoolOnchainAdapter = (*StellarCCVTokenPoolOnchainAdapter)(nil)

// tarPoolResolver is the TokenAdminRegistry surface the adapter needs.
type tarPoolResolver interface {
	GetPool(ctx context.Context, token string) (*string, error)
}

// remotePoolClient is the token-pool remote-pool surface the adapter needs.
type remotePoolClient interface {
	GetRemotePools(ctx context.Context, remoteChainSelector uint64) ([][]byte, error)
	RemoveRemotePool(ctx context.Context, remoteChainSelector uint64, remotePoolAddress []byte) error
}

func (a *StellarCCVTokenPoolOnchainAdapter) tarClient(invoker bindings.Invoker, contractID string) tarPoolResolver {
	if a.newTarClient != nil {
		return a.newTarClient(invoker, contractID)
	}
	return tarbindings.NewTokenAdminRegistryClient(invoker, contractID)
}

func (a *StellarCCVTokenPoolOnchainAdapter) poolClient(invoker bindings.Invoker, contractID string) remotePoolClient {
	if a.newPoolClient != nil {
		return a.newPoolClient(invoker, contractID)
	}
	return tokenpoolbindings.NewTokenPoolClient(invoker, contractID)
}

// RemoveRemotePool implements ccvdeploymentadapters.TokenPoolOnchainAdapter.
func (a *StellarCCVTokenPoolOnchainAdapter) RemoveRemotePool() *cldf_ops.Sequence[ccvdeploymentadapters.RemoveRemotePoolInput, ccvdeploymentadapters.RemoveRemotePoolOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-remove-remote-pool",
		stellarops.ContractDeploymentVersion,
		"Removes a single remote pool from a token's pool on a Stellar chain",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input ccvdeploymentadapters.RemoveRemotePoolInput) (ccvdeploymentadapters.RemoveRemotePoolOutput, error) {
			ch, ok := chains.StellarChains()[input.ChainSelector]
			if !ok {
				return ccvdeploymentadapters.RemoveRemotePoolOutput{}, fmt.Errorf("stellar chain %d not found in environment", input.ChainSelector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch)
			if err != nil {
				return ccvdeploymentadapters.RemoveRemotePoolOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.ChainSelector, err)
			}
			tarStrkey, err := resolveTarStrkey(input.RegistryAddress, input.ExistingAddresses, input.ChainSelector)
			if err != nil {
				return ccvdeploymentadapters.RemoveRemotePoolOutput{}, err
			}
			return a.removeRemotePool(b.GetContext(), dep, tarStrkey, input)
		},
	)
}

// removeRemotePool resolves the token's active pool via the TokenAdminRegistry,
// verifies the requested remote pool is currently configured for the remote
// chain (erroring clearly when it is not), and removes it. The pool stores
// remote addresses verbatim and removes by exact equality, so the raw input is
// matched first; the 32-byte left-padded form (how EVM counterpart pools are
// conventionally wired) is only a fallback — padding unconditionally would
// make a configured unpadded entry impossible to remove.
func (a *StellarCCVTokenPoolOnchainAdapter) removeRemotePool(ctx context.Context, invoker bindings.Invoker, tarStrkey string, input ccvdeploymentadapters.RemoveRemotePoolInput) (ccvdeploymentadapters.RemoveRemotePoolOutput, error) {
	pool, err := a.resolvePool(ctx, invoker, tarStrkey, input.TokenAddress)
	if err != nil {
		return ccvdeploymentadapters.RemoveRemotePoolOutput{}, err
	}
	poolClient := a.poolClient(invoker, pool)
	configured, err := poolClient.GetRemotePools(ctx, input.RemoteChainSelector)
	if err != nil {
		return ccvdeploymentadapters.RemoveRemotePoolOutput{}, fmt.Errorf("failed to get remote pools for token %s remote chain %d on pool %s (chain %d): %w",
			input.TokenAddress, input.RemoteChainSelector, pool, input.ChainSelector, err)
	}
	target := input.RemotePoolAddress
	if !slices.ContainsFunc(configured, func(p []byte) bool { return bytes.Equal(p, target) }) {
		target = common.LeftPadBytes(input.RemotePoolAddress, 32)
		if !slices.ContainsFunc(configured, func(p []byte) bool { return bytes.Equal(p, target) }) {
			return ccvdeploymentadapters.RemoveRemotePoolOutput{}, fmt.Errorf(
				"remote pool 0x%x is not configured for token %s remote chain %d on pool %s (chain %d) (checked the raw and 32-byte left-padded forms)",
				input.RemotePoolAddress, input.TokenAddress, input.RemoteChainSelector, pool, input.ChainSelector)
		}
	}
	if err := poolClient.RemoveRemotePool(ctx, input.RemoteChainSelector, target); err != nil {
		return ccvdeploymentadapters.RemoveRemotePoolOutput{}, fmt.Errorf("failed to remove remote pool for token %s remote chain %d on chain %d: %w",
			input.TokenAddress, input.RemoteChainSelector, input.ChainSelector, err)
	}
	// Direct-exec marker: an ExecInfo-marked empty batch op (deployer-key mode
	// submits directly; the shared OutputBuilder drops the empty op).
	batchOp, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
		ChainSelector: input.ChainSelector,
		Tx: mcmstypes.Transaction{
			OperationMetadata: mcmstypes.OperationMetadata{ContractType: bmpops.ContractType},
			To:                pool,
		},
		ExecInfo: &evmcontract.ExecInfo{Hash: "stellar-remove-remote-pool-direct-exec"},
	}})
	if err != nil {
		return ccvdeploymentadapters.RemoveRemotePoolOutput{}, fmt.Errorf("build batch operation for chain %d: %w", input.ChainSelector, err)
	}
	return ccvdeploymentadapters.RemoveRemotePoolOutput{BatchOps: []mcmstypes.BatchOperation{batchOp}}, nil
}

// ResolveRemotePoolAddress implements ccvdeploymentadapters.TokenPoolOnchainAdapter.
// Resolves the token's active pool via the TokenAdminRegistry and returns its
// raw 32-byte contract ID — the form counterpart pools store to reference a
// stellar pool (left-padded to 32 bytes, the padding is the identity).
func (a *StellarCCVTokenPoolOnchainAdapter) ResolveRemotePoolAddress(e deployment.Environment, chainSelector uint64, tokenAddress string) ([]byte, error) {
	ch, ok := e.BlockChains.StellarChains()[chainSelector]
	if !ok {
		return nil, fmt.Errorf("stellar chain %d not found in environment", chainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return nil, fmt.Errorf("stellar deployer from chain %d: %w", chainSelector, err)
	}
	refs, err := e.DataStore.Addresses().Fetch()
	if err != nil {
		return nil, fmt.Errorf("fetch datastore addresses: %w", err)
	}
	tarStrkey, err := resolveTarStrkey("", refs, chainSelector)
	if err != nil {
		return nil, err
	}
	pool, err := a.resolvePool(context.Background(), dep, tarStrkey, tokenAddress)
	if err != nil {
		return nil, err
	}
	raw, err := strkey.Decode(strkey.VersionByteContract, pool)
	if err != nil {
		return nil, fmt.Errorf("decode pool strkey %s: %w", pool, err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("pool %s decodes to %d bytes, want 32", pool, len(raw))
	}
	return raw, nil
}

// resolvePool resolves the token's active pool strkey via the TokenAdminRegistry.
func (a *StellarCCVTokenPoolOnchainAdapter) resolvePool(ctx context.Context, invoker bindings.Invoker, tarStrkey, token string) (string, error) {
	pool, err := a.tarClient(invoker, tarStrkey).GetPool(ctx, token)
	if err != nil {
		return "", fmt.Errorf("resolve pool for token %s via TokenAdminRegistry %s: %w", token, tarStrkey, err)
	}
	if pool == nil {
		return "", fmt.Errorf("no pool configured for token %s on the TokenAdminRegistry", token)
	}
	return *pool, nil
}

// resolveTarStrkey resolves the TokenAdminRegistry strkey: an explicit registry
// override wins, otherwise the registry ref is looked up in the chain's refs.
func resolveTarStrkey(registryOverride string, refs []datastore.AddressRef, chainSelector uint64) (string, error) {
	if registryOverride != "" {
		if _, err := strkey.Decode(strkey.VersionByteContract, registryOverride); err != nil {
			return "", fmt.Errorf("invalid registry strkey %q: %w", registryOverride, err)
		}
		return registryOverride, nil
	}
	tar := stellarccip.TokenAdminRegistryDatastoreRef()
	for _, ref := range refs {
		if ref.ChainSelector == chainSelector &&
			string(ref.Type) == string(tar.Type) &&
			ref.Version != nil && ref.Version.Equal(tar.Version) {
			return scval.HexToContractStrkey(ref.Address)
		}
	}
	return "", fmt.Errorf("no TokenAdminRegistry ref found for chain %d", chainSelector)
}
