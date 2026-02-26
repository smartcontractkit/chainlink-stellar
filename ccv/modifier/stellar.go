package modifier

import (
	"fmt"

	"github.com/testcontainers/testcontainers-go"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/services/committeeverifier"
	"github.com/smartcontractkit/chainlink-testing-framework/framework/components/blockchain"
)

func StellarModifier(req testcontainers.ContainerRequest, verifierInput *committeeverifier.Input, outputs []*blockchain.Output) (testcontainers.ContainerRequest, error) {
	req.Name = fmt.Sprintf("stellar-%s", verifierInput.ContainerName)
	return req, nil
}
