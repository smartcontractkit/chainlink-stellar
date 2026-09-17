package sequences

import (
	"encoding/hex"
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	"github.com/stretchr/testify/require"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
)

func TestStellarActivateRMN_sequenceMetadata(t *testing.T) {
	t.Parallel()
	require.Equal(t, "stellar-seq-activate-rmn", StellarActivateRMN.ID())
	require.Equal(t, deploy.MCMSVersion.String(), StellarActivateRMN.Version())
}

func TestStellarActivateRMN_RejectsMissingStellarChain(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	_, err := cldf_ops.ExecuteSequence(b, StellarActivateRMN, chains, StellarActivateRMNInput{
		ChainSelector:     sel,
		ExistingAddresses: fullActivateRMNRefs(t, sel),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in environment")
}

func TestStellarActivateRMN_RejectsMissingFastCurseTimelock(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	refs := fullActivateRMNRefs(t, sel)
	// Drop the UltraFastCurse timelock, keep everything else.
	var pruned []datastore.AddressRef
	for _, r := range refs {
		if r.Qualifier == utils.UltraFastCurseMCMSQualifier {
			continue
		}
		pruned = append(pruned, r)
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarActivateRMN, chains, StellarActivateRMNInput{
		ChainSelector:     sel,
		ExistingAddresses: pruned,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "UltraFastCurse")
	require.Contains(t, err.Error(), "deploy the fast-curse MCMS stack first")
}

func TestStellarActivateRMN_RejectsMissingGovernanceTimelock(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	refs := fullActivateRMNRefs(t, sel)
	var pruned []datastore.AddressRef
	for _, r := range refs {
		if r.Qualifier == utils.RMNTimelockQualifier {
			continue
		}
		pruned = append(pruned, r)
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarActivateRMN, chains, StellarActivateRMNInput{
		ChainSelector:     sel,
		ExistingAddresses: pruned,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "RMNMCMS")
	require.Contains(t, err.Error(), "deploy the RMNMCMS MCMS stack first")
}

func TestStellarActivateRMN_RejectsMissingRMNRemoteRef(t *testing.T) {
	t.Parallel()
	b := newTestBundle(t)
	sel := chainsel.STELLAR_LOCALNET.Selector
	chains := cldf_chain.NewBlockChains(nil)
	refs := fullActivateRMNRefs(t, sel)
	var pruned []datastore.AddressRef
	for _, r := range refs {
		if r.Type == datastore.ContractType(stellarccip.RMNRemoteContractType) {
			continue
		}
		pruned = append(pruned, r)
	}
	_, err := cldf_ops.ExecuteSequence(b, StellarActivateRMN, chains, StellarActivateRMNInput{
		ChainSelector:     sel,
		ExistingAddresses: pruned,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no RMN Remote ref found")
}

func TestRouteCurseAdminGrant(t *testing.T) {
	t.Parallel()
	deployer := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	govTL := "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAMAC"
	stranger := "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA2K"

	t.Run("deployer owner grants directly", func(t *testing.T) {
		t.Parallel()
		route, err := routeCurseAdminGrant(deployer, deployer, govTL)
		require.NoError(t, err)
		require.Equal(t, curseAdminGrantDirect, route)
	})
	t.Run("governance-owned RMN proposes", func(t *testing.T) {
		t.Parallel()
		route, err := routeCurseAdminGrant(govTL, deployer, govTL)
		require.NoError(t, err)
		require.Equal(t, curseAdminGrantPropose, route)
	})
	t.Run("stranger owner errors naming all three", func(t *testing.T) {
		t.Parallel()
		_, err := routeCurseAdminGrant(stranger, deployer, govTL)
		require.Error(t, err)
		require.Contains(t, err.Error(), stranger)
		require.Contains(t, err.Error(), deployer)
		require.Contains(t, err.Error(), govTL)
	})
}

func TestResolveRMNRemoteRef(t *testing.T) {
	t.Parallel()
	sel := chainsel.STELLAR_LOCALNET.Selector
	refs := fullActivateRMNRefs(t, sel)

	t.Run("resolves hex ref to strkey", func(t *testing.T) {
		t.Parallel()
		ref, err := resolveRMNRemoteRef(StellarActivateRMNInput{
			ChainSelector:     sel,
			ExistingAddresses: refs,
		})
		require.NoError(t, err)
		require.Equal(t, datastore.ContractType(stellarccip.RMNRemoteContractType), ref.Type)
		require.NotEmpty(t, ref.Address)
		require.Equal(t, "C", string(ref.Address[0]), "resolved address must be a contract strkey")
	})
	t.Run("explicit override wins", func(t *testing.T) {
		t.Parallel()
		override := &datastore.AddressRef{
			ChainSelector: sel,
			Type:          datastore.ContractType(stellarccip.RMNRemoteContractType),
			Address:       "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK54",
		}
		ref, err := resolveRMNRemoteRef(StellarActivateRMNInput{
			ChainSelector:     sel,
			ExistingAddresses: refs,
			RMNRemoteRef:      override,
		})
		require.NoError(t, err)
		require.Equal(t, override.Address, ref.Address)
	})
	t.Run("override with empty address errors", func(t *testing.T) {
		t.Parallel()
		_, err := resolveRMNRemoteRef(StellarActivateRMNInput{
			ChainSelector: sel,
			RMNRemoteRef:  &datastore.AddressRef{ChainSelector: sel},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "Address is empty")
	})
	t.Run("missing ref errors", func(t *testing.T) {
		t.Parallel()
		_, err := resolveRMNRemoteRef(StellarActivateRMNInput{ChainSelector: sel})
		require.Error(t, err)
		require.Contains(t, err.Error(), "no RMN Remote ref found")
	})
}

func TestDedupeStrkeys(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"a", "b"}, dedupeStrkeys([]string{"a", "b", "a"}))
	require.Empty(t, dedupeStrkeys(nil))
}

// fullActivateRMNRefs builds a ref set with the RMN Remote ref (hex, as recorded
// by the full deploy) plus both governance timelocks.
func fullActivateRMNRefs(t *testing.T, sel uint64) []datastore.AddressRef {
	t.Helper()
	rmnHex := hex.EncodeToString(make([]byte, 32))
	rmnRef := stellarccip.RMNRemoteDatastoreRef().FullAddressRef(sel, rmnHex)
	govRef := mcmsutil.StellarTimelockDatastoreRef(sel, utils.RMNTimelockQualifier, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK24")
	fastRef := mcmsutil.StellarTimelockDatastoreRef(sel, utils.UltraFastCurseMCMSQualifier, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK54")
	refs := []datastore.AddressRef{rmnRef, govRef, fastRef}
	// sanity: the refs must actually resolve through the finder
	_, ok := mcmsutil.FindExistingStellarTimelock(refs, sel, utils.RMNTimelockQualifier)
	require.True(t, ok, "governance timelock ref must resolve")
	_, ok = mcmsutil.FindExistingStellarTimelock(refs, sel, utils.UltraFastCurseMCMSQualifier)
	require.True(t, ok, "fast-curse timelock ref must resolve")
	return refs
}
