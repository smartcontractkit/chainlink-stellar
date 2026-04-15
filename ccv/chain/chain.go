package ccvchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	routeroperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/onramp"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/proxy"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccipChangesets "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/changesets"
	ccipOffchain "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/offchain"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccipdevenv "github.com/smartcontractkit/chainlink-stellar/deployment/ccip/devenv"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/simple_node_set"
)

// stellarAddressLen is 32 bytes for ed25519 public key
const stellarAddressLen = 32

// CcipReceiverContractType is the datastore contract type for the example CCIP
// receiver deployed on Stellar so that GetEOAReceiverAddress can return a valid
// Wasm contract address in tests.
const CcipReceiverContractType = stellarccipdevenv.CcipReceiverContractType

const TokenAdminRegistryContractType = stellarccipdevenv.TokenAdminRegistryContractType
const LockReleaseTokenPoolContractType = stellarccipdevenv.LockReleaseTokenPoolContractType

// generateAccountAddress generates a Stellar account address (G...) from a seed.
// This uses the Stellar SDK's keypair package to create a proper strkey-encoded address.
func generateAccountAddress(seed string) (string, error) {
	// Create deterministic seed from input
	hash := sha256.Sum256([]byte(seed))

	// Create a keypair from the seed bytes
	kp, err := keypair.FromRawSeed(hash)
	if err != nil {
		return "", fmt.Errorf("failed to create keypair from seed: %w", err)
	}

	return kp.Address(), nil
}

var (
	_ cciptestinterfaces.CCIP17              = &Chain{}
	_ cciptestinterfaces.CCIP17Configuration = &Chain{}
)

// Chain implements the CCIP17 and CCIP17Configuration interfaces for Stellar/Soroban.
type Chain struct {
	chainSelector       uint64
	logger              zerolog.Logger
	rpcClient           *rpcclient.Client
	networkPassphrase   string
	sorobanRPCURL       string
	deployerKeypair     *keypair.Full
	deployer            *stellardeployment.Deployer
	onRampClient        *onrampbindings.OnRampClient
	onRampContractID    string
	offRampClient       *offrampbindings.OffRampClient
	offRampContractID   string
	routerClient        *routerbindings.RouterClient
	routerContractID    string
	feeQuoterClient     *fqbindings.FeeQuoterClient
	vvrContractID       string
	cvContractID        string
	receiverContractID  string
	rmnProxyContractID  string
	rmnProxyClient      *rmnproxybindings.RmnProxyClient
	rmnRemoteContractID string
	rmnRemoteClient     *rmnremotebindings.RmnRemoteClient

	tokenAdminRegistryContractID string
	tokenAdminRegistryClient     *tarbindings.TokenAdminRegistryClient
	tokenPoolContractID          string
	tokenPoolClient              *tokenpoolbindings.TokenPoolClient
	testTokenContractID          string
	testTokenIssuerKeypair       *keypair.Full
	feeTokenContractID           string
	feeTokenIssuerKeypair        *keypair.Full
	friendbotURL                 string
}

// New creates a new Stellar Chain instance.
func New(logger zerolog.Logger, selector uint64) *Chain {
	return &Chain{
		logger:        logger,
		chainSelector: selector,
	}
}

// NetworkPassphrase returns the network passphrase for this chain.
func (c *Chain) NetworkPassphrase() string {
	return c.networkPassphrase
}

// SorobanRPCURL returns the Soroban RPC URL for this chain.
func (c *Chain) SorobanRPCURL() string {
	return c.sorobanRPCURL
}

// DeployerAddress returns the deployer's Stellar address.
func (c *Chain) DeployerAddress() string {
	if c.deployerKeypair == nil {
		return ""
	}
	return c.deployerKeypair.Address()
}

// ChainFamily implements cciptestinterfaces.CCIP17Configuration.
func (c *Chain) ChainFamily() string {
	return chainsel.FamilyStellar
}

// ChainSelector implements cciptestinterfaces.CCIP17.
// Returns the selector for this chain.
func (c *Chain) ChainSelector() uint64 {
	return c.chainSelector
}

// GetConnectionProfile implements cciptestinterfaces.OnChainConfigurable.
// Returns a ChainDefinition describing this Stellar chain as a lane endpoint,
// plus the default committee verifier config to apply for each remote chain.
func (c *Chain) GetConnectionProfile(_ *deployment.Environment, selector uint64) (lanes.ChainDefinition, lanes.CommitteeVerifierRemoteChainInput, error) {
	feeQuoterOverride := stellarFeeQuoterDestChainConfigOverride(selector)
	chainDef := lanes.ChainDefinition{
		Selector:                          selector,
		AddressBytesLength:                stellarAddressLen,
		BaseExecutionGasCost:              100_000,
		FeeQuoterDestChainConfigOverrides: &feeQuoterOverride,
		DefaultInboundCCVs: []datastore.AddressRef{
			{
				Type:          datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
				Version:       versioned_verifier_resolver.Version,
				ChainSelector: selector,
				Qualifier:     devenvcommon.DefaultCommitteeVerifierQualifier,
			},
		},
		DefaultOutboundCCVs: []datastore.AddressRef{
			{
				Type:          datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
				Version:       versioned_verifier_resolver.Version,
				ChainSelector: selector,
				Qualifier:     devenvcommon.DefaultCommitteeVerifierQualifier,
			},
		},
		DefaultExecutor: datastore.AddressRef{
			Type:          datastore.ContractType(proxy.ContractType),
			Version:       proxy.Version,
			ChainSelector: selector,
			Qualifier:     devenvcommon.DefaultExecutorQualifier,
		},
		ExecutorDestChainConfig: lanes.ExecutorDestChainConfig{
			Enabled: true,
		},
	}

	cvConfig := lanes.CommitteeVerifierRemoteChainInput{
		GasForVerification: 10_000,
	}

	return chainDef, cvConfig, nil
}

// stellarFeeQuoterDestChainConfigOverride returns a FeeQuoterDestChainConfigOverride
// that configures Stellar as a valid destination on remote (EVM) FeeQuoter contracts.
//
// Uses the EVM family selector (0x2812d52c) as a stand-in because Stellar does not
// yet have its own registered ChainFamilySelector in the on-chain FeeQuoter contract.
// TODO(NONEVM-4241): replace with a real Stellar family selector once registered.
func stellarFeeQuoterDestChainConfigOverride(selector uint64) lanes.FeeQuoterDestChainConfigOverride {
	// bytes4(keccak256("CCIP ChainFamilySelector EVM")) — used as stand-in for Stellar.
	var evmFamilyBytes [4]byte
	evmFamilyHex, _ := hex.DecodeString("2812d52c")
	copy(evmFamilyBytes[:], evmFamilyHex)

	return func(cfg *lanes.FeeQuoterDestChainConfig) {
		cfg.IsEnabled = true
		cfg.MaxDataBytes = 30_000
		cfg.MaxPerMsgGasLimit = 3_000_000
		cfg.DestGasOverhead = 300_000
		cfg.DefaultTokenFeeUSDCents = 25
		cfg.DestGasPerPayloadByteBase = 16
		cfg.DefaultTokenDestGasOverhead = 90_000
		cfg.DefaultTxGasLimit = 200_000
		cfg.NetworkFeeUSDCents = 10
		cfg.ChainFamilySelector = binary.BigEndian.Uint32(evmFamilyBytes[:])
		cfg.V2Params = &lanes.FeeQuoterV2Params{
			LinkFeeMultiplierPercent: 90,
			USDPerUnitGas:            big.NewInt(1e6),
		}
	}
}

