package ccvchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/rs/zerolog/log"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccv/build/devenv/chainreg"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar_provider "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar/provider"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
)

// NewCLDFProviderFactory returns a chainreg.CLDFProviderFactory that creates an
// initialized Stellar CLDF BlockChain provider from a blockchain.Input.
func NewCLDFProviderFactory() chainreg.CLDFProviderFactory {
	return func(ctx context.Context, b *blockchain.Input) (cldf_chain.BlockChain, uint64, error) {
		details, err := chainsel.GetChainDetailsByChainIDAndFamily(b.Out.ChainID, chainsel.FamilyStellar)
		if err != nil {
			return nil, 0, err
		}

		if b.Out.NetworkSpecificData == nil || b.Out.NetworkSpecificData.StellarNetwork == nil {
			return nil, 0, fmt.Errorf("stellar network specific data is required")
		}

		networkPassphrase := b.Out.NetworkSpecificData.StellarNetwork.NetworkPassphrase
		friendbotURL := b.Out.NetworkSpecificData.StellarNetwork.FriendbotURL
		sorobanRPCURL := b.Out.Nodes[0].ExternalHTTPUrl

		// Prefer env for production; otherwise derive deterministic keypair from passphrase (stable for tests).
		var deployerKeypairGen cldf_stellar_provider.KeypairGenerator
		if pk := os.Getenv("STELLAR_DEPLOYER_PRIVATE_KEY"); pk != "" {
			deployerKeypairGen = cldf_stellar_provider.KeypairFromHex(pk)
		} else {
			deployerSeed := sha256.Sum256([]byte("deployer-" + networkPassphrase))
			deployerKeypairGen = cldf_stellar_provider.KeypairFromHex(hex.EncodeToString(deployerSeed[:]))
		}

		log.Info().Msgf("Stellar network passphrase: %s", networkPassphrase)
		log.Info().Msgf("Stellar friendbot URL: %s", friendbotURL)
		log.Info().Msgf("Stellar Soroban RPC URL: %s", sorobanRPCURL)
		deployerKeypair, err := deployerKeypairGen.Generate()
		if err != nil {
			return nil, 0, fmt.Errorf("failed to generate deployer keypair: %w", err)
		}
		log.Info().Msgf("Stellar deployer keypair: %s", deployerKeypair.Address())

		p, err := cldf_stellar_provider.NewRPCChainProvider(details.ChainSelector, cldf_stellar_provider.RPCChainProviderConfig{
			NetworkPassphrase:  networkPassphrase,
			FriendbotURL:       friendbotURL,
			SorobanRPCURL:      sorobanRPCURL,
			DeployerKeypairGen: deployerKeypairGen,
		}).Initialize(ctx)
		if err != nil {
			return nil, 0, err
		}

		return p, details.ChainSelector, nil
	}
}
