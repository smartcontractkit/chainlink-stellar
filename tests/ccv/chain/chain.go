package ccvchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
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
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	slrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/siloed_lock_release_pool"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	ccvchainprod "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellarcommon "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	contracttransmitter "github.com/smartcontractkit/chainlink-stellar/ccv/contract_transmitter"
	destinationreader "github.com/smartcontractkit/chainlink-stellar/ccv/destination_reader"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	bnmops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
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
func stellarFeeQuoterDestChainConfigOverride(_ uint64) lanes.FeeQuoterDestChainConfigOverride {
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
	//
	// CommitteeVerifierGasForVerification is deliberately NOT overridden: the lane
	// changeset coalesces unset fields with the REMOTE chain family adapter's
	// defaults (configure_chains_for_lanes_from_topology.go
	// resolveRemoteChainConfig / mergeCommitteeVerifierRemoteChainConfig), which
	// describe what executing on that remote actually costs. Overriding it DOWN
	// to stellar-scale numbers under-budgets the on-wire execution_gas_limit of
	// messages this chain sends, and the EVM Router's CallWithExactGas reverts
	// NOT_ENOUGH_GAS_FOR_CALL (0x37c3be29). Unset = the EVM family default
	// (per-CCV 75_000), matching green EVM↔EVM lanes for any remote family.
	//
	// BaseExecutionGasCost IS overridden UP from the EVM family default
	// (200_000): the default leaves no room for calldata intrinsic + OffRamp
	// pre-call processing at large payloads. The ccv executor sizes the dest
	// execute tx as execution_gas_limit + EIP-150 buffer only
	// (txm_evm_contract_transmitter.go — intrinsic is NOT added), while the
	// Router's CallWithExactGas demands gasleft ≥ ccipReceiveGasLimit +
	// gasForCallExactCheck at the receiver call. At the 12,252-byte max payload
	// the ~13KB execute calldata costs ~227K intrinsic (payload is nonzero
	// bytes, 16 gas each) and verification/state-writes/decode cost ~90-150K
	// more, so the check falls ~40-100K short at base 200K + CV 75K (same
	// 0x37c3be29, CI run 38774977247 max_data_size). Raising the user gasLimit
	// cannot fix it — it scales both sides of the check ~1:1 because the
	// buffer is proportional to it — only the fixed terms move the inequality.
	// 400_000 covers intrinsic + processing at the max payload with ~2x margin
	// on the processing estimate; wire total at max ≈ 400K+75K+733K = 1.21M,
	// well under MaxPerMsgGasLimit. Devenv fees are fake, so the over-declare
	// costs nothing.
	//
	// NOTE: this profile is only ONE of the writers that feed the on-wire gas
	// fields. The changeset's OnRamp dest-config write is what this profile
	// shapes (see PostConnect for why nothing may rewrite it afterwards), and
	// the CV remote config's gas_for_verification (the per-CCV term) is written
	// by the full deploy, not the lane changeset — both must agree with the
	// remote family's scale or the same under-budget failure returns.
	enabled := true
	executorQualifier := devenvcommon.DefaultExecutorQualifier
	return ccipChangesets.ChainOverrides{
		RemoteChainCfg: ccipChangesets.PartialRemoteChainConfig{
			BaseExecutionGasCost: ptrU32(400_000),
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

	// No OnRamp DestChainConfig write happens here, deliberately. PostConnect's
	// only caller (devenv deploy.go ConnectAllChainsCanonical) runs it directly
	// AFTER the ConfigureChainsForLanesFromTopology changeset, which has already
	// written the OnRamp dest configs with per-remote-family values (remote
	// adapter defaults coalesced with the lane-profile overrides). Rewriting
	// them here with flat stellar-scale values silently clobbered
	// base_execution_gas_cost back to 100_000, under-budgeting the on-wire
	// execution_gas_limit of messages this chain sends: on an EVM destination
	// the Router's CallWithExactGas then reverts NOT_ENOUGH_GAS_FOR_CALL
	// (0x37c3be29) because the receiver's ccipReceiveGasLimit cannot be
	// delivered. EVM parity: the EVM impl's PostConnect does not touch OnRamp
	// dest configs either — the changeset is the single writer.
	offRampSourceConfigs, err := c.buildOffRampSourceConfigs(env.DataStore, remoteSelectors)
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

	// Shared-pipeline BnM pairs: wire each stellar burn-mint pool's remote
	// updates (the EVM side of every pair is wired by the shared
	// ConfigureTokensForTransfers pipeline, which runs before PostConnect).
	if err := c.configureSharedBnmPools(env, selector, remoteSelectors); err != nil {
		return fmt.Errorf("configure shared BnM pools: %w", err)
	}

	// Separate-token-receiver sends: both lane OnRamps default
	// token_receiver_allowed to false; flip it for the token-transfer suite.
	if err := c.enableTokenReceivers(env, selector, remoteSelectors); err != nil {
		return fmt.Errorf("enable token receivers on lane onramps: %w", err)
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
// Post-deploy parity with EVM: deploys the legacy + siloed lock-release test pools (with token lock box),
// applies FeeQuoter pricing for the SAC test token ([stellarccip.ApplyFeeQuoterTokenConfig]), and returns
// a datastore delta with the siloed pool, legacy pool, lock box, and test token AddressRefs. The shared
// TokenExpansion pipeline deploys the BnM pairs (see GetTokenExpansionConfigs).
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
		if err := stellarccip.ApplyFeeQuoterTokenConfig(ctx, c.feeQuoterClient, c.deployerKeypair.Address(), c.testTokenContractID, allSelectors); err != nil {
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
// deploy through the shared TokenExpansion / ConfigureTokensForTransfers
// pipeline (one pair per combo; stellar-side pool remote updates are applied
// in PostConnect): burn-mint pools and plain lock-release pools (EVM LR
// capability parity; the pipeline deploys each with its own lock box and a
// qualifier-keyed BnM token). The bespoke siloed lane pair stays in
// PostConnect for the legacy hand-written e2e tests.
func (c *Chain) GetSupportedPools() []devenvcommon.PoolCapability {
	return []devenvcommon.PoolCapability{
		{PoolType: stellarccip.DevenvCatalogBurnMintTokenPoolType, PoolVersion: stellarops.ContractDeploymentVersion},
		{PoolType: stellarccip.DevenvCatalogLockReleaseTokenPoolType, PoolVersion: stellarops.ContractDeploymentVersion},
	}
}

// bnmTokenDecimals is the decimals every shared-pipeline BnM token uses (SAC
// convention); bnmPreMintTokens is the deployer's pre-minted allocation in
// whole tokens (EVM parity: 1 billion).
const (
	bnmTokenDecimals        = 7
	bnmPreMintTokens uint64 = 1_000_000_000
)

// GetTokenExpansionConfigs mirrors the EVM implementation: deploy the LOCAL
// leg of each combo this chain supports (burn-mint and lock-release pairs,
// dedup by full ref) through the shared TokenExpansion pipeline — one BnM
// token + one pool per combo (a lock-release combo adds its lock box),
// deployed and onboarded by the StellarTokenAdapter sequences, with the
// deployer pre-funded through the token's mint_as pre-mint.
func (c *Chain) GetTokenExpansionConfigs(
	env *deployment.Environment,
	selector uint64,
	combos []devenvcommon.TokenCombination,
) ([]tokenscore.TokenExpansionInputPerChain, error) {
	if _, ok := env.BlockChains.StellarChains()[selector]; !ok {
		return nil, fmt.Errorf("stellar chain not found for selector %d", selector)
	}
	if c.deployerKeypair == nil {
		return nil, fmt.Errorf("deployer keypair not initialized")
	}

	supported := devenvcommon.BuildSupportedPoolsMap(c.GetSupportedPools())
	preMint := bnmPreMintTokens
	seen := make(map[string]bool)
	var configs []tokenscore.TokenExpansionInputPerChain
	for _, combo := range combos {
		// Only deploy the local leg; the remote pool belongs to the
		// counterpart chain (skip combos where another chain is local).
		poolRef := combo.LocalPoolAddressRef()
		if !devenvcommon.IsPoolSupported(supported, poolRef) {
			continue
		}
		key := devenvcommon.AddressRefFullKey(poolRef)
		if seen[key] {
			continue
		}
		seen[key] = true
		configs = append(configs, tokenscore.TokenExpansionInputPerChain{
			TokenPoolVersion:      poolRef.Version,
			SkipOwnershipTransfer: true,
			DeployTokenInput: &tokenscore.DeployTokenInput{
				Symbol:        poolRef.Qualifier,
				Name:          poolRef.Qualifier,
				Decimals:      bnmTokenDecimals,
				Type:          deployment.ContractType(bnmops.ContractType),
				ExternalAdmin: c.deployerKeypair.Address(),
				CCIPAdmin:     c.deployerKeypair.Address(),
				PreMint:       &preMint,
			},
			DeployTokenPoolInput: &tokenscore.DeployTokenPoolInput{
				PoolType:           string(poolRef.Type),
				TokenPoolQualifier: poolRef.Qualifier,
			},
		})
	}
	return configs, nil
}

// PostTokenDeploy applies FeeQuoter pricing for every deployed shared BnM
// pair token (mirroring the SAC test token's treatment: the OnRamp threads
// the token fee's dest gas overhead into the on-wire execution gas limit, so
// under-configuring it risks NOT_ENOUGH_GAS_FOR_CALL on EVM destinations) and
// seeds the lock box of every deployed shared lock-release pool (the box
// holds all liquidity, never the pool).
func (c *Chain) PostTokenDeploy(
	env *deployment.Environment,
	selector uint64,
	deployedRefs []datastore.AddressRef,
) error {
	if len(deployedRefs) == 0 {
		return nil
	}
	if c.feeQuoterClient == nil {
		return fmt.Errorf("FeeQuoter client not initialized")
	}
	if c.deployerKeypair == nil {
		return fmt.Errorf("deployer keypair not initialized")
	}
	allSelectors := selectorsFromBlockChains(env.BlockChains)
	ctx := context.Background()
	for _, ref := range deployedRefs {
		tokenRef, err := stellarccip.LookupAddressRef(env.DataStore, selector,
			datastore.ContractType(bnmops.ContractType), stellarops.ContractDeploymentVersion, ref.Qualifier)
		if err != nil {
			return fmt.Errorf("lookup BnM token for pool %q: %w", ref.Qualifier, err)
		}
		tokenStrkey, err := scval.HexToContractStrkey(tokenRef.Address)
		if err != nil {
			return fmt.Errorf("convert BnM token address: %w", err)
		}
		if err := stellarccip.ApplyFeeQuoterTokenConfig(ctx, c.feeQuoterClient, c.deployerKeypair.Address(), tokenStrkey, allSelectors); err != nil {
			return fmt.Errorf("apply fee quoter BnM token config for %q: %w", ref.Qualifier, err)
		}
	}
	c.logger.Info().Int("poolCount", len(deployedRefs)).Msg("FeeQuoter BnM token fees applied (post-token-deploy)")

	if err := c.fundLockReleasePoolLockBoxes(env, selector, deployedRefs); err != nil {
		return fmt.Errorf("fund lock-release pool lock boxes: %w", err)
	}
	return nil
}

// GetTokenTransferConfigs mirrors the EVM implementation: the hand-curated
// catalog filtered by the datastore, emitting configs only for remote
// selectors that hold the combo's remote pool ref, merged per local pool. The
// shared ConfigureAllTokenTransfers pipeline applies them; the stellar-side
// pool remote updates are applied in PostConnect (the adapter's configure
// sequence is a documented no-op).
func (c *Chain) GetTokenTransferConfigs(
	env *deployment.Environment,
	selector uint64,
	remoteSelectors []uint64,
	topology *ccvdeployment.EnvironmentTopology,
) ([]tokenscore.TokenTransferConfig, error) {
	applicableCombos := devenvcommon.FilterTokenCombinations(
		devenvcommon.AllTokenCombinations(), topology, env.DataStore, append([]uint64{selector}, remoteSelectors...))
	hasAddressRef := func(chainSelector uint64, ref datastore.AddressRef) bool {
		_, err := env.DataStore.Addresses().Get(datastore.NewAddressRefKey(
			chainSelector, ref.Type, ref.Version, ref.Qualifier))
		return err == nil
	}
	merged := make(map[string]tokenscore.TokenTransferConfig)
	for _, combo := range applicableCombos {
		eligibleRemoteSelectors := make([]uint64, 0, len(remoteSelectors))
		for _, rs := range remoteSelectors {
			if hasAddressRef(rs, combo.RemotePoolAddressRef()) {
				eligibleRemoteSelectors = append(eligibleRemoteSelectors, rs)
			}
		}
		if len(eligibleRemoteSelectors) == 0 {
			continue
		}
		cfg, err := c.buildStellarTokenTransferConfig(selector, eligibleRemoteSelectors,
			combo.LocalPoolAddressRef(), combo.RemotePoolAddressRef(), combo.LocalPoolCCVQualifiers())
		if err != nil {
			return nil, err
		}
		key := string(cfg.TokenPoolRef.Type) + "\x00" + cfg.TokenPoolRef.Version.String() + "\x00" + cfg.TokenPoolRef.Qualifier
		if existing, ok := merged[key]; ok {
			maps.Copy(existing.RemoteChains, cfg.RemoteChains)
			merged[key] = existing
		} else {
			merged[key] = cfg
		}
	}
	configs := make([]tokenscore.TokenTransferConfig, 0, len(merged))
	for _, cfg := range merged {
		configs = append(configs, cfg)
	}
	return configs, nil
}

// buildStellarTokenTransferConfig mirrors the EVM builder: remote entries
// carry the counterpart pool ref plus the lane's committee-verifier resolver
// refs for the combo's CCV qualifiers (shared by outbound and inbound).
func (c *Chain) buildStellarTokenTransferConfig(
	selector uint64,
	remoteSelectors []uint64,
	localRef datastore.AddressRef,
	remoteRef datastore.AddressRef,
	ccvQualifiers []string,
) (tokenscore.TokenTransferConfig, error) {
	remoteChains := make(map[uint64]tokenscore.RemoteChainConfig[*datastore.AddressRef, datastore.AddressRef])
	vvr := stellarccip.VVRDatastoreRef()
	ccvRefs := make([]datastore.AddressRef, 0, len(ccvQualifiers))
	for _, qualifier := range ccvQualifiers {
		ccvRefs = append(ccvRefs, datastore.AddressRef{
			Type:      vvr.Type,
			Version:   vvr.Version,
			Qualifier: qualifier,
		})
	}
	for _, rs := range remoteSelectors {
		remoteChains[rs] = tokenscore.RemoteChainConfig[*datastore.AddressRef, datastore.AddressRef]{
			RemotePool:   &remoteRef,
			OutboundCCVs: ccvRefs,
			InboundCCVs:  ccvRefs,
		}
	}
	tokenRef, err := stellarTokenRefForPool(localRef)
	if err != nil {
		return tokenscore.TokenTransferConfig{}, err
	}
	tar := stellarccip.TokenAdminRegistryDatastoreRef()
	return tokenscore.TokenTransferConfig{
		ChainSelector: selector,
		TokenPoolRef:  localRef,
		TokenRef:      tokenRef,
		RegistryRef: datastore.AddressRef{
			Type:    tar.Type,
			Version: tar.Version,
		},
		RemoteChains:          remoteChains,
		AllowedFinalityConfig: finality.Config{BlockDepth: 1, WaitForSafe: true},
	}, nil
}

// stellarTokenRefForPool maps a shared pool ref (burn-mint or lock-release)
// to its BnM token ref (same qualifier — the pipeline deploys one token per
// pool, keyed by the pool's qualifier; EVM TokenRefForPool parity).
func stellarTokenRefForPool(poolRef datastore.AddressRef) (datastore.AddressRef, error) {
	switch string(poolRef.Type) {
	case stellarccip.DevenvCatalogBurnMintTokenPoolType, stellarccip.DevenvCatalogLockReleaseTokenPoolType:
	default:
		return datastore.AddressRef{}, fmt.Errorf("unsupported stellar pool type %q", string(poolRef.Type))
	}
	return datastore.AddressRef{
		Type:      datastore.ContractType(bnmops.ContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: poolRef.Qualifier,
	}, nil
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

	var filter func(*CCIPMessageSentEvent) bool
	if key.MessageID != (protocol.Bytes32{}) {
		want := [32]byte(key.MessageID)
		c.logger.Info().
			Uint64("destChainSelector", to).
			Str("messageID", hex.EncodeToString(want[:])).
			Uint32("startLedger", latestLedger.Sequence).
			Dur("timeout", timeout).
			Msg("Waiting for CCIPMessageSent event from Stellar OnRamp (by message ID)")
		filter = func(e *CCIPMessageSentEvent) bool {
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
		filter = func(e *CCIPMessageSentEvent) bool {
			return e.DestChainSelector == to && e.SequenceNumber == seq
		}
	}

	event, err := c.waitForCCIPMessageSentEvent(
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

// WaitForCCIPMessageSentEvent is an exported wrapper around the internal
// OnRamp event waiter, returning the full [CCIPMessageSentEvent] (including
// the per-receiver Receipts) rather than the trimmed MessageSentEvent. It is
// used by e2e tests that need to inspect executor receipts — e.g. asserting
// that an executor-sentinel message produced a zero-fee receipt with the
// sentinel as issuer (M-7 no-execution) or a concrete executor receipt (M-5
// use-default resolved to the lane default_executor).
//
// `key` must set either MessageID or SeqNum. `startLedger` is taken from the
// latest ledger at call time.
func (c *Chain) WaitForCCIPMessageSentEvent(ctx context.Context, to uint64, key cciptestinterfaces.MessageEventKey, timeout time.Duration) (*CCIPMessageSentEvent, error) {
	if key.MessageID == (protocol.Bytes32{}) && key.SeqNum == 0 {
		return nil, fmt.Errorf("MessageEventKey must set MessageID or SeqNum")
	}
	if c.onRampClient == nil {
		return nil, fmt.Errorf("OnRamp client not initialized")
	}

	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest ledger: %w", err)
	}

	var filter func(*CCIPMessageSentEvent) bool
	if key.MessageID != (protocol.Bytes32{}) {
		want := [32]byte(key.MessageID)
		filter = func(e *CCIPMessageSentEvent) bool {
			return e.DestChainSelector == to && e.MessageId == want
		}
	} else {
		seq := key.SeqNum
		filter = func(e *CCIPMessageSentEvent) bool {
			return e.DestChainSelector == to && e.SequenceNumber == seq
		}
	}

	return c.waitForCCIPMessageSentEvent(ctx, latestLedger.Sequence, timeout, filter)
}

// GetDefaultExecutor returns the OnRamp's configured `default_executor` for the
// given destination chain selector — i.e. the concrete Executor contract that
// the "use default" executor sentinel resolves to (M-5). Used by e2e tests to
// assert that a use-default-sentinel message produced an executor receipt
// issued by the lane default, not by the sentinel strkey itself.
func (c *Chain) GetDefaultExecutor(ctx context.Context, destChainSelector uint64) (string, error) {
	if c.onRampClient == nil {
		return "", fmt.Errorf("OnRamp client not initialized")
	}
	cfg, err := c.onRampClient.GetDestChainConfig(ctx, destChainSelector)
	if err != nil {
		return "", fmt.Errorf("get dest chain config: %w", err)
	}
	return cfg.DefaultExecutor, nil
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

	var filter func(*destinationreader.ExecutionStateChangedEvent) bool
	if key.MessageID != (protocol.Bytes32{}) {
		want := [32]byte(key.MessageID)
		c.logger.Info().
			Uint64("sourceChainSelector", from).
			Str("messageID", hex.EncodeToString(want[:])).
			Uint32("startLedger", latestLedger.Sequence).
			Dur("timeout", timeout).
			Msg("Waiting for ExecutionStateChanged event from Stellar OffRamp (by message ID)")
		filter = func(e *destinationreader.ExecutionStateChangedEvent) bool {
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
		filter = func(e *destinationreader.ExecutionStateChangedEvent) bool {
			return e.SourceChainSelector == from && e.SequenceNumber == seq
		}
	}

	event, err := c.waitForExecutionStateChangedEvent(
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

// messageV1StellarSourceBaseSize mirrors MESSAGE_V1_STELLAR_SOURCE_BASE_SIZE
// (contracts/common/message): MessageV1 framing plus the stellar sender and
// onramp address XDR bytes billed for every stellar-sourced message.
const messageV1StellarSourceBaseSize uint32 = 159

// cvRemoteConfig returns the outbound committee verifier's remote config for
// remoteChainSelector, or nil when no committee verifier is deployed.
func (c *Chain) cvRemoteConfig(ctx context.Context, remoteChainSelector uint64) (*cvbindings.RemoteChainConfig, error) {
	if c.vvrContractID == "" {
		return nil, nil
	}
	cvID, err := vvrbindings.NewVersionedVerifierResolverClient(c.deployer, c.vvrContractID).
		GetOutboundImplementation(ctx, remoteChainSelector, []byte{})
	if err != nil {
		return nil, fmt.Errorf("get outbound committee verifier: %w", err)
	}
	if cvID == "" {
		return nil, nil
	}
	cvCfg, err := cvbindings.NewCommitteeVerifierClient(c.deployer, cvID).
		GetRemoteChainConfig(ctx, remoteChainSelector)
	if err != nil {
		return nil, fmt.Errorf("get committee verifier remote config: %w", err)
	}
	return cvCfg, nil
}

// messageOverheadBytes bills a data-only message's non-payload bytes from the
// OnRamp dest-chain config and the committee verifier's remote config.
func messageOverheadBytes(onrampCfg *onrampbindings.DestChainConfig, cvCfg *cvbindings.RemoteChainConfig) uint32 {
	overhead := messageV1StellarSourceBaseSize + 2*onrampCfg.AddressBytesLength
	if cvCfg != nil {
		overhead += cvCfg.PayloadSizeBytes
	}
	return overhead
}

// GetMessageOverheadBytes implements cciptestinterfaces.MessageOverheadProvider.
// The non-payload bytes the stellar OnRamp bills inside the fee quoter's
// maxDataBytes budget for a data-only message to the remote: MessageV1
// stellar-source framing, the remote's receiver and offramp address fields,
// and the default committee verifier's dest_bytes_overhead. Executor args are
// assumed empty (data-only case; the EVM destination returns none).
func (c *Chain) GetMessageOverheadBytes(ctx context.Context, remoteChainSelector uint64) (uint32, error) {
	if c.onRampClient == nil {
		return 0, fmt.Errorf("OnRamp client not initialized")
	}
	onrampCfg, err := c.onRampClient.GetDestChainConfig(ctx, remoteChainSelector)
	if err != nil {
		return 0, fmt.Errorf("get dest chain config: %w", err)
	}
	cvCfg, err := c.cvRemoteConfig(ctx, remoteChainSelector)
	if err != nil {
		return 0, err
	}
	return messageOverheadBytes(onrampCfg, cvCfg), nil
}

// GetExecutionGasLimit implements cciptestinterfaces.ExecutionGasLimitProvider.
// Sizes the gas limit for a dataLen-byte message from the remote to execute
// on stellar from the fee quoter's own dest-chain terms for the remote:
// DestGasOverhead as the flat margin plus DestGasPerPayloadByte over the data
// and framing (quote_gas_for_exec's cost model). On Soroban the value is a
// logical budget — the execute tx's compute is simulation-derived — so its
// hard caps live source-side; the clamp reserves the OnRamp's added terms.
func (c *Chain) GetExecutionGasLimit(ctx context.Context, remoteChainSelector uint64, dataLen uint32) (uint32, error) {
	if c.feeQuoterClient == nil {
		return 0, fmt.Errorf("FeeQuoter client not initialized")
	}
	fqCfg, err := c.feeQuoterClient.GetDestChainConfig(ctx, remoteChainSelector)
	if err != nil {
		return 0, fmt.Errorf("failed to get fee quoter dest chain config: %w", err)
	}
	if c.onRampClient == nil {
		return 0, fmt.Errorf("OnRamp client not initialized")
	}
	onrampCfg, err := c.onRampClient.GetDestChainConfig(ctx, remoteChainSelector)
	if err != nil {
		return 0, fmt.Errorf("get dest chain config: %w", err)
	}
	cvCfg, err := c.cvRemoteConfig(ctx, remoteChainSelector)
	if err != nil {
		return 0, err
	}
	overhead := messageOverheadBytes(onrampCfg, cvCfg)
	gas := uint64(fqCfg.DestGasOverhead) +
		uint64(fqCfg.DestGasPerPayloadByte)*(uint64(dataLen)+uint64(overhead))
	// MessageGasLimitTooHigh caps the OnRamp's whole sum PLUS the per-byte
	// charge on payload AND framing (quote_gas_for_exec's calldata_size), so
	// the reserve covers base + per-CCV verification + perByte*(dataLen+overhead).
	reserve := uint64(onrampCfg.BaseExecutionGasCost) +
		uint64(fqCfg.DestGasPerPayloadByte)*(uint64(dataLen)+uint64(overhead))
	if cvCfg != nil {
		reserve += uint64(cvCfg.GasForVerification)
	}
	if maxPerMsg := uint64(fqCfg.MaxPerMsgGasLimit); maxPerMsg > reserve && gas > maxPerMsg-reserve {
		gas = maxPerMsg - reserve
	}
	if gas > math.MaxUint32 {
		gas = math.MaxUint32
	}
	return uint32(gas), nil
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
// Gets the balance of a token for a raw 32-byte holder address. Raw
// addresses carry no account-vs-contract tag: the account-encoded read
// of a contract holder errors on SAC tokens (missing trustline entry)
// or silently returns 0 on custom tokens (BnM balance is unwrap_or(0)),
// so a non-positive account read is retried with the contract encoding.
func (c *Chain) GetTokenBalance(ctx context.Context, address, tokenAddress protocol.UnknownAddress) (*big.Int, error) {
	acctStrkey, err := strkey.Encode(strkey.VersionByteAccountID, []byte(address))
	if err != nil {
		return nil, fmt.Errorf("encode account holder address: %w", err)
	}
	acctBal, acctErr := c.GetTokenBalanceForAddress(ctx, acctStrkey, tokenAddress)
	if acctErr == nil && acctBal != nil && acctBal.Sign() > 0 {
		return acctBal, nil
	}
	ctrStrkey, err := strkey.Encode(strkey.VersionByteContract, []byte(address))
	if err != nil {
		return nil, fmt.Errorf("encode contract holder address: %w", err)
	}
	ctrBal, ctrErr := c.GetTokenBalanceForAddress(ctx, ctrStrkey, tokenAddress)
	if ctrErr == nil && ctrBal != nil && ctrBal.Sign() > 0 {
		return ctrBal, nil
	}
	// A successful contract-form read (zero included) outranks a failed
	// account-form read: a contract holder encoded as an account hits SAC
	// trustline errors (#13) on real SAC tokens while the contract form
	// reads fine — the holder is exactly one of the two forms, so trust the
	// form that works.
	if ctrErr == nil && ctrBal != nil {
		return ctrBal, nil
	}
	// Genuine zero balance or both reads failing: prior behavior.
	return acctBal, acctErr
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
	return bal, nil
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
	// The Stellar OffRamp / committee verifier expects EIP-2098 compact
	// signatures: recovery id encoded in bit 255 of the S word, with S in its
	// original (un-flipped) form. The CCV aggregator emits v=27-normalized
	// R||S blobs instead, so every writer that calls offramp.execute must
	// convert first — the DON executor does this in
	// ContractTransmitter.ConvertAndWriteMessageToChain. Mirror it here so the
	// permissionless manual-execute path feeds identical bytes to the chain;
	// otherwise decode_compact_sig recovers the wrong signer and verify_message
	// fails with UnexpectedSigner (#72).
	convertedVerifierResults := make([][]byte, len(verifierResults))
	for i, blob := range verifierResults {
		converted, convErr := contracttransmitter.ConvertVerifierBlobToEIP2098(blob)
		if convErr != nil {
			return cciptestinterfaces.ExecutionStateChangedEvent{},
				fmt.Errorf("convert verifier blob %d to EIP-2098: %w", i, convErr)
		}
		convertedVerifierResults[i] = converted
	}

	if err := c.offRampClient.Execute(ctx, encoded, ccvStrs, convertedVerifierResults, uint32(gasLimit)); err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("offramp execute: %w", err)
	}
	latestLedger, err := c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return cciptestinterfaces.ExecutionStateChangedEvent{}, fmt.Errorf("failed to get latest ledger: %w", err)
	}
	event, err := c.waitForExecutionStateChangedEvent(
		ctx, latestLedger.Sequence, 2*time.Minute,
		func(e *destinationreader.ExecutionStateChangedEvent) bool {
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
	// RMN proxy: pools store it immutably at initialize (EVM `immutable i_rmnProxy` parity),
	// so the post-deploy token-pool initialization must rehydrate it from the datastore
	// (the shared DeployChainContracts changeset deployed + recorded it; this Chain did not
	// hold it in memory). Without this, DeployLockReleaseTestTokenPool's required non-empty
	// RMN-proxy guard reverts.
	if c.rmnProxyContractID == "" {
		if id, err := stellarccip.GetRMNProxyStrkey(ds, selector); err == nil && id != "" {
			c.rmnProxyContractID = id
			if c.rmnProxyClient == nil {
				c.rmnProxyClient = rmnproxybindings.NewRmnProxyClient(c.deployer, id)
			}
		}
	} else if c.rmnProxyClient == nil {
		c.rmnProxyClient = rmnproxybindings.NewRmnProxyClient(c.deployer, c.rmnProxyContractID)
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
		// The inbound message's token_transfer.source_pool_address is abi-encoded
		// by the source chain's OnRamp. For an EVM source that is abi.encode(address),
		// i.e. the 20-byte address left-padded to 32 bytes (Internal.SourceTokenData:
		// "sourcePoolAddress ... abi encoded in the case of EVM chains"). The pool's
		// is_remote_source_pool check (C-3) compares the stored remote_pool_address
		// against that message field with exact byte equality, so the stored value
		// must be in the SAME 32-byte abi-encoded form. resolveRemotePoolAndToken
		// returns the raw EVM address (20 bytes); left-pad to 32 to match. For
		// Stellar remotes the address is already 32 bytes, so LeftPadBytes is a
		// no-op. This mirrors the EVM pool side, which stores the Stellar pool via
		// common.LeftPadBytes(_, 32) in paddedAddressBytes.
		remotePoolBytes = common.LeftPadBytes(remotePoolBytes, 32)
		updates = append(updates, tokenpoolbindings.ChainUpdate{
			RemoteChainSelector:       rs,
			RemotePoolAddresses:       [][]byte{remotePoolBytes},
			RemoteTokenAddress:        remoteTokenBytes,
			OutboundRateLimiterConfig: tokenpoolbindings.RateLimitConfig{},
			InboundRateLimiterConfig:  tokenpoolbindings.RateLimitConfig{},
		})
	}
	return updates, nil
}

func (c *Chain) buildOffRampSourceConfigs(ds datastore.DataStore, remoteSelectors []uint64) ([]offrampbindings.SourceChainConfigArgs, error) {
	return stellarccip.BuildOffRampSourceConfigs(ds, remoteSelectors, c.vvrContractID, c.routerContractID)
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