// GetChainLaneProfile implements cciptestinterfaces.OnChainConfigurable.
// Returns the lane profile for Stellar as a destination chain, mirroring the
// values used in GetConnectionProfile and stellarFeeQuoterDestChainConfigOverride.
func (c *Chain) GetChainLaneProfile(_ *deployment.Environment, selector uint64) (cciptestinterfaces.ChainLaneProfile, error) {
	// bytes4(keccak256("CCIP ChainFamilySelector EVM")) — used as stand-in for Stellar.
	// TODO(NONEVM-4241): replace with a real Stellar family selector once registered.
	var evmFamilySelector [4]byte
	evmFamilyHex, _ := hex.DecodeString("2812d52c")
	copy(evmFamilySelector[:], evmFamilyHex)

	return cciptestinterfaces.ChainLaneProfile{
		AddressBytesLength:   stellarAddressLen,
		BaseExecutionGasCost: 100_000,
		FeeQuoterDestChainConfig: adapters.FeeQuoterDestChainConfig{
			IsEnabled:                   true,
			MaxDataBytes:                30_000,
			MaxPerMsgGasLimit:           3_000_000,
			DestGasOverhead:             300_000,
			DefaultTokenFeeUSDCents:     25,
			DestGasPerPayloadByteBase:   16,
			DefaultTokenDestGasOverhead: 90_000,
			DefaultTxGasLimit:           200_000,
			NetworkFeeUSDCents:          10,
			ChainFamilySelector:         evmFamilySelector,
			LinkFeeMultiplierPercent:    90,
			USDPerUnitGas:               big.NewInt(1e6),
		},
		ExecutorDestChainConfig: adapters.ExecutorDestChainConfig{
			Enabled: true,
		},
		DefaultExecutorQualifier: devenvcommon.DefaultExecutorQualifier,
		DefaultInboundCCVs: []datastore.AddressRef{
			{
				Type:          datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
				Version:       versioned_verifier_resolver.Version,
				ChainSelector: selector,
				Qualifier:     devenvcommon.DefaultCommitteeVerifierQualifier,
			},
		},
		DefaultOutboundCCVs: []datastore.AddressRef{
			{
				Type:          datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
				Version:       versioned_verifier_resolver.Version,
				ChainSelector: selector,
				Qualifier:     devenvcommon.DefaultCommitteeVerifierQualifier,
			},
		},
		GasForVerification: 10_000,
	}, nil
}

// PostConnect implements cciptestinterfaces.OnChainConfigurable.
// Runs Stellar-specific setup after the centralized lane configuration changeset
// has connected all chains (e.g. Router onramp/offramp mappings).
func (c *Chain) PostConnect(env *deployment.Environment, selector uint64, remoteSelectors []uint64) error {
	if env == nil {
		return fmt.Errorf("environment is nil")
	}
	if env.DataStore == nil {
		return fmt.Errorf("environment datastore is nil")
	}

	remoteSelectors = stellarutil.FilterRemoteSelectors(remoteSelectors, selector)
	if len(remoteSelectors) == 0 {
		return nil
	}
	if err := c.ensureLocalContracts(env.DataStore, selector); err != nil {
		return fmt.Errorf("ensure local stellar contracts: %w", err)
	}

	defaultExecutor, err := lookupStellarContractID(
		env.DataStore,
		selector,
		datastore.ContractType(proxy.ContractType),
		proxy.Version,
		devenvcommon.DefaultExecutorQualifier,
	)
	if err != nil {
		return fmt.Errorf("resolve default executor proxy: %w", err)
	}

	onRampDestConfigs, err := c.buildOnRampDestConfigs(env.DataStore, remoteSelectors, defaultExecutor, true)
	if err != nil {
		return fmt.Errorf("build onramp dest configs: %w", err)
	}
	if err := c.onRampClient.ApplyDestChainConfigUpdates(context.Background(), onRampDestConfigs); err != nil {
		return fmt.Errorf("apply onramp dest configs in post-connect: %w", err)
	}

	offRampSourceConfigs, err := c.buildOffRampSourceConfigs(env.DataStore, remoteSelectors, true)
	if err != nil {
		return fmt.Errorf("build offramp source configs: %w", err)
	}
	if err := c.offRampClient.ApplySourceChainCfgUpdates(context.Background(), offRampSourceConfigs); err != nil {
		return fmt.Errorf("apply offramp source configs in post-connect: %w", err)
	}

	onRampEntries := make([]routerbindings.OnRampEntry, 0, len(remoteSelectors))
	offRampEntries := make([]routerbindings.OffRampEntry, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		onRampEntries = append(onRampEntries, routerbindings.OnRampEntry{
			DestChainSelector: rs,
			Onramp:            c.onRampContractID,
		})
		offRampEntries = append(offRampEntries, routerbindings.OffRampEntry{
			SourceChainSelector: rs,
			Offramp:             c.offRampContractID,
		})
	}

	if err := c.routerClient.ApplyRampUpdates(context.Background(), onRampEntries, []routerbindings.OffRampEntry{}, offRampEntries); err != nil {
		return fmt.Errorf("apply router ramp updates in post-connect: %w", err)
	}

	// Configure pool chain updates so the pool accepts lock/burn and
	// release/mint for each remote chain. The remote pool and token addresses
	// are placeholders (zero bytes) because the EVM-side pool pairing is
	// handled separately by the CCV token config pipeline.
	// TODO(NONEVM-3946): populate real remote pool/token addresses once
	// cross-chain pool pairing is implemented in PostConnect.
	if c.tokenPoolClient != nil && c.testTokenContractID != "" {
		var chainUpdates []tokenpoolbindings.ChainUpdate
		for _, rs := range remoteSelectors {
			remotePoolLen, addrErr := addressBytesLengthForSelector(rs)
			if addrErr != nil {
				return fmt.Errorf("address bytes length for %d: %w", rs, addrErr)
			}
			chainUpdates = append(chainUpdates, tokenpoolbindings.ChainUpdate{
				RemoteChainSelector:       rs,
				RemotePoolAddresses:       make([]byte, remotePoolLen),
				RemoteTokenAddress:        make([]byte, remotePoolLen),
				OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
				InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
			})
		}
		if err := c.tokenPoolClient.ApplyChainUpdates(context.Background(), chainUpdates, nil); err != nil {
			return fmt.Errorf("apply pool chain updates in post-connect: %w", err)
		}
	}

	return nil
}

// ConfigureNodes implements cciptestinterfaces.CCIP17Configuration.
// Returns TOML configuration for Chainlink nodes to connect to Stellar.
func (c *Chain) ConfigureNodes(ctx context.Context, bc *blockchain.Input) (string, error) {
	c.logger.Info().Msg("Configuring Chainlink nodes for Stellar")

	name := fmt.Sprintf("node-stellar-%s", uuid.New().String()[0:5])

	// Get Stellar-specific endpoints from the blockchain output
	sorobanRPCURL := bc.Out.Nodes[0].InternalHTTPUrl
	networkPassphrase := c.networkPassphrase

	// Return TOML configuration for Chainlink nodes to connect to Stellar/Soroban
	// NOTE: This assumes Chainlink nodes have Stellar plugin support.
	// The actual TOML structure may need adjustment based on the Stellar plugin implementation.
	return fmt.Sprintf(`
       [[Stellar]]
       NetworkPassphrase = '%s'
       ChainID = '%s'

       [[Stellar.Nodes]]
       Name = '%s'
       SorobanRPCUrl = '%s'`,
		networkPassphrase,
		bc.ChainID,
		name,
		sorobanRPCURL,
	), nil
}

