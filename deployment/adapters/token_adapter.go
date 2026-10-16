package adapters

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	datastore_utils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	stellarutil "github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	bnmops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	sacops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/sac_token"
	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
)

var _ tokens.TokenAdapter = (*StellarTokenAdapter)(nil)

// StellarTokenAdapter implements tokens.TokenAdapter for Stellar token pools.
// Pool deployment and TAR registration are handled by the deploy sequences
// below (shared TokenExpansion pipeline); stellar-side pool chain updates are
// applied during PostConnect, so ConfigureTokenForTransfersSequence is a no-op.
// The adapter's remaining role is address/decimals resolution so the shared
// ConfigureTokensForTransfers changeset can resolve Stellar pool/token
// addresses when configuring EVM pools with Stellar remotes.
type StellarTokenAdapter struct{}

func (a *StellarTokenAdapter) AddressRefToBytes(ref datastore.AddressRef) ([]byte, error) {
	addr := strings.TrimPrefix(ref.Address, "0x")
	return hex.DecodeString(addr)
}

// DeriveTokenAddress resolves the token a Stellar pool wraps for cross-family
// config. Burn-mint pools wrap their own qualified BnmToken row (the shared
// pipeline deploys one BnM token per pool, keyed by the pool's qualifier);
// lock-release pools wrap the single SAC test token.
func (a *StellarTokenAdapter) DeriveTokenAddress(e deployment.Environment, chainSelector uint64, poolRef datastore.AddressRef) (string, error) {
	switch string(poolRef.Type) {
	case stellarccip.DevenvCatalogBurnMintTokenPoolType, bmpops.ContractType:
		ref, err := stellarccip.LookupAddressRef(e.DataStore, chainSelector,
			datastore.ContractType(bnmops.ContractType), stellarops.ContractDeploymentVersion, poolRef.Qualifier)
		if err != nil {
			return "", fmt.Errorf("find BnM token for pool qualifier %q on chain %d: %w", poolRef.Qualifier, chainSelector, err)
		}
		return ref.Address, nil
	default:
		qualifier := poolRef.Qualifier
		if qualifier == "" {
			qualifier = stellarccip.DevenvTestTokenPoolQualifier
		}
		tokenRef, err := datastore_utils.FindAndFormatRef(e.DataStore, datastore.AddressRef{
			Type:      datastore.ContractType(stellarccip.TestTokenContractType),
			Version:   stellarops.ContractDeploymentVersion,
			Qualifier: qualifier,
		}, chainSelector, datastore_utils.FullRef)
		if err != nil {
			return "", fmt.Errorf("find test token ref for chain %d: %w", chainSelector, err)
		}
		// The caller feeds this straight back into ResolveTokenRef as a datastore AddressRef.Address
		// and does its own AddressRefToBytes conversion, so return the datastore address form.
		return tokenRef.Address, nil
	}
}

const stellarTestTokenDecimals = 7

func (a *StellarTokenAdapter) DeriveTokenDecimals(_ deployment.Environment, _ uint64, _ datastore.AddressRef, _ []byte) (uint8, error) {
	return stellarTestTokenDecimals, nil
}

func (a *StellarTokenAdapter) DeriveTokenPoolCounterpart(_ deployment.Environment, _ uint64, tokenPool []byte, _ []byte) ([]byte, error) {
	return tokenPool, nil
}

// bnmTokenDecimals is the decimals every lane BnM token uses (SAC convention).
const bnmTokenDecimals uint32 = 7

// DeployToken deploys a BnM Soroban token for a shared-pipeline pair and
// pre-mints the deployer allocation. The pre-mint goes through mint_as before
// any pool exists: the deployer is still MINTERS[0] right after initialize
// (DeployTokenPoolForToken hands mint authority to the pool afterwards), so no
// secondary-minter setup is needed — the adapter's equivalent of the EVM
// constructor pre-mint.
func (a *StellarTokenAdapter) DeployToken() *cldf_ops.Sequence[tokens.DeployTokenInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return stellarDeployToken
}

