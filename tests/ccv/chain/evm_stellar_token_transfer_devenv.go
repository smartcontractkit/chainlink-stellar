package ccvchain

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/Masterminds/semver/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	chainsel "github.com/smartcontractkit/chain-selectors"
	evmtar "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_5_0/operations/token_admin_registry"
	evmregister "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_5_0/sequences"
	evmtokenpool "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/token_pool"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	ccvchainprod "github.com/smartcontractkit/chainlink-stellar/ccv/chain"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

func (c *Chain) configureEVMToStellarTokenTransfers(env *deployment.Environment, stellarSelector uint64, remoteSelectors []uint64) error {
	allRefs, err := env.DataStore.Addresses().Fetch()
	if err != nil {
		return fmt.Errorf("fetch datastore addresses: %w", err)
	}

	poolRef := stellarccip.LockReleasePoolDevenvDatastoreRef()
	stellarPool, err := stellarccip.LookupAddressRef(
		env.DataStore,
		stellarSelector,
		poolRef.Type,
		poolRef.Version,
		poolRef.Qualifier,
	)
	if err != nil {
		return fmt.Errorf("lookup Stellar siloed lock-release pool: %w", err)
	}
	tokenRef := stellarccip.DevenvTestTokenDatastoreRef()
	stellarToken, err := stellarccip.LookupAddressRef(
		env.DataStore,
		stellarSelector,
		tokenRef.Type,
		tokenRef.Version,
		tokenRef.Qualifier,
	)
	if err != nil {
		return fmt.Errorf("lookup Stellar SAC token: %w", err)
	}

	for _, remoteSelector := range remoteSelectors {
		family, err := chainsel.GetSelectorFamily(remoteSelector)
		if err != nil {
			return fmt.Errorf("get selector family for %d: %w", remoteSelector, err)
		}
		if family != chainsel.FamilyEVM {
			continue
		}

		evmPool, evmToken, found := ccvchainprod.ResolveEVMTokenPoolForStellar(allRefs, remoteSelector)
		if !found {
			c.logger.Warn().
				Uint64("evmChain", remoteSelector).
				Msg("No EVM BurnMint token pool found for Stellar token transfer setup")
			continue
		}

		if err := c.registerEVMTokenPool(env, remoteSelector, evmPool, evmToken); err != nil {
			return err
		}
		if err := c.configureEVMPoolRemote(env, remoteSelector, stellarSelector, evmPool, stellarPool, stellarToken); err != nil {
			return err
		}

		c.logger.Info().
			Uint64("evmChain", remoteSelector).
			Uint64("stellarChain", stellarSelector).
			Str("evmToken", evmToken.Address).
			Str("evmPool", evmPool.Address).
			Str("stellarToken", stellarToken.Address).
			Str("stellarPool", stellarPool.Address).
			Msg("Configured EVM-to-Stellar token transfer pair")
	}
	return nil
}

func (c *Chain) registerEVMTokenPool(env *deployment.Environment, evmSelector uint64, evmPool, evmToken datastore.AddressRef) error {
	evmChain, ok := env.BlockChains.EVMChains()[evmSelector]
	if !ok {
		return fmt.Errorf("EVM chain %d not found in environment", evmSelector)
	}

	tokenAdminRegistry, err := stellarccip.LookupAddressRef(
		env.DataStore,
		evmSelector,
		datastore.ContractType(evmtar.ContractType),
		semver.MustParse("1.5.0"),
		"",
	)
	if err != nil {
		return fmt.Errorf("lookup EVM TokenAdminRegistry for chain %d: %w", evmSelector, err)
	}

	tokenAddress, err := evmAddressFromRef(evmToken)
	if err != nil {
		return err
	}
	poolAddress, err := evmAddressFromRef(evmPool)
	if err != nil {
		return err
	}
	registryAddress, err := evmAddressFromRef(tokenAdminRegistry)
	if err != nil {
		return err
	}

	_, err = cldf_ops.ExecuteSequence(
		env.OperationsBundle,
		evmregister.RegisterToken,
		evmChain,
		evmregister.RegisterTokenInput{
			ChainSelector:             evmSelector,
			TokenAddress:              tokenAddress,
			TokenPoolAddress:          poolAddress,
			TokenAdminRegistryAddress: registryAddress,
		},
	)
	if err != nil {
		return fmt.Errorf("register EVM token %s with pool %s on chain %d: %w", evmToken.Address, evmPool.Address, evmSelector, err)
	}
	return nil
}