// PreDeployContractsForSelector implements cciptestinterfaces.OnChainConfigurable.
// // Stellar has no CREATE2-style pre-bootstrap like EVM, so this stage only patches topology and registers deploy context for the adapter.
func (c *Chain) PreDeployContractsForSelector(ctx context.Context, env *deployment.Environment, selector uint64, topology *ccipOffchain.EnvironmentTopology) (datastore.DataStore, error) {
	_ = ctx
	_ = env
	ensureStellarFeeAggregatorsInTopology(c, topology)
	registerStellarDeployChangesetCtx(selector, c, topology)
	return nil, nil
}

// GetDeployChainContractsCfg implements cciptestinterfaces.OnChainConfigurable.
func (c *Chain) GetDeployChainContractsCfg(env *deployment.Environment, selector uint64, topology *ccipOffchain.EnvironmentTopology) (ccipChangesets.DeployChainContractsPerChainCfg, error) {
	_ = env
	_ = selector
	_ = topology
	if c.deployerKeypair == nil {
		return ccipChangesets.DeployChainContractsPerChainCfg{}, fmt.Errorf("stellar chain deployer not initialized; call DeployLocalNetwork first")
	}
	raw, err := strkey.Decode(strkey.VersionByteAccountID, c.deployerKeypair.Address())
	if err != nil {
		return ccipChangesets.DeployChainContractsPerChainCfg{}, fmt.Errorf("decode stellar deployer: %w", err)
	}
	return ccipChangesets.DeployChainContractsPerChainCfg{
		DeployerContract: hexutil.Encode(raw),
		DeployerKeyOwned: true,
	}, nil
}

// PostDeployContractsForSelector implements cciptestinterfaces.OnChainConfigurable.
// Deploys the lock-release test pool and SAC token (EVM post-deploy parity), applies FeeQuoter
// pricing for the test token, and returns a datastore delta with the pool AddressRef.
func (c *Chain) PostDeployContractsForSelector(ctx context.Context, env *deployment.Environment, selector uint64, topology *ccipOffchain.EnvironmentTopology) (datastore.DataStore, error) {
	_ = topology
	defer clearStellarDeployChangesetCtx(selector)

	if env == nil {
		return nil, fmt.Errorf("environment is nil")
	}
	if env.DataStore == nil {
		return nil, fmt.Errorf("environment datastore is nil")
	}
	if err := c.ensureLocalContracts(env.DataStore, selector); err != nil {
		return nil, fmt.Errorf("ensure local contracts before token pool deploy: %w", err)
	}

	host := &stellarCCIPDeployHost{c: c}
	if err := stellarccipdevenv.DeployLockReleaseTestTokenPool(ctx, host); err != nil {
		return nil, fmt.Errorf("deploy lock-release test token pool: %w", err)
	}

	allSelectors := selectorsFromBlockChains(env.BlockChains)
	if c.testTokenContractID != "" && c.feeQuoterClient != nil {
		if err := stellarccipdevenv.ApplyFeeQuoterTestTokenConfig(ctx, c.feeQuoterClient, c.testTokenContractID, allSelectors); err != nil {
			return nil, fmt.Errorf("apply fee quoter test token config: %w", err)
		}
		c.logger.Info().Int("destChainCount", len(allSelectors)).Msg("FeeQuoter test token fees applied (post-deploy)")
	}

	if c.tokenPoolContractID == "" {
		return nil, nil
	}
	ds, err := stellarccipdevenv.LockReleasePoolAddressRefDataStore(selector, c.tokenPoolContractID)
	if err != nil {
		return nil, err
	}
	return ds, nil
}

// DeployStellarCCIPContracts runs deployStellarCCIPContracts and returns output for the
// shared DeployChainContracts changeset merge path.
func (c *Chain) DeployStellarCCIPContracts(ctx context.Context, chains cldf_chain.BlockChains, selector uint64, topology *ccipOffchain.EnvironmentTopology) (seq_core.OnChainOutput, error) {
	allSelectors := selectorsFromBlockChains(chains)
	ds, err := c.deployStellarCCIPContracts(ctx, allSelectors, selector, topology)
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	addrs, err := ds.Addresses().Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	return seq_core.OnChainOutput{Addresses: addrs}, nil
}

// DeployLocalNetwork implements cciptestinterfaces.CCIP17Configuration.
// Deploys a local Stellar network for testing.
func (c *Chain) DeployLocalNetwork(ctx context.Context, input *blockchain.Input) (*blockchain.Output, error) {
	c.logger.Info().Msg("Deploying Stellar local network")

	out, err := blockchain.NewBlockchainNetwork(input)
	if err != nil {
		return nil, fmt.Errorf("failed to create Stellar blockchain network: %w", err)
	}

	c.sorobanRPCURL = input.Out.Nodes[0].ExternalHTTPUrl
	c.networkPassphrase = input.Out.NetworkSpecificData.StellarNetwork.NetworkPassphrase
	c.friendbotURL = input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL

	// Initialize the Soroban RPC client
	c.rpcClient = rpcclient.NewClient(c.sorobanRPCURL, &http.Client{Timeout: 60 * time.Second})

	// Generate a deployer keypair for this network
	// Use the network passphrase as part of the seed for deterministic key generation
	deployerSeed := fmt.Sprintf("deployer-%s", c.networkPassphrase)
	c.logger.Info().Str("deployerSeed", deployerSeed).Msg("Deployer seed")
	seedHash := sha256.Sum256([]byte(deployerSeed))
	deployerKP, err := keypair.FromRawSeed(seedHash)
	if err != nil {
		return nil, fmt.Errorf("failed to create deployer keypair: %w", err)
	}
	c.deployerKeypair = deployerKP

	// Fund the deployer account via Friendbot before any contract deployments.
	// Friendbot may not be ready immediately after the container starts, so retry.
	friendbotURL := input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL
	if friendbotURL != "" {
		if err := c.fundViaFriendbot(friendbotURL, c.deployerKeypair.Address()); err != nil {
			return nil, fmt.Errorf("failed to fund deployer account: %w", err)
		}
	}

	// Create the deployer which serves as the common Invoker for contract interactions
	c.deployer = stellardeployment.NewDeployer(c.rpcClient, c.networkPassphrase, c.deployerKeypair)

	c.logger.Info().
		Str("sorobanRPCURL", c.sorobanRPCURL).
		Str("networkPassphrase", c.networkPassphrase).
		Str("deployerAddress", c.deployerKeypair.Address()).
		Msg("Stellar network deployed and configured")

	return out, nil
}

// fundViaFriendbot funds a Stellar account using the Friendbot faucet with retries.
// Friendbot may take up to 90 seconds to become ready after a container starts.
func (c *Chain) fundViaFriendbot(friendbotURL, address string) error {
	faucetURL := fmt.Sprintf("%s?addr=%s", friendbotURL, address)

	var lastErr error
	maxRetries := 9
	retryInterval := 20 * time.Second

	for attempt := range maxRetries {
		resp, err := http.Get(faucetURL)
		if err != nil {
			lastErr = fmt.Errorf("friendbot request failed: %w", err)
			c.logger.Debug().Err(err).Int("attempt", attempt+1).Msg("Friendbot request failed, retrying...")
			time.Sleep(retryInterval)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			c.logger.Info().Str("address", address).Msg("Account funded via Friendbot")
			return nil
		}

		resp.Body.Close()
		lastErr = fmt.Errorf("friendbot returned status %s", resp.Status)
		c.logger.Debug().
			Str("status", resp.Status).
			Int("attempt", attempt+1).
			Int("maxRetries", maxRetries).
			Msg("Friendbot not ready, retrying...")
		time.Sleep(retryInterval)
	}

	return fmt.Errorf("friendbot not ready after %d attempts: %w", maxRetries, lastErr)
}

