package ccvchain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_0_0/operations/rmn_proxy"
	routeroperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_6_0/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	fee_quoter "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/onramp"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	ccv "github.com/smartcontractkit/chainlink-ccv/build/devenv"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	ccvservices "github.com/smartcontractkit/chainlink-ccv/build/devenv/services"
	"github.com/smartcontractkit/chainlink-ccv/protocol"
	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccipdevenv "github.com/smartcontractkit/chainlink-stellar/deployment/ccip/devenv"
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

// DefaultSignerKey returns the default signer key for this chain family
// given the bootstrap keys from a verifier node. Each family selects the
// appropriate key type (e.g. EVM uses ECDSAAddress, Stellar uses EdDSA).
// Return "" if no default signer is available.
func (f *ImplFactory) DefaultSignerKey(keys ccvservices.BootstrapKeys) string {
	pubHex := strings.TrimPrefix(keys.EdDSAPublicKey, "0x")
	raw, err := hex.DecodeString(pubHex)
	if err != nil || len(raw) != 32 {
		return ""
	}
	addr, err := strkey.Encode(strkey.VersionByteAccountID, raw)
	if err != nil {
		return ""
	}
	return addr
}

// DefaultFeeAggregator implements [ccv.ImplFactory].
// Returns the CLDF Stellar deployer account address when topology omits fee_aggregator for this chain.
func (f *ImplFactory) DefaultFeeAggregator(env *deployment.Environment, chainSelector uint64) string {
	stellarChains := env.BlockChains.StellarChains()
	if chain, ok := stellarChains[chainSelector]; ok && chain.Signer != nil {
		return chain.Signer.Address()
	}
	return ""
}

// SupportsFunding reports whether this chain family supports native token
// funding of executor addresses. Families that lack on-chain transfer
// primitives in devenv (e.g. Canton) return false.
func (f *ImplFactory) SupportsFunding() bool {
	return true
}

// SupportsBootstrapExecutor reports whether executors for this family
// use the bootstrap.Run lifecycle (JD-managed with DB). Families that
// use standalone executors (legacy mode, no bootstrap) return false.
func (f *ImplFactory) SupportsBootstrapExecutor() bool {
	return true
}

// GenerateTransmitterKey generates a fresh private key for executor
// transaction signing in the native format for this chain family.
// Returns the hex-encoded private key string.
func (f *ImplFactory) GenerateTransmitterKey() (string, error) {
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return "", fmt.Errorf("generate transmitter seed: %w", err)
	}
	return hex.EncodeToString(seed[:]), nil
}

// TransmitterAddress implements [ccv.ImplFactory].
func (f *ImplFactory) TransmitterAddress(privateKeyHex string) (protocol.UnknownAddress, error) {
	kp, err := cldfstellar.KeypairFromHex(privateKeyHex)
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("invalid Stellar transmitter private key: %w", err)
	}
	raw, err := strkey.Decode(strkey.VersionByteAccountID, kp.Address())
	if err != nil {
		return protocol.UnknownAddress{}, fmt.Errorf("decode Stellar account id: %w", err)
	}
	return protocol.UnknownAddress(raw), nil
}

