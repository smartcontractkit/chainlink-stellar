package ccip

import (
	"context"
	"testing"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockReleasePoolAddressRefDataStore(t *testing.T) {
	t.Run("invalid_pool_strkey", func(t *testing.T) {
		_, err := LockReleasePoolAddressRefDataStore(42, "not-a-strkey", "")
		require.Error(t, err)
	})

	t.Run("pool_only", func(t *testing.T) {
		poolID := stellarutil.MustGenerateMockContractID("deployer", "pool-ref-test")
		ds, err := LockReleasePoolAddressRefDataStore(4242, poolID, "")
		require.NoError(t, err)
		addrs, err := ds.Addresses().Fetch()
		require.NoError(t, err)
		require.Len(t, addrs, 1)
		ref := addrs[0]
		assert.Equal(t, uint64(4242), ref.ChainSelector)
		assert.Equal(t, datastore.ContractType(LockReleaseTokenPoolContractType), ref.Type)
		assert.Equal(t, stellarops.ContractDeploymentVersion, ref.Version)
		assert.Equal(t, DevenvLegacyLockReleasePoolQualifier, ref.Qualifier)
		assert.NotEmpty(t, ref.Address)
	})

	t.Run("pool_and_token", func(t *testing.T) {
		poolID := stellarutil.MustGenerateMockContractID("deployer", "pool-ref-test")
		tokenID := stellarutil.MustGenerateMockContractID("deployer", "token-ref-test")
		ds, err := LockReleasePoolAddressRefDataStore(4242, poolID, tokenID)
		require.NoError(t, err)
		addrs, err := ds.Addresses().Fetch()
		require.NoError(t, err)
		require.Len(t, addrs, 2)

		poolFound, tokenFound := false, false
		for _, ref := range addrs {
			assert.Equal(t, uint64(4242), ref.ChainSelector)
			assert.NotEmpty(t, ref.Address)
			switch ref.Type {
			case datastore.ContractType(LockReleaseTokenPoolContractType):
				poolFound = true
				assert.Equal(t, DevenvLegacyLockReleasePoolQualifier, ref.Qualifier)
			case datastore.ContractType(TestTokenContractType):
				tokenFound = true
				assert.Equal(t, DevenvTestTokenPoolQualifier, ref.Qualifier)
			}
		}
		assert.True(t, poolFound, "pool address ref not found")
		assert.True(t, tokenFound, "token address ref not found")
	})
}

func TestDevenvTokenPoolsAddressRefDataStore(t *testing.T) {
	siloedID := stellarutil.MustGenerateMockContractID("deployer", "siloed-pool")
	legacyID := stellarutil.MustGenerateMockContractID("deployer", "legacy-pool")
	lockBoxID := stellarutil.MustGenerateMockContractID("deployer", "lock-box")
	tokenID := stellarutil.MustGenerateMockContractID("deployer", "test-token")

	ds, err := DevenvTokenPoolsAddressRefDataStore(4242, siloedID, legacyID, lockBoxID, tokenID)
	require.NoError(t, err)
	addrs, err := ds.Addresses().Fetch()
	require.NoError(t, err)
	require.Len(t, addrs, 4)

	found := map[datastore.ContractType]bool{}
	for _, ref := range addrs {
		found[ref.Type] = true
		assert.Equal(t, uint64(4242), ref.ChainSelector)
		switch ref.Type {
		case datastore.ContractType(SiloedLockReleaseTokenPoolContractType),
			datastore.ContractType(TokenLockBoxContractType),
			datastore.ContractType(TestTokenContractType):
			assert.Equal(t, DevenvTestTokenPoolQualifier, ref.Qualifier)
		case datastore.ContractType(LockReleaseTokenPoolContractType):
			assert.Equal(t, DevenvLegacyLockReleasePoolQualifier, ref.Qualifier)
		}
	}
	assert.True(t, found[datastore.ContractType(SiloedLockReleaseTokenPoolContractType)])
	assert.True(t, found[datastore.ContractType(LockReleaseTokenPoolContractType)])
	assert.True(t, found[datastore.ContractType(TokenLockBoxContractType)])
	assert.True(t, found[datastore.ContractType(TestTokenContractType)])
}

func TestApplyFeeQuoterTokenConfig_validation(t *testing.T) {
	ctx := context.Background()
	dummy := fee_quoter.NewFeeQuoterClient(nil, "dummy")

	t.Run("nil_client", func(t *testing.T) {
		err := ApplyFeeQuoterTokenConfig(ctx, nil, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", "token", []uint64{1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fee quoter client is nil")
	})

	t.Run("empty_test_token", func(t *testing.T) {
		err := ApplyFeeQuoterTokenConfig(ctx, dummy, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", "", []uint64{1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token contract id is empty")
	})

	t.Run("empty_price_updater", func(t *testing.T) {
		err := ApplyFeeQuoterTokenConfig(ctx, dummy, "", "token", []uint64{1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "price updater address is empty")
	})
}
