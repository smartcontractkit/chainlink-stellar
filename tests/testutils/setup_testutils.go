package helpers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog"
	chain_selectors "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fastcurse"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	"github.com/smartcontractkit/chainlink-ccv/bootstrap"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-common/keystore"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldfdeployment "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	"github.com/smartcontractkit/chainlink-deployments-framework/operations"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellarcommon "github.com/smartcontractkit/chainlink-stellar/ccv/common"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"
)

// Sha256 hash of the network passphrase
const STELLAR_LOCALNET_PASSPHRASE = "Standalone Network ; February 2017"

// StellarQuickstartImage pins the local-network image by manifest-list digest
// instead of the rolling `stellar/quickstart:testing` tag.
const StellarQuickstartImage = "stellar/quickstart@sha256:c28a5a9374cb28b70a82ef1bd37871946b0ef966b0c5e02e7855913297421ec1"

// getFreePort asks the OS for an available TCP port.
func getFreePort(t *testing.T) string {
	port, err := getFreePortErr()
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	return port
}

// getFreePortErr asks the OS for an available TCP port, returning an error on failure.
func getFreePortErr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("failed to listen: %w", err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return "", fmt.Errorf("failed to parse port: %w", err)
	}
	return port, nil
}

func SetupTestEnv(ctx context.Context, t *testing.T) (string, *keypair.Full, *stellardeployment.Deployer, *rpcclient.Client, string) {
	chainID := chain_selectors.STELLAR_LOCALNET.ChainID
	stellarSelector := chain_selectors.STELLAR_LOCALNET.Selector

	// Deploy local Stellar network using devenv
	chain := ccvchain.New(zerolog.New(os.Stdout), stellarSelector)

	port := getFreePort(t)
	containerName := fmt.Sprintf("blockchain-stellar-%s", t.Name())

	input := &blockchain.Input{
		Type:          "stellar",
		ChainID:       string(chainID[:]),
		ContainerName: containerName,
		Port:          port,
		DockerCmdParamsOverrides: []string{
			"--enable-soroban-rpc",
			"--local",
		},
		Image: StellarQuickstartImage,
	}

	output, err := chain.DeployLocalNetwork(ctx, input)
	if err != nil {
		t.Fatalf("Failed to deploy local network: %v", err)
	}
	t.Logf("Local network deployed at: %s", output.ContainerName)

	rpcURL := output.Nodes[0].ExternalHTTPUrl
	networkPassphrase := chain.NetworkPassphrase()

	// Create RPC client
	rpcClient := rpcclient.NewClient(rpcURL, &http.Client{Timeout: 60 * time.Second})

	// Wait for Friendbot to be ready - it takes longer than the RPC endpoint
	// The quickstart container starts multiple services and friendbot initializes last
	t.Log("Waiting for Friendbot to be ready (this can take up to 90 seconds)...")
	if err := WaitForFriendbot(
		ctx,
		input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL,
		3*time.Minute,
	); err != nil {
		t.Fatalf("Friendbot not ready: %v", err)
	}
	t.Log("Friendbot is ready")

	deployerKP, err := keypair.Random()
	if err != nil {
		t.Fatalf("Failed to generate deployer keypair: %v", err)
	}

	deployerAddressBytes, err := strkey.Decode(strkey.VersionByteAccountID, deployerKP.Address())
	if err != nil {
		t.Fatalf("Failed to decode deployer address: %v", err)
	}

	err = chain.FundAddresses(ctx, input, []protocol.UnknownAddress{deployerAddressBytes}, nil)
	if err != nil {
		t.Fatalf("Failed to fund deployer account: %v", err)
	}

	deployer := stellardeployment.NewDeployer(rpcClient, networkPassphrase, deployerKP)

	// Find the project root (where Cargo.toml is)
	projectRoot := FindProjectRoot(t)

	return projectRoot, deployerKP, deployer, rpcClient, networkPassphrase
}

// SharedTestEnv holds the result of SetupTestEnvShared for use across multiple integration tests.
type SharedTestEnv struct {
	ProjectRoot       string
	DeployerKP        *keypair.Full
	Deployer          *stellardeployment.Deployer
	RPCClient         *rpcclient.Client
	NetworkPassphrase string
	FriendbotURL      string             // faucet base URL (no ?addr=), for funding issuers / SAC setup
	Output            *blockchain.Output // for teardown
}

