package devenv

import (
	"context"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLockReleasePoolAddressRefDataStore(t *testing.T) {
	t.Run("invalid_pool_strkey", func(t *testing.T) {
		_, err := LockReleasePoolAddressRefDataStore(42, "not-a-strkey")
		require.Error(t, err)
	})

	t.Run("valid_pool", func(t *testing.T) {
		poolID := stellarutil.MustGenerateMockContractID("deployer", "pool-ref-test")
		ds, err := LockReleasePoolAddressRefDataStore(4242, poolID)
		require.NoError(t, err)
		addrs, err := ds.Addresses().Fetch()
		require.NoError(t, err)
		require.Len(t, addrs, 1)
		ref := addrs[0]
		assert.Equal(t, uint64(4242), ref.ChainSelector)
		assert.Equal(t, datastore.ContractType(LockReleaseTokenPoolContractType), ref.Type)
		assert.Equal(t, semver.MustParse("1.0.0"), ref.Version)
		assert.Equal(t, DevenvTestTokenPoolQualifier, ref.Qualifier)
		assert.NotEmpty(t, ref.Address)
	})
}

func TestApplyFeeQuoterTestTokenConfig_validation(t *testing.T) {
	ctx := context.Background()
	dummy := fee_quoter.NewFeeQuoterClient(nil, "dummy")

	t.Run("nil_client", func(t *testing.T) {
		err := ApplyFeeQuoterTestTokenConfig(ctx, nil, "caller", "token", []uint64{1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fee quoter client is nil")
	})

	t.Run("empty_test_token", func(t *testing.T) {
		err := ApplyFeeQuoterTestTokenConfig(ctx, dummy, "caller", "", []uint64{1})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "test token contract id is empty")
	})
}
