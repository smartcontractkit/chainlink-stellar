package adapters

import (
	"context"
	"fmt"
	"maps"
	"slices"

	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

var _ ccvadapters.GasPriceValidator = (*StellarChainFamilyAdapter)(nil)

// ValidateGasPricesForLanes implements ccvadapters.GasPriceValidator: a
// read-only preflight asserting the local FeeQuoter carries a non-zero
// USD-per-unit-gas price for every remote lane about to be configured. A
// missing entry is not a local-transaction-gas concern (Soroban has no gas
// market) — it is the price the FeeQuoter uses to quote each remote's
// execution gas, and without it every quote on the lane fails. Running this
// before any chain is written surfaces the failure at lane-configuration
// time instead of at the first send; the deploy seeds one price per remote,
// and the offchain price updater owns them afterwards.
//
// The lane input's USDPerUnitGas override is deliberately ignored, matching
// the sequence: the stellar ConfigureChainForLanes never writes prices (the
// updater owns them), so only the on-chain state can satisfy the check.
func (a *StellarChainFamilyAdapter) ValidateGasPricesForLanes(
	e cldf.Environment,
	chainSelector uint64,
	feeQuoter []byte,
	remoteChains map[uint64]ccvadapters.RemoteChainConfig[[]byte, string],
) error {
	deps, err := stellarReadDeps(e.BlockChains, chainSelector)
	if err != nil {
		return err
	}
	return validateGasPricesForLanes(e.GetContext(), deps, chainSelector, feeQuoter, remoteChains)
}

// validateGasPricesForLanes is the deps-driven core of the preflight.
func validateGasPricesForLanes(
	ctx context.Context,
	deps stellardeps.StellarDeps,
	chainSelector uint64,
	feeQuoter []byte,
	remoteChains map[uint64]ccvadapters.RemoteChainConfig[[]byte, string],
) error {
	fqStrkey, err := scval.BytesToContractStrkey(feeQuoter)
	if err != nil {
		return fmt.Errorf("fee quoter address: %w", err)
	}
	client := fqbindings.NewFeeQuoterClient(deps.Invoker, fqStrkey)
	for _, remote := range slices.Sorted(maps.Keys(remoteChains)) {
		price, err := client.GetDestChainGasPrice(ctx, remote)
		if err != nil {
			return fmt.Errorf(
				"read dest chain gas price for remote %d on FeeQuoter %s (chain %d): %w",
				remote, fqStrkey, chainSelector, err)
		}
		if price == nil || price.Value == (scval.U128{}) {
			return fmt.Errorf(
				"FeeQuoter %s (chain %d) has no gas price for destination chain %d: every quote on that lane fails — seed it with the offchain price updater before configuring the lane",
				fqStrkey, chainSelector, remote)
		}
	}
	return nil
}