// FundAddresses implements cciptestinterfaces.CCIP17Configuration.
// Funds addresses with native Stellar Lumens (XLM).
// Addresses that are not exactly 32 bytes (ed25519 public keys) are silently
// skipped — this handles the case where the framework passes EVM pricer
// addresses (20 bytes) to every chain implementation.
func (c *Chain) FundAddresses(ctx context.Context, input *blockchain.Input, addresses []protocol.UnknownAddress, nativeAmount *big.Int) error {
	for _, addr := range addresses {
		if len(addr) != stellarAddressLen {
			c.logger.Debug().
				Int("addressLen", len(addr)).
				Int("expectedLen", stellarAddressLen).
				Msg("Skipping non-Stellar address in FundAddresses")
			continue
		}
		addrStr := strkey.MustEncode(strkey.VersionByteAccountID, addr)
		faucetUrl := fmt.Sprintf("%s?addr=%s", input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL, addrStr)

		// Retry logic for friendbot - it may take up to 90 seconds to be ready after container start
		var lastErr error
		maxRetries := 9
		retryInterval := 20 * time.Second

		for attempt := 0; attempt < maxRetries; attempt++ {
			resp, err := http.Get(faucetUrl)
			if err != nil {
				lastErr = fmt.Errorf("failed to get faucet (friendbot) URL: %w", err)
				c.logger.Debug().
					Err(err).
					Int("attempt", attempt+1).
					Int("maxRetries", maxRetries).
					Msg("Friendbot request failed, retrying...")
				time.Sleep(retryInterval)
				continue
			}

			if resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				c.logger.Debug().
					Str("address", addrStr).
					Int("attempt", attempt+1).
					Msg("Successfully funded address via friendbot")
				lastErr = nil
				break
			}

			// Non-OK status, might be 502 if friendbot isn't ready yet
			resp.Body.Close()
			lastErr = fmt.Errorf("friendbot returned status %s", resp.Status)
			c.logger.Debug().
				Str("status", resp.Status).
				Int("attempt", attempt+1).
				Int("maxRetries", maxRetries).
				Str("address", addrStr).
				Str("faucetUrl", faucetUrl).
				Msg("Friendbot not ready, retrying...")
			time.Sleep(retryInterval)
		}

		if lastErr != nil {
			return fmt.Errorf("failed to fund address %s after %d attempts: %w", addrStr, maxRetries, lastErr)
		}
	}

	c.logger.Info().
		Int("numAddresses", len(addresses)).
		Msg("Funded Stellar addresses")
	return nil
}

// FundNodes implements cciptestinterfaces.CCIP17Configuration.
// Funds Chainlink nodes with XLM and LINK tokens.
func (c *Chain) FundNodes(ctx context.Context, cls []*simple_node_set.Input, bc *blockchain.Input, linkAmount, nativeAmount *big.Int) error {
	// TODO: implement node funding for Stellar
	// This should:
	// 1. Fund each node's Stellar address with XLM
	// 2. Fund each node with LINK tokens (if LINK is available on Stellar)
	c.logger.Info().
		Int("numNodes", len(cls)).
		Str("linkAmount", linkAmount.String()).
		Str("nativeAmount", nativeAmount.String()).
		Msg("Funding Stellar nodes (not implemented)")
	return nil
}

// Curse implements cciptestinterfaces.CCIP17.
// Curses a list of chains on this chain's RMN.
func (c *Chain) Curse(ctx context.Context, subjects [][16]byte) error {
	if c.rmnRemoteClient == nil {
		return fmt.Errorf("RMN Remote client not initialized")
	}
	err := c.rmnRemoteClient.Curse(ctx, subjects)
	if err != nil {
		return fmt.Errorf("failed to curse RMN Remote: %w", err)
	}
	c.logger.Debug().
		Int("numSubjects", len(subjects)).
		Msg("Cursed RMN Remote")
	return nil
}

// ExposeMetrics implements cciptestinterfaces.CCIP17.
// Exposes Prometheus metrics for monitoring.
func (c *Chain) ExposeMetrics(ctx context.Context, source, dest uint64) ([]string, *prometheus.Registry, error) {
	// TODO: implement metrics exposure for Stellar lanes
	return nil, nil, nil
}

// GetEOAReceiverAddress implements cciptestinterfaces.CCIP17.
// On Stellar, CCIP receivers must be deployed Wasm contracts (the OffRamp
// checks executable() on the receiver address). This returns the address of
// the ccip_receiver_example contract deployed during DeployContractsForSelector.
// TODO: check if this assumption is always correct or if it only applies to arbitrary messages?
func (c *Chain) GetEOAReceiverAddress() (protocol.UnknownAddress, error) {
	if c.receiverContractID == "" {
		return protocol.UnknownAddress{}, fmt.Errorf("ccip_receiver contract not deployed; run DeployContractsForSelector first")
	}
	rawBytes, err := strkey.Decode(strkey.VersionByteContract, c.receiverContractID)
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("failed to decode receiver contract address: %w", err)
	}
	return protocol.UnknownAddress(rawBytes), nil
}

// GetExpectedNextSequenceNumber implements cciptestinterfaces.CCIP17.
// Gets the expected next sequence number for messages to the specified destination.
func (c *Chain) GetExpectedNextSequenceNumber(ctx context.Context, to uint64) (uint64, error) {
	if c.onRampClient == nil {
		return 0, fmt.Errorf("OnRamp client not initialized")
	}

	seqNo, err := c.onRampClient.GetExpectedNextMessageNumber(ctx, to)
	if err != nil {
		return 0, fmt.Errorf("failed to get next sequence number: %w", err)
	}

	c.logger.Debug().
		Uint64("destChainSelector", to).
		Uint64("nextSequenceNumber", seqNo).
		Msg("Got expected next sequence number from OnRamp")

	return seqNo, nil
}

// GetMaxDataBytes implements cciptestinterfaces.CCIP17.
// Gets the maximum data size for a CCIP message to the specified remote chain.
func (c *Chain) GetMaxDataBytes(ctx context.Context, remoteChainSelector uint64) (uint32, error) {
	if c.feeQuoterClient == nil {
		return 0, fmt.Errorf("FeeQuoter client not initialized")
	}
	cfg, err := c.feeQuoterClient.GetDestChainConfig(ctx, remoteChainSelector)
	if err != nil {
		return 0, fmt.Errorf("failed to get fee quoter dest chain config: %w", err)
	}
	return cfg.MaxDataBytes, nil
}

// GetRoundRobinUser implements cciptestinterfaces.CCIP17.
// Gets a round-robin user for sending transactions.
func (c *Chain) GetRoundRobinUser() func() *bind.TransactOpts {
	// NOTE: bind.TransactOpts is EVM-specific. For Stellar, we would need a different
	// transaction signing mechanism. This method may need to be refactored for
	// chain-agnostic transaction signing.
	return nil
}

