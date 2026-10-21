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
	lrpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	sacops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/sac_token"
	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
	poolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

var _ tokens.TokenAdapter = (*StellarTokenAdapter)(nil)

// StellarTokenAdapter implements tokens.TokenAdapter for Stellar token pools.
// Pool deployment is handled by the deploy sequences below (shared
// TokenExpansion pipeline); pool ops (UpdateAuthorities, rate limits, TAR
// registration, per-remote chain updates) live in token_adapter_pool_ops.go
// and token_adapter_configure_transfers.go. Address/decimals resolution lets
// the shared ConfigureTokensForTransfers changeset resolve Stellar pool/token
// addresses when configuring EVM pools with Stellar remotes.
type StellarTokenAdapter struct{}

func (a *StellarTokenAdapter) AddressRefToBytes(ref datastore.AddressRef) ([]byte, error) {
	addr := strings.TrimPrefix(ref.Address, "0x")
	return hex.DecodeString(addr)
}

// DeriveTokenAddress resolves the token a Stellar pool wraps for cross-family
// config. EVM parity: the pool contract itself is authoritative — its wrapped
// token is read on chain — with the qualified BnmToken row (the pipeline
// convention: one BnM token per pool, keyed by the pool's qualifier) as the
// fallback; the bespoke lock-release pools wrap the single SAC test token.
func (a *StellarTokenAdapter) DeriveTokenAddress(e deployment.Environment, chainSelector uint64, poolRef datastore.AddressRef) (string, error) {
	switch string(poolRef.Type) {
	case stellarccip.DevenvCatalogBurnMintTokenPoolType, stellarccip.DevenvCatalogLockReleaseTokenPoolType, bmpops.ContractType:
		var tokenHex string
		var onChainErr error
		if deps, err := stellarReadDeps(e.BlockChains, chainSelector); err != nil {
			onChainErr = err
		} else {
			tokenHex, onChainErr = poolWrappedTokenHex(e.OperationsBundle, deps, poolRef.Address)
		}
		if onChainErr == nil {
			return tokenHex, nil
		}
		ref, dsErr := stellarccip.LookupAddressRef(e.DataStore, chainSelector,
			datastore.ContractType(bnmops.ContractType), stellarops.ContractDeploymentVersion, poolRef.Qualifier)
		if dsErr == nil {
			return ref.Address, nil
		}
		return "", fmt.Errorf(
			"read wrapped token of pool on chain %d on chain (%v) and via the BnmToken row for pool qualifier %q: %w",
			chainSelector, onChainErr, poolRef.Qualifier, dsErr)
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

// poolWrappedTokenHex reads the token a pool wraps via pool get_token (EVM
// GetToken parity) and returns it in the datastore hex form the caller feeds
// back into ResolveTokenRef.
func poolWrappedTokenHex(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolAddress string) (string, error) {
	poolStrkey, err := ownership.NormalizeContractAddress(poolAddress)
	if err != nil {
		return "", fmt.Errorf("normalize pool address: %w", err)
	}
	report, err := cldf_ops.ExecuteOperation(b, poolops.GetToken, deps, poolops.GetTokenInput{ContractID: poolStrkey})
	if err != nil {
		return "", fmt.Errorf("read wrapped token of pool %s: %w", poolStrkey, err)
	}
	tokenHex, err := stellarutil.StrkeyToHex(report.Output)
	if err != nil {
		return "", fmt.Errorf("convert wrapped token %s to hex: %w", report.Output, err)
	}
	return tokenHex, nil
}

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
		dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
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
	"Deploys a stellar token pool (burn-mint or lock-release) and onboards its token",
	func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in tokens.DeployTokenPoolInput) (sequences.OnChainOutput, error) {
		if in.TokenPoolVersion == nil || in.TokenPoolVersion.String() != "2.0.0" {
			return sequences.OnChainOutput{}, fmt.Errorf("unsupported stellar token pool version %v", in.TokenPoolVersion)
		}
		ch, ok := chains.StellarChains()[in.ChainSelector]
		if !ok {
			return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", in.ChainSelector)
		}
		dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
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
			// Caveat: the exit keys on the pool row only — a pool row without its
			// lock box row skips the box (acceptable for fresh devenvs).
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

		var poolID string
		var lockBoxHex string
		switch in.PoolType {
		case stellarccip.DevenvCatalogLockReleaseTokenPoolType:
			// Plain LockRelease pool (EVM LockReleaseTokenPool 2.0.0 parity): a
			// single immutable lock box fixed at `initialize` holds all liquidity
			// (deposit at lock_or_burn, withdraw at release_or_mint); PostTokenDeploy
			// seeds the box. No set_admin — the pool never takes mint authority.
			boxWasm, err := stellarReleaseWasm("pools_token_lock_box.wasm", "token lock box")
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			boxDeploy, err := execStellarAdapterOp(b, deps, tlbops.Deploy, stellarops.DeployInput{
				WasmPath: boxWasm,
				Salt:     stellardeployment.GenerateDeterministicSalt(deployerAddr, "lock-release-box-"+poolQualifier),
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("deploy token lock box: %w", err)
			}
			lockBoxID := boxDeploy.ContractID
			if _, err := execStellarAdapterOp(b, deps, tlbops.Initialize, tlbops.InitializeInput{
				ContractID: lockBoxID,
				Owner:      deployerAddr,
				Token:      tokenStrkey,
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("initialize token lock box: %w", err)
			}

			poolWasm, err := stellarReleaseWasm("pools_lock_release_pool.wasm", "lock-release pool")
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			poolDeploy, err := execStellarAdapterOp(b, deps, poolops.Deploy, stellarops.DeployInput{
				WasmPath: poolWasm,
				Salt:     stellardeployment.GenerateDeterministicSalt(deployerAddr, "lock-release-pool-"+poolQualifier),
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("deploy lock-release pool: %w", err)
			}
			poolID = poolDeploy.ContractID
			if _, err := execStellarAdapterOp(b, deps, lrpops.Initialize, lrpops.InitializeInput{
				ContractID:    poolID,
				Owner:         deployerAddr,
				Token:         tokenStrkey,
				TokenDecimals: bnmTokenDecimals,
				Router:        routerID,
				RampRegistry:  rampRegistryID,
				RmnProxy:      rmnProxyID,
				LockBox:       lockBoxID,
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("initialize lock-release pool: %w", err)
			}
			if _, err := execStellarAdapterOp(b, deps, tlbops.AddAllowedCallers, tlbops.AddAllowedCallersInput{
				ContractID: lockBoxID,
				Callers:    []string{poolID, deployerAddr},
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("add lock-release pool and deployer as lock box callers: %w", err)
			}
			lockBoxHex, err = stellarutil.StrkeyToHex(lockBoxID)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("convert token lock box address: %w", err)
			}
		case stellarccip.DevenvCatalogBurnMintTokenPoolType, bmpops.ContractType:
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
			poolID = deployOut.ContractID

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
		default:
			return sequences.OnChainOutput{}, fmt.Errorf("unsupported stellar pool type %q", in.PoolType)
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
			return sequences.OnChainOutput{}, fmt.Errorf("convert token pool address: %w", err)
		}
		addresses := []datastore.AddressRef{poolRef.FullAddressRef(in.ChainSelector, hexAddr)}
		if lockBoxHex != "" {
			// The lock box row rides along so PostTokenDeploy can resolve and fund it.
			addresses = append(addresses, stellarccip.TokenLockBoxDatastoreRef(poolQualifier).
				FullAddressRef(in.ChainSelector, lockBoxHex))
		}
		return sequences.OnChainOutput{Addresses: addresses}, nil
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
