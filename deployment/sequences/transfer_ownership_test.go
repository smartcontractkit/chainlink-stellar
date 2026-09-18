package sequences

import (
	"encoding/json"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	cldf_stellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	stellarbindings "github.com/smartcontractkit/chainlink-stellar/bindings"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/require"
)

func TestStellarTransferOwnershipViaMCMS_sequenceMetadata(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-seq-transfer-ownership-via-mcms", StellarTransferOwnershipViaMCMS.ID())
	require.Equal(t, deploy.MCMSVersion.String(), StellarTransferOwnershipViaMCMS.Version())
}

func TestStellarAcceptOwnership_sequenceMetadata(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-seq-accept-ownership", StellarAcceptOwnership.ID())
	require.Equal(t, deploy.MCMSVersion.String(), StellarAcceptOwnership.Version())
}

func TestStellarMCMSTxAdditionalFields_isValidStellarJSON(t *testing.T) {
	t.Parallel()
	raw := stellarMCMSTxAdditionalFields()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	require.EqualValues(t, 1, m["version"])
	require.Equal(t, "stellar", m["family"])
}

func TestStellarTransferOwnershipViaMCMS_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	in := StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: sel,
			ContractRef:   nil,
			ProposedOwner: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarTransferOwnershipViaMCMS, chains, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarTransferOwnershipViaMCMS_SDKOnlySignerEmptyContractRefs(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420020)
	ch := cldf_stellar.Chain{
		ChainMetadata: cldf_stellar.ChainMetadata{Selector: sel},
		Signer:        testStellarSigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: sel,
			ContractRef:   nil,
			ProposedOwner: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	out, err := cldf_ops.ExecuteSequence(b, StellarTransferOwnershipViaMCMS, chains, in)
	require.NoError(t, err)
	require.Empty(t, out.Output.BatchOps)
}

func TestStellarAcceptOwnership_SDKOnlySignerEmptyContractRefs(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420022)
	ch := cldf_stellar.Chain{
		ChainMetadata: cldf_stellar.ChainMetadata{Selector: sel},
		Signer:        testStellarSigner{addr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"},
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: sel,
			ContractRef:   nil,
			ProposedOwner: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	out, err := cldf_ops.ExecuteSequence(b, StellarAcceptOwnership, chains, in)
	require.NoError(t, err)
	require.Empty(t, out.Output.BatchOps)
}

func TestStellarTransferOwnershipViaMCMS_EmptyContractRefsReturnsNoBatchOps(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420021)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: sel,
			ContractRef:   nil,
			ProposedOwner: kp.Address(),
		},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	out, err := cldf_ops.ExecuteSequence(b, StellarTransferOwnershipViaMCMS, chains, in)
	require.NoError(t, err)
	require.Empty(t, out.Output.BatchOps)
}

func TestStellarAcceptOwnership_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	in := StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: sel,
			ContractRef:   nil,
			ProposedOwner: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarAcceptOwnership, chains, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestStellarAcceptOwnership_EmptyContractRefsReturnsNoBatchOps(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := uint64(424242420023)
	kp := keypair.MustRandom()
	ch := cldf_stellar.Chain{
		ChainMetadata:     cldf_stellar.ChainMetadata{Selector: sel},
		Signer:            stellarbindings.NewStellarKeypairSigner(kp),
		Client:            nil,
		NetworkPassphrase: "Standalone Network ; February 2017",
	}
	chains := cldf_chain.NewBlockChains(map[uint64]cldf_chain.BlockChain{sel: ch})
	in := StellarTransferOwnershipInput{
		TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
			ChainSelector: sel,
			ContractRef:   nil,
			ProposedOwner: kp.Address(),
		},
		GovernanceAddr: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
	}
	out, err := cldf_ops.ExecuteSequence(b, StellarAcceptOwnership, chains, in)
	require.NoError(t, err)
	require.Empty(t, out.Output.BatchOps)
}