// GetSenderAddress implements cciptestinterfaces.CCIP17.
// Gets the sender address for this chain (the deployer's address).
func (c *Chain) GetSenderAddress() (protocol.UnknownAddress, error) {
	if c.deployerKeypair == nil {
		return protocol.UnknownAddress{}, fmt.Errorf("deployer keypair not initialized")
	}
	// Decode the strkey address to raw bytes
	rawBytes, err := strkey.Decode(strkey.VersionByteAccountID, c.deployerKeypair.Address())
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("failed to decode sender address: %w", err)
	}
	return protocol.UnknownAddress(rawBytes), nil
}

// GetTokenBalance implements cciptestinterfaces.CCIP17.
// Gets the balance of a token for an address by calling the SAC balance function.
func (c *Chain) GetTokenBalance(ctx context.Context, address, tokenAddress protocol.UnknownAddress) (*big.Int, error) {
	if c.deployer == nil {
		return nil, fmt.Errorf("deployer not initialized")
	}

	tokenStrkey, err := strkey.Encode(strkey.VersionByteContract, []byte(tokenAddress))
	if err != nil {
		return nil, fmt.Errorf("encode token address: %w", err)
	}

	addrStrkey, err := strkey.Encode(strkey.VersionByteAccountID, []byte(address))
	if err != nil {
		addrStrkey, err = strkey.Encode(strkey.VersionByteContract, []byte(address))
		if err != nil {
			return nil, fmt.Errorf("encode holder address: %w", err)
		}
	}

	balanceArgs := []xdr.ScVal{scval.AddressToScVal(addrStrkey)}
	result, err := c.deployer.SimulateContract(ctx, tokenStrkey, "balance", balanceArgs)
	if err != nil {
		return nil, fmt.Errorf("query token balance: %w", err)
	}

	bal, err := scval.I128FromScVal(*result)
	if err != nil {
		return nil, fmt.Errorf("parse balance: %w", err)
	}
	return big.NewInt(bal), nil
}

// GetUserNonce implements cciptestinterfaces.CCIP17.
// Returns the nonce for the given user address on this chain.
func (c *Chain) GetUserNonce(ctx context.Context, userAddress protocol.UnknownAddress) (uint64, error) {
	if c.deployer == nil {
		return 0, fmt.Errorf("deployer not initialized")
	}
	_, seq, exists, err := c.deployer.NativeAccountState(ctx, []byte(userAddress))
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	return seq, nil
}

// ManuallyExecuteMessage implements cciptestinterfaces.CCIP17.
// Manually executes a CCIP message on this chain.
func (c *Chain) ManuallyExecuteMessage(ctx context.Context, message protocol.Message, gasLimit uint64, ccvs []protocol.UnknownAddress, verifierResults [][]byte) (cciptestinterfaces.ExecutionStateChangedEvent, error) {
	if c.offRampClient == nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("OffRamp client not initialized")
	}
	if c.rpcClient == nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("RPC client not initialized")
	}
	if gasLimit > math.MaxUint32 {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("gas limit overflows uint32")
	}
	encoded, err := message.Encode()
	if err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("encode message: %w", err)
	}
	ccvStrs := make([]string, 0, len(ccvs))
	for _, a := range ccvs {
		s, err := strkey.Encode(strkey.VersionByteContract, []byte(a))
		if err != nil {
			return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("encode ccv address: %w", err)
		}
		ccvStrs = append(ccvStrs, s)
	}
	if err := c.offRampClient.Execute(ctx, encoded, ccvStrs, verifierResults, uint32(gasLimit)); err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("offramp execute: %w", err)
	}
	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("failed to get latest ledger: %w", err)
	}
	event, err := c.offRampClient.WaitForExecutionStateChangedEvent(
		ctx, latestLedger.Sequence, 2*time.Minute,
		func(e *offrampbindings.ExecutionStateChangedEvent) bool {
			return e.SourceChainSelector == uint64(message.SourceChainSelector) && e.SequenceNumber == uint64(message.SequenceNumber)
		},
	)
	if err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("failed waiting for execution event: %w", err)
	}
	return cciptestinterfaces.ExecutionStateChangedEvent{
		SourceChainSelector: protocol.ChainSelector(event.SourceChainSelector),
		MessageID:           event.MessageId,
		MessageNumber:       event.SequenceNumber,
		State:               cciptestinterfaces.MessageExecutionState(event.State),
		ReturnData:          event.ReturnData,
	}, nil
}

// SendMessage implements cciptestinterfaces.CCIP17.
// Sends a CCIP message to the specified destination chain via the Router's ccip_send.
func (c *Chain) SendMessage(ctx context.Context, dest uint64, fields cciptestinterfaces.MessageFields, opts cciptestinterfaces.MessageOptions) (cciptestinterfaces.MessageSentEvent, error) {
	if c.routerClient == nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("Router client not initialized")
	}

	c.logger.Info().
		Uint64("destChainSelector", dest).
		Str("receiver", hex.EncodeToString(fields.Receiver)).
		Msg("Sending CCIP message from Stellar via Router")

	executorContractID := stellarutil.MustGenerateMockContractID(c.deployerKeypair.Address(), "executor")

	extraArgs := onrampbindings.GenericExtraArgsV3{
		Ccvs:               []string{c.vvrContractID},
		CcvArgs:            [][]byte{{}},
		Executor:           executorContractID,
		ExecutorArgs:       []byte{},
		GasLimit:           0,
		BlockConfirmations: 0,
		TokenReceiver:      []byte{},
		TokenArgs:          []byte{},
	}
	encodedExtraArgs, err := EncodeExtraArgsV3(extraArgs)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed to encode extra args: %w", err)
	}

	if c.feeTokenContractID == "" {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("fee token not deployed; run DeployContractsForSelector first")
	}
	sender := c.deployerKeypair.Address()

	var tokenAmounts []routerbindings.TokenAmount
	if fields.TokenAmount.Amount != nil && fields.TokenAmount.Amount.Sign() > 0 && len(fields.TokenAmount.TokenAddress) > 0 {
		if !fields.TokenAmount.Amount.IsInt64() {
			return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("token amount out of int64 range: %s", fields.TokenAmount.Amount.String())
		}
		tokenAddr, encErr := strkey.Encode(strkey.VersionByteContract, []byte(fields.TokenAmount.TokenAddress))
		if encErr != nil {
			return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("encode token address for send: %w", encErr)
		}
		tokenAmount := fields.TokenAmount.Amount.Int64()
		tokenAmounts = []routerbindings.TokenAmount{{
			Token:  tokenAddr,
			Amount: tokenAmount,
		}}
		c.logger.Info().
			Str("token", tokenAddr).
			Int64("amount", tokenAmount).
			Msg("Including token transfer in CCIP message")
	}

	routerMsg := routerbindings.StellarToAnyMessage{
		Receiver:     fields.Receiver,
		Data:         fields.Data,
		TokenAmounts: tokenAmounts,
		FeeToken:     c.feeTokenContractID,
		ExtraArgs:    encodedExtraArgs,
	}

	requiredFee, err := c.routerClient.GetFee(ctx, dest, routerMsg)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed to get fee from Router: %w", err)
	}
	c.logger.Info().Int64("requiredFee", requiredFee).Msg("Fee quote from Router")

	messageID, err := c.routerClient.CcipSend(ctx, sender, dest, routerMsg, requiredFee)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed to send message via Router: %w", err)
	}

	c.logger.Info().
		Str("messageID", hexutil.Encode(messageID[:])).
		Msg("CCIP message sent from Stellar via Router")

	return cciptestinterfaces.MessageSentEvent{
		MessageID: messageID,
		Sender:    protocol.UnknownAddress([]byte(sender)),
	}, nil
}

