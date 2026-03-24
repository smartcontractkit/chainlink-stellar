package ccvchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v1_7_0/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/ccv/chains/evm/deployment/v2_0_0/operations/onramp"
	routeroperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"

	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

var _ ccv.ImplFactory = &ImplFactory{}

// ImplFactory creates Stellar CCIP17 chain implementations.
// It implements [registry.ImplFactory] and is registered with the global factory
// registry via RegisterImplFactory(chainsel.FamilyStellar, NewImplFactory()).
type ImplFactory struct{}

// NewImplFactory returns a new Stellar ImplFactory.
func NewImplFactory() *ImplFactory {
	return &ImplFactory{}
}

// NewEmpty implements [registry.ImplFactory].
// Returns a bare Chain used by NewEnvironment() to call DeployLocalNetwork and
// DeployContractsForSelector.
func (f *ImplFactory) NewEmpty() cciptestinterfaces.CCIP17Configuration {
	return New(
		zerolog.New(os.Stderr).With().Str("component", "Stellar").Logger(),
		chainsel.STELLAR_LOCALNET.Selector,
	)
}

// New implements [registry.ImplFactory].
// Returns a fully initialised Chain for test interactions against an already-deployed
// network. It reconstructs all necessary state (RPC client, deployer keypair, OnRamp
// client) from the blockchain.Input output and the deployment datastore.
func (f *ImplFactory) New(ctx context.Context, cfg *ccv.Cfg, lggr zerolog.Logger, env *deployment.Environment, bc *blockchain.Input) (cciptestinterfaces.CCIP17, error) {
	details, err := chainsel.GetChainDetailsByChainIDAndFamily(bc.ChainID, chainsel.FamilyStellar)
	if err != nil {
		return nil, fmt.Errorf("get chain details for Stellar chain %s: %w", bc.ChainID, err)
	}

	if bc.Out == nil {
		return nil, fmt.Errorf("blockchain output is nil for chain %s", bc.ChainID)
	}
	if len(bc.Out.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes in blockchain output for Stellar chain %s", bc.ChainID)
	}
	if bc.Out.NetworkSpecificData == nil || bc.Out.NetworkSpecificData.StellarNetwork == nil {
		return nil, fmt.Errorf("missing Stellar network info in blockchain output for chain %s", bc.ChainID)
	}

	sorobanRPCURL := bc.Out.Nodes[0].ExternalHTTPUrl
	networkPassphrase := bc.Out.NetworkSpecificData.StellarNetwork.NetworkPassphrase

	// Derive the same deployer keypair used during DeployLocalNetwork.
	deployerSeed := sha256.Sum256([]byte(fmt.Sprintf("deployer-%s", networkPassphrase)))
	deployerKP, err := keypair.FromRawSeed(deployerSeed)
	if err != nil {
		return nil, fmt.Errorf("derive deployer keypair for Stellar chain %s: %w", bc.ChainID, err)
	}

	rpcClient := rpcclient.NewClient(sorobanRPCURL, &http.Client{Timeout: 60 * time.Second})
	deployer := stellardeployment.NewDeployer(rpcClient, networkPassphrase, deployerKP)

	chain := &Chain{
		chainSelector:     details.ChainSelector,
		logger:            lggr,
		rpcClient:         rpcClient,
		networkPassphrase: networkPassphrase,
		sorobanRPCURL:     sorobanRPCURL,
		deployerKeypair:   deployerKP,
		deployer:          deployer,
	}

	// Look up deployed contract addresses from the datastore and wire up clients.
	if env.DataStore != nil {
		onrampKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(onrampoperations.ContractType),
			semver.MustParse(onrampoperations.Deploy.Version()),
			"",
		)
		onrampRef, err := env.DataStore.Addresses().Get(onrampKey)
		if err == nil && onrampRef.Address != "" {
			onrampContractID, convErr := scval.HexToContractStrkey(onrampRef.Address)
			if convErr == nil {
				chain.onRampContractID = onrampContractID
				chain.onRampClient = onrampbindings.NewOnRampClient(deployer, onrampContractID)
			}
		}

		offrampKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(offrampoperations.ContractType),
			semver.MustParse(offrampoperations.Deploy.Version()),
			"",
		)
		offrampRef, err := env.DataStore.Addresses().Get(offrampKey)
		if err == nil && offrampRef.Address != "" {
			offrampContractID, convErr := scval.HexToContractStrkey(offrampRef.Address)
			if convErr == nil {
				chain.offRampContractID = offrampContractID
				chain.offRampClient = offrampbindings.NewOffRampClient(deployer, offrampContractID)
			}
		}

		routerKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(routeroperations.ContractType),
			semver.MustParse(routeroperations.Deploy.Version()),
			"",
		)
		routerRef, err := env.DataStore.Addresses().Get(routerKey)
		if err == nil && routerRef.Address != "" {
			routerContractID, convErr := scval.HexToContractStrkey(routerRef.Address)
			if convErr == nil {
				chain.routerContractID = routerContractID
				chain.routerClient = routerbindings.NewRouterClient(deployer, routerContractID)
			}
		}

		vvrKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(versioned_verifier_resolver.ContractType),
			semver.MustParse(versioned_verifier_resolver.Version.String()),
			devenvcommon.DefaultCommitteeVerifierQualifier,
		)
		vvrRef, err := env.DataStore.Addresses().Get(vvrKey)
		if err == nil && vvrRef.Address != "" {
			vvrContractID, convErr := scval.HexToContractStrkey(vvrRef.Address)
			if convErr == nil {
				chain.vvrContractID = vvrContractID
			}
		}

		cvKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(committee_verifier.ContractType),
			semver.MustParse(committee_verifier.Version.String()),
			devenvcommon.DefaultCommitteeVerifierQualifier,
		)
		cvRef, err := env.DataStore.Addresses().Get(cvKey)
		if err == nil && cvRef.Address != "" {
			cvContractID, convErr := scval.HexToContractStrkey(cvRef.Address)
			if convErr == nil {
				chain.cvContractID = cvContractID
			}
		}
	}

	return chain, nil
}
