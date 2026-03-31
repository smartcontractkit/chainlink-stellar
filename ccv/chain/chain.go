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
	"os"
	"path/filepath"
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
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/executor"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/onramp"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/proxy"
	routeroperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_6_0/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	ccipOffchain "github.com/smartcontractkit/chainlink-ccip/deployment/v1_7_0/offchain"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cciprecv "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ccip_receiver"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/simple_node_set"
)

// stellarAddressLen is 32 bytes for ed25519 public key
const stellarAddressLen = 32

// CcipReceiverContractType is the datastore contract type for the example CCIP
// receiver deployed on Stellar so that GetReceiverAddress can return a valid
// Wasm contract address in tests.
const CcipReceiverContractType = "CcipReceiverExample"

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
	chainSelector      uint64
	logger             zerolog.Logger
	rpcClient          *rpcclient.Client
	networkPassphrase  string
	sorobanRPCURL      string
	deployerKeypair    *keypair.Full
	deployer           *stellardeployment.Deployer
	onRampClient       *onrampbindings.OnRampClient
	onRampContractID   string
	offRampClient      *offrampbindings.OffRampClient
	offRampContractID  string
	routerClient       *routerbindings.RouterClient
	routerContractID   string
	feeQuoterClient    *fqbindings.FeeQuoterClient
	vvrContractID      string
	cvContractID       string
	receiverContractID string
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

	remoteSelectors = filterRemoteSelectors(remoteSelectors, selector)
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

