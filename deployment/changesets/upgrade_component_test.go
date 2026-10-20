package changesets

import (
	"testing"

	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// upgradeCase drives the per-component VerifyPreconditions table: name is the
// spec name (also the "deploy <name> first" token), newCS builds the
// changeset, and ref/qualifier describe the instance the happy path seeds
// and selects.
type upgradeCase struct {
	name      string
	newCS     func(r *changesets.MCMSReaderRegistry) cldf.ChangeSetV2[changesets.WithMCMS[UpgradeComponentConfig]]
	ref       stellarccip.DatastoreSorobanContractRef
	qualifier string
}

// upgradeCases lists one entry per upgradeable component: singletons with no
// qualifier, per-instance components on their default instance, and pools on
// a token-symbol qualifier.
var upgradeCases = []upgradeCase{
	{name: "OnRamp", newCS: UpgradeOnRamp, ref: stellarccip.OnRampDatastoreRef()},
	{name: "OffRamp", newCS: UpgradeOffRamp, ref: stellarccip.OffRampDatastoreRef()},
	{name: "Router", newCS: UpgradeRouter, ref: stellarccip.RouterDatastoreRef()},
	{name: "FeeQuoter", newCS: UpgradeFeeQuoter, ref: stellarccip.FeeQuoterDatastoreRef()},
	{name: "TokenAdminRegistry", newCS: UpgradeTokenAdminRegistry, ref: stellarccip.TokenAdminRegistryDatastoreRef()},
	{name: "RMN Proxy", newCS: UpgradeRmnProxy, ref: stellarccip.RMNProxyDatastoreRef()},
	{name: "RMN Remote", newCS: UpgradeRmnRemote, ref: stellarccip.RMNRemoteDatastoreRef()},
	{name: "RampRegistry", newCS: UpgradeRampRegistry, ref: stellarccip.RampRegistryDatastoreRef()},
	{name: "Committee Verifier", newCS: UpgradeCommitteeVerifier, ref: stellarccip.CommitteeVerifierDatastoreRefWithQualifier("")},
	{name: "VersionedVerifierResolver", newCS: UpgradeVersionedVerifierResolver, ref: stellarccip.VVRDatastoreRefWithQualifier("")},
	{name: "CCIP Receiver", newCS: UpgradeCCIPReceiver, ref: stellarccip.CCIPReceiverDatastoreRefWithQualifier("")},
	{name: "Executor", newCS: UpgradeExecutor, ref: stellarccip.ExecutorDatastoreRefWithQualifier("")},
	{name: "BurnMintPool", newCS: UpgradeBurnMintPool, ref: stellarccip.BurnMintPoolDatastoreRef("BnM"), qualifier: "BnM"},
	{name: "LockReleasePool", newCS: UpgradeLockReleasePool, ref: stellarccip.LockReleasePoolDatastoreRef("LINK"), qualifier: "LINK"},
	{name: "SiloedLockReleasePool", newCS: UpgradeSiloedLockReleasePool, ref: stellarccip.SiloedLockReleasePoolDatastoreRef("LINK"), qualifier: "LINK"},
	{name: "TokenLockBox", newCS: UpgradeTokenLockBox, ref: stellarccip.TokenLockBoxDatastoreRef("LINK"), qualifier: "LINK"},
	{name: "AdvancedPoolHooks", newCS: UpgradeAdvancedPoolHooks, ref: stellarccip.AdvancedPoolHooksDatastoreRef("BnM"), qualifier: "BnM"},
}

func TestUpgradeComponent_VerifyPreconditions(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector

	for _, tc := range upgradeCases {
		t.Run(tc.name, func(t *testing.T) {
			input := changesets.WithMCMS[UpgradeComponentConfig]{
				MCMS: mcmsutils.Input{},
				Cfg:  UpgradeComponentConfig{ChainSelector: sel, Qualifier: tc.qualifier},
			}
			seeded := testRef(tc.ref, sel)

			env := testEnvironment(t, sel, false, seeded)
			err := tc.newCS(changesets.GetRegistry()).VerifyPreconditions(env, input)
			require.ErrorContains(t, err, "stellar chain")

			env = testEnvironment(t, sel, true)
			err = tc.newCS(changesets.GetRegistry()).VerifyPreconditions(env, input)
			require.ErrorContains(t, err, "deploy "+tc.name+" first")

			env = testEnvironment(t, sel, true, seeded)
			err = tc.newCS(changesets.GetRegistry()).VerifyPreconditions(env, input)
			require.NoError(t, err)
		})
	}
}

// TestUpgradeComponent_QualifiedInstance verifies the per-instance components
// resolve a non-default qualifier, and that an empty qualifier keeps the
// default instance rather than matching a differently-qualified one.
func TestUpgradeComponent_QualifiedInstance(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector

	for _, tc := range []upgradeCase{
		{name: "Committee Verifier", newCS: UpgradeCommitteeVerifier, ref: stellarccip.CommitteeVerifierDatastoreRefWithQualifier("secondary")},
		{name: "VersionedVerifierResolver", newCS: UpgradeVersionedVerifierResolver, ref: stellarccip.VVRDatastoreRefWithQualifier("secondary")},
		{name: "CCIP Receiver", newCS: UpgradeCCIPReceiver, ref: stellarccip.CCIPReceiverDatastoreRefWithQualifier("secondary")},
		{name: "Executor", newCS: UpgradeExecutor, ref: stellarccip.ExecutorDatastoreRefWithQualifier("secondary")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secondary := testRef(tc.ref, sel)

			// The qualified instance resolves when it is the one deployed.
			env := testEnvironment(t, sel, true, secondary)
			err := tc.newCS(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[UpgradeComponentConfig]{
				MCMS: mcmsutils.Input{},
				Cfg:  UpgradeComponentConfig{ChainSelector: sel, Qualifier: "secondary"},
			})
			require.NoError(t, err)

			// The default instance is not implicitly the secondary one.
			env = testEnvironment(t, sel, true, secondary)
			err = tc.newCS(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[UpgradeComponentConfig]{
				MCMS: mcmsutils.Input{},
				Cfg:  UpgradeComponentConfig{ChainSelector: sel},
			})
			require.ErrorContains(t, err, "deploy "+tc.name+" first")
		})
	}
}

