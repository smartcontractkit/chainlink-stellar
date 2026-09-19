package sequences

import (
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"
)

func TestStellarSetTokenTransferFee_sequenceMetadata(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-set-token-transfer-fee", StellarSetTokenTransferFee.ID())
	require.Equal(t, stellarops.ContractDeploymentVersion.String(), StellarSetTokenTransferFee.Version())
}

func TestStellarApplyDestChainConfig_sequenceMetadata(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-apply-dest-chain-config", StellarApplyDestChainConfig.ID())
	require.Equal(t, stellarops.ContractDeploymentVersion.String(), StellarApplyDestChainConfig.Version())
}

func TestApplyStellarFeeAggregator_RejectsNilDatastore(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	chains := cldf_chain.NewBlockChains(nil)
	env := cldf.Environment{}
	kp := keypair.MustRandom()
	_, err := ApplyStellarFeeAggregator(b, chains, env, fees.FeeAggregatorForChain{
		ChainSelector: chainsel.STELLAR_LOCALNET.Selector,
		FeeAggregator: kp.Address(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DataStore")
}

func TestApplyStellarFeeAggregator_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	chains := cldf_chain.NewBlockChains(nil)
	env := cldf.Environment{DataStore: datastore.NewMemoryDataStore().Seal()}
	kp := keypair.MustRandom()
	_, err := ApplyStellarFeeAggregator(b, chains, env, fees.FeeAggregatorForChain{
		ChainSelector: chainsel.STELLAR_LOCALNET.Selector,
		FeeAggregator: kp.Address(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarSetTokenTransferFee_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	in := StellarSetTokenTransferFeeInput{
		SetTokenTransferFeeSequenceInput: fees.SetTokenTransferFeeSequenceInput{
			Selector: sel,
			Settings: map[uint64]map[string]*fees.TokenTransferFeeArgs{
				123: {"TOKEN": {IsEnabled: true, MinFeeUSDCents: 1}},
			},
		},
		FQContractID: "CCONTRACTTESTFEE00000000000000000000000",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarSetTokenTransferFee, chains, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarSetTokenTransferFee_EmptySettingsSkipsOnChainCall(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420031)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarSetTokenTransferFeeInput{
		SetTokenTransferFeeSequenceInput: fees.SetTokenTransferFeeSequenceInput{
			Selector: sel,
			Settings: nil,
		},
		FQContractID: "CCONTRACTTESTFEE00000000000000000000000",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarSetTokenTransferFee, chains, in)
	require.NoError(t, err)
}

func TestStellarSetTokenTransferFee_OnlyNilFeeArgsSkipsOnChainCall(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420032)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarSetTokenTransferFeeInput{
		SetTokenTransferFeeSequenceInput: fees.SetTokenTransferFeeSequenceInput{
			Selector: sel,
			Settings: map[uint64]map[string]*fees.TokenTransferFeeArgs{
				888: {"TOKEN": nil},
			},
		},
		FQContractID: "CCONTRACTTESTFEE00000000000000000000000",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarSetTokenTransferFee, chains, in)
	require.NoError(t, err)
}

func TestStellarApplyDestChainConfig_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	in := StellarApplyDestChainConfigInput{
		ApplyDestChainConfigSequenceInput: fees.ApplyDestChainConfigSequenceInput{
			Selector: sel,
			Settings: map[uint64]lanes.FeeQuoterDestChainConfig{
				123: {IsEnabled: true, MaxDataBytes: 100},
			},
		},
		FQContractID: "CCONTRACTTESTFQDEST00000000000000000000",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarApplyDestChainConfig, chains, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarApplyDestChainConfig_EmptySettingsSkipsOnChainCall(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420034)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarApplyDestChainConfigInput{
		ApplyDestChainConfigSequenceInput: fees.ApplyDestChainConfigSequenceInput{
			Selector: sel,
			Settings: nil,
		},
		FQContractID: "CCONTRACTTESTFQDEST00000000000000000000",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarApplyDestChainConfig, chains, in)
	require.NoError(t, err)
}