// SendMessageWithNonce implements cciptestinterfaces.CCIP17.
// Sends a CCIP message with a specific nonce.
func (c *Chain) SendMessageWithNonce(ctx context.Context, dest uint64, fields cciptestinterfaces.MessageFields, opts cciptestinterfaces.MessageOptions, sender *bind.TransactOpts, nonce *uint64, disableTokenAmountCheck bool) (cciptestinterfaces.MessageSentEvent, error) {
	if sender != nil || nonce != nil || disableTokenAmountCheck {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("SendMessageWithNonce: explicit sender, nonce, or token amount check override is not supported on Stellar")
	}
	return c.SendMessage(ctx, dest, fields, opts)
}

// Uncurse implements cciptestinterfaces.CCIP17.
// Uncurses a list of chains on this chain's RMN.
func (c *Chain) Uncurse(ctx context.Context, subjects [][16]byte) error {
	if c.rmnRemoteClient == nil {
		return fmt.Errorf("RMN Remote client not initialized")
	}
	err := c.rmnRemoteClient.Uncurse(ctx, subjects)
	if err != nil {
		return fmt.Errorf("failed to uncurse RMN Remote: %w", err)
	}
	c.logger.Debug().
		Int("numSubjects", len(subjects)).
		Msg("Uncursed RMN Remote")
	return nil
}

// WaitOneExecEventBySeqNo implements cciptestinterfaces.CCIP17.
// Waits for exactly one execution state change event.
func (c *Chain) WaitOneExecEventBySeqNo(ctx context.Context, from, seq uint64, timeout time.Duration) (cciptestinterfaces.ExecutionStateChangedEvent, error) {
	if c.offRampClient == nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("OffRamp client not initialized")
	}

	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("failed to get latest ledger: %w", err)
	}

	c.logger.Info().
		Uint64("sourceChainSelector", from).
		Uint64("sequenceNumber", seq).
		Uint32("startLedger", latestLedger.Sequence).
		Dur("timeout", timeout).
		Msg("Waiting for ExecutionStateChanged event from Stellar OffRamp")

	event, err := c.offRampClient.WaitForExecutionStateChangedEvent(
		ctx, latestLedger.Sequence, timeout,
		func(e *offrampbindings.ExecutionStateChangedEvent) bool {
			return e.SourceChainSelector == from && e.SequenceNumber == seq
		},
	)
	if err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("failed waiting for execution event: %w", err)
	}

	return cciptestinterfaces.ExecutionStateChangedEvent{
		SourceChainSelector: protocol.ChainSelector(event.SourceChainSelector),
		MessageID:           event.MessageId,
		MessageNumber:       event.SequenceNumber,
		State:               cciptestinterfaces.MessageExecutionState(event.State),
		ReturnData:          event.ReturnData,
	}, nil
}

// WaitOneSentEventBySeqNo implements cciptestinterfaces.CCIP17.
// Waits for exactly one CCIPMessageSent event matching the destination selector and sequence number.
func (c *Chain) WaitOneSentEventBySeqNo(ctx context.Context, to, seq uint64, timeout time.Duration) (cciptestinterfaces.MessageSentEvent, error) {
	if c.onRampClient == nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("OnRamp client not initialized")
	}

	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed to get latest ledger: %w", err)
	}

	c.logger.Info().
		Uint64("destChainSelector", to).
		Uint64("sequenceNumber", seq).
		Uint32("startLedger", latestLedger.Sequence).
		Dur("timeout", timeout).
		Msg("Waiting for CCIPMessageSent event from Stellar OnRamp")

	event, err := c.onRampClient.WaitForCCIPMessageSentEvent(
		ctx, latestLedger.Sequence, timeout,
		func(e *onrampbindings.CCIPMessageSentEvent) bool {
			return e.DestChainSelector == to && e.SequenceNumber == seq
		},
	)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed waiting for sent event: %w", err)
	}

	return cciptestinterfaces.MessageSentEvent{
		MessageID: event.MessageId,
		Sender:    protocol.UnknownAddress([]byte(event.Sender)),
	}, nil
}

// testTokenAssetCode is the classic Stellar asset code used by the test SAC token.
const testTokenAssetCode = "TEST"

// createTestToken sets up a SAC-wrapped classic Stellar asset for E2E token
// transfer tests. It creates an issuer, funds it, establishes trustlines, mints
// an initial supply, and deploys the SAC wrapper.
func (c *Chain) createTestToken(ctx context.Context, friendbotURL string) (string, error) {
	issuerSeed := sha256.Sum256([]byte(fmt.Sprintf("test-token-issuer-%s", c.networkPassphrase)))
	issuerKP, err := keypair.FromRawSeed(issuerSeed)
	if err != nil {
		return "", fmt.Errorf("create issuer keypair: %w", err)
	}
	c.testTokenIssuerKeypair = issuerKP

	if friendbotURL != "" {
		if err := c.fundViaFriendbot(friendbotURL, issuerKP.Address()); err != nil {
			return "", fmt.Errorf("fund issuer: %w", err)
		}
	}

	issuerDeployer := stellardeployment.NewDeployer(c.rpcClient, c.networkPassphrase, issuerKP)
	asset := txnbuild.CreditAsset{Code: testTokenAssetCode, Issuer: issuerKP.Address()}

	err = c.deployer.SubmitClassicOperation(ctx, &txnbuild.ChangeTrust{
		Line:          asset.MustToChangeTrustAsset(),
		SourceAccount: c.deployerKeypair.Address(),
	})
	if err != nil {
		return "", fmt.Errorf("establish trustline: %w", err)
	}

	err = issuerDeployer.SubmitClassicOperation(ctx, &txnbuild.Payment{
		Destination:   c.deployerKeypair.Address(),
		Amount:        "100000000",
		Asset:         asset,
		SourceAccount: issuerKP.Address(),
	})
	if err != nil {
		return "", fmt.Errorf("issue tokens: %w", err)
	}

	xdrAsset, err := asset.ToXDR()
	if err != nil {
		return "", fmt.Errorf("convert asset to XDR: %w", err)
	}
	contractID, err := c.deployer.DeploySACToken(ctx, xdrAsset)
	if err != nil {
		return "", fmt.Errorf("deploy SAC: %w", err)
	}

	c.logger.Info().
		Str("contractID", contractID).
		Str("issuer", issuerKP.Address()).
		Msg("Test SAC token deployed")

	return contractID, nil
}

const feeTokenAssetCode = "FEE"

