package ccvchain

import (
	"fmt"
	"strconv"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"

	sourcereader "github.com/smartcontractkit/chainlink-stellar/ccv/source_reader"
)

// StellarChainConfigLoader is a [chainconfig.ChainConfigLoader] that returns
// placeholder blockchain info for each Stellar chain in outputs.
//
// The committee verifier service calls this to hydrate its per-chain configuration
// before the verifier container is launched. For Stellar, the real values (network
// passphrase and Soroban RPC URL) are populated via the bind-mounted config file
// written by the Stellar modifier; here we return placeholder values so that the framework
// knows a Stellar chain entry exists for the given selector.
func StellarChainConfigLoader(outputs []*blockchain.Output) (map[string]any, error) {
	ret := make(map[string]any)

	for _, output := range outputs {
		if output.Family != chainsel.FamilyStellar {
			continue
		}

		details, err := chainsel.GetChainDetailsByChainIDAndFamily(output.ChainID, output.Family)
		if err != nil {
			return nil, fmt.Errorf("get chain details for Stellar chain %s: %w", output.ChainID, err)
		}

		strSelector := strconv.FormatUint(details.ChainSelector, 10)

		// Return basic node info for the Stellar chain.
		// Other values are populated by the bind-mounted config file written by the Stellar modifier.
		// TODO: this can be made more generic and not just specific to ReaderConfig values.
		ret[strSelector] = sourcereader.ReaderConfig{
			NetworkPassphrase: output.NetworkSpecificData.StellarNetwork.NetworkPassphrase,
			SorobanRPCURL:     output.Nodes[0].InternalHTTPUrl,
			// contract IDs are unknown at this point, they are populated by the modifier
			OnRampContractID:    "",
			RMNRemoteContractID: "",
		}
	}

	return ret, nil
}
