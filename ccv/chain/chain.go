package ccvchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
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

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/operations/executor"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/operations/onramp"
	routeroperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_6_0/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/deployments"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/simple_node_set"
)

// stellarAddressLen is 32 bytes for ed25519 public key
const stellarAddressLen = 32

// generateContractAddress generates a deterministic Soroban contract address from a name and network passphrase.
// Soroban contract addresses are derived from the network ID (SHA-256 of passphrase) and a unique identifier.
// The resulting address is 32 bytes (the raw ed25519 public key format used internally).
func generateContractAddress(name, networkPassphrase string) []byte {
	// Network ID is SHA-256 of the network passphrase
	networkID := sha256.Sum256([]byte(networkPassphrase))

	// Combine network ID with name to create deterministic seed
	combined := append(networkID[:], []byte(name)...)
	hash := sha256.Sum256(combined)

	return hash[:]
}

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
	logger            zerolog.Logger
	rpcClient         *rpcclient.Client
	networkPassphrase string
	sorobanRPCURL     string
	deployerKeypair   *keypair.Full
	deployer          *stellardeployment.Deployer
	onRampClient      *onrampbindings.OnRampClient
	onRampContractID  string
}

// New creates a new Stellar Chain instance.
func New(logger zerolog.Logger) *Chain {
	return &Chain{
		logger: logger,
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

// ConnectContractsWithSelectors implements cciptestinterfaces.CCIP17Configuration.
// Connects this chain's OnRamp to OffRamps on remote chains and configures CommitteeVerifiers.
func (c *Chain) ConnectContractsWithSelectors(ctx context.Context, e *deployment.Environment, selector uint64, remoteSelectors []uint64, committees *deployments.EnvironmentTopology) error {
	c.logger.Info().Uint64("selector", selector).Interface("remoteSelectors", remoteSelectors).Msg("Connecting contracts with selectors")

	// TODO: implement contract connection logic for Stellar
	// This should:
	// 1. Configure the OnRamp with destination chain selectors
	// 2. Configure the OffRamp with source chain selectors
	// 3. Set up CommitteeVerifier signers from the topology

	executorContractID := mustGenerateMockContractID(c.deployerKeypair.Address(), "executor")

	destChainConfigArgs := []onrampbindings.DestChainConfigArgs{}
	for _, remoteSelector := range remoteSelectors {
		destChainConfigArgs = append(destChainConfigArgs, onrampbindings.DestChainConfigArgs{
			AddressBytesLength:        32,
			BaseExecutionGasCost:      100000,
			DefaultCcvs:               []string{c.onRampContractID},
			DestChainSelector:         remoteSelector,
			Router:                    c.deployerKeypair.Address(),                                     // deployer acts as router,
			OffRamp:                   generateContractAddress("stellar-offramp", c.networkPassphrase), // TODO: use the actual OffRamp contract address
			DefaultExecutor:           executorContractID,                                              // TODO: use the actual Executor contract address
			LaneMandatedCcvs:          []string{},
			MessageNetworkFeeUsdCents: 0,
			TokenNetworkFeeUsdCents:   0,
			TokenReceiverAllowed:      false,
		})
	}

	err := c.onRampClient.ApplyDestChainConfigUpdates(ctx, destChainConfigArgs)
	if err != nil {
		return fmt.Errorf("failed to apply dest chain config updates: %w", err)
	}

	c.logger.Info().Uints64("remote selectors", remoteSelectors).Str("onramp contract ID", c.onRampContractID).Str("router address", c.deployerKeypair.Address()).Msg("Dest chain config updates applied")
	return nil
}

// DeployContractsForSelector implements cciptestinterfaces.CCIP17Configuration.
// Deploys CCIP contracts for the given chain selector.
func (c *Chain) DeployContractsForSelector(ctx context.Context, env *deployment.Environment, selector uint64, committees *deployments.EnvironmentTopology) (datastore.DataStore, error) {
	c.logger.Info().Uint64("selector", selector).Msg("Deploying Stellar CCIP contracts")

	ds := datastore.NewMemoryDataStore()

	// Helper to generate a hex-encoded contract address (used for mock/placeholder contracts)
	contractHexAddr := func(name string) string {
		return hexutil.Encode(generateContractAddress(name, c.networkPassphrase))
	}

	// strkeyToHex decodes a strkey address (C… contract or G… account) to a 0x-prefixed hex string.
	strkeyToHex := func(addr string) (string, error) {
		var vb strkey.VersionByte
		switch {
		case len(addr) > 0 && addr[0] == 'C':
			vb = strkey.VersionByteContract
		case len(addr) > 0 && addr[0] == 'G':
			vb = strkey.VersionByteAccountID
		default:
			return "", fmt.Errorf("unsupported strkey prefix: %s", addr)
		}
		raw, err := strkey.Decode(vb, addr)
		if err != nil {
			return "", fmt.Errorf("decode strkey %s: %w", addr, err)
		}
		return hexutil.Encode(raw), nil
	}

	// Locate the compiled OnRamp WASM
	origDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}
	stellarRoot, err := filepath.Abs(filepath.Join(origDir, "../../../chainlink-stellar"))
	if err != nil {
		return nil, fmt.Errorf("failed to get stellar root: %w", err)
	}

	c.logger.Info().Str("stellarRoot", stellarRoot).Msg("Stellar root")

	onrampWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "onramp.wasm")
	if _, statErr := os.Stat(onrampWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("OnRamp WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", onrampWasmPath)
	}

	// Deploy the OnRamp contract
	c.logger.Info().Str("wasmPath", onrampWasmPath).Msg("Deploying OnRamp contract...")

	onrampSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "onramp")
	onrampContractID, err := c.deployer.DeployContract(ctx, onrampWasmPath, onrampSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy OnRamp contract: %w", err)
	}
	c.logger.Info().Str("contractID", onrampContractID).Msg("OnRamp contract deployed")

	// Initialize the OnRamp client with the contract ID
	// Note: For actual deployment, we would:
	// 1. Deploy the WASM: DeployOnRamp(ctx, c.rpcClient, c.networkPassphrase, c.deployerKeypair, wasmPath)
	// 2. Initialize it with proper config
	// For now, we use the deterministic address and will deploy when WASM is available
	c.onRampContractID = onrampContractID
	c.onRampClient = onrampbindings.NewOnRampClient(c.deployer, onrampContractID)

	// Deploy the RMN Remote contract
	rmnRemoteWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	if _, statErr := os.Stat(rmnRemoteWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("RMN Remote WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", rmnRemoteWasmPath)
	}

	c.logger.Info().Str("wasmPath", rmnRemoteWasmPath).Msg("Deploying RMN Remote contract...")
	rmnRemoteSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "rmn-remote")
	rmnRemoteContractID, err := c.deployer.DeployContract(ctx, rmnRemoteWasmPath, rmnRemoteSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy RMN Remote contract: %w", err)
	}
	c.logger.Info().Str("contractID", rmnRemoteContractID).Msg("RMN Remote contract deployed")

	rmnRemoteClient := rmnremotebindings.NewRmnRemoteClient(c.deployer, rmnRemoteContractID)
	err = rmnRemoteClient.Initialize(ctx, c.deployerKeypair.Address(), selector)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize RMN Remote: %w", err)
	}
	c.logger.Info().Str("rmnRemoteContractID", rmnRemoteContractID).Msg("RMN Remote initialized")

	// Deploy the RMN Proxy contract
	rmnProxyWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "rmn_proxy.wasm")
	if _, statErr := os.Stat(rmnProxyWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("RMN Proxy WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", rmnProxyWasmPath)
	}

	c.logger.Info().Str("wasmPath", rmnProxyWasmPath).Msg("Deploying RMN Proxy contract...")
	rmnProxySalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "rmn-proxy")
	rmnProxyContractID, err := c.deployer.DeployContract(ctx, rmnProxyWasmPath, rmnProxySalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy RMN Proxy contract: %w", err)
	}
	c.logger.Info().Str("contractID", rmnProxyContractID).Msg("RMN Proxy contract deployed")

	rmnProxyClient := rmnproxybindings.NewRmnProxyClient(c.deployer, rmnProxyContractID)
	err = rmnProxyClient.Initialize(ctx, c.deployerKeypair.Address(), rmnRemoteContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize RMN Proxy: %w", err)
	}
	c.logger.Info().Str("rmnProxyContractID", rmnProxyContractID).Msg("RMN Proxy initialized")

	// Initialize the OnRamp with dependency contracts
	mockFeeQuoter := mustGenerateMockContractID(c.deployerKeypair.Address(), "fee-quoter")
	mockFeeAggregator := mustGenerateMockContractID(c.deployerKeypair.Address(), "fee-aggregator")
	mockTokenAdminRegistry := mustGenerateMockContractID(c.deployerKeypair.Address(), "token-admin-registry")

	err = c.onRampClient.Initialize(ctx, c.deployerKeypair.Address(), onrampbindings.StaticConfig{
		ChainSelector:         selector,
		TokenAdminRegistry:    mockTokenAdminRegistry,
		RmnProxy:              rmnProxyContractID,
		MaxUsdCentsPerMessage: 10000, // $100
	}, onrampbindings.DynamicConfig{
		FeeQuoter:     mockFeeQuoter,
		FeeAggregator: mockFeeAggregator,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OnRamp: %w", err)
	}

	c.logger.Info().
		Str("onRampContractID", onrampContractID).
		Msg("OnRamp client initialized")

	// Deploy the Versioned Verifier Resolver (VVR) contract
	vvrWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccvs_versioned_verifier_resolver.wasm")
	if _, statErr := os.Stat(vvrWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("VVR WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", vvrWasmPath)
	}

	c.logger.Info().Str("wasmPath", vvrWasmPath).Msg("Deploying Versioned Verifier Resolver contract...")

	vvrSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "versioned-verifier-resolver")
	vvrContractID, err := c.deployer.DeployContract(ctx, vvrWasmPath, vvrSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy VVR contract: %w", err)
	}
	c.logger.Info().Str("contractID", vvrContractID).Msg("VVR contract deployed")

	vvrClient := vvrbindings.NewVersionedVerifierResolverClient(c.deployer, vvrContractID)

	err = vvrClient.Initialize(ctx, c.deployerKeypair.Address(), mockFeeAggregator)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize VVR: %w", err)
	}

	c.logger.Info().
		Str("vvrContractID", vvrContractID).
		Msg("VVR client initialized")

	// Deploy the Committee Verifier contract
	cvWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccvs_committee_verifier.wasm")
	if _, statErr := os.Stat(cvWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("Committee Verifier WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", cvWasmPath)
	}

	c.logger.Info().Str("wasmPath", cvWasmPath).Msg("Deploying Committee Verifier contract...")

	cvSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "committee-verifier")
	cvContractID, err := c.deployer.DeployContract(ctx, cvWasmPath, cvSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy Committee Verifier contract: %w", err)
	}
	c.logger.Info().Str("contractID", cvContractID).Msg("Committee Verifier contract deployed")

	cvClient := cvbindings.NewCommitteeVerifierClient(c.deployer, cvContractID)

	allowlistAdmin := c.deployerKeypair.Address()
	mockStorageLocation := generateContractAddress("storage-location", c.networkPassphrase)
	err = cvClient.Initialize(ctx, c.deployerKeypair.Address(), cvbindings.DynamicConfig{
		AllowlistAdmin: &allowlistAdmin,
		FeeAggregator:  &mockFeeAggregator,
	}, [][]byte{mockStorageLocation}, rmnProxyContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Committee Verifier: %w", err)
	}

	c.logger.Info().
		Str("cvContractID", cvContractID).
		Msg("Committee Verifier client initialized")

	remoteSelectors := []uint64{}
	for selector := range env.BlockChains.All() {
		remoteSelectors = append(remoteSelectors, selector)
	}
	outboundImplUpdates := []vvrbindings.OutboundImplementationUpdate{}
	for _, remoteSelector := range remoteSelectors {
		outboundImplUpdates = append(outboundImplUpdates, vvrbindings.OutboundImplementationUpdate{
			DestChainSelector: remoteSelector,
			Verifier:          &cvContractID,
		})
	}

	err = vvrClient.ApplyOutboundImplUpdates(ctx, outboundImplUpdates)
	if err != nil {
		return nil, fmt.Errorf("failed to apply outbound implementation updates: %w", err)
	}

	remoteChainConfigs := make([]cvbindings.RemoteChainConfig, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		router := c.deployerKeypair.Address()
		remoteChainConfigs = append(remoteChainConfigs, cvbindings.RemoteChainConfig{
			RemoteChainSelector: rs,
			FeeUsdCents:         0,
			GasForVerification:  10000, // CANNOT be zero
			PayloadSizeBytes:    0,
			AllowlistEnabled:    false,
			Router:              &router,
		})
	}
	err = cvClient.ApplyRemoteChainCfgUpdates(ctx, remoteChainConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to apply remote chain config updates on committee verifier: %w", err)
	}
	c.logger.Info().Int("count", len(remoteChainConfigs)).Msg("Committee Verifier remote chain configs applied")

	// Add OnRamp to datastore
	onrampHex, err := strkeyToHex(onrampContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert OnRamp address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       onrampHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(onrampoperations.ContractType),
		Version:       semver.MustParse(onrampoperations.Deploy.Version()),
	})

	// Add OffRamp
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       onrampHex, // TODO: use the actual OffRamp contract address
		ChainSelector: selector,
		Type:          datastore.ContractType(offrampoperations.ContractType),
		Version:       semver.MustParse(offrampoperations.Deploy.Version()),
	})

	// Add Router
	routerHex, err := strkeyToHex(c.deployerKeypair.Address()) // TODO: use the actual Router contract address
	if err != nil {
		return nil, fmt.Errorf("failed to convert Router address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       routerHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(routeroperations.ContractType),
		Version:       semver.MustParse(routeroperations.Deploy.Version()),
	})

	// // Add token pools
	// for i, combo := range devenvcommon.AllTokenCombinations() {
	// 	addressRef := combo.DestPoolAddressRef()
	// 	ds.AddressRefStore.Add(datastore.AddressRef{
	// 		Address:       contractHexAddr(fmt.Sprintf("stellar-dst-token-%d", i)),
	// 		Type:          addressRef.Type,
	// 		Version:       addressRef.Version,
	// 		Qualifier:     addressRef.Qualifier,
	// 		ChainSelector: selector,
	// 	})
	// 	addressRef = combo.SourcePoolAddressRef()
	// 	ds.AddressRefStore.Add(datastore.AddressRef{
	// 		Address:       contractHexAddr(fmt.Sprintf("stellar-src-token-%d", i)),
	// 		Type:          addressRef.Type,
	// 		Version:       addressRef.Version,
	// 		Qualifier:     addressRef.Qualifier,
	// 		ChainSelector: selector,
	// 	})
	// }

	// Add CCV refs — use the deployed VVR contract address
	vvrHex, err := strkeyToHex(vvrContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert VVR address: %w", err)
	}
	for _, qualifier := range []string{
		devenvcommon.DefaultCommitteeVerifierQualifier,
		// devenvcommon.SecondaryCommitteeVerifierQualifier,
		// devenvcommon.TertiaryCommitteeVerifierQualifier,
		// devenvcommon.QuaternaryReceiverQualifier,
	} {
		ds.AddressRefStore.Add(datastore.AddressRef{
			Address:       vvrHex,
			Type:          datastore.ContractType(committee_verifier.ResolverType),
			Version:       semver.MustParse(committee_verifier.Deploy.Version()),
			Qualifier:     qualifier,
			ChainSelector: selector,
		})
	}

	// Add executor refs
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       contractHexAddr("stellar-executor"),
		Type:          datastore.ContractType(executor.ContractType),
		Version:       semver.MustParse(executor.Deploy.Version()),
		Qualifier:     devenvcommon.DefaultExecutorQualifier,
		ChainSelector: selector,
	})

	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       contractHexAddr("stellar-executor-proxy"),
		Type:          datastore.ContractType(executor.ProxyType),
		Version:       semver.MustParse(executor.DeployProxy.Version()),
		Qualifier:     devenvcommon.DefaultExecutorQualifier,
		ChainSelector: selector,
	})

	// Add RMN remote refs — use the deployed RMN Remote contract address
	rmnRemoteHex, err := strkeyToHex(rmnRemoteContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert RMN Remote address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       rmnRemoteHex,
		Type:          datastore.ContractType(rmn_remote.ContractType),
		Version:       semver.MustParse(rmn_remote.Deploy.Version()),
		ChainSelector: selector,
	})

	return ds.Seal(), nil
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
func (c *Chain) FundAddresses(ctx context.Context, input *blockchain.Input, addresses []protocol.UnknownAddress, nativeAmount *big.Int) error {
	for _, addr := range addresses {
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
		Str("amount", nativeAmount.String()).
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
	// TODO: implement RMN curse for Stellar
	return nil
}

// ExposeMetrics implements cciptestinterfaces.CCIP17.
// Exposes Prometheus metrics for monitoring.
func (c *Chain) ExposeMetrics(ctx context.Context, source, dest uint64) ([]string, *prometheus.Registry, error) {
	// TODO: implement metrics exposure for Stellar lanes
	return nil, nil, nil
}

// GetEOAReceiverAddress implements cciptestinterfaces.CCIP17.
// Gets an EOA receiver address for this chain.
func (c *Chain) GetEOAReceiverAddress() (protocol.UnknownAddress, error) {
	// Generate a deterministic receiver address based on the network passphrase
	// This ensures the same address is returned for the same network
	receiverSeed := fmt.Sprintf("receiver-%s", c.networkPassphrase)
	seedHash := sha256.Sum256([]byte(receiverSeed))
	receiverKP, err := keypair.FromRawSeed(seedHash)
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("failed to create receiver keypair: %w", err)
	}
	// Decode the strkey address to raw bytes
	rawBytes, err := strkey.Decode(strkey.VersionByteAccountID, receiverKP.Address())
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("failed to decode receiver address: %w", err)
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
	// TODO: implement - query the OnRamp contract for max data bytes
	return 0, nil
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
// Gets the balance of a token for an address.
func (c *Chain) GetTokenBalance(ctx context.Context, address, tokenAddress protocol.UnknownAddress) (*big.Int, error) {
	// TODO: implement - query the token balance using Soroban RPC
	return nil, nil
}

// GetUserNonce implements cciptestinterfaces.CCIP17.
// Returns the nonce for the given user address on this chain.
func (c *Chain) GetUserNonce(ctx context.Context, userAddress protocol.UnknownAddress) (uint64, error) {
	// TODO: implement - query the user's sequence number from the Stellar network
	return 0, nil
}

// ManuallyExecuteMessage implements cciptestinterfaces.CCIP17.
// Manually executes a CCIP message on this chain.
func (c *Chain) ManuallyExecuteMessage(ctx context.Context, message protocol.Message, gasLimit uint64, ccvs []protocol.UnknownAddress, verifierResults [][]byte) (cciptestinterfaces.ExecutionStateChangedEvent, error) {
	// TODO: implement - call the OffRamp contract to execute a message manually
	return cciptestinterfaces.ExecutionStateChangedEvent{}, nil
}

// SendMessage implements cciptestinterfaces.CCIP17.
// Sends a CCIP message to the specified destination chain.
func (c *Chain) SendMessage(ctx context.Context, dest uint64, fields cciptestinterfaces.MessageFields, opts cciptestinterfaces.MessageOptions) (cciptestinterfaces.MessageSentEvent, error) {
	if c.onRampClient == nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("OnRamp client not initialized")
	}

	c.logger.Info().
		Uint64("destChainSelector", dest).
		Str("receiver", fields.Receiver.String()).
		Msg("Sending CCIP message from Stellar")

	// Build the message
	message := onrampbindings.StellarToAnyMessage{
		Receiver:     fields.Receiver,
		Data:         fields.Data,
		TokenAmounts: make([]onrampbindings.TokenAmount, 0), // No token transfers for basic test
		FeeToken:     c.deployerKeypair.Address(),           // Use deployer as fee token placeholder
		ExtraArgs:    []byte{},
	}

	// Get the original sender address
	originalSender := c.deployerKeypair.Address()

	// TODO: re-enable this to be called via the router instead of directly on the OnRamp
	// // Call forward_from_router on the OnRamp
	// result, err := c.onRampClient.ForwardFromRouter(ctx, dest, message, 0, originalSender)
	// if err != nil {
	// 	return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed to send message: %w", err)
	// }

	// c.logger.Info().
	// 	Str("messageID", hexutil.Encode(result[:])).
	// 	Msg("CCIP message sent from Stellar")

	// // Build the response
	// return cciptestinterfaces.MessageSentEvent{
	// 	MessageID: result,
	// 	Sender:    protocol.UnknownAddress([]byte(originalSender)),
	// 	Message: &protocol.Message{
	// 		Sender:         protocol.UnknownAddress([]byte(originalSender)),
	// 		SenderLength:   uint8(len(originalSender)),
	// 		Receiver:       protocol.UnknownAddress(fields.Receiver),
	// 		ReceiverLength: uint8(len(fields.Receiver)),
	// 		Data:           protocol.ByteSlice(fields.Data),
	// 		DataLength:     uint16(len(fields.Data)),
	// 		Version:        protocol.MessageVersion,
	// 	},
	// }, nil

	c.logger.Info().
		Str("message", fmt.Sprintf("%+v", message)).
		Str("originalSender", originalSender).
		Msg("CCIP message built")

	return cciptestinterfaces.MessageSentEvent{}, nil
}

