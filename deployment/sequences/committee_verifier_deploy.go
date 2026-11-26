package sequences

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
)

// DeployCommitteeVerifierInput creates the CommitteeVerifier
// contract. StorageLocations is a required input (G4): the deploy never invents
// one. AllowlistAdmin and FeeAggregator default to the owner, VersionTag to the
// default committee-verifier tag, and RmnProxy is a required dependency.
// An empty Qualifier deploys the default instance; a non-empty one deploys a
// distinct instance (separate datastore ref, salt and op reports) for extra,
// non-default verifiers.
type DeployCommitteeVerifierInput struct {
	ChainSelector     uint64                 `json:"chainSelector"`
	Qualifier         string                 `json:"qualifier,omitempty"`
	Owner             string                 `json:"owner,omitempty"`
	WasmPath          string                 `json:"wasmPath"`
	StorageLocations  [][]byte               `json:"storageLocations"`
	RmnProxy          string                 `json:"rmnProxy"`
	AllowlistAdmin    string                 `json:"allowlistAdmin,omitempty"`
	FeeAggregator     string                 `json:"feeAggregator,omitempty"`
	VersionTag        [4]byte                `json:"versionTag"`
	ExistingAddresses []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployCommitteeVerifier = cldf_ops.NewSequence(
	"stellar-deploy-committee-verifier",
	SequenceVersion,
	"Creates the CommitteeVerifier Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployCommitteeVerifierInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		allowlistAdmin := in.AllowlistAdmin
		if allowlistAdmin == "" {
			allowlistAdmin = owner
		}
		feeAggregator := in.FeeAggregator
		if feeAggregator == "" {
			feeAggregator = owner
		}
		versionTag := in.VersionTag
		if versionTag == [4]byte{} {
			versionTag = stellarutil.DefaultCommitteeVerifierVersionTag()
		}
		if err := statReleaseWasm(in.WasmPath, "Committee Verifier"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if len(in.StorageLocations) == 0 {
			return ComponentDeployOutput{}, fmt.Errorf("storageLocations is required: pass the committee storage locations")
		}
		if in.RmnProxy == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnProxy is required: deploy RMN Proxy first")
		}
		dynamicConfig := cvbindings.DynamicConfig{
			AllowlistAdmin: &allowlistAdmin,
			FeeAggregator:  &feeAggregator,
		}
		ctorArgs, err := cvbindings.ConstructorArgs(owner, dynamicConfig, in.StorageLocations, in.RmnProxy, versionTag)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode CommitteeVerifier constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, cvops.Deploy,
			stellarccip.CommitteeVerifierDatastoreRefWithQualifier(in.Qualifier), in.ChainSelector, componentSaltLabel("committee-verifier", in.Qualifier), in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := cvbindings.NewCommitteeVerifierClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				dynamic, err := c.GetDynamicConfig(ctx)
				if err != nil {
					return err
				}
				if err := requireField("allowlist admin", ptrString(dynamic.AllowlistAdmin), allowlistAdmin); err != nil {
					return err
				}
				if err := requireField("fee aggregator", ptrString(dynamic.FeeAggregator), feeAggregator); err != nil {
					return err
				}
				locations, err := c.GetStorageLocations(ctx)
				if err != nil {
					return err
				}
				if !slices.EqualFunc(locations, in.StorageLocations, bytes.Equal) {
					return fmt.Errorf("storage locations are %x, expected %x", locations, in.StorageLocations)
				}
				gotRmnProxy, err := c.GetRmnProxy(ctx)
				if err != nil {
					return err
				}
				if err := requireField("rmn proxy", ptrString(gotRmnProxy), in.RmnProxy); err != nil {
					return err
				}
				// The generated VersionTag submits a transaction; readback only simulates.
				tagVal, err := deps.Invoker.SimulateContract(ctx, contractID, "version_tag", nil)
				if err != nil {
					return fmt.Errorf("read version tag: %w", err)
				}
				if tagVal == nil {
					return fmt.Errorf("no return value from version_tag")
				}
				gotTag, err := scval.Bytes4FromScVal(*tagVal)
				if err != nil {
					return err
				}
				return requireField("version tag", gotTag, versionTag)
			})
	},
)
