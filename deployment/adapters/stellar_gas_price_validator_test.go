package adapters

import (
	"context"
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// stubDestGasPrice stubs the get_dest_chain_gas_price read for one remote with
// the given value; unset stubs a zero price.
func stubDestGasPrice(t *testing.T, inv *operationstest.RecordingInvoker, fq string, remote uint64, value scval.U128) *operationstest.RecordingInvoker {
	t.Helper()
	price := fqbindings.TimestampedPrice{Timestamp: 1, Value: value}
	val, err := price.ToScVal()
	require.NoError(t, err)
	return inv.WithSimulateResultForContract(fq, "get_dest_chain_gas_price", &val)
}

func remoteChainsForGasPrice(remotes ...uint64) map[uint64]ccvadapters.RemoteChainConfig[[]byte, string] {
	out := make(map[uint64]ccvadapters.RemoteChainConfig[[]byte, string], len(remotes))
	for _, r := range remotes {
		out[r] = ccvadapters.RemoteChainConfig[[]byte, string]{}
	}
	return out
}

func TestValidateGasPricesForLanes(t *testing.T) {
	t.Parallel()
	fq := stellarutil.MustGenerateMockContractID("deployer", "gas-price-validator-test-fq")
	fqBytes, err := strkey.Decode(strkey.VersionByteContract, fq)
	require.NoError(t, err)
	one := scval.U128(xdr.UInt128Parts{Lo: 1_000_000})
	remotes := remoteChainsForGasPrice(7, 8)

	t.Run("all remotes priced returns nil", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		inv = stubDestGasPrice(t, inv, fq, 7, one)
		inv = stubDestGasPrice(t, inv, fq, 8, one)
		err := validateGasPricesForLanes(context.Background(), stellardeps.StellarDeps{Invoker: inv}, 42, fqBytes, remotes)
		require.NoError(t, err)
		require.Len(t, inv.Records(), 2)
	})

	t.Run("one unpriced remote errors naming the lane", func(t *testing.T) {
		t.Parallel()
		// The recording invoker stubs by (contract, fn), so one remote per
		// lane keeps the priced and unpriced reads distinguishable.
		inv := operationstest.NewRecordingInvoker()
		inv = stubDestGasPrice(t, inv, fq, 8, scval.U128{})
		err := validateGasPricesForLanes(context.Background(), stellardeps.StellarDeps{Invoker: inv}, 42, fqBytes, remoteChainsForGasPrice(8))
		require.ErrorContains(t, err, "no gas price for destination chain 8")
	})

	t.Run("read error propagates", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker().
			WithSimulateErrorForContract(fq, "get_dest_chain_gas_price", operationstest.ContractErrorCode(1))
		err := validateGasPricesForLanes(context.Background(), stellardeps.StellarDeps{Invoker: inv}, 42, fqBytes, remotes)
		require.ErrorContains(t, err, "read dest chain gas price")
	})

	t.Run("bad fee quoter address errors", func(t *testing.T) {
		t.Parallel()
		inv := operationstest.NewRecordingInvoker()
		err := validateGasPricesForLanes(context.Background(), stellardeps.StellarDeps{Invoker: inv}, 42, []byte{1, 2, 3}, remotes)
		require.ErrorContains(t, err, "fee quoter address")
	})
}

// TestStellarChainFamilyAdapter_GasPriceValidatorInterface pins the interface
// compliance and that the globally registered ChainFamily instance
// type-asserts to GasPriceValidator, which is how the topology changeset
// discovers it.
func TestStellarChainFamilyAdapter_GasPriceValidatorInterface(t *testing.T) {
	t.Parallel()
	var _ ccvadapters.GasPriceValidator = (*StellarChainFamilyAdapter)(nil)

	family, ok := ccvadapters.GetChainFamilyRegistry().GetChainFamily(chainsel.FamilyStellar)
	require.True(t, ok)
	_, ok = family.(ccvadapters.GasPriceValidator)
	require.True(t, ok, "registered ChainFamily must implement GasPriceValidator")
}
