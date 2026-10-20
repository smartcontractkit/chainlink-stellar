package ccvchain

import (
	"context"
	"fmt"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	bnmops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/bnm_token"
	sacops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/sac_token"
	stellardeps "github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
)

// Lock-box liquidity seeded per shared-pipeline lock-release pool (EVM
// fundLockReleaseTokenPool parity: the box holds all liquidity, never the
// pool): whole tokens at the BnM token's 7 decimals (chain.go
// bnmTokenDecimals) — ≈10% of the 1e9 pre-mint, ample for the 1000-token
// inbound e2e cases per pool.
const (
	lrLockBoxLiquidityWholeTokens uint64 = 100_000_000
	lrBnmTokenBaseUnit            int64  = 10_000_000 // 10^7 (7 decimals)
	lrApproveLedgerBuffer         uint32 = 1_000
)

// fundLockReleasePoolLockBoxes seeds the lock box of every deployed
// shared-pipeline lock-release pool (PostTokenDeploy): approve the box to
// pull the pair's BnM token from the deployer, then deposit. The box pulls via
// transfer_from, so the pool's own token balance stays fees-only.
func (c *Chain) fundLockReleasePoolLockBoxes(env *deployment.Environment, selector uint64, deployedRefs []datastore.AddressRef) error {
	var lrQualifiers []string
	for _, ref := range deployedRefs {
		if string(ref.Type) == stellarccip.DevenvCatalogLockReleaseTokenPoolType {
			lrQualifiers = append(lrQualifiers, ref.Qualifier)
		}
	}
	if len(lrQualifiers) == 0 {
		return nil
	}
	if c.deployer == nil {
		return fmt.Errorf("stellar deployer not initialized")
	}
	if c.deployerKeypair == nil {
		return fmt.Errorf("deployer keypair not initialized")
	}
	deps := stellardeps.FromDeployer(c.deployer)
	deployerAddr := c.deployerKeypair.Address()
	ctx := context.Background()

	for _, qualifier := range lrQualifiers {
		boxStrkey, err := stellarccip.TokenLockBoxDatastoreRef(qualifier).LookupStrkey(env.DataStore, selector)
		if err != nil {
			return fmt.Errorf("lookup lock box for lock-release pool %q: %w", qualifier, err)
		}
		tokenRef, err := stellarccip.LookupAddressRef(env.DataStore, selector,
			datastore.ContractType(bnmops.ContractType), stellarops.ContractDeploymentVersion, qualifier)
		if err != nil {
			return fmt.Errorf("lookup BnM token for lock-release pool %q: %w", qualifier, err)
		}
		tokenStrkey, err := scval.HexToContractStrkey(tokenRef.Address)
		if err != nil {
			return fmt.Errorf("convert BnM token address for %q: %w", qualifier, err)
		}

		// Expiration: latest ledger + buffer (the deposit lands within seconds);
		// far-future fallback when the ledger read fails (bespoke path parity).
		expirationLedger := uint32(9_999_999)
		if latest, err := c.rpcClient.GetLatestLedger(ctx); err == nil {
			expirationLedger = latest.Sequence + lrApproveLedgerBuffer
		} else {
			c.logger.Warn().Err(err).Msg("lock-box funding: could not read latest ledger; using far-future SAC approve expiration")
		}
		amount := int64(lrLockBoxLiquidityWholeTokens) * lrBnmTokenBaseUnit
		if _, err := cldf_ops.ExecuteOperation(env.OperationsBundle, sacops.Approve, deps, sacops.ApproveInput{
			ContractID:       tokenStrkey,
			From:             deployerAddr,
			Spender:          boxStrkey,
			Amount:           amount,
			ExpirationLedger: expirationLedger,
		}); err != nil {
			return fmt.Errorf("approve lock box %s to pull BnM token for pool %q: %w", boxStrkey, qualifier, err)
		}
		if _, err := cldf_ops.ExecuteOperation(env.OperationsBundle, tlbops.Deposit, deps, tlbops.DepositInput{
			ContractID: boxStrkey,
			Caller:     deployerAddr,
			Amount:     amount,
		}); err != nil {
			return fmt.Errorf("deposit liquidity into lock box %s for pool %q: %w", boxStrkey, qualifier, err)
		}
		c.logger.Info().
			Str("lockBox", boxStrkey).
			Str("poolQualifier", qualifier).
			Int64("amount", amount).
			Msg("Funded shared lock-release pool lock box with BnM liquidity")
	}
	return nil
}