func (c *Chain) configureEVMPoolRemote(
	env *deployment.Environment,
	evmSelector uint64,
	stellarSelector uint64,
	evmPool datastore.AddressRef,
	stellarPool datastore.AddressRef,
	stellarToken datastore.AddressRef,
) error {
	evmChain, ok := env.BlockChains.EVMChains()[evmSelector]
	if !ok {
		return fmt.Errorf("EVM chain %d not found in environment", evmSelector)
	}
	poolAddress, err := evmAddressFromRef(evmPool)
	if err != nil {
		return err
	}
	remotePoolBytes, err := paddedAddressBytes(stellarPool)
	if err != nil {
		return err
	}
	remoteTokenBytes, err := paddedAddressBytes(stellarToken)
	if err != nil {
		return err
	}

	alreadyConfigured, err := evmPoolHasRemote(env, evmSelector, poolAddress, stellarSelector, remotePoolBytes, remoteTokenBytes)
	if err != nil {
		return fmt.Errorf("check EVM pool remote config for pool %s on chain %d: %w", evmPool.Address, evmSelector, err)
	}
	if alreadyConfigured {
		return nil
	}

	disabledRL := evmtokenpool.Config{
		IsEnabled: false,
		Capacity:  big.NewInt(0),
		Rate:      big.NewInt(0),
	}
	_, err = cldf_ops.ExecuteOperation(
		env.OperationsBundle,
		evmtokenpool.ApplyChainUpdates,
		evmChain,
		evmcontract.FunctionInput[evmtokenpool.ApplyChainUpdatesArgs]{
			Address:       poolAddress,
			ChainSelector: evmSelector,
			Args: evmtokenpool.ApplyChainUpdatesArgs{
				ChainsToAdd: []evmtokenpool.ChainUpdate{
					{
						RemoteChainSelector:       stellarSelector,
						RemotePoolAddresses:       [][]byte{remotePoolBytes},
						RemoteTokenAddress:        remoteTokenBytes,
						OutboundRateLimiterConfig: disabledRL,
						InboundRateLimiterConfig:  disabledRL,
					},
				},
			},
		},
	)
	if err != nil {
		return fmt.Errorf("apply EVM pool remote for pool %s to Stellar chain %d: %w", evmPool.Address, stellarSelector, err)
	}
	return nil
}

func evmPoolHasRemote(
	env *deployment.Environment,
	evmSelector uint64,
	poolAddress common.Address,
	stellarSelector uint64,
	stellarPool []byte,
	stellarToken []byte,
) (bool, error) {
	evmChain := env.BlockChains.EVMChains()[evmSelector]
	supportedChains, err := cldf_ops.ExecuteOperation(
		env.OperationsBundle,
		evmtokenpool.GetSupportedChains,
		evmChain,
		evmcontract.FunctionInput[struct{}]{
			Address:       poolAddress,
			ChainSelector: evmSelector,
			Args:          struct{}{},
		},
	)
	if err != nil {
		return false, err
	}
	if !uint64SliceContains(supportedChains.Output, stellarSelector) {
		return false, nil
	}

	remotePools, err := cldf_ops.ExecuteOperation(
		env.OperationsBundle,
		evmtokenpool.GetRemotePools,
		evmChain,
		evmcontract.FunctionInput[uint64]{
			Address:       poolAddress,
			ChainSelector: evmSelector,
			Args:          stellarSelector,
		},
	)
	if err != nil {
		return false, err
	}
	remoteToken, err := cldf_ops.ExecuteOperation(
		env.OperationsBundle,
		evmtokenpool.GetRemoteToken,
		evmChain,
		evmcontract.FunctionInput[uint64]{
			Address:       poolAddress,
			ChainSelector: evmSelector,
			Args:          stellarSelector,
		},
	)
	if err != nil {
		return false, err
	}

	if bytes.Equal(remoteToken.Output, stellarToken) && bytesSliceContains(remotePools.Output, stellarPool) {
		return true, nil
	}
	return false, fmt.Errorf("pool %s already has remote chain %d configured with different pool/token", poolAddress.Hex(), stellarSelector)
}

func evmAddressFromRef(ref datastore.AddressRef) (common.Address, error) {
	if !common.IsHexAddress(ref.Address) {
		return common.Address{}, fmt.Errorf("invalid EVM address for %s %q: %s", ref.Type, ref.Qualifier, ref.Address)
	}
	return common.HexToAddress(ref.Address), nil
}

func paddedAddressBytes(ref datastore.AddressRef) ([]byte, error) {
	raw, err := hexutil.Decode(ref.Address)
	if err != nil {
		return nil, fmt.Errorf("decode address %s: %w", ref.Address, err)
	}
	return common.LeftPadBytes(raw, 32), nil
}

func uint64SliceContains(values []uint64, target uint64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func bytesSliceContains(values [][]byte, target []byte) bool {
	for _, value := range values {
		if bytes.Equal(value, target) {
			return true
		}
	}
	return false
}