// SendMessageWithNonce implements cciptestinterfaces.CCIP17.
// Sends a CCIP message with a specific nonce.
func (c *Chain) SendMessageWithNonce(ctx context.Context, dest uint64, fields cciptestinterfaces.MessageFields, opts cciptestinterfaces.MessageOptions, sender *bind.TransactOpts, nonce *atomic.Uint64, disableTokenAmountCheck bool) (cciptestinterfaces.MessageSentEvent, error) {
	// TODO: implement - call the Router/OnRamp contract with specific nonce
	// NOTE: sender *bind.TransactOpts is EVM-specific and will need adaptation for Stellar
	return cciptestinterfaces.MessageSentEvent{}, nil
}

// Uncurse implements cciptestinterfaces.CCIP17.
// Uncurses a list of chains on this chain's RMN.
func (c *Chain) Uncurse(ctx context.Context, subjects [][16]byte) error {
	// TODO: implement RMN uncurse for Stellar
	return nil
}

// WaitOneExecEventBySeqNo implements cciptestinterfaces.CCIP17.
// Waits for exactly one execution state change event.
func (c *Chain) WaitOneExecEventBySeqNo(ctx context.Context, from, seq uint64, timeout time.Duration) (cciptestinterfaces.ExecutionStateChangedEvent, error) {
	// TODO: implement - poll for execution events from the OffRamp contract
	return cciptestinterfaces.ExecutionStateChangedEvent{}, nil
}

// WaitOneSentEventBySeqNo implements cciptestinterfaces.CCIP17.
// Waits for exactly one message sent event.
func (c *Chain) WaitOneSentEventBySeqNo(ctx context.Context, to, seq uint64, timeout time.Duration) (cciptestinterfaces.MessageSentEvent, error) {
	if c.onRampClient == nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("OnRamp client not initialized")
	}

	c.logger.Info().
		Uint64("destChainSelector", to).
		Uint64("sequenceNumber", seq).
		Dur("timeout", timeout).
		Msg("Waiting for CCIPMessageSent event from Stellar OnRamp")

	// TODO: implement - poll for the event

	return cciptestinterfaces.MessageSentEvent{}, nil
}

// generateMockContractID generates a deterministic mock contract ID for testing.
func mustGenerateMockContractID(deployerAddress, contractName string) string {
	// Generate a deterministic salt
	salt := stellardeployment.GenerateDeterministicSalt(deployerAddress, contractName)

	// Encode as a Stellar contract address
	encoded, err := strkey.Encode(strkey.VersionByteContract, salt[:])
	if err != nil {
		panic(fmt.Errorf("failed to encode mock contract ID: %w", err))
	}
	return encoded
}
