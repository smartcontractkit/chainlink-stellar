package helpers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/registry"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/committeeverifier"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	ccvchain "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	chain "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/require"

	chain_selectors "github.com/smartcontractkit/chain-selectors"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	stellar "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	modifier "github.com/smartcontractkit/chainlink-stellar/ccv/modifier"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// Sha256 hash of the network passphrase
const STELLAR_LOCALNET_PASSPHRASE = "Standalone Network ; February 2017"

// getFreePort asks the OS for an available TCP port.
func getFreePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("Failed to parse free port: %v", err)
	}
	return port
}

func SetupTestEnv(ctx context.Context, t *testing.T) (string, *keypair.Full, *deployment.Deployer, *rpcclient.Client, string) {
	// Deploy local Stellar network using devenv
	chain := chain.New(zerolog.New(os.Stdout))

	chainID := network.ID(STELLAR_LOCALNET_PASSPHRASE)

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
		Image: "stellar/quickstart:testing",
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

	deployer := deployment.NewDeployer(rpcClient, networkPassphrase, deployerKP)

	// Find the project root (where Cargo.toml is)
	projectRoot := FindProjectRoot(t)

	return projectRoot, deployerKP, deployer, rpcClient, networkPassphrase
}

type E2ETestEnv struct {
	DeployerKP         *keypair.Full
	Deployer           *deployment.Deployer
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
}

func NewE2ETestEnv(t *testing.T, ctx context.Context, l *zerolog.Logger, configOutputPath string, stellarChainID string) *E2ETestEnv {
	// Register the Stellar chain adapter by using the EVM adapter as a base
	global_family_registry := registry.GetGlobalChainFamilyAdapterRegistry()
	evm_adapter, ok := global_family_registry.GetChainFamily(chain_selectors.FamilyEVM)
	require.True(t, ok)
	require.NotNil(t, evm_adapter)

	stellar_adapter := ccvchain.NewChainFamilyAdapter(evm_adapter)
	global_family_registry.RegisterChainFamily(chain_selectors.FamilyStellar, stellar_adapter)

	// Register the Stellar chain implementation
	registry.GetGlobalChainImplRegistry().
		Register(stellarChainID, chain_selectors.FamilyStellar, stellar.New(zerolog.New(os.Stdout)))

	committeeverifier.RegisterModifier(
		chain_selectors.FamilyStellar,
		modifier.StellarModifier,
	)

	in, err := ccv.NewEnvironment()
	require.NoError(t, err)

	// Reconstruct the DataStore from CLDF addresses.
	// in.CLDF.DataStore is empty (Init() creates an empty sealed store);
	// the real addresses are JSON-serialized in in.CLDF.Addresses.
	ds := datastore.NewMemoryDataStore()
	for _, addrJSON := range in.CLDF.Addresses {
		var refs []datastore.AddressRef
		err := json.Unmarshal([]byte(addrJSON), &refs)
		require.NoError(t, err)
		for _, ref := range refs {
			ds.AddressRefStore.Add(ref)
		}
	}

	// Load EVM chain for destination interactions
	lib, err := ccv.NewLib(l, configOutputPath, chain_selectors.FamilyEVM)
	require.NoError(t, err)
	chains, err := lib.ChainsMap(ctx)
	require.NoError(t, err)
	require.NotNil(t, chains)

	// Set up aggregator client
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

	configsOutput, err := ccv.LoadOutput[ccv.Cfg](configOutputPath)
	require.NoError(t, err)
	require.NotNil(t, configsOutput)

	// Find Stellar chain
	var stellarChain *blockchain.Input
	for _, bc := range configsOutput.Blockchains {
		if bc.Type == blockchain.TypeStellar {
			stellarChain = bc
			break
		}
	}
	require.NotNil(t, stellarChain, "need at least one stellar chain for this test")

	// Find EVM chain
	var evmChain *blockchain.Input
	for _, bc := range configsOutput.Blockchains {
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

	sourceChain := chains[stellarDetails.ChainSelector]
	require.NotNil(t, sourceChain)

	// Get Stellar network configuration from the environment output
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
	deployerSeed := sha256.Sum256([]byte(fmt.Sprintf("deployer-%s", networkPassphrase)))
	deployerKP, err := keypair.FromRawSeed(deployerSeed)
	require.NoError(t, err)
	l.Info().Str("deployerAddress", deployerKP.Address()).Msg("Derived deployer keypair (matches chain.go)")

	// Create Soroban RPC client
	rpc := rpcclient.NewClient(stellarRPCURL, &http.Client{Timeout: 60 * time.Second})
	t.Cleanup(func() { rpc.Close() })

	// No Friendbot funding needed here -- this deployer was already funded
	// by chain.go DeployLocalNetwork during ccv.NewEnvironment().

	// Create the Deployer (implements bindings.Invoker) for contract interactions
	deployer := stellardeployment.NewDeployer(rpc, networkPassphrase, deployerKP)

	return &E2ETestEnv{
		DeployerKP:         deployerKP,
		Deployer:           deployer,
		RPCClient:          rpc,
		NetworkPassphrase:  networkPassphrase,
		DataStore:          ds.Seal(),
		Chains:             chains,
		SourceChain:        sourceChain,
		DestChain:          destChain,
		SourceChainDetails: &stellarDetails,
		DestChainDetails:   &evmDetails,
		AggregatorClients:  aggregatorClients,
		IndexerMonitor:     indexerMonitor,
	}
}
