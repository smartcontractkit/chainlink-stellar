package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
)

// DeployVVRInput creates the VersionedVerifierResolver
// contract. FeeAggregator defaults to the owner (the monolith's devenv value).
// An empty Qualifier deploys the default instance; a non-empty one deploys a
// distinct instance (separate datastore ref, salt and op reports) for extra,
// non-default committees.
type DeployVVRInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Qualifier         string                 `json:"qualifier,omitempty"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	FeeAggregator     string                 `json:"feeAggregator,omitempty"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployVVR = cldf_ops.NewSequence(
	"stellar-deploy-versioned-verifier-resolver",
	SequenceVersion,
	"Creates the VersionedVerifierResolver Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployVVRInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		feeAggregator := in.FeeAggregator
		if feeAggregator == "" {
			feeAggregator = owner
		}
		if err := statReleaseWasm(in.WasmPath, "VVR"); err != nil {
			return ComponentDeployOutput{}, err
		}
		ctorArgs, err := vvrbindings.ConstructorArgs(owner, feeAggregator)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode VVR constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, vvrops.Deploy,
			stellarccip.VVRDatastoreRefWithQualifier(in.Qualifier), in.ChainSelector, componentSaltLabel("versioned-verifier-resolver", in.Qualifier), in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := vvrbindings.NewVersionedVerifierResolverClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				gotFeeAggregator, err := c.GetFeeAggregator(ctx)
				if err != nil {
					return err
				}
				return requireField("fee aggregator", gotFeeAggregator, feeAggregator)
			})
	},
)