// createFeeToken sets up a SAC-wrapped classic Stellar asset used for CCIP fee
// payments. Similar to createTestToken but with a separate issuer and asset code
// so the fee token is independent of the transfer-test token.
func (c *Chain) createFeeToken(ctx context.Context, friendbotURL string) (string, error) {
	issuerSeed := sha256.Sum256([]byte(fmt.Sprintf("fee-token-issuer-%s", c.networkPassphrase)))
	issuerKP, err := keypair.FromRawSeed(issuerSeed)
	if err != nil {
		return "", fmt.Errorf("create fee token issuer keypair: %w", err)
	}
	c.feeTokenIssuerKeypair = issuerKP

	if friendbotURL != "" {
		if err := c.fundViaFriendbot(friendbotURL, issuerKP.Address()); err != nil {
			return "", fmt.Errorf("fund fee token issuer: %w", err)
		}
	}

	issuerDeployer := stellardeployment.NewDeployer(c.rpcClient, c.networkPassphrase, issuerKP)
	asset := txnbuild.CreditAsset{Code: feeTokenAssetCode, Issuer: issuerKP.Address()}

	err = c.deployer.SubmitClassicOperation(ctx, &txnbuild.ChangeTrust{
		Line:          asset.MustToChangeTrustAsset(),
		SourceAccount: c.deployerKeypair.Address(),
	})
	if err != nil {
		return "", fmt.Errorf("establish fee token trustline: %w", err)
	}

	err = issuerDeployer.SubmitClassicOperation(ctx, &txnbuild.Payment{
		Destination:   c.deployerKeypair.Address(),
		Amount:        "100000000",
		Asset:         asset,
		SourceAccount: issuerKP.Address(),
	})
	if err != nil {
		return "", fmt.Errorf("issue fee tokens: %w", err)
	}

	xdrAsset, err := asset.ToXDR()
	if err != nil {
		return "", fmt.Errorf("convert fee asset to XDR: %w", err)
	}
	contractID, err := c.deployer.DeploySACToken(ctx, xdrAsset)
	if err != nil {
		return "", fmt.Errorf("deploy fee token SAC: %w", err)
	}

	c.logger.Info().
		Str("contractID", contractID).
		Str("issuer", issuerKP.Address()).
		Msg("Fee token SAC deployed")

	return contractID, nil
}

// GetTokenAddress returns the SAC contract ID of the test token deployed during
// DeployContractsForSelector, or an error if no token was deployed.
func (c *Chain) GetTokenAddress() (string, error) {
	if c.testTokenContractID == "" {
		return "", fmt.Errorf("test token not deployed; run DeployContractsForSelector first")
	}
	return c.testTokenContractID, nil
}

// GetTokenPoolAddress returns the lock-release pool contract ID deployed during
// DeployContractsForSelector.
func (c *Chain) GetTokenPoolAddress() (string, error) {
	if c.tokenPoolContractID == "" {
		return "", fmt.Errorf("token pool not deployed; run DeployContractsForSelector first")
	}
	return c.tokenPoolContractID, nil
}

// stellarFeeAggregatorHexForTopology returns the 0x-prefixed hex encoding of the same
// deterministic mock fee-aggregator contract ID used when initializing Stellar FeeQuoter,
// CommitteeVerifier, and VVR (mustGenerateMockContractID(..., "fee-aggregator")).
// Topology FeeAggregator must match that on-chain value so downstream tooling and
// changesets stay consistent; an EVM-style 0x1 placeholder does not match Soroban state.
func stellarFeeAggregatorHexForTopology(c *Chain) (string, error) {
	if c.deployerKeypair == nil {
		return "", fmt.Errorf("deployer keypair not set")
	}
	mockStrkey := stellarutil.MustGenerateMockContractID(c.deployerKeypair.Address(), "fee-aggregator")
	raw, err := strkey.Decode(strkey.VersionByteContract, mockStrkey)
	if err != nil {
		return "", fmt.Errorf("decode mock fee aggregator strkey: %w", err)
	}
	return hexutil.Encode(raw), nil
}

func ensureStellarFeeAggregatorsInTopology(c *Chain, topology *ccipOffchain.EnvironmentTopology) {
	if topology == nil || topology.NOPTopology == nil {
		return
	}
	const evmStyleFallback = "0x0000000000000000000000000000000000000001"
	feeAggHex, err := stellarFeeAggregatorHexForTopology(c)
	if err != nil {
		c.logger.Warn().Err(err).Msg("could not derive Stellar fee aggregator for topology; using EVM-style fallback (may break verifier/aggregator alignment)")
		feeAggHex = evmStyleFallback
	}
	for name, committee := range topology.NOPTopology.Committees {
		if committee.ChainConfigs == nil {
			continue
		}
		for chainKey, chainCfg := range committee.ChainConfigs {
			if chainCfg.FeeAggregator != "" {
				continue
			}
			sel, err := strconv.ParseUint(chainKey, 10, 64)
			if err != nil {
				continue
			}
			fam, err := chainsel.GetSelectorFamily(sel)
			if err != nil || fam != chainsel.FamilyStellar {
				continue
			}
			chainCfg.FeeAggregator = feeAggHex
			committee.ChainConfigs[chainKey] = chainCfg
		}
		topology.NOPTopology.Committees[name] = committee
	}
}

func selectorsFromBlockChains(chains cldf_chain.BlockChains) []uint64 {
	selectors := make([]uint64, 0)
	for sel := range chains.All() {
		selectors = append(selectors, sel)
	}
	sort.Slice(selectors, func(i, j int) bool {
		return selectors[i] < selectors[j]
	})
	return selectors
}

func addressBytesLengthForSelector(selector uint64) (uint32, error) {
	family, err := chainsel.GetSelectorFamily(selector)
	if err != nil {
		return 0, fmt.Errorf("get selector family for %d: %w", selector, err)
	}
	if family == chainsel.FamilyStellar {
		return stellarAddressLen, nil
	}
	return 20, nil
}

func zeroAddressBytesForSelector(selector uint64) ([]byte, error) {
	addressBytesLength, err := addressBytesLengthForSelector(selector)
	if err != nil {
		return nil, err
	}
	return make([]byte, addressBytesLength), nil
}

func lookupAddressRef(ds datastore.DataStore, selector uint64, contractType datastore.ContractType, version *semver.Version, qualifier string) (datastore.AddressRef, error) {
	ref, err := ds.Addresses().Get(datastore.NewAddressRefKey(selector, contractType, version, qualifier))
	if err != nil {
		return datastore.AddressRef{}, err
	}
	return ref, nil
}

func lookupStellarContractID(ds datastore.DataStore, selector uint64, contractType datastore.ContractType, version *semver.Version, qualifier string) (string, error) {
	ref, err := lookupAddressRef(ds, selector, contractType, version, qualifier)
	if err != nil {
		return "", err
	}
	contractID, err := scval.HexToContractStrkey(ref.Address)
	if err != nil {
		return "", fmt.Errorf("convert %s address %s to contract strkey: %w", contractType, ref.Address, err)
	}
	return contractID, nil
}

func addressBytesForSelector(ref datastore.AddressRef, selector uint64) ([]byte, error) {
	raw, err := hexutil.Decode(ref.Address)
	if err != nil {
		return nil, fmt.Errorf("decode address %s: %w", ref.Address, err)
	}
	expectedLen, err := addressBytesLengthForSelector(selector)
	if err != nil {
		return nil, err
	}
	if len(raw) != int(expectedLen) {
		return nil, fmt.Errorf("address %s has %d bytes, expected %d for selector %d", ref.Address, len(raw), expectedLen, selector)
	}
	return raw, nil
}

func canonicalSourceOnRampBytesForSelector(ref datastore.AddressRef, selector uint64) ([]byte, error) {
	raw, err := addressBytesForSelector(ref, selector)
	if err != nil {
		return nil, err
	}

	family, err := chainsel.GetSelectorFamily(selector)
	if err != nil {
		return nil, fmt.Errorf("get selector family for %d: %w", selector, err)
	}
	if family != chainsel.FamilyEVM {
		return raw, nil
	}

	// Canonical CCIP messages encode EVM source addresses as 32-byte left-padded
	// fields. Store the same bytes in Stellar OffRamp source config so
	// verify_onramp_allowed hashes the exact same payload.
	padded := make([]byte, 32)
	copy(padded[len(padded)-len(raw):], raw)
	return padded, nil
}

