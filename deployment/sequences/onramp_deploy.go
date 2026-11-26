package sequences

import (
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
)

// DeployOnRampInput creates the OnRamp contract in one step.
// TokenAdminRegistry, RmnProxy and FeeQuoter are the dependency strkeys,
// required inputs. MaxUsdCentsPerMessage defaults to 500000 ($5000) and
// FeeAggregator to the owner (the monolith's devenv values).
type DeployOnRampInput struct {
	ChainSelector         uint64                 `json:"chainSelector"`
	Owner                 string                 `json:"owner,omitempty"`
	WasmPath              string                 `json:"wasmPath"`
	TokenAdminRegistry    string                 `json:"tokenAdminRegistry"`
	RmnProxy              string                 `json:"rmnProxy"`
	FeeQuoter             string                 `json:"feeQuoter"`
	MaxUsdCentsPerMessage uint32                 `json:"maxUsdCentsPerMessage,omitempty"`
	FeeAggregator         string                 `json:"feeAggregator,omitempty"`
	ExistingAddresses     []datastore.AddressRef `json:"existingAddresses,omitempty"`
}

var DeployOnRamp = cldf_ops.NewSequence(
	"stellar-deploy-onramp",
	SequenceVersion,
	"Creates the OnRamp Soroban contract through its constructor with skip-if-exists",
	func(b cldf_ops.Bundle, deps ComponentDeps, in DeployOnRampInput) (ComponentDeployOutput, error) {
		owner := defaultOwner(in.Owner, deps.DeployerAddress)
		feeAggregator := in.FeeAggregator
		if feeAggregator == "" {
			feeAggregator = owner
		}
		maxUsdCents := in.MaxUsdCentsPerMessage
		if maxUsdCents == 0 {
			// Per-message fee cap in USD cents. Enforced post-conversion via
			// fee_math::usd_cents_to_fee_token (onramp lib.rs:224 → FeeExceedsMaxAllowed),
			// so it is scale-correct under the USDPriceWith18Decimals convention.
			// $5000: at the devenv gas price (~$100/M-gas ≈ 33 gwei) a 350k-gas
			// message costs ~$35, but an Ethereum spike (200 gwei) + complex 1M-gas
			// receiver + non-LINK premium (2×) can reach ~$1200. $5000 admits normal
			// + spike sends while rejecting pathological (>$5k) quotes that signal a
			// gas/oracle anomaly. u32 max is 4.29e9, so 500_000 cents fits easily.
			maxUsdCents = 500_000 // $5000
		}
		if err := statReleaseWasm(in.WasmPath, "OnRamp"); err != nil {
			return ComponentDeployOutput{}, err
		}
		if in.TokenAdminRegistry == "" {
			return ComponentDeployOutput{}, fmt.Errorf("tokenAdminRegistry is required: deploy TokenAdminRegistry first")
		}
		if in.RmnProxy == "" {
			return ComponentDeployOutput{}, fmt.Errorf("rmnProxy is required: deploy RMN Proxy first")
		}
		if in.FeeQuoter == "" {
			return ComponentDeployOutput{}, fmt.Errorf("feeQuoter is required: deploy FeeQuoter first")
		}
		staticConfig := onrampbindings.StaticConfig{
			ChainSelector:         in.ChainSelector,
			TokenAdminRegistry:    in.TokenAdminRegistry,
			RmnProxy:              in.RmnProxy,
			MaxUsdCentsPerMessage: maxUsdCents,
		}
		dynamicConfig := onrampbindings.DynamicConfig{
			FeeQuoter:     in.FeeQuoter,
			FeeAggregator: feeAggregator,
		}
		ctorArgs, err := onrampbindings.ConstructorArgs(owner, staticConfig, dynamicConfig)
		if err != nil {
			return ComponentDeployOutput{}, fmt.Errorf("encode OnRamp constructor args: %w", err)
		}
		ctx := b.GetContext()
		return deployConstructedComponent(ctx, b, deps, onrampops.Deploy,
			stellarccip.OnRampDatastoreRef(), in.ChainSelector, "onramp", in.WasmPath, ctorArgs, in.ExistingAddresses,
			func(contractID string) error {
				c := onrampbindings.NewOnRampClient(deps.Invoker, contractID)
				gotOwner, err := c.Owner(ctx)
				if err != nil {
					return err
				}
				if err := requireOwner(gotOwner, owner); err != nil {
					return err
				}
				static, err := c.GetStaticConfig(ctx)
				if err != nil {
					return err
				}
				if err := requireField("static config", *static, staticConfig); err != nil {
					return err
				}
				dynamic, err := c.GetDynamicConfig(ctx)
				if err != nil {
					return err
				}
				return requireField("dynamic config", *dynamic, dynamicConfig)
			})
	},
)