// NewEmpty implements [registry.ImplFactory].
// Returns a bare Chain used by NewEnvironment() to call DeployLocalNetwork and
// the shared DeployChainContracts path (Pre/GetDeployChainContractsCfg/Post).
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

		fqKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(fee_quoter.ContractType),
			semver.MustParse(fee_quoter.Deploy.Version()),
			"",
		)
		fqRef, err := env.DataStore.Addresses().Get(fqKey)
		if err == nil && fqRef.Address != "" {
			fqContractID, convErr := scval.HexToContractStrkey(fqRef.Address)
			if convErr == nil {
				chain.feeQuoterClient = fqbindings.NewFeeQuoterClient(deployer, fqContractID)
			}
		}

		vvrKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
			versioned_verifier_resolver.Version,
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
			committee_verifier.Version,
			devenvcommon.DefaultCommitteeVerifierQualifier,
		)
		cvRef, err := env.DataStore.Addresses().Get(cvKey)
		if err == nil && cvRef.Address != "" {
			cvContractID, convErr := scval.HexToContractStrkey(cvRef.Address)
			if convErr == nil {
				chain.cvContractID = cvContractID
			}
		}

		receiverKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(CcipReceiverContractType),
			semver.MustParse("1.0.0"),
			"",
		)
		receiverRef, err := env.DataStore.Addresses().Get(receiverKey)
		if err == nil && receiverRef.Address != "" {
			receiverContractID, convErr := scval.HexToContractStrkey(receiverRef.Address)
			if convErr == nil {
				chain.receiverContractID = receiverContractID
			}
		}

		rmnProxyKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(rmn_proxy.ContractType),
			semver.MustParse(rmn_proxy.Deploy.Version()),
			"",
		)
		rmnProxyRef, err := env.DataStore.Addresses().Get(rmnProxyKey)
		if err == nil && rmnProxyRef.Address != "" {
			rmnProxyContractID, convErr := scval.HexToContractStrkey(rmnProxyRef.Address)
			if convErr == nil {
				chain.rmnProxyContractID = rmnProxyContractID
				chain.rmnProxyClient = rmnproxybindings.NewRmnProxyClient(deployer, rmnProxyContractID)
			}
		}

		rmnRemoteKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(rmn_remote.ContractType),
			semver.MustParse(rmn_remote.Deploy.Version()),
			"",
		)
		rmnRemoteRef, err := env.DataStore.Addresses().Get(rmnRemoteKey)
		if err == nil && rmnRemoteRef.Address != "" {
			rmnRemoteContractID, convErr := scval.HexToContractStrkey(rmnRemoteRef.Address)
			if convErr == nil {
				chain.rmnRemoteContractID = rmnRemoteContractID
				chain.rmnRemoteClient = rmnremotebindings.NewRmnRemoteClient(deployer, rmnRemoteContractID)
			}
		}

		poolKey := datastore.NewAddressRefKey(
			details.ChainSelector,
			datastore.ContractType(stellarccipdevenv.LockReleaseTokenPoolContractType),
			semver.MustParse("1.0.0"),
			stellarccipdevenv.DevenvTestTokenPoolQualifier,
		)
		poolRef, err := env.DataStore.Addresses().Get(poolKey)
		if err == nil && poolRef.Address != "" {
			poolContractID, convErr := scval.HexToContractStrkey(poolRef.Address)
			if convErr == nil {
				chain.tokenPoolContractID = poolContractID
				chain.tokenPoolClient = tokenpoolbindings.NewTokenPoolClient(deployer, poolContractID)
			}
		}
	}

	// Re-derive the deterministic test SAC token address so that token transfer
	// tests can call GetTokenAddress() without re-running deployment.
	issuerSeed := sha256.Sum256([]byte(fmt.Sprintf("test-token-issuer-%s", networkPassphrase)))
	issuerKP, err := keypair.FromRawSeed(issuerSeed)
	if err == nil {
		asset := txnbuild.CreditAsset{Code: testTokenAssetCode, Issuer: issuerKP.Address()}
		xdrAsset, xdrErr := asset.ToXDR()
		if xdrErr == nil {
			if contractID, sacErr := stellardeployment.ComputeSACContractID(networkPassphrase, xdrAsset); sacErr == nil {
				chain.testTokenContractID = contractID
			}
		}
	}

	// Re-derive the deterministic fee SAC token address so that SendMessage
	// can reference it without re-running deployment.
	feeIssuerSeed := sha256.Sum256([]byte(fmt.Sprintf("fee-token-issuer-%s", networkPassphrase)))
	feeIssuerKP, err := keypair.FromRawSeed(feeIssuerSeed)
	if err == nil {
		feeAsset := txnbuild.CreditAsset{Code: feeTokenAssetCode, Issuer: feeIssuerKP.Address()}
		xdrAsset, xdrErr := feeAsset.ToXDR()
		if xdrErr == nil {
			if contractID, sacErr := stellardeployment.ComputeSACContractID(networkPassphrase, xdrAsset); sacErr == nil {
				chain.feeTokenContractID = contractID
			}
		}
	}

	return chain, nil
}
