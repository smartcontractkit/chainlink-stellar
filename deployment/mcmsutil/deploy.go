package mcmsutil

import (
	"context"
	"fmt"

	mcmsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/mcms"

	"github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/cre"

	stellarmcms "github.com/smartcontractkit/mcms/sdk/stellar"
	"github.com/smartcontractkit/mcms/types"
)

// DeployMCMS uploads + instantiates the MCMS.
func DeployMCMS(
	ctx context.Context,
	deployer *deployment.Deployer,
	owner string,
	chainNetworkID [32]byte,
	config *types.Config,
	instanceLabel string,
	salt [32]byte,
) (string, error) {
	if deployer == nil {
		return "", fmt.Errorf("deployer is nil")
	}
	if owner == "" {
		return "", fmt.Errorf("owner is empty")
	}
	if config == nil {
		return "", fmt.Errorf("config is nil")
	}
	if instanceLabel == "" {
		return "", fmt.Errorf("instance label is empty")
	}

	signerAddresses, signerGroups, groupQuorums, groupParents, err :=
		stellarmcms.ConfigToSetConfigInputs(config)
	if err != nil {
		return "", fmt.Errorf("convert mcms config: %w", err)
	}

	wasm, err := cre.Artifact(cre.MCMSWasm)
	if err != nil {
		return "", fmt.Errorf("artifact wasm: %w", err)
	}

	contractID, err := deployer.DeployContractBytes(ctx, wasm, salt)
	if err != nil {
		return "", fmt.Errorf("deploy mcms wasm: %w", err)
	}

	client := mcmsbindings.NewMcmsClient(deployer, contractID)

	if err := client.Initialize(
		ctx,
		owner,
		chainNetworkID,
		signerAddresses,
		signerGroups,
		groupQuorums,
		groupParents,
		instanceLabel,
	); err != nil {
		return "", fmt.Errorf(
			"initialize mcms %s (owner %s): %w",
			contractID,
			owner,
			err,
		)
	}

	return contractID, nil
}