// TestUpgradeComponent_PoolQualifierRequired verifies the pools fail fast on
// an empty qualifier — before any ref lookup, so the error names the missing
// qualifier, not a missing deploy.
func TestUpgradeComponent_PoolQualifierRequired(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector

	for _, tc := range []upgradeCase{
		{name: "BurnMintPool", newCS: UpgradeBurnMintPool, ref: stellarccip.BurnMintPoolDatastoreRef("")},
		{name: "LockReleasePool", newCS: UpgradeLockReleasePool, ref: stellarccip.LockReleasePoolDatastoreRef("")},
		{name: "SiloedLockReleasePool", newCS: UpgradeSiloedLockReleasePool, ref: stellarccip.SiloedLockReleasePoolDatastoreRef("")},
		{name: "TokenLockBox", newCS: UpgradeTokenLockBox, ref: stellarccip.TokenLockBoxDatastoreRef("")},
		{name: "AdvancedPoolHooks", newCS: UpgradeAdvancedPoolHooks, ref: stellarccip.AdvancedPoolHooksDatastoreRef("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Chain present, datastore empty: a ref-lookup failure would say
			// "deploy <name> first", proving the check ordering if seen.
			env := testEnvironment(t, sel, true)
			err := tc.newCS(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[UpgradeComponentConfig]{
				MCMS: mcmsutils.Input{},
				Cfg:  UpgradeComponentConfig{ChainSelector: sel},
			})
			require.ErrorContains(t, err, "qualifier is required")
			require.NotContains(t, err.Error(), "deploy "+tc.name+" first")
		})
	}
}