// SetupTestEnvShared performs the same setup as SetupTestEnv but without *testing.T.
// It returns the blockchain Output for teardown. Use containerName for a stable container name
// when sharing across tests (e.g. "blockchain-stellar-integration-shared").
func SetupTestEnvShared(ctx context.Context, containerName string) (*SharedTestEnv, error) {
	chainID := chain_selectors.STELLAR_LOCALNET.ChainID
	stellarSelector := chain_selectors.STELLAR_LOCALNET.Selector

	chain := ccvchain.New(zerolog.New(os.Stdout), stellarSelector)

	port, err := getFreePortErr()
	if err != nil {
		return nil, fmt.Errorf("get free port: %w", err)
	}

	input := &blockchain.Input{
		Type:          "stellar",
		ChainID:       string(chainID[:]),
		ContainerName: containerName,
		Port:          port,
		DockerCmdParamsOverrides: []string{
			"--enable-soroban-rpc",
			"--local",
		},
		Image: StellarQuickstartImage,
	}

	output, err := chain.DeployLocalNetwork(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("deploy local network: %w", err)
	}

	// Defer cleanup on failure: if any step after deployment fails, we must terminate
	// the container to avoid leaking it. On success, we skip cleanup so the caller
	// can use the env and teardown in TestMain.
	success := false
	defer func() {
		if !success && output != nil && output.Container != nil {
			termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = output.Container.Terminate(termCtx)
		}
	}()

	rpcURL := output.Nodes[0].ExternalHTTPUrl
	networkPassphrase := chain.NetworkPassphrase()

	rpcClient := rpcclient.NewClient(rpcURL, &http.Client{Timeout: 60 * time.Second})

	if err := WaitForFriendbot(
		ctx,
		input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL,
		3*time.Minute,
	); err != nil {
		return nil, fmt.Errorf("friendbot not ready: %w", err)
	}

	deployerKP, err := keypair.Random()
	if err != nil {
		return nil, fmt.Errorf("generate deployer keypair: %w", err)
	}

	deployerAddressBytes, err := strkey.Decode(strkey.VersionByteAccountID, deployerKP.Address())
	if err != nil {
		return nil, fmt.Errorf("decode deployer address: %w", err)
	}

	err = chain.FundAddresses(ctx, input, []protocol.UnknownAddress{deployerAddressBytes}, nil)
	if err != nil {
		return nil, fmt.Errorf("fund deployer: %w", err)
	}

	deployer := stellardeployment.NewDeployer(rpcClient, networkPassphrase, deployerKP)

	projectRoot, err := FindProjectRootErr()
	if err != nil {
		return nil, fmt.Errorf("find project root: %w", err)
	}

	friendbotURL := ""
	if input.Out != nil && input.Out.NetworkSpecificData != nil && input.Out.NetworkSpecificData.StellarNetwork != nil {
		friendbotURL = input.Out.NetworkSpecificData.StellarNetwork.FriendbotURL
	}

	success = true
	return &SharedTestEnv{
		ProjectRoot:       projectRoot,
		DeployerKP:        deployerKP,
		Deployer:          deployer,
		RPCClient:         rpcClient,
		NetworkPassphrase: networkPassphrase,
		FriendbotURL:      friendbotURL,
		Output:            output,
	}, nil
}

type E2ETestEnv struct {
	DeployerKP         *keypair.Full
	Deployer           *stellardeployment.Deployer
	RPCClient          *rpcclient.Client
	NetworkPassphrase  string
	StellarRoot        string
	DataStore          datastore.DataStore
	SourceChain        cciptestinterfaces.CCIP17
	DestChain          cciptestinterfaces.CCIP17
	SourceChainDetails *chain_selectors.ChainDetails
	DestChainDetails   *chain_selectors.ChainDetails
	Chains             map[uint64]cciptestinterfaces.CCIP17
	AggregatorClients  map[string]*ccv.AggregatorClient
	IndexerMonitor     *ccv.IndexerMonitor
	FriendbotURL       string
	CLDFEnv            *cldfdeployment.Environment
}