var stellarDeployToken = cldf_ops.NewSequence(
	"StellarTokenAdapter.DeployToken",
	stellarops.ContractDeploymentVersion,
	"Deploys a BnM Soroban token and pre-mints the deployer allocation",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in tokens.DeployTokenInput) (sequences.OnChainOutput, error) {
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("build stellar deployer: %w", err)
		}
		deps := stellardeps.FromDeployer(dep)
		deployerAddr := ch.Signer.KeypairFull().Address()

		tokenRef := stellarccip.BnmTokenDatastoreRef()
		tokenRef.Qualifier = in.Symbol
		if existing, err := tokenRef.LookupAddressRef(in.ExistingDataStore, in.ChainSelector); err == nil {
			return sequences.OnChainOutput{Addresses: []datastore.AddressRef{existing}}, nil
		}

		wasmPath, err := stellarReleaseWasm("bnm_token.wasm", "BnM token")
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		deployOut, err := execStellarAdapterOp(b, deps, bnmops.Deploy, stellarops.DeployInput{
			WasmPath: wasmPath,
			Salt:     stellardeployment.GenerateDeterministicSalt(deployerAddr, "bnm-token-"+in.Symbol),
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("deploy BnM token: %w", err)
		}
		tokenID := deployOut.ContractID

		name := in.Name
		if name == "" {
			name = in.Symbol
		}
		if _, err := execStellarAdapterOp(b, deps, bnmops.Initialize, bnmops.InitializeInput{
			ContractID: tokenID,
			Admin:      deployerAddr,
			Name:       name,
			Symbol:     in.Symbol,
			Decimals:   bnmTokenDecimals,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("initialize BnM token: %w", err)
		}

		if in.PreMint != nil && *in.PreMint > 0 {
			amount := new(big.Int).Mul(
				new(big.Int).SetUint64(*in.PreMint),
				new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(bnmTokenDecimals)), nil),
			)
			if _, err := execStellarAdapterOp(b, deps, bnmops.MintAs, bnmops.MintAsInput{
				ContractID: tokenID,
				Caller:     deployerAddr,
				To:         deployerAddr,
				Amount:     amount,
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("pre-mint BnM deployer allocation: %w", err)
			}
		}

		hexAddr, err := stellarutil.StrkeyToHex(tokenID)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("convert BnM token address: %w", err)
		}
		return sequences.OnChainOutput{Addresses: []datastore.AddressRef{
			tokenRef.FullAddressRef(in.ChainSelector, hexAddr),
		}}, nil
	},
)

// DeployTokenPoolForToken deploys a stellar burn-mint pool for the token the
// shared pipeline deployed for the pair: deploy the pool, hand the token's
// mint authority to it via set_admin, initialize it against the core CCIP
// stack (Router/RampRegistry/RMN proxy resolved from the pipeline datastore),
// and register the token↔pool link in the TokenAdminRegistry.
func (a *StellarTokenAdapter) DeployTokenPoolForToken() *cldf_ops.Sequence[tokens.DeployTokenPoolInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return stellarDeployTokenPool
}

