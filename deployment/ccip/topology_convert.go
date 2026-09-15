package ccip

import (
	"encoding/json"
	"fmt"

	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/offchain"
	ccvdeployment "github.com/smartcontractkit/chainlink-ccv/deployment"
)

// CCVEnvironmentTopologyToOffchain converts a chainlink-ccv topology to the chainlink-ccip
// offchain topology type used by DeployChainContracts. Struct layouts are aligned; JSON
// round-trip avoids manual field mapping.
func CCVEnvironmentTopologyToOffchain(t *ccvdeployment.EnvironmentTopology) (*offchain.EnvironmentTopology, error) {
	if t == nil {
		return nil, nil
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("marshal ccv topology: %w", err)
	}
	var out offchain.EnvironmentTopology
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal to offchain topology: %w", err)
	}
	return &out, nil
}