func NewE2ETestEnv(t *testing.T, ctx context.Context, l *zerolog.Logger, configOutputPath string, stellarChainID string, stellarSelector uint64) *E2ETestEnv {
	// Register all Stellar devenv components: modifier, chain config loader,
	// chain family adapter, and ImplFactory.
	ccvchain.RegisterStellarComponents()

	// Load the output config written by the devenv CLI (pre-started separately).
	// LoadOutput also populates CLDF.DataStore from the serialised addresses.
	in, err := ccv.LoadOutput[ccv.Cfg](configOutputPath)
	require.NoError(t, err)
	require.NotNil(t, in)

	// Load both EVM and Stellar chains; the ImplFactory handles Stellar chain construction.
	lib, err := ccv.NewLibFromCCVEnv(l, configOutputPath, chain_selectors.FamilyEVM, chain_selectors.FamilyStellar)
	require.NoError(t, err)
	chains, err := lib.ChainsMap(ctx)
	require.NoError(t, err)
	require.NotNil(t, chains)

	// Set up indexer monitor
	var indexerMonitor *ccv.IndexerMonitor
	indexerClient, err := lib.Indexer()
	require.NoError(t, err)
	indexerMonitor, err = ccv.NewIndexerMonitor(
		zerolog.Ctx(ctx).With().Str("component", "indexer-client").Logger(),
		indexerClient)
	require.NoError(t, err)
	require.NotNil(t, indexerMonitor)

	aggregatorClients := make(map[string]*ccv.AggregatorClient)
	for qualifier := range in.AggregatorEndpoints {
		client, err := in.NewAggregatorClientForCommittee(
			zerolog.Ctx(ctx).With().Str("component", fmt.Sprintf("aggregator-client-%s", qualifier)).Logger(),
			qualifier)
		require.NoError(t, err)
		require.NotNil(t, client)
		aggregatorClients[qualifier] = client
		t.Cleanup(func() {
			client.Close()
		})
	}
	defaultAggregatorClient := aggregatorClients[devenvcommon.DefaultCommitteeVerifierQualifier]
	require.NotNil(t, defaultAggregatorClient)

	// Find Stellar chain
	var stellarChain *blockchain.Input
	for _, bc := range in.Blockchains {
		if bc.Type == blockchain.TypeStellar {
			stellarChain = bc
			break
		}
	}
	require.NotNil(t, stellarChain, "need at least one stellar chain for this test")

	// Find EVM chain
	var evmChain *blockchain.Input
	for _, bc := range in.Blockchains {
		if bc.Type == blockchain.TypeAnvil {
			evmChain = bc
			break
		}
	}
	require.NotNil(t, evmChain, "need at least one evm chain for this test")

	stellarDetails, err := chain_selectors.GetChainDetailsByChainIDAndFamily(stellarChain.ChainID, chain_selectors.FamilyStellar)
	require.NoError(t, err)
	require.NotNil(t, stellarDetails)

	evmDetails, err := chain_selectors.GetChainDetailsByChainIDAndFamily(evmChain.ChainID, chain_selectors.FamilyEVM)
	require.NoError(t, err)
	require.NotNil(t, evmDetails)

	destChain := chains[evmDetails.ChainSelector]
	require.NotNil(t, destChain)

	// sourceChain is constructed by ImplFactory.New() when ChainsMap() is called.
	sourceChain := chains[stellarDetails.ChainSelector]
	require.NotNil(t, sourceChain)

	// Get Stellar network configuration from the environment output.
	require.NotEmpty(t, stellarChain.Out.Nodes, "stellar chain output must have nodes")
	stellarRPCURL := stellarChain.Out.Nodes[0].ExternalHTTPUrl
	require.NotEmpty(t, stellarRPCURL, "stellar RPC URL is required")
	networkPassphrase := stellarChain.Out.NetworkSpecificData.StellarNetwork.NetworkPassphrase
	require.NotEmpty(t, networkPassphrase, "network passphrase is required")
	friendbotURL := stellarChain.Out.NetworkSpecificData.StellarNetwork.FriendbotURL
	require.NotEmpty(t, friendbotURL, "friendbot URL is required")

	l.Info().
		Str("stellarRPCURL", stellarRPCURL).
		Str("networkPassphrase", networkPassphrase).
		Str("friendbotURL", friendbotURL).
		Msg("Stellar network configuration")

	// Derive the same deployer keypair that chain.go uses in DeployLocalNetwork.
	// This must match the deterministic derivation: sha256("deployer-" + networkPassphrase).
	deployerSeed := sha256.Sum256(fmt.Appendf(nil, "deployer-%s", networkPassphrase))
	deployerKP, err := keypair.FromRawSeed(deployerSeed)
	require.NoError(t, err)
	l.Info().Str("deployerAddress", deployerKP.Address()).Msg("Derived deployer keypair (matches chain.go)")

	// Create Soroban RPC client
	rpc := rpcclient.NewClient(stellarRPCURL, &http.Client{Timeout: 60 * time.Second})
	t.Cleanup(func() { rpc.Close() })

	// Create the Deployer (implements bindings.Invoker) for contract interactions.
	// The deployer was funded during ccv.NewEnvironment().
	deployer := stellardeployment.NewDeployer(rpc, networkPassphrase, deployerKP)

	fundStellarExecutorTransmitters(t, ctx, in, friendbotURL, deployer, l)

	cldfEnv, err := lib.CLDFEnvironment()
	require.NoError(t, err)
	require.NotNil(t, cldfEnv)

	return &E2ETestEnv{
		DeployerKP:         deployerKP,
		Deployer:           deployer,
		RPCClient:          rpc,
		NetworkPassphrase:  networkPassphrase,
		DataStore:          in.CLDF.DataStore,
		Chains:             chains,
		SourceChain:        sourceChain,
		DestChain:          destChain,
		SourceChainDetails: &stellarDetails,
		DestChainDetails:   &evmDetails,
		AggregatorClients:  aggregatorClients,
		IndexerMonitor:     indexerMonitor,
		FriendbotURL:       friendbotURL,
		CLDFEnv:            cldfEnv,
	}
}