var stellarDeployTokenPool = cldf_ops.NewSequence(
	"StellarTokenAdapter.DeployTokenPoolForToken",
	stellarops.ContractDeploymentVersion,
	"Deploys a stellar burn-mint pool and onboards its BnM token",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in tokens.DeployTokenPoolInput) (sequences.OnChainOutput, error) {
		if in.TokenPoolVersion == nil || in.TokenPoolVersion.String() != "2.0.0" {
			return sequences.OnChainOutput{}, fmt.Errorf("unsupported stellar token pool version %v", in.TokenPoolVersion)
		}
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("build stellar deployer: %w", err)
		}
		deps := stellardeps.FromDeployer(dep)
		deployerAddr := ch.Signer.KeypairFull().Address()

		poolQualifier := in.TokenPoolQualifier
		if poolQualifier == "" && in.TokenRef != nil {
			poolQualifier = in.TokenRef.Qualifier
		}
		if poolQualifier == "" {
			return sequences.OnChainOutput{}, fmt.Errorf("token pool qualifier is required")
		}
		poolRef := stellarccip.DatastoreSorobanContractRef{
			Type:      datastore.ContractType(in.PoolType),
			Version:   in.TokenPoolVersion,
			Qualifier: poolQualifier,
		}
		if existing, err := poolRef.LookupAddressRef(in.ExistingDataStore, in.ChainSelector); err == nil {
			return sequences.OnChainOutput{Addresses: []datastore.AddressRef{existing}}, nil
		}

		if in.TokenRef == nil || in.TokenRef.Address == "" {
			return sequences.OnChainOutput{}, fmt.Errorf("token ref is required")
		}
		tokenStrkey, err := scval.HexToContractStrkey(in.TokenRef.Address)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("convert token ref address %s: %w", in.TokenRef.Address, err)
		}

		routerID, err := stellarccip.RouterDatastoreRef().LookupStrkey(in.ExistingDataStore, in.ChainSelector)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("lookup local router: %w", err)
		}
		rampRegistryID, err := stellarccip.RampRegistryDatastoreRef().LookupStrkey(in.ExistingDataStore, in.ChainSelector)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("lookup local ramp registry: %w", err)
		}
		rmnProxyID, err := stellarccip.RMNProxyDatastoreRef().LookupStrkey(in.ExistingDataStore, in.ChainSelector)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("lookup local RMN proxy: %w", err)
		}
		tarID, err := stellarccip.TokenAdminRegistryDatastoreRef().LookupStrkey(in.ExistingDataStore, in.ChainSelector)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("lookup local token admin registry: %w", err)
		}

		wasmPath, err := stellarReleaseWasm("pools_burn_mint_pool.wasm", "burn-mint pool")
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		deployOut, err := execStellarAdapterOp(b, deps, bmpops.Deploy, stellarops.DeployInput{
			WasmPath: wasmPath,
			Salt:     stellardeployment.GenerateDeterministicSalt(deployerAddr, "burn-mint-pool-"+poolQualifier),
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("deploy burn-mint pool: %w", err)
		}
		poolID := deployOut.ContractID

		if _, err := execStellarAdapterOp(b, deps, sacops.SetAdmin, sacops.SetAdminInput{
			ContractID: tokenStrkey,
			NewAdmin:   poolID,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("set_admin (BnM mint authority → pool): %w", err)
		}

		if _, err := execStellarAdapterOp(b, deps, bmpops.Initialize, bmpops.InitializeInput{
			ContractID:    poolID,
			Owner:         deployerAddr,
			Token:         tokenStrkey,
			TokenDecimals: bnmTokenDecimals,
			Router:        routerID,
			RampRegistry:  rampRegistryID,
			RmnProxy:      rmnProxyID,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("initialize burn-mint pool: %w", err)
		}

		if _, err := execStellarAdapterOp(b, deps, tarops.ProposeAdministrator, tarops.ProposeAdministratorInput{
			ContractID:    tarID,
			Caller:        deployerAddr,
			LocalToken:    tokenStrkey,
			Administrator: deployerAddr,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("propose BnM administrator in TokenAdminRegistry: %w", err)
		}
		if _, err := execStellarAdapterOp(b, deps, tarops.AcceptAdminRole, tarops.AcceptAdminRoleInput{
			ContractID: tarID,
			LocalToken: tokenStrkey,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("accept BnM admin role in TokenAdminRegistry: %w", err)
		}
		if _, err := execStellarAdapterOp(b, deps, tarops.SetPool, tarops.SetPoolInput{
			ContractID: tarID,
			LocalToken: tokenStrkey,
			Pool:       &poolID,
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("register BnM pool in TokenAdminRegistry: %w", err)
		}

		hexAddr, err := stellarutil.StrkeyToHex(poolID)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("convert burn-mint pool address: %w", err)
		}
		return sequences.OnChainOutput{Addresses: []datastore.AddressRef{
			poolRef.FullAddressRef(in.ChainSelector, hexAddr),
		}}, nil
	},
)

// DeployTokenVerify checks the BnM token for a shared-pipeline pair exists.
func (a *StellarTokenAdapter) DeployTokenVerify(e deployment.Environment, in tokens.DeployTokenInput) error {
	tokenRef := stellarccip.BnmTokenDatastoreRef()
	tokenRef.Qualifier = in.Symbol
	if _, err := tokenRef.LookupAddressRef(e.DataStore, in.ChainSelector); err != nil {
		return fmt.Errorf("BnM token %q not deployed on chain %d: %w", in.Symbol, in.ChainSelector, err)
	}
	return nil
}

var stellarConfigureTokenNoOp = cldf_ops.NewSequence(
	"StellarConfigureTokenForTransfers",
	stellarops.ContractDeploymentVersion,
	"No-op: Stellar token pool chain updates are applied during PostConnect",
	func(_ cldf_ops.Bundle, _ cldf_chain.BlockChains, _ tokens.ConfigureTokenForTransfersInput) (sequences.OnChainOutput, error) {
		return sequences.OnChainOutput{}, nil
	},
)

func (a *StellarTokenAdapter) ConfigureTokenForTransfersSequence() *cldf_ops.Sequence[tokens.ConfigureTokenForTransfersInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return stellarConfigureTokenNoOp
}

func (a *StellarTokenAdapter) ManualRegistration() *cldf_ops.Sequence[tokens.ManualRegistrationSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}

func (a *StellarTokenAdapter) MigrateLockReleasePoolLiquiditySequence() *cldf_ops.Sequence[tokens.MigrateLockReleasePoolLiquidityInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}

// execStellarAdapterOp runs a stellar op through the recording bundle.
func execStellarAdapterOp[IN, OUT any](
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	op *cldf_ops.Operation[IN, OUT, stellardeps.StellarDeps],
	in IN,
) (OUT, error) {
	rep, err := cldf_ops.ExecuteOperation(b, op, deps, in)
	if err != nil {
		var z OUT
		return z, err
	}
	return rep.Output, nil
}

// stellarReleaseWasm locates a built Soroban release WASM under the repo root.
func stellarReleaseWasm(name, what string) (string, error) {
	root, err := stellarutil.FindStellarRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "target", "wasm32v1-none", "release", name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s wasm not built (%s); run make build: %w", what, path, err)
	}
	return path, nil
}