func (c *Chain) ensureLocalContracts(ds datastore.DataStore, selector uint64) error {
	if c.deployer == nil {
		return fmt.Errorf("deployer not initialized")
	}

	var err error
	if c.onRampContractID == "" {
		c.onRampContractID, err = lookupStellarContractID(ds, selector, datastore.ContractType(onrampoperations.ContractType), semver.MustParse(onrampoperations.Deploy.Version()), "")
		if err != nil {
			return fmt.Errorf("lookup local onramp: %w", err)
		}
	}
	if c.offRampContractID == "" {
		c.offRampContractID, err = lookupStellarContractID(ds, selector, datastore.ContractType(offrampoperations.ContractType), semver.MustParse(offrampoperations.Deploy.Version()), "")
		if err != nil {
			return fmt.Errorf("lookup local offramp: %w", err)
		}
	}
	if c.routerContractID == "" {
		c.routerContractID, err = lookupStellarContractID(ds, selector, datastore.ContractType(routeroperations.ContractType), routeroperations.Version, "")
		if err != nil {
			return fmt.Errorf("lookup local router: %w", err)
		}
	}
	if c.vvrContractID == "" {
		c.vvrContractID, err = lookupStellarContractID(ds, selector, datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType), versioned_verifier_resolver.Version, devenvcommon.DefaultCommitteeVerifierQualifier)
		if err != nil {
			return fmt.Errorf("lookup local versioned verifier resolver: %w", err)
		}
	}

	if c.onRampClient == nil {
		c.onRampClient = onrampbindings.NewOnRampClient(c.deployer, c.onRampContractID)
	}
	if c.offRampClient == nil {
		c.offRampClient = offrampbindings.NewOffRampClient(c.deployer, c.offRampContractID)
	}
	if c.routerClient == nil {
		c.routerClient = routerbindings.NewRouterClient(c.deployer, c.routerContractID)
	}

	return nil
}

func (c *Chain) buildOnRampDestConfigs(ds datastore.DataStore, remoteSelectors []uint64, defaultExecutor string, useRemoteOffRamp bool) ([]onrampbindings.DestChainConfigArgs, error) {
	configs := make([]onrampbindings.DestChainConfigArgs, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		addressBytesLength, err := addressBytesLengthForSelector(rs)
		if err != nil {
			return nil, err
		}

		offRampBytes, err := zeroAddressBytesForSelector(rs)
		if err != nil {
			return nil, err
		}
		if useRemoteOffRamp {
			offRampRef, err := lookupAddressRef(ds, rs, datastore.ContractType(offrampoperations.ContractType), semver.MustParse(offrampoperations.Deploy.Version()), "")
			if err != nil {
				return nil, fmt.Errorf("lookup remote offramp for %d: %w", rs, err)
			}
			offRampBytes, err = addressBytesForSelector(offRampRef, rs)
			if err != nil {
				return nil, fmt.Errorf("resolve remote offramp bytes for %d: %w", rs, err)
			}
		}

		configs = append(configs, onrampbindings.DestChainConfigArgs{
			DestChainSelector:         rs,
			AddressBytesLength:        addressBytesLength,
			BaseExecutionGasCost:      100_000,
			DefaultCcvs:               []string{c.vvrContractID},
			DefaultExecutor:           defaultExecutor,
			LaneMandatedCcvs:          []string{},
			MessageNetworkFeeUsdCents: 100,
			OffRamp:                   offRampBytes,
			Router:                    c.routerContractID,
			TokenNetworkFeeUsdCents:   50,
			TokenReceiverAllowed:      true,
		})
	}
	return configs, nil
}

func (c *Chain) buildOffRampSourceConfigs(ds datastore.DataStore, remoteSelectors []uint64, useRemoteOnRamp bool) ([]offrampbindings.SourceChainConfigArgs, error) {
	configs := make([]offrampbindings.SourceChainConfigArgs, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		onRampBytes := make([]byte, 32)
		if useRemoteOnRamp {
			onRampRef, err := lookupAddressRef(ds, rs, datastore.ContractType(onrampoperations.ContractType), semver.MustParse(onrampoperations.Deploy.Version()), "")
			if err != nil {
				return nil, fmt.Errorf("lookup remote onramp for %d: %w", rs, err)
			}
			onRampBytes, err = canonicalSourceOnRampBytesForSelector(onRampRef, rs)
			if err != nil {
				return nil, fmt.Errorf("resolve remote onramp bytes for %d: %w", rs, err)
			}
		}

		configs = append(configs, offrampbindings.SourceChainConfigArgs{
			SourceChainSelector: rs,
			IsEnabled:           true,
			DefaultCcvs:         []string{c.vvrContractID},
			LaneMandatedCcvs:    []string{},
			OnRamps:             [][]byte{onRampBytes},
			Router:              c.routerContractID,
		})
	}
	return configs, nil
}

// EncodeExtraArgsV3 converts a GenericExtraArgsV3 to XDR bytes suitable for
// the OnRamp contract's ExtraArgs field (parsed via GenericExtraArgsV3::from_xdr).
func EncodeExtraArgsV3(args onrampbindings.GenericExtraArgsV3) ([]byte, error) {
	scVal, err := args.ToScVal()
	if err != nil {
		return nil, fmt.Errorf("failed to convert extra args to ScVal: %w", err)
	}
	return scVal.MarshalBinary()
}

func (c *Chain) NativeBalance(ctx context.Context, address protocol.UnknownAddress) (*big.Int, error) {
	if c.deployer == nil {
		return nil, fmt.Errorf("deployer not initialized")
	}
	bal, _, _, err := c.deployer.NativeAccountState(ctx, []byte(address))
	if err != nil {
		return nil, err
	}
	return bal, nil
}

func (c *Chain) TransferNative(ctx context.Context, from, to protocol.UnknownAddress, amount *big.Int) error {
	if c.deployer == nil || c.deployerKeypair == nil {
		return fmt.Errorf("deployer not initialized")
	}
	deployerRaw, err := strkey.Decode(strkey.VersionByteAccountID, c.deployerKeypair.Address())
	if err != nil {
		return fmt.Errorf("decode deployer account: %w", err)
	}
	if !bytes.Equal(deployerRaw, []byte(from)) {
		return fmt.Errorf("address %x is not a configured account in this environment", []byte(from))
	}
	toStr, err := strkey.Encode(strkey.VersionByteAccountID, []byte(to))
	if err != nil {
		return fmt.Errorf("encode destination account: %w", err)
	}
	var stroops int64
	if amount == nil {
		bal, _, exists, err := c.deployer.NativeAccountState(ctx, deployerRaw)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: account has no ledger entry", cciptestinterfaces.ErrInsufficientNativeBalance)
		}
		reserve := big.NewInt(int64(txnbuild.MinBaseFee) * 10)
		if bal.Cmp(reserve) <= 0 {
			return fmt.Errorf("%w: balance %s stroops, reserve %s stroops", cciptestinterfaces.ErrInsufficientNativeBalance, bal.String(), reserve.String())
		}
		send := new(big.Int).Sub(bal, reserve)
		if !send.IsInt64() || send.Int64() <= 0 {
			return fmt.Errorf("%w: spendable amount does not fit transfer", cciptestinterfaces.ErrInsufficientNativeBalance)
		}
		stroops = send.Int64()
	} else {
		if amount.Sign() <= 0 {
			return fmt.Errorf("transfer amount must be positive")
		}
		if !amount.IsInt64() {
			return fmt.Errorf("amount must fit int64 stroops")
		}
		stroops = amount.Int64()
	}
	if err := c.deployer.SendNativePayment(ctx, toStr, stroops); err != nil {
		return err
	}
	return nil
}