// The methods below are a temporary workaround to fund the Stellar executor transmitter.
// TODO: Remove this once upstream handles Stellar-family executor transmitters in the fundExecutorTransmitters function.
func fundStellarExecutorTransmitters(
	t *testing.T,
	ctx context.Context,
	in *ccv.Cfg,
	friendbotURL string,
	deployer *stellardeployment.Deployer,
	l *zerolog.Logger,
) {
	t.Helper()

	for _, exec := range in.Executor {
		if exec == nil || exec.ChainFamily != chain_selectors.FamilyStellar {
			continue
		}
		require.NotNil(t, exec.Out, "stellar executor %q must have output", exec.ContainerName)
		require.NotEmpty(t, exec.Out.BootstrapDBURL, "stellar executor %q must expose bootstrap URL", exec.ContainerName)

		pubKey, err := fetchBootstrapPublicKey(ctx, exec.Out.BootstrapDBURL, stellarcommon.StellarTransmitterKeyName)
		require.NoError(t, err, "fetch Stellar transmitter key for executor %q", exec.ContainerName)
		require.Len(t, pubKey, 32, "stellar executor %q transmitter public key must be 32 bytes", exec.ContainerName)

		address, err := strkey.Encode(strkey.VersionByteAccountID, pubKey)
		require.NoError(t, err, "encode Stellar transmitter account for executor %q", exec.ContainerName)

		_, _, exists, err := deployer.NativeAccountState(ctx, pubKey)
		require.NoError(t, err, "check Stellar transmitter account for executor %q", exec.ContainerName)
		if exists {
			l.Info().
				Str("executor", exec.ContainerName).
				Str("address", address).
				Msg("Stellar executor transmitter already funded")
			continue
		}

		require.NoError(t, FundViaFriendbot(friendbotURL, address), "fund Stellar transmitter for executor %q", exec.ContainerName)
		l.Info().
			Str("executor", exec.ContainerName).
			Str("address", address).
			Msg("Funded Stellar executor transmitter via Friendbot")
	}
}

