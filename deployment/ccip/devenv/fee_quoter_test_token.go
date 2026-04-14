package devenv

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ApplyFeeQuoterTestTokenConfig sets oracle price and per-destination token transfer fee
// configs for the devenv test SAC token on FeeQuoter.
func ApplyFeeQuoterTestTokenConfig(
	ctx context.Context,
	feeQuoterClient *fee_quoter.FeeQuoterClient,
	caller string,
	testToken string,
	allSelectors []uint64,
) error {
	if feeQuoterClient == nil {
		return fmt.Errorf("fee quoter client is nil")
	}
	if testToken == "" {
		return fmt.Errorf("test token contract id is empty")
	}
	tokenPriceUpdates := fee_quoter.PriceUpdates{
		TokenPriceUpdates: []fee_quoter.TokenPriceUpdate{{
			Token:       testToken,
			UsdPerToken: scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 1_000_000_000_000_000_000}), // $1
		}},
		GasPriceUpdates: []fee_quoter.GasPriceUpdate{},
	}
	if err := feeQuoterClient.UpdatePrices(ctx, caller, tokenPriceUpdates); err != nil {
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