// DeployContractsForSelector implements cciptestinterfaces.CCIP17Configuration.
// Deploys CCIP contracts for the given chain selector.
func (c *Chain) DeployContractsForSelector(ctx context.Context, env *deployment.Environment, selector uint64, topology *ccipOffchain.EnvironmentTopology) (datastore.DataStore, error) {
	c.logger.Info().Uint64("selector", selector).Msg("Deploying Stellar CCIP contracts")

	// TODO: can we just use env.DataStore instead of creating a new one?
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

	stellarRoot, err := findStellarRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to locate chainlink-stellar root: %w", err)
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

	// Deploy the FeeQuoter contract
	feeQuoterWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "fee_quoter.wasm")
	if _, statErr := os.Stat(feeQuoterWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("FeeQuoter WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", feeQuoterWasmPath)
	}

	c.logger.Info().Str("wasmPath", feeQuoterWasmPath).Msg("Deploying FeeQuoter contract...")
	feeQuoterSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "fee-quoter")
	feeQuoterContractID, err := c.deployer.DeployContract(ctx, feeQuoterWasmPath, feeQuoterSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy FeeQuoter contract: %w", err)
	}
	c.logger.Info().Str("contractID", feeQuoterContractID).Msg("FeeQuoter contract deployed")

	mockLinkToken := mustGenerateMockContractID(c.deployerKeypair.Address(), "link-token")
	feeQuoterClient := fqbindings.NewFeeQuoterClient(c.deployer, feeQuoterContractID)
	c.feeQuoterClient = feeQuoterClient
	err = feeQuoterClient.Initialize(ctx, c.deployerKeypair.Address(), fqbindings.StaticConfig{
		LinkToken:         mockLinkToken,
		MaxFeeJuelsPerMsg: 1_000_000_000_000_000_000, // 1e18
	}, []string{c.deployerKeypair.Address()})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize FeeQuoter: %w", err)
	}
	c.logger.Info().Str("feeQuoterContractID", feeQuoterContractID).Msg("FeeQuoter initialized")

	// Initialize the OnRamp with dependency contracts
	mockFeeAggregator := mustGenerateMockContractID(c.deployerKeypair.Address(), "fee-aggregator")
	mockTokenAdminRegistry := mustGenerateMockContractID(c.deployerKeypair.Address(), "token-admin-registry")

	err = c.onRampClient.Initialize(ctx, c.deployerKeypair.Address(), onrampbindings.StaticConfig{
		ChainSelector:         selector,
		TokenAdminRegistry:    mockTokenAdminRegistry,
		RmnProxy:              rmnProxyContractID,
		MaxUsdCentsPerMessage: 10000, // $100
	}, onrampbindings.DynamicConfig{
		FeeQuoter:     feeQuoterContractID,
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
	c.vvrContractID = vvrContractID

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

	c.cvContractID = cvContractID
	c.logger.Info().
		Str("cvContractID", cvContractID).
		Msg("Committee Verifier client initialized")

	allSelectors := selectorsFromEnvironment(env)
	remoteSelectors := filterRemoteSelectors(allSelectors, selector)
	outboundImplUpdates := []vvrbindings.OutboundImplementationUpdate{}
	for _, remoteSelector := range allSelectors {
		outboundImplUpdates = append(outboundImplUpdates, vvrbindings.OutboundImplementationUpdate{
			DestChainSelector: remoteSelector,
			Verifier:          &cvContractID,
		})
	}

	err = vvrClient.ApplyOutboundImplUpdates(ctx, outboundImplUpdates)
	if err != nil {
		return nil, fmt.Errorf("failed to apply outbound implementation updates: %w", err)
	}

	inboundImplUpdates := []vvrbindings.InboundImplementationUpdate{
		{
			Version:  [4]byte{0x49, 0xff, 0x34, 0xed}, // VERSION_TAG_V1_7_0
			Verifier: &cvContractID,
		},
		{
			// EVM CommitteeVerifier 2.0.0 attestation blobs use this prefix.
			// Register it against the Stellar CommitteeVerifier so inbound
			// EVM verifier results can be resolved during OffRamp execution.
			Version:  [4]byte{0xe9, 0xa0, 0x5a, 0x20},
			Verifier: &cvContractID,
		},
	}
	err = vvrClient.ApplyInboundImplUpdates(ctx, inboundImplUpdates)
	if err != nil {
		return nil, fmt.Errorf("failed to apply inbound implementation updates: %w", err)
	}

	c.logger.Info().Msg("Inbound implementation and outbound updates applied")

	remoteChainConfigs := make([]cvbindings.RemoteChainConfig, 0, len(allSelectors))
	for _, rs := range allSelectors {
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

	signatureQuorumConfigs := make([]cvbindings.SignatureQuorumConfig, 0, len(allSelectors))
	for _, rs := range allSelectors {
		signers, threshold := resolveSignersFromTopology(topology, rs, chainsel.FamilyStellar)
		if len(signers) == 0 {
			c.logger.Warn().Uint64("sourceChainSelector", rs).Msg("No signers found in topology, using placeholder")
			// TODO: should we keep this or fail the deployment?
			// This is a placeholder to avoid panic but will cause the Committee Verifier to fail verification.
			signers = [][32]byte{{1}}
			threshold = 1
		}
		signatureQuorumConfigs = append(signatureQuorumConfigs, cvbindings.SignatureQuorumConfig{
			SourceChainSelector: rs,
			Threshold:           threshold,
			Signers:             signers,
		})
	}
	err = cvClient.ApplySignatureConfigs(ctx, []uint64{}, signatureQuorumConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to apply signature quorum configs: %w", err)
	}
	c.logger.Info().Int("count", len(signatureQuorumConfigs)).Msg("Signature quorum configs applied")

	// Configure FeeQuoter destination chains
	fqDestChainConfigs := []fqbindings.DestChainConfigArgs{}
	for _, rs := range allSelectors {
		fqDestChainConfigs = append(fqDestChainConfigs, fqbindings.DestChainConfigArgs{
			DestChainSelector: rs,
			Config: fqbindings.DestChainConfig{
				IsEnabled:             true,
				MaxDataBytes:          50000,
				MaxPerMsgGasLimit:     4_000_000,
				DestGasOverhead:       350_000,
				DestGasPerPayloadByte: 16,
				DefaultTokenFeeUsd:    50,
				DefaultTokenDestGas:   50_000,
				DefaultTxGasLimit:     200_000,
				NetworkFeeUsdCents:    100,
				LinkPremiumPercent:    90,
			},
		})
	}
	err = feeQuoterClient.ApplyDestChainConfigs(ctx, fqDestChainConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to apply dest chain configs on FeeQuoter: %w", err)
	}
	c.logger.Info().Int("count", len(fqDestChainConfigs)).Msg("FeeQuoter dest chain configs applied")

	// Set token and gas prices on the FeeQuoter so get_message_fee works
	mockFeeToken := mustGenerateMockContractID(c.deployerKeypair.Address(), "fee-token")
	gasPriceUpdates := make([]fqbindings.GasPriceUpdate, 0, len(allSelectors))
	for _, rs := range allSelectors {
		gasPriceUpdates = append(gasPriceUpdates, fqbindings.GasPriceUpdate{
			DestChainSelector: rs,
			UsdPerUnitGas:     scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 100_000_000_000_000}), // 1e14
		})
	}
	err = feeQuoterClient.UpdatePrices(ctx, fqbindings.PriceUpdates{
		TokenPriceUpdates: []fqbindings.TokenPriceUpdate{
			{
				Token:       mockLinkToken,
				UsdPerToken: scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 15_000_000_000_000_000_000}), // $15
			},
			{
				Token:       mockFeeToken,
				UsdPerToken: scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 1_000_000_000_000_000_000}), // $1
			},
		},
		GasPriceUpdates: gasPriceUpdates,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update prices on FeeQuoter: %w", err)
	}
	c.logger.Info().Msg("FeeQuoter prices updated")

	// Deploy the OffRamp contract
	offRampWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "offramp.wasm")
	if _, statErr := os.Stat(offRampWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("OffRamp WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", offRampWasmPath)
	}

	c.logger.Info().Str("wasmPath", offRampWasmPath).Msg("Deploying OffRamp contract...")
	offRampSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "offramp")
	offRampContractID, err := c.deployer.DeployContract(ctx, offRampWasmPath, offRampSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy OffRamp contract: %w", err)
	}
	c.logger.Info().Str("contractID", offRampContractID).Msg("OffRamp contract deployed")

	c.offRampContractID = offRampContractID
	c.offRampClient = offrampbindings.NewOffRampClient(c.deployer, offRampContractID)

	err = c.offRampClient.Initialize(ctx, c.deployerKeypair.Address(), offrampbindings.StaticConfig{
		ChainSelector:      selector,
		RmnProxy:           rmnProxyContractID,
		TokenAdminRegistry: mockTokenAdminRegistry,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OffRamp: %w", err)
	}
	c.logger.Info().Str("offRampContractID", offRampContractID).Msg("OffRamp initialized")

	// Deploy the Router contract
	routerWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "router.wasm")
	if _, statErr := os.Stat(routerWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("Router WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", routerWasmPath)
	}

	c.logger.Info().Str("wasmPath", routerWasmPath).Msg("Deploying Router contract...")
	routerSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "router")
	routerContractID, err := c.deployer.DeployContract(ctx, routerWasmPath, routerSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy Router contract: %w", err)
	}
	c.logger.Info().Str("contractID", routerContractID).Msg("Router contract deployed")

	routerClient := routerbindings.NewRouterClient(c.deployer, routerContractID)
	err = routerClient.Initialize(ctx, c.deployerKeypair.Address(), rmnProxyContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Router: %w", err)
	}
	c.routerContractID = routerContractID
	c.routerClient = routerClient
	c.logger.Info().Str("routerContractID", routerContractID).Msg("Router initialized")

	// Configure OnRamp with provisional destination chain entries so that
	// GetExpectedNextMessageNumber and ccip_send don't revert with
	// DestinationChainNotSupported. Remote ramp addresses are patched with
	// their real values in PostConnect after all chains have been deployed.
	executorProxyHex := contractHexAddr("stellar-executor-proxy")
	executorContractID, err := scval.HexToContractStrkey(executorProxyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to convert executor proxy placeholder address: %w", err)
	}
	onRampDestConfigs, err := c.buildOnRampDestConfigs(nil, remoteSelectors, executorContractID, false)
	if err != nil {
		return nil, fmt.Errorf("build provisional onramp dest configs: %w", err)
	}
	err = c.onRampClient.ApplyDestChainConfigUpdates(ctx, onRampDestConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to apply dest chain config updates on OnRamp: %w", err)
	}
	c.logger.Info().Int("count", len(onRampDestConfigs)).Msg("OnRamp dest chain configs applied")

	// Configure OffRamp with supported source chains so inbound messages are
	// accepted rather than rejected as unknown sources.
	offRampSourceConfigs, err := c.buildOffRampSourceConfigs(nil, remoteSelectors, false)
	if err != nil {
		return nil, fmt.Errorf("build provisional offramp source configs: %w", err)
	}
	err = c.offRampClient.ApplySourceChainCfgUpdates(ctx, offRampSourceConfigs)
	if err != nil {
		return nil, fmt.Errorf("failed to apply source chain config updates on OffRamp: %w", err)
	}
	c.logger.Info().Int("count", len(offRampSourceConfigs)).Msg("OffRamp source chain configs applied")

	// Configure Router with OnRamp/OffRamp mappings so that ccip_send can
	// look up the correct OnRamp for each destination, and inbound messages
	// can be routed through the correct OffRamp for each source.
	onRampEntries := make([]routerbindings.OnRampEntry, 0, len(remoteSelectors))
	offRampEntries := make([]routerbindings.OffRampEntry, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		onRampEntries = append(onRampEntries, routerbindings.OnRampEntry{
			DestChainSelector: rs,
			Onramp:            onrampContractID,
		})
		offRampEntries = append(offRampEntries, routerbindings.OffRampEntry{
			SourceChainSelector: rs,
			Offramp:             offRampContractID,
		})
	}
	err = routerClient.ApplyRampUpdates(ctx, onRampEntries, []routerbindings.OffRampEntry{}, offRampEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to apply ramp updates on Router: %w", err)
	}
	c.logger.Info().
		Int("onRampEntries", len(onRampEntries)).
		Int("offRampEntries", len(offRampEntries)).
		Msg("Router ramp updates applied")

	// Deploy an example CCIP receiver contract. Stellar OffRamp requires
	// receivers to be deployed Wasm contracts (unlike EVM which accepts
	// plain EOA receivers).
	receiverWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccip_receiver_example.wasm")
	if _, statErr := os.Stat(receiverWasmPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("ccip_receiver_example WASM not found at %s. Run 'make build' from the chainlink-stellar root to compile contracts.", receiverWasmPath)
	}

	c.logger.Info().Str("wasmPath", receiverWasmPath).Msg("Deploying CCIP receiver example contract...")
	receiverSalt := stellardeployment.GenerateDeterministicSalt(c.deployerKeypair.Address(), "ccip-receiver-example")
	receiverContractID, err := c.deployer.DeployContract(ctx, receiverWasmPath, receiverSalt)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy ccip_receiver_example contract: %w", err)
	}

	recvClient := cciprecv.NewExampleCcipReceiverClient(c.deployer, receiverContractID)
	if err := recvClient.Initialize(ctx, routerContractID); err != nil {
		return nil, fmt.Errorf("failed to initialize ccip_receiver_example: %w", err)
	}

	c.receiverContractID = receiverContractID
	c.logger.Info().Str("receiverContractID", receiverContractID).Msg("CCIP receiver example deployed and initialized")

	// Add CCIP receiver to datastore so ImplFactory.New can reconstruct it.
	receiverHex, err := strkeyToHex(receiverContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert receiver address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       receiverHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(CcipReceiverContractType),
		Version:       semver.MustParse("1.0.0"),
	})

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

	// Add OffRamp — use the deployed OffRamp contract address
	offRampHex, err := strkeyToHex(offRampContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert OffRamp address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       offRampHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(offrampoperations.ContractType),
		Version:       semver.MustParse(offrampoperations.Deploy.Version()),
	})

	// Add Router
	routerHex, err := strkeyToHex(routerContractID)
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
	} {
		ds.AddressRefStore.Add(datastore.AddressRef{
			Address:       vvrHex,
			Type:          datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
			Version:       versioned_verifier_resolver.Version,
			Qualifier:     qualifier,
			ChainSelector: selector,
		})
	}

	cvHex, err := strkeyToHex(cvContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Committee Verifier address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       cvHex,
		Type:          datastore.ContractType(committee_verifier.ContractType),
		Version:       committee_verifier.Version,
		Qualifier:     devenvcommon.DefaultCommitteeVerifierQualifier,
		ChainSelector: selector,
	})

	// Add executor refs
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       contractHexAddr("stellar-executor"),
		Type:          datastore.ContractType(executor.ContractType),
		Version:       executor.Version,
		Qualifier:     devenvcommon.DefaultExecutorQualifier,
		ChainSelector: selector,
	})

	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       contractHexAddr("stellar-executor-proxy"),
		Type:          datastore.ContractType(proxy.ContractType),
		Version:       proxy.Version,
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

	// Add fee quoter refs — use the deployed FeeQuoter contract address
	feeQuoterHex, err := strkeyToHex(feeQuoterContractID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert FeeQuoter address: %w", err)
	}
	ds.AddressRefStore.Add(datastore.AddressRef{
		Address:       feeQuoterHex,
		Type:          datastore.ContractType(fee_quoter.ContractType),
		Version:       semver.MustParse(fee_quoter.Deploy.Version()),
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
//
// NOTE: Despite the name, on Stellar this returns a *contract* address, not an
// EOA. The Stellar OffRamp requires receivers to be deployed Wasm contracts
// (it checks executable() on the receiver). The interface should be renamed
// upstream in chainlink-ccv to something chain-agnostic like GetReceiverAddress.
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
// Gets the balance of a token for an address.
func (c *Chain) GetTokenBalance(ctx context.Context, address, tokenAddress protocol.UnknownAddress) (*big.Int, error) {
	return nil, fmt.Errorf("not implemented")
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

	executorContractID := mustGenerateMockContractID(c.deployerKeypair.Address(), "executor")

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

	mockFeeToken := mustGenerateMockContractID(c.deployerKeypair.Address(), "fee-token")
	sender := c.deployerKeypair.Address()

	routerMsg := routerbindings.StellarToAnyMessage{
		Receiver:     fields.Receiver,
		Data:         fields.Data,
		TokenAmounts: []routerbindings.TokenAmount{},
		FeeToken:     mockFeeToken,
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
	// TODO: implement RMN uncurse for Stellar
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

// findStellarRoot locates the chainlink-stellar project root by walking up from
// CWD looking for go.mod. This works whether the devenv CLI is run from the
// chainlink-stellar root directly or from a subdirectory.
func findStellarRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "target")); err == nil {
				return dir, nil
			}
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod in any parent of %s", dir)
		}
		dir = parent
	}
}

