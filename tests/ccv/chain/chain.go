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

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	tokenscore "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	ccipChangesets "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/changesets"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	ccvdeployment "github.com/smartcontractkit/chainlink-ccv/deployment"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchainprod "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellarcommon "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	lrpoolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/simple_node_set"
)

// CcipReceiverContractType is the datastore contract type for the example CCIP
// receiver deployed on Stellar so that GetEOAReceiverAddress can return a valid
// Wasm contract address in tests.
const CcipReceiverContractType = stellarccip.CcipReceiverContractType

const TokenAdminRegistryContractType = stellarccip.TokenAdminRegistryContractType
const LockReleaseTokenPoolContractType = stellarccip.LockReleaseTokenPoolContractType

func ptrU32(v uint32) *uint32 { return &v }

func ptrU16(v uint16) *uint16 { return &v }

func ptrU8(v uint8) *uint8 { return &v }

// generateAccountAddress generates a Stellar account address (G...) from a seed.
// This uses the Stellar SDK's keypair package to create a proper strkey-encoded address.
// TODO: move to a test helper since it's not used outside of tests.
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
	_ cciptestinterfaces.TokenConfigProvider = &Chain{}
)

// Chain implements the CCIP17 and CCIP17Configuration interfaces for Stellar/Soroban.
type Chain struct {
	chainSelector          uint64
	logger                 zerolog.Logger
	rpcClient              *rpcclient.Client
	networkPassphrase      string
	deployerKeypair        *keypair.Full
	deployer               *stellardeployment.Deployer
	onRampClient           *onrampbindings.OnRampClient
	onRampContractID       string
	offRampClient          *offrampbindings.OffRampClient
	offRampContractID      string
	routerClient           *routerbindings.RouterClient
	routerContractID       string
	rampRegistryContractID string
	feeQuoterClient        *fqbindings.FeeQuoterClient
	vvrContractID          string
	cvContractID           string
	receiverContractID     string
	rmnProxyContractID     string
	rmnProxyClient         *rmnproxybindings.RmnProxyClient
	rmnRemoteContractID    string

	tokenAdminRegistryContractID string
	tokenAdminRegistryClient     *tarbindings.TokenAdminRegistryClient
	tokenPoolContractID          string
	tokenPoolClient              *tokenpoolbindings.TokenPoolClient
	legacyLockReleasePoolID      string
	tokenLockBoxContractID       string
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
		AddressBytesLength:                stellarccip.StellarAddressByteLen,
		BaseExecutionGasCost:              100_000,
		FeeQuoterDestChainConfigOverrides: &feeQuoterOverride,
		DefaultInboundCCVs: []datastore.AddressRef{
			stellarccip.VVRDatastoreRef().LaneAddressRef(selector),
		},
		DefaultOutboundCCVs: []datastore.AddressRef{
			stellarccip.VVRDatastoreRef().LaneAddressRef(selector),
		},
		DefaultExecutor: stellarccip.ExecutorProxyDatastoreRef(devenvcommon.DefaultExecutorQualifier).LaneAddressRef(selector),
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
// Returns the lane overrides for Stellar as a destination chain, mirroring the
// values used in GetConnectionProfile and stellarFeeQuoterDestChainConfigOverride.
func (c *Chain) GetChainLaneProfile(_ *deployment.Environment, selector uint64) (ccipChangesets.ChainOverrides, error) {
	// FeeQuoter family selector and DestGasOverhead are populated from the Stellar
	// lane adapter defaults; overrides here only set fields that differ from those.
	enabled := true
	executorQualifier := devenvcommon.DefaultExecutorQualifier
	return ccipChangesets.ChainOverrides{
		CommitteeVerifierGasForVerification: ptrU32(10_000),
		RemoteChainCfg: ccipChangesets.PartialRemoteChainConfig{
			BaseExecutionGasCost: ptrU32(100_000),
			FeeQuoterDestChainConfig: adapters.FeeQuoterDestChainConfigOverrides{
				IsEnabled:                   &enabled,
				MaxDataBytes:                ptrU32(30_000),
				MaxPerMsgGasLimit:           ptrU32(3_000_000),
				DestGasPerPayloadByteBase:   ptrU8(16),
				DefaultTokenFeeUSDCents:     ptrU16(25),
				DefaultTokenDestGasOverhead: ptrU32(90_000),
				DefaultTxGasLimit:           ptrU32(200_000),
				NetworkFeeUSDCents:          ptrU16(10),
				LinkFeeMultiplierPercent:    ptrU8(90),
				USDPerUnitGas:               big.NewInt(1e6),
			},
			ExecutorDestChainConfig: &adapters.ExecutorDestChainConfig{
				USDCentsFee: 0,
				Enabled:     true,
			},
			DefaultExecutorQualifier: &executorQualifier,
			DefaultInboundCCVs: []datastore.AddressRef{
				stellarccip.VVRDatastoreRef().LaneAddressRef(selector),
			},
			DefaultOutboundCCVs: []datastore.AddressRef{
				stellarccip.VVRDatastoreRef().LaneAddressRef(selector),
			},
		},
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

	defaultExecutor, err := stellarccip.ExecutorProxyDatastoreRef(devenvcommon.DefaultExecutorQualifier).LookupStrkey(
		env.DataStore,
		selector,
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

	if c.tokenPoolClient != nil && c.testTokenContractID != "" {
		chainUpdates, err := c.buildPoolChainUpdates(env.DataStore, remoteSelectors)
		if err != nil {
			return fmt.Errorf("build pool chain updates: %w", err)
		}
		if err := c.tokenPoolClient.ApplyChainUpdates(context.Background(), chainUpdates, nil); err != nil {
			return fmt.Errorf("apply pool chain updates in post-connect: %w", err)
		}

		if c.tokenLockBoxContractID != "" {
			if err := c.configureSiloedPoolLockBoxes(env.OperationsBundle, remoteSelectors); err != nil {
				return fmt.Errorf("configure siloed pool lock boxes: %w", err)
			}
		}

		if err := c.configureEVMToStellarTokenTransfers(env, selector, remoteSelectors); err != nil {
			return fmt.Errorf("configure EVM-to-Stellar token transfers: %w", err)
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
// Stellar has no CREATE2-style pre-bootstrap like EVM; this stage only applies topology fixes that must run before the shared DeployChainContracts changeset (adapter path).
func (c *Chain) PreDeployContractsForSelector(ctx context.Context, env *deployment.Environment, selector uint64, topology *ccvdeployment.EnvironmentTopology) (datastore.DataStore, error) {
	_ = ctx
	_ = env
	stellarsequences.ClearStellarDeployOffchainTopologyForSelector(selector)
	if topology != nil {
		offTopo, err := stellarccip.CCVEnvironmentTopologyToOffchain(topology)
		if err != nil {
			c.logger.Warn().Err(err).Uint64("selector", selector).Msg("stellar pre-deploy: topology stash skipped (CCV→offchain convert failed)")
		} else if offTopo != nil {
			stellarsequences.RegisterStellarDeployOffchainTopologyForSelector(selector, offTopo)
		}
	}
	ensureStellarFeeAggregatorsInTopology(c, topology)
	return nil, nil
}

// GetDeployChainContractsCfg implements cciptestinterfaces.OnChainConfigurable.
func (c *Chain) GetDeployChainContractsCfg(env *deployment.Environment, selector uint64, topology *ccvdeployment.EnvironmentTopology) (ccipChangesets.DeployChainContractsPerChainCfg, error) {
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
	deployerContract := stellarcommon.HexEncode(raw)
	return ccipChangesets.DeployChainContractsPerChainCfg{
		DeployerContract: &deployerContract,
		DeployerKeyOwned: true,
	}, nil
}

// PostDeployContractsForSelector implements cciptestinterfaces.OnChainConfigurable.
// Post-deploy parity with EVM: deploys legacy + siloed lock-release test pools (with token lock box),
// applies FeeQuoter pricing for the test token ([stellarccip.ApplyFeeQuoterTestTokenConfig]), and returns
// a datastore delta with siloed pool, legacy pool, lock box, and test token AddressRefs.
func (c *Chain) PostDeployContractsForSelector(ctx context.Context, env *deployment.Environment, selector uint64, topology *ccvdeployment.EnvironmentTopology) (datastore.DataStore, error) {
	_ = topology
	stellarsequences.ClearStellarDeployOffchainTopologyForSelector(selector)

	if env == nil {
		return nil, fmt.Errorf("environment is nil")
	}

	// DeployChainContracts runs on a CLDF-backed host; post-deploy uses *Chain.
	// Rehydrate Soroban clients from env.DataStore (same pattern as EVM post-deploy).
	if env.DataStore != nil {
		c.hydrateDevenvClientsFromDataStore(env.DataStore, selector)
	}

	host := &stellarCCIPDeployHost{c: c}
	if err := stellarccip.DeployLockReleaseTestTokenPool(ctx, env.OperationsBundle, host); err != nil {
		return nil, fmt.Errorf("deploy lock-release test token pool: %w", err)
	}

	allSelectors := selectorsFromBlockChains(env.BlockChains)
	if c.testTokenContractID != "" && c.feeQuoterClient != nil {
		if err := stellarccip.ApplyFeeQuoterTestTokenConfig(ctx, c.feeQuoterClient, c.deployerKeypair.Address(), c.testTokenContractID, allSelectors); err != nil {
			return nil, fmt.Errorf("apply fee quoter test token config: %w", err)
		}
		c.logger.Info().Int("destChainCount", len(allSelectors)).Msg("FeeQuoter test token fees applied (post-deploy)")
	}

	if c.tokenPoolContractID == "" {
		return nil, nil
	}
	ds, err := stellarccip.DevenvTokenPoolsAddressRefDataStore(
		selector,
		c.tokenPoolContractID,
		c.legacyLockReleasePoolID,
		c.tokenLockBoxContractID,
		c.testTokenContractID,
	)
	if err != nil {
		return nil, err
	}
	return ds, nil
}

// ---------------------------------------------------------------------------
// TokenConfigProvider implementation
// ---------------------------------------------------------------------------

// GetSupportedPools returns the pool types and versions the Stellar chain can
// deploy. Returning nil keeps Stellar out of the ComputeTokenCombinations matrix
// (Stellar pool pairing is handled in PostConnect, not via the shared
// TokenExpansion / ConfigureTokensForTransfers pipeline).
func (c *Chain) GetSupportedPools() []devenvcommon.PoolCapability {
	return nil
}

// GetTokenExpansionConfigs returns nil because Stellar deploys its own test
// token and lock-release pool in PostDeployContractsForSelector.
func (c *Chain) GetTokenExpansionConfigs(
	_ *deployment.Environment,
	_ uint64,
	_ []devenvcommon.TokenCombination,
) ([]tokenscore.TokenExpansionInputPerChain, error) {
	return nil, nil
}

// PostTokenDeploy is a no-op; Stellar handles post-deploy work in
// PostDeployContractsForSelector (FeeQuoter pricing, TAR registration).
func (c *Chain) PostTokenDeploy(
	_ *deployment.Environment,
	_ uint64,
	_ []datastore.AddressRef,
) error {
	return nil
}

// GetTokenTransferConfigs returns nil because Stellar-EVM cross-chain pool
// pairing is wired in PostConnect rather than through the shared
// ConfigureAllTokenTransfers pipeline. The shared pipeline requires strict
// symmetric grouping by pool identity, which does not align with the
// asymmetric LockRelease (Stellar) <-> BurnMint (EVM) pairing model.
func (c *Chain) GetTokenTransferConfigs(
	_ *deployment.Environment,
	_ uint64,
	_ []uint64,
	_ *ccvdeployment.EnvironmentTopology,
) ([]tokenscore.TokenTransferConfig, error) {
	return nil, nil
}

// DeployStellarCCIPContracts runs the sequences-based full Soroban CCIP deploy and returns output for the
// shared DeployChainContracts changeset merge path.
// opBundle is the CLDF bundle from the executing sequence (same bundle as nested ExecuteOperation).
// topology must be non-nil (with NOP data): Stellar full deploy configures committee verifier signature
// quorum from the converted offchain topology.
func (c *Chain) DeployStellarCCIPContracts(ctx context.Context, opBundle cldf_ops.Bundle, allSelectors []uint64, selector uint64, topology *ccvdeployment.EnvironmentTopology, existingAddresses []datastore.AddressRef) (seq_core.OnChainOutput, error) {
	return stellarsequences.RunStellarCCIPFullDeployForCCV(ctx, opBundle, c.StellarDepsForDeploy(), c.CCIPDevenvHost(), allSelectors, selector, topology, existingAddresses)
}

// StellarDepsForDeploy returns CLDF Stellar operation dependencies from this chain's deployer (same role as passing evm.Chain into EVM ops).
func (c *Chain) StellarDepsForDeploy() stellardeps.StellarDeps {
	return stellardeps.FromDeployer(c.deployer)
}

// DeployLocalNetwork implements cciptestinterfaces.CCIP17Configuration.
// Deploys a local Stellar network for testing.
func (c *Chain) DeployLocalNetwork(ctx context.Context, input *blockchain.Input) (*blockchain.Output, error) {
	c.logger.Info().Msg("Deploying Stellar local network")

	out, err := blockchain.NewBlockchainNetwork(input)
	if err != nil {
		return nil, fmt.Errorf("failed to create Stellar blockchain network: %w", err)
	}

	sorobanRPCURL := input.Out.Nodes[0].ExternalHTTPUrl
	c.networkPassphrase = input.Out.NetworkSpecificData.StellarNetwork.NetworkPassphrase
	c.friendbotURL = input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL

	// Initialize the Soroban RPC client
	c.rpcClient = rpcclient.NewClient(sorobanRPCURL, &http.Client{Timeout: 60 * time.Second})

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
		Str("sorobanRPCURL", sorobanRPCURL).
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
// Addresses that are not exactly 32 bytes (ed25519 public keys) are silently skipped.
func (c *Chain) FundAddresses(ctx context.Context, input *blockchain.Input, addresses []protocol.UnknownAddress, nativeAmount *big.Int) error {
	c.logger.Debug().Int("numAddresses", len(addresses)).Msg("Attempting to fund Stellar addresses")

	for _, addr := range addresses {
		if len(addr) != stellarccip.StellarAddressByteLen {
			c.logger.Debug().
				Int("addressLen", len(addr)).
				Int("expectedLen", stellarccip.StellarAddressByteLen).
				Msg("Skipping non-Stellar address in FundAddresses")
			continue
		}
		addrStr := strkey.MustEncode(strkey.VersionByteAccountID, addr)

		if err := c.fundViaFriendbot(c.friendbotURL, addrStr); err != nil {
			return fmt.Errorf("fund address %s: %w", addrStr, err)
		}

		c.logger.Info().
			Str("address", addrStr).
			Msg("Funded Stellar address via Friendbot")
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

// ConfirmSendOnSource implements cciptestinterfaces.Chain.
func (c *Chain) ConfirmSendOnSource(ctx context.Context, to uint64, key cciptestinterfaces.MessageEventKey, timeout time.Duration) (cciptestinterfaces.MessageSentEvent, error) {
	if key.MessageID == (protocol.Bytes32{}) && key.SeqNum == 0 {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("MessageEventKey must set MessageID or SeqNum")
	}
	if c.onRampClient == nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("OnRamp client not initialized")
	}

	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed to get latest ledger: %w", err)
	}

	var filter func(*onrampbindings.CCIPMessageSentEvent) bool
	if key.MessageID != (protocol.Bytes32{}) {
		want := [32]byte(key.MessageID)
		c.logger.Info().
			Uint64("destChainSelector", to).
			Str("messageID", hex.EncodeToString(want[:])).
			Uint32("startLedger", latestLedger.Sequence).
			Dur("timeout", timeout).
			Msg("Waiting for CCIPMessageSent event from Stellar OnRamp (by message ID)")
		filter = func(e *onrampbindings.CCIPMessageSentEvent) bool {
			return e.DestChainSelector == to && e.MessageId == want
		}
	} else {
		seq := key.SeqNum
		c.logger.Info().
			Uint64("destChainSelector", to).
			Uint64("sequenceNumber", seq).
			Uint32("startLedger", latestLedger.Sequence).
			Dur("timeout", timeout).
			Msg("Waiting for CCIPMessageSent event from Stellar OnRamp (by sequence)")
		filter = func(e *onrampbindings.CCIPMessageSentEvent) bool {
			return e.DestChainSelector == to && e.SequenceNumber == seq
		}
	}

	event, err := c.onRampClient.WaitForCCIPMessageSentEvent(
		ctx, latestLedger.Sequence, timeout,
		filter,
	)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, fmt.Errorf("failed waiting for sent event: %w", err)
	}

	return cciptestinterfaces.MessageSentEvent{
		MessageID: event.MessageId,
		Sender:    protocol.UnknownAddress([]byte(event.Sender)),
	}, nil
}

// ConfirmExecOnDest implements cciptestinterfaces.Chain.
// TxID is left empty: the OffRamp event waiter does not expose the enclosing transaction hash.
func (c *Chain) ConfirmExecOnDest(ctx context.Context, from uint64, key cciptestinterfaces.MessageEventKey, timeout time.Duration) (cciptestinterfaces.ExecEnvelope, error) {
	if key.MessageID == (protocol.Bytes32{}) && key.SeqNum == 0 {
		return cciptestinterfaces.ExecEnvelope{}, fmt.Errorf("MessageEventKey must set MessageID or SeqNum")
	}
	if c.offRampClient == nil {
		return cciptestinterfaces.ExecEnvelope{}, fmt.Errorf("OffRamp client not initialized")
	}

	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.ExecEnvelope{}, fmt.Errorf("failed to get latest ledger: %w", err)
	}

	var filter func(*offrampbindings.ExecutionStateChangedEvent) bool
	if key.MessageID != (protocol.Bytes32{}) {
		want := [32]byte(key.MessageID)
		c.logger.Info().
			Uint64("sourceChainSelector", from).
			Str("messageID", hex.EncodeToString(want[:])).
			Uint32("startLedger", latestLedger.Sequence).
			Dur("timeout", timeout).
			Msg("Waiting for ExecutionStateChanged event from Stellar OffRamp (by message ID)")
		filter = func(e *offrampbindings.ExecutionStateChangedEvent) bool {
			return e.SourceChainSelector == from && e.MessageId == want
		}
	} else {
		seq := key.SeqNum
		c.logger.Info().
			Uint64("sourceChainSelector", from).
			Uint64("sequenceNumber", seq).
			Uint32("startLedger", latestLedger.Sequence).
			Dur("timeout", timeout).
			Msg("Waiting for ExecutionStateChanged event from Stellar OffRamp (by sequence)")
		filter = func(e *offrampbindings.ExecutionStateChangedEvent) bool {
			return e.SourceChainSelector == from && e.SequenceNumber == seq
		}
	}

	event, err := c.offRampClient.WaitForExecutionStateChangedEvent(
		ctx, latestLedger.Sequence, timeout,
		filter,
	)
	if err != nil {
		return cciptestinterfaces.ExecEnvelope{}, fmt.Errorf("failed waiting for execution event: %w", err)
	}

	return cciptestinterfaces.ExecEnvelope{
		Event: cciptestinterfaces.ExecutionStateChangedEvent{
			SourceChainSelector: protocol.ChainSelector(event.SourceChainSelector),
			MessageID:           event.MessageId,
			MessageNumber:       event.SequenceNumber,
			State:               cciptestinterfaces.MessageExecutionState(event.State),
			ReturnData:          event.ReturnData,
		},
	}, nil
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
// Gets the balance of a token for a raw Stellar account address by calling the
// SAC balance function. Use GetTokenBalanceForAddress for contract holders.
func (c *Chain) GetTokenBalance(ctx context.Context, address, tokenAddress protocol.UnknownAddress) (*big.Int, error) {
	addrStrkey, err := strkey.Encode(strkey.VersionByteAccountID, []byte(address))
	if err != nil {
		return nil, fmt.Errorf("encode account holder address: %w", err)
	}
	return c.GetTokenBalanceForAddress(ctx, addrStrkey, tokenAddress)
}

// GetTokenBalanceForAddress gets a token balance for a typed Stellar holder
// address. Use this for contract holders, because raw 32-byte Stellar addresses
// do not carry the Soroban account-vs-contract address tag.
func (c *Chain) GetTokenBalanceForAddress(ctx context.Context, holderAddress string, tokenAddress protocol.UnknownAddress) (*big.Int, error) {
	if c.deployer == nil {
		return nil, fmt.Errorf("deployer not initialized")
	}

	tokenStrkey, err := strkey.Encode(strkey.VersionByteContract, []byte(tokenAddress))
	if err != nil {
		return nil, fmt.Errorf("encode token address: %w", err)
	}

	holderAddressScVal := scval.AddressToScVal(holderAddress)
	if holderAddressScVal.Address == nil {
		return nil, fmt.Errorf("invalid holder address: %s", holderAddress)
	}

	balanceArgs := []xdr.ScVal{holderAddressScVal}
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

// SendMessage implements cciptestinterfaces.Chain.
//
// DEPRECATED upstream in chainlink-ccv changelog/2026-04-27_extra_args_data_provider.md.
// New callers should use BuildChainMessage + SendChainMessage directly.
//
// Stellar-as-source builds its own Soroban GenericExtraArgsV3 XDR blob inside
// BuildChainMessage and ignores the provider-shaped extra args supplied here,
// so we accept any ExtraArgsDataProvider and any messageVersion without
// type-asserting.
func (c *Chain) SendMessage(ctx context.Context, dest uint64, fields cciptestinterfaces.MessageFields, _ cciptestinterfaces.ExtraArgsDataProvider, _ uint8) (cciptestinterfaces.MessageSentEvent, error) {
	c.logger.Info().
		Uint64("destChainSelector", dest).
		Str("receiver", hex.EncodeToString(fields.Receiver)).
		Msg("Sending CCIP message from Stellar via Router")

	msg, err := c.BuildChainMessage(ctx, fields, nil)
	if err != nil {
		return cciptestinterfaces.MessageSentEvent{}, err
	}

	event, _, err := c.SendChainMessage(ctx, dest, msg, nil)
	return event, err
}

// testTokenAssetCode is the classic Stellar asset code used by the test SAC token.
const testTokenAssetCode = "TEST"

// createTestToken sets up a SAC-wrapped classic Stellar asset for E2E token
// transfer tests. It creates an issuer, funds it, establishes trustlines, mints
// an initial supply, and deploys the SAC wrapper.
func (c *Chain) createTestToken(ctx context.Context, friendbotURL string) (string, error) {
	contractID, issuerKP, err := stellarccip.DeployDevenvSACToken(stellarccip.DevenvSACTokenParams{
		Ctx:               ctx,
		RPCClient:         c.rpcClient,
		MainDeployer:      c.deployer,
		OwnerKeypair:      c.deployerKeypair,
		NetworkPassphrase: c.networkPassphrase,
		FriendbotURL:      friendbotURL,
		IssuerSeedLabel:   fmt.Sprintf("test-token-issuer-%s", c.networkPassphrase),
		AssetCode:         testTokenAssetCode,
		Logger:            &c.logger,
	})
	if err != nil {
		return "", err
	}
	c.testTokenIssuerKeypair = issuerKP
	return contractID, nil
}

const feeTokenAssetCode = "FEE"

// createFeeToken sets up a SAC-wrapped classic Stellar asset used for CCIP fee
// payments. Similar to createTestToken but with a separate issuer and asset code
// so the fee token is independent of the transfer-test token.
func (c *Chain) createFeeToken(ctx context.Context, friendbotURL string) (string, error) {
	contractID, issuerKP, err := stellarccip.DeployDevenvSACToken(stellarccip.DevenvSACTokenParams{
		Ctx:               ctx,
		RPCClient:         c.rpcClient,
		MainDeployer:      c.deployer,
		OwnerKeypair:      c.deployerKeypair,
		NetworkPassphrase: c.networkPassphrase,
		FriendbotURL:      friendbotURL,
		IssuerSeedLabel:   fmt.Sprintf("fee-token-issuer-%s", c.networkPassphrase),
		AssetCode:         feeTokenAssetCode,
		Logger:            &c.logger,
	})
	if err != nil {
		return "", err
	}
	c.feeTokenIssuerKeypair = issuerKP
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

// GetReceiverContractAddress returns the CCIP receiver contract ID deployed
// during DeployContractsForSelector.
func (c *Chain) GetReceiverContractAddress() (string, error) {
	if c.receiverContractID == "" {
		return "", fmt.Errorf("ccip_receiver contract not deployed; run DeployContractsForSelector first")
	}
	return c.receiverContractID, nil
}

// FeeQuoterClient returns the FeeQuoter contract client, or nil if deploy has not
// initialized it yet.
func (c *Chain) FeeQuoterClient() *fqbindings.FeeQuoterClient {
	return c.feeQuoterClient
}

// stellarFeeAggregatorHexForTopology returns the 0x-prefixed hex encoding of the deployer account
// bytes used as the on-chain fee aggregator in Stellar devenv (same address as [RunStellarCCIPFullDeploy]
// passes into OnRamp/VVR/CommitteeVerifier). Topology FeeAggregator must match that value so downstream
// tooling and changesets stay aligned.
func stellarFeeAggregatorHexForTopology(c *Chain) (string, error) {
	if c.deployerKeypair == nil {
		return "", fmt.Errorf("deployer keypair not set")
	}
	raw, err := strkey.Decode(strkey.VersionByteAccountID, c.deployerKeypair.Address())
	if err != nil {
		return "", fmt.Errorf("decode deployer account for fee aggregator hex: %w", err)
	}
	return stellarcommon.HexEncode(raw), nil
}

func ensureStellarFeeAggregatorsInTopology(c *Chain, topology *ccvdeployment.EnvironmentTopology) {
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

func (c *Chain) ensureLocalContracts(ds datastore.DataStore, selector uint64) error {
	if c.deployer == nil {
		return fmt.Errorf("deployer not initialized")
	}

	var err error
	if c.onRampContractID == "" {
		c.onRampContractID, err = stellarccip.OnRampDatastoreRef().LookupStrkey(ds, selector)
		if err != nil {
			return fmt.Errorf("lookup local onramp: %w", err)
		}
	}
	if c.offRampContractID == "" {
		c.offRampContractID, err = stellarccip.OffRampDatastoreRef().LookupStrkey(ds, selector)
		if err != nil {
			return fmt.Errorf("lookup local offramp: %w", err)
		}
	}
	if c.routerContractID == "" {
		c.routerContractID, err = stellarccip.RouterDatastoreRef().LookupStrkey(ds, selector)
		if err != nil {
			return fmt.Errorf("lookup local router: %w", err)
		}
	}
	if c.vvrContractID == "" {
		c.vvrContractID, err = stellarccip.VVRDatastoreRef().LookupStrkey(ds, selector)
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

// hydrateDevenvClientsFromDataStore fills TokenAdminRegistry, FeeQuoter, Router, and RampRegistry
// clients/IDs from the deployment datastore when they are missing on Chain. After the shared
// DeployChainContracts changeset, contract state lives in env.DataStore while the CLDF deploy host
// held the in-memory clients; this aligns Stellar with EVM post-deploy reading env.DataStore.
func (c *Chain) hydrateDevenvClientsFromDataStore(ds datastore.DataStore, selector uint64) {
	if c == nil || ds == nil || c.deployer == nil {
		return
	}
	if c.tokenAdminRegistryClient == nil {
		if id, err := stellarccip.GetTokenAdminRegistryStrkey(ds, selector); err == nil && id != "" {
			c.tokenAdminRegistryContractID = id
			c.tokenAdminRegistryClient = tarbindings.NewTokenAdminRegistryClient(c.deployer, id)
		}
	}
	if c.feeQuoterClient == nil {
		if id, err := stellarccip.GetFeeQuoterStrkey(ds, selector); err == nil && id != "" {
			c.feeQuoterClient = fqbindings.NewFeeQuoterClient(c.deployer, id)
		}
	}
	if c.routerContractID == "" {
		if id, err := stellarccip.GetRouterStrkey(ds, selector); err == nil && id != "" {
			c.routerContractID = id
			c.routerClient = routerbindings.NewRouterClient(c.deployer, id)
		}
	} else if c.routerClient == nil && c.routerContractID != "" {
		c.routerClient = routerbindings.NewRouterClient(c.deployer, c.routerContractID)
	}
	if c.rampRegistryContractID == "" {
		if id, err := stellarccip.GetRampRegistryStrkey(ds, selector); err == nil && id != "" {
			c.rampRegistryContractID = id
		}
	}
}

// remotePoolContractTypes lists pool contract types to probe when looking up
// the counterpart pool on a remote chain. Ordered by preference (burn-mint is
// the natural counterpart for a lock-release pool).
var remotePoolContractTypes = []string{
	"BurnMintTokenPool",
	"LockReleaseTokenPool",
}

// resolveRemotePoolAndToken finds the counterpart pool and token on the remote
// chain. EVM remotes use the deterministic EVM-to-Stellar token pair resolver;
// other remotes fall back to the first matching pool and token with the same
// qualifier. Falls back to zero bytes if nothing is found.
func resolveRemotePoolAndToken(ds datastore.DataStore, remoteSelector uint64) (poolBytes, tokenBytes []byte, err error) {
	addrLen, err := stellarccip.AddressBytesLength(remoteSelector)
	if err != nil {
		return nil, nil, err
	}
	zeroPad := func() []byte { return make([]byte, addrLen) }

	allRefs, err := ds.Addresses().Fetch()
	if err != nil {
		return zeroPad(), zeroPad(), nil
	}

	family, err := chainsel.GetSelectorFamily(remoteSelector)
	if err == nil && family == chainsel.FamilyEVM {
		if poolRef, tokenRef, found := ccvchainprod.ResolveEVMTokenPoolForStellar(allRefs, remoteSelector); found {
			poolBytes, err = stellarccip.AddressBytesHex(poolRef, remoteSelector)
			if err != nil {
				return zeroPad(), zeroPad(), nil
			}
			tokenBytes, err = stellarccip.AddressBytesHex(tokenRef, remoteSelector)
			if err != nil {
				return zeroPad(), zeroPad(), nil
			}
			return poolBytes, tokenBytes, nil
		}
	}

	// Index refs by (type, qualifier) for the remote chain.
	type refKey struct {
		ct        string
		qualifier string
	}
	byKey := make(map[refKey]datastore.AddressRef)
	for _, ref := range allRefs {
		if ref.ChainSelector != remoteSelector {
			continue
		}
		byKey[refKey{string(ref.Type), ref.Qualifier}] = ref
	}

	// Find the first pool match.
	var poolRef datastore.AddressRef
	poolFound := false
	for _, ct := range remotePoolContractTypes {
		for key, ref := range byKey {
			if key.ct == ct {
				poolRef = ref
				poolFound = true
				break
			}
		}
		if poolFound {
			break
		}
	}
	if !poolFound {
		return zeroPad(), zeroPad(), nil
	}

	poolBytes, err = stellarccip.AddressBytesHex(poolRef, remoteSelector)
	if err != nil {
		return zeroPad(), zeroPad(), nil
	}

	// Find a token with the same qualifier as the pool.
	tokenBytes = zeroPad()
	for _, ct := range ccvchainprod.RemoteTokenContractTypes {
		if ref, ok := byKey[refKey{ct, poolRef.Qualifier}]; ok {
			if tb, err := stellarccip.AddressBytesHex(ref, remoteSelector); err == nil {
				tokenBytes = tb
				break
			}
		}
	}

	return poolBytes, tokenBytes, nil
}

func (c *Chain) configureSiloedPoolLockBoxes(bundle cldf_ops.Bundle, remoteSelectors []uint64) error {
	if c.deployer == nil || c.tokenPoolContractID == "" || c.tokenLockBoxContractID == "" {
		return fmt.Errorf("siloed pool lock box wiring incomplete")
	}
	configs := make([]slrbindings.LockBoxEntry, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		configs = append(configs, slrbindings.LockBoxEntry{
			RemoteChainSelector: rs,
			LockBox:             c.tokenLockBoxContractID,
		})
	}
	deps := stellardeps.FromDeployer(c.deployer)
	if _, err := cldf_ops.ExecuteOperation(bundle, lrpoolops.ConfigureLockBoxes, deps, lrpoolops.ConfigureLockBoxesInput{
		ContractID: c.tokenPoolContractID,
		Configs:    configs,
	}); err != nil {
		return err
	}
	c.logger.Info().
		Str("pool", c.tokenPoolContractID).
		Str("lockBox", c.tokenLockBoxContractID).
		Int("remoteChains", len(configs)).
		Msg("Configured siloed pool lock boxes for remote chains")
	return nil
}

func (c *Chain) buildPoolChainUpdates(ds datastore.DataStore, remoteSelectors []uint64) ([]tokenpoolbindings.ChainUpdate, error) {
	var updates []tokenpoolbindings.ChainUpdate
	for _, rs := range remoteSelectors {
		remotePoolBytes, remoteTokenBytes, err := resolveRemotePoolAndToken(ds, rs)
		if err != nil {
			return nil, fmt.Errorf("resolve remote pool/token for %d: %w", rs, err)
		}
		updates = append(updates, tokenpoolbindings.ChainUpdate{
			RemoteChainSelector:       rs,
			RemotePoolAddresses:       remotePoolBytes,
			RemoteTokenAddress:        remoteTokenBytes,
			OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
			InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
		})
	}
	return updates, nil
}

func (c *Chain) buildOnRampDestConfigs(ds datastore.DataStore, remoteSelectors []uint64, defaultExecutor string, useRemoteOffRamp bool) ([]onrampbindings.DestChainConfigArgs, error) {
	return stellarccip.BuildOnRampDestConfigs(ds, remoteSelectors, defaultExecutor, useRemoteOffRamp, c.vvrContractID, c.routerContractID)
}

func (c *Chain) buildOffRampSourceConfigs(ds datastore.DataStore, remoteSelectors []uint64, useRemoteOnRamp bool) ([]offrampbindings.SourceChainConfigArgs, error) {
	return stellarccip.BuildOffRampSourceConfigs(ds, remoteSelectors, useRemoteOnRamp, c.vvrContractID, c.routerContractID)
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
