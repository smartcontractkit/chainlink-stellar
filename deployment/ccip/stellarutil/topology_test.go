package stellarutil

import (
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/offchain"
	ccvdeployment "github.com/smartcontractkit/chainlink-ccv/deployment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSignersFromTopology(t *testing.T) {
	evm := chainsel.FamilyEVM
	stellar := chainsel.FamilyStellar

	t.Run("nil_topology", func(t *testing.T) {
		signers, th := ResolveSignersFromTopology(nil, 1, evm)
		assert.Nil(t, signers)
		assert.Equal(t, uint32(0), th)
	})

	t.Run("nil_NOPTopology", func(t *testing.T) {
		topo := &ccvdeployment.EnvironmentTopology{NOPTopology: nil}
		signers, th := ResolveSignersFromTopology(topo, 1, evm)
		assert.Nil(t, signers)
		assert.Equal(t, uint32(0), th)
	})

	t.Run("no_matching_chain", func(t *testing.T) {
		topo := &ccvdeployment.EnvironmentTopology{
			NOPTopology: &ccvdeployment.NOPTopology{
				NOPs: []ccvdeployment.NOPConfig{
					{Alias: "a", Name: "a", SignerAddressByFamily: map[string]string{evm: "1111111111111111111111111111111111111111"}},
				},
				Committees: map[string]ccvdeployment.CommitteeConfig{
					"c": {
						ChainConfigs: map[string]ccvdeployment.ChainCommitteeConfig{
							"999": {NOPAliases: []string{"a"}, Threshold: 1},
						},
					},
				},
			},
		}
		signers, th := ResolveSignersFromTopology(topo, 12345, evm)
		assert.Nil(t, signers)
		assert.Equal(t, uint32(0), th)
	})

	t.Run("sorts_signers_and_returns_threshold", func(t *testing.T) {
		topo := &ccvdeployment.EnvironmentTopology{
			NOPTopology: &ccvdeployment.NOPTopology{
				NOPs: []ccvdeployment.NOPConfig{
					{Alias: "nopA", Name: "nopA", SignerAddressByFamily: map[string]string{evm: "1111111111111111111111111111111111111111"}},
					{Alias: "nopB", Name: "nopB", SignerAddressByFamily: map[string]string{evm: "2222222222222222222222222222222222222222"}},
				},
				Committees: map[string]ccvdeployment.CommitteeConfig{
					"only": {
						ChainConfigs: map[string]ccvdeployment.ChainCommitteeConfig{
							"12345": {
								NOPAliases: []string{"nopB", "nopA"},
								Threshold:  7,
							},
						},
					},
				},
			},
		}
		signers, th := ResolveSignersFromTopology(topo, 12345, evm)
		require.Len(t, signers, 2)
		assert.Equal(t, uint32(7), th)
		var wantA, wantB [32]byte
		copy(wantA[12:], []byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11})
		copy(wantB[12:], []byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22})
		assert.Equal(t, wantA, signers[0])
		assert.Equal(t, wantB, signers[1])
	})

	t.Run("skips_invalid_and_missing_evm_signer", func(t *testing.T) {
		topo := &ccvdeployment.EnvironmentTopology{
			NOPTopology: &ccvdeployment.NOPTopology{
				NOPs: []ccvdeployment.NOPConfig{
					{Alias: "badhex", Name: "badhex", SignerAddressByFamily: map[string]string{evm: "not-hex"}},
					{Alias: "wronglen", Name: "wronglen", SignerAddressByFamily: map[string]string{evm: "111111"}},
					{Alias: "nosvm", Name: "nosvm", SignerAddressByFamily: map[string]string{"other": "1111111111111111111111111111111111111111"}},
					{Alias: "good", Name: "good", SignerAddressByFamily: map[string]string{evm: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
				},
				Committees: map[string]ccvdeployment.CommitteeConfig{
					"only": {
						ChainConfigs: map[string]ccvdeployment.ChainCommitteeConfig{
							"1": {
								NOPAliases: []string{"badhex", "wronglen", "nosvm", "missing", "good"},
								Threshold:  3,
							},
						},
					},
				},
			},
		}
		signers, th := ResolveSignersFromTopology(topo, 1, evm)
		require.Len(t, signers, 1)
		assert.Equal(t, uint32(3), th)
	})

	t.Run("fallbacks_to_stellar_when_evm_empty", func(t *testing.T) {
		addr := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		topo := &ccvdeployment.EnvironmentTopology{
			NOPTopology: &ccvdeployment.NOPTopology{
				NOPs: []ccvdeployment.NOPConfig{
					{Alias: "sv", Name: "sv", SignerAddressByFamily: map[string]string{stellar: addr}},
				},
				Committees: map[string]ccvdeployment.CommitteeConfig{
					"only": {
						ChainConfigs: map[string]ccvdeployment.ChainCommitteeConfig{
							"999": {NOPAliases: []string{"sv"}, Threshold: 2},
						},
					},
				},
			},
		}
		// Preferred EVM first (empty on NOP); should still pick stellar bucket.
		signers, th := ResolveSignersFromTopology(topo, 999, evm)
		require.Len(t, signers, 1)
		assert.Equal(t, uint32(2), th)
	})

	t.Run("preferred_stellar_order", func(t *testing.T) {
		evAddr := "1111111111111111111111111111111111111111"
		stAddr := "2222222222222222222222222222222222222222"
		topo := &ccvdeployment.EnvironmentTopology{
			NOPTopology: &ccvdeployment.NOPTopology{
				NOPs: []ccvdeployment.NOPConfig{
					{Alias: "m", Name: "m", SignerAddressByFamily: map[string]string{evm: evAddr, stellar: stAddr}},
				},
				Committees: map[string]ccvdeployment.CommitteeConfig{
					"only": {
						ChainConfigs: map[string]ccvdeployment.ChainCommitteeConfig{
							"1": {NOPAliases: []string{"m"}, Threshold: 5},
						},
					},
				},
			},
		}
		signers, th := ResolveSignersFromTopology(topo, 1, stellar)
		require.Len(t, signers, 1)
		assert.Equal(t, uint32(5), th)
		var want [32]byte
		copy(want[12:], []byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22})
		assert.Equal(t, want, signers[0])
	})
}

func TestResolveSignersFromOffchainTopology_fallbackStellar(t *testing.T) {
	evm := chainsel.FamilyEVM
	stellar := chainsel.FamilyStellar
	addr := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	topo := &offchain.EnvironmentTopology{
		NOPTopology: &offchain.NOPTopology{
			NOPs: []offchain.NOPConfig{
				{Alias: "sv", Name: "sv", SignerAddressByFamily: map[string]string{stellar: addr}},
			},
			Committees: map[string]offchain.CommitteeConfig{
				"only": {
					ChainConfigs: map[string]offchain.ChainCommitteeConfig{
						"42": {NOPAliases: []string{"sv"}, Threshold: 2},
					},
				},
			},
		},
	}
	signers, th := ResolveSignersFromOffchainTopology(topo, 42, evm)
	require.Len(t, signers, 1)
	assert.Equal(t, uint32(2), th)
}