func selectorsFromEnvironment(env *deployment.Environment) []uint64 {
	selectors := make([]uint64, 0)
	for selector := range env.BlockChains.All() {
		selectors = append(selectors, selector)
	}
	sort.Slice(selectors, func(i, j int) bool {
		return selectors[i] < selectors[j]
	})
	return selectors
}

func filterRemoteSelectors(selectors []uint64, localSelector uint64) []uint64 {
	remote := make([]uint64, 0, len(selectors))
	seen := make(map[uint64]struct{}, len(selectors))
	for _, selector := range selectors {
		if selector == 0 || selector == localSelector {
			continue
		}
		if _, ok := seen[selector]; ok {
			continue
		}
		seen[selector] = struct{}{}
		remote = append(remote, selector)
	}
	sort.Slice(remote, func(i, j int) bool {
		return remote[i] < remote[j]
	})
	return remote
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
			TokenNetworkFeeUsdCents:   0,
			TokenReceiverAllowed:      false,
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

// resolveSignersFromTopology extracts signer addresses and threshold for a
// given source chain selector from the environment topology. All committee
// verifier DONs sign with ECDSA (secp256k1), so we always look up the EVM
// family signer address (20-byte Ethereum address) and left-pad it to 32
// bytes to match the on-chain Soroban storage format.
func resolveSignersFromTopology(topology *ccipOffchain.EnvironmentTopology, sourceChainSelector uint64, _ string) ([][32]byte, uint32) {
	if topology == nil || topology.NOPTopology == nil {
		return nil, 0
	}

	selectorStr := strconv.FormatUint(sourceChainSelector, 10)

	for _, committee := range topology.NOPTopology.Committees {
		chainCfg, ok := committee.ChainConfigs[selectorStr]
		if !ok {
			continue
		}

		var signers [][32]byte
		for _, alias := range chainCfg.NOPAliases {
			nop, found := topology.NOPTopology.GetNOP(alias)
			if !found {
				continue
			}
			// All CCV DONs sign with ECDSA regardless of destination family.
			addrHex := nop.SignerAddressByFamily[chainsel.FamilyEVM]
			if addrHex == "" {
				continue
			}
			decoded, decErr := hex.DecodeString(addrHex)
			if decErr != nil {
				continue
			}
			// Accept 20-byte Ethereum addresses; left-pad to 32 bytes.
			if len(decoded) != 20 {
				continue
			}
			var signer [32]byte
			copy(signer[32-len(decoded):], decoded)
			signers = append(signers, signer)
		}

		if len(signers) > 0 {
			sort.Slice(signers, func(i, j int) bool {
				return bytes.Compare(signers[i][:], signers[j][:]) < 0
			})
			return signers, uint32(chainCfg.Threshold)
		}
	}

	return nil, 0
}