func fetchBootstrapPublicKey(ctx context.Context, bootstrapURL, keyName string) ([]byte, error) {
	reqBody, err := json.Marshal(keystore.GetKeysRequest{KeyNames: []string{keyName}})
	if err != nil {
		return nil, fmt.Errorf("marshal get keys request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(bootstrapURL, "/")+bootstrap.GetKeysEndpoint,
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return nil, fmt.Errorf("create get keys request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get bootstrap keys: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get bootstrap keys returned %s", resp.Status)
	}

	var keyResp keystore.GetKeysResponse
	if err := json.NewDecoder(resp.Body).Decode(&keyResp); err != nil {
		return nil, fmt.Errorf("decode get keys response: %w", err)
	}

	for _, key := range keyResp.Keys {
		if key.KeyInfo.Name == keyName {
			return append([]byte(nil), key.KeyInfo.PublicKey...), nil
		}
	}

	return nil, fmt.Errorf("bootstrap key %q not found", keyName)
}

// CurseChain curses a subject chain from the perspective of the given chain using fastcurse changeset.
// This replaces the deprecated Chain.Curse() method.
func CurseChain(t *testing.T, env *cldfdeployment.Environment, chainSelector, subjectChainSelector uint64) {
	t.Helper()

	// Derive the correct curse adapter version for the chain family
	curseRegistry := fastcurse.GetCurseRegistry()
	version := deriveCurseAdapterVersion(t, env, curseRegistry, chainSelector)

	// Use a shallow copy so this helper does not mutate the caller's shared environment.
	envCopy := *env
	envCopy.OperationsBundle = operations.NewBundle(env.GetContext, env.Logger, operations.NewMemoryReporter())

	curseCS := fastcurse.CurseChangeset(curseRegistry, changesets.GetRegistry())
	_, err := curseCS.Apply(envCopy, fastcurse.RMNCurseConfig{
		CurseActions: []fastcurse.CurseActionInput{
			{
				ChainSelector:        chainSelector,
				SubjectChainSelector: subjectChainSelector,
				Version:              version,
				IsGlobalCurse:        false,
			},
		},
	})
	require.NoError(t, err, "failed to curse chain %d from chain %d", subjectChainSelector, chainSelector)

	adapter, ok := curseRegistry.GetCurseAdapter(chain_selectors.FamilyStellar, version)
	require.True(t, ok, "no curse adapter registered for chain family '%s'", chain_selectors.FamilyStellar)

	require.Eventually(t, func() bool {
		isCursed, err := adapter.IsSubjectCursedOnChain(*env, chainSelector, fastcurse.GenericSelectorToSubject(subjectChainSelector))
		return err == nil && isCursed
	}, 5*time.Second, 1*time.Second, "chain %d should be cursed on chain %d", subjectChainSelector, chainSelector)
}

// UncurseChain uncurses a subject chain from the perspective of the given chain using fastcurse changeset.
// This replaces the deprecated Chain.Uncurse() method.
func UncurseChain(t *testing.T, env *cldfdeployment.Environment, chainSelector, subjectChainSelector uint64) {
	t.Helper()

	// Derive the correct curse adapter version for the chain family
	curseRegistry := fastcurse.GetCurseRegistry()
	version := deriveCurseAdapterVersion(t, env, curseRegistry, chainSelector)

	// Reset the bundle so it doesn't cache previous uncurses
	bundle := operations.NewBundle(env.GetContext, env.Logger, operations.NewMemoryReporter())
	env.OperationsBundle = bundle

	uncurseCS := fastcurse.UncurseChangeset(curseRegistry, changesets.GetRegistry())
	_, err := uncurseCS.Apply(*env, fastcurse.RMNCurseConfig{
		CurseActions: []fastcurse.CurseActionInput{
			{
				ChainSelector:        chainSelector,
				SubjectChainSelector: subjectChainSelector,
				Version:              version,
				IsGlobalCurse:        false,
			},
		},
	})
	require.NoError(t, err, "failed to uncurse chain %d from chain %d", subjectChainSelector, chainSelector)

	adapter, ok := curseRegistry.GetCurseAdapter(chain_selectors.FamilyStellar, version)
	require.True(t, ok, "no curse adapter registered for chain family '%s'", chain_selectors.FamilyStellar)

	require.Eventually(t, func() bool {
		isCursed, err := adapter.IsSubjectCursedOnChain(*env, chainSelector, fastcurse.GenericSelectorToSubject(subjectChainSelector))
		return err == nil && !isCursed
	}, 5*time.Second, 1*time.Second, "chain %d should be uncursed on chain %d", subjectChainSelector, chainSelector)
}

// deriveCurseAdapterVersion gets the appropriate curse adapter version for a chain.
func deriveCurseAdapterVersion(t *testing.T, env *cldfdeployment.Environment, curseRegistry *fastcurse.CurseRegistry, chainSelector uint64) *semver.Version {
	family, err := chain_selectors.GetSelectorFamily(chainSelector)
	require.NoError(t, err, "failed to get chain family for selector %d", chainSelector)

	// Get the curse subject adapter for this chain family
	subjectAdapter, ok := curseRegistry.GetCurseSubjectAdapter(family)
	require.True(t, ok, "no curse subject adapter registered for chain family '%s'", family)

	// Derive the version from the adapter
	// Note: DeriveCurseAdapterVersion expects cldf.Environment by value, not pointer
	version, err := subjectAdapter.DeriveCurseAdapterVersion(*env, chainSelector)
	require.NoError(t, err, "failed to derive curse adapter version for chain %d", chainSelector)

	return version
}
