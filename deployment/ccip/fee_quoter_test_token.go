package ccip

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ApplyFeeQuoterTestTokenConfig sets oracle price and per-destination token transfer fee
// configs for the devenv test SAC token on FeeQuoter.
// priceUpdater must be an address in FeeQuoter's authorized-callers set and must match the invoker's auth.
func ApplyFeeQuoterTestTokenConfig(
	ctx context.Context,
	feeQuoterClient *fee_quoter.FeeQuoterClient,
	priceUpdater string,
	testToken string,
	allSelectors []uint64,
) error {
	if feeQuoterClient == nil {
		return fmt.Errorf("fee quoter client is nil")
	}
	if testToken == "" {
		return fmt.Errorf("test token contract id is empty")
	}
	if priceUpdater == "" {
		return fmt.Errorf("price updater address is empty")
	}
	tokenPriceUpdates := fee_quoter.PriceUpdates{
		TokenPriceUpdates: []fee_quoter.TokenPriceUpdate{{
			Token: testToken,
			// Devenv test token is a 7-decimal SAC. USDPriceWith18Decimals
			// convention (EVM Internal.Price.usdPerToken) = "USD × 1e18 per 1e18
			// smallest units", scaled by decimals: $1 × 10^(36-7) = 1e29.
			// See contracts/common/helpers/src/fee_math.rs.
			UsdPerToken: scval.U128(xdr.UInt128Parts{ // $1 (7-dec SAC) = 1e29 = $1 × 10^(36-7); split into u64 limbs (Lo alone overflows u64).
				Hi: 5421010862, Lo: 7886392056514347008,
			}),
		}},
		GasPriceUpdates: []fee_quoter.GasPriceUpdate{},
	}
	if err := feeQuoterClient.UpdatePrices(ctx, priceUpdater, tokenPriceUpdates); err != nil {
		return fmt.Errorf("failed to set test token price on FeeQuoter: %w", err)
	}

	tokenFeeConfigs := make([]fee_quoter.TokenFeeConfigArgs, 0, len(allSelectors))
	for _, rs := range allSelectors {
		tokenFeeConfigs = append(tokenFeeConfigs, fee_quoter.TokenFeeConfigArgs{
			Token:             testToken,
			DestChainSelector: rs,
			Config: fee_quoter.TokenTransferFeeConfig{
				FeeUsdCents:       25,
				DestGasOverhead:   90_000,
				DestBytesOverhead: 32,
				IsEnabled:         true,
			},
		})
	}
	if err := feeQuoterClient.ApplyTokenFeeConfigs(ctx, tokenFeeConfigs, nil); err != nil {
		return fmt.Errorf("failed to apply token fee configs on FeeQuoter: %w", err)
	}
	return nil
}
