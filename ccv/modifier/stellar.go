package modifier

import (
	"fmt"

	"github.com/testcontainers/testcontainers-go"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/committeeverifier"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
)

// StellarModifier is a function that modifies a testcontainers.ContainerRequest for Stellar.
func StellarModifier(req testcontainers.ContainerRequest, verifierInput *committeeverifier.Input, outputs []*blockchain.Output) (testcontainers.ContainerRequest, error) {
	// Update name to reflect chain family.
	req.Name = fmt.Sprintf("stellar-%s", verifierInput.ContainerName)

	return req, nil
}
