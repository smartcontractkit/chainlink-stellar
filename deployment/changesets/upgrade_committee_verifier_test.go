package changesets

import (
	"testing"

	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

func TestUpgradeCommitteeVerifier_VerifyPreconditions(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	cv := testRef(stellarccip.CommitteeVerifierDatastoreRefWithQualifier("mcms-governance-test"), sel)

	for _, tt := range []struct {
		name      string
		withChain bool
		refs      []datastore.AddressRef
		qualifier string
		wantErr   string
	}{
		{name: "missing chain", refs: []datastore.AddressRef{cv}, qualifier: "mcms-governance-test", wantErr: "stellar chain"},
		{name: "missing verifier", withChain: true, qualifier: "mcms-governance-test", wantErr: "deploy Committee Verifier first"},
		{name: "default verifier not deployed", withChain: true, refs: []datastore.AddressRef{cv}, wantErr: "deploy Committee Verifier first"},
		{name: "ok", withChain: true, refs: []datastore.AddressRef{cv}, qualifier: "mcms-governance-test"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnvironment(t, sel, tt.withChain, tt.refs...)
			err := UpgradeCommitteeVerifier(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[UpgradeCommitteeVerifierConfig]{
				MCMS: mcmsutils.Input{},
				Cfg:  UpgradeCommitteeVerifierConfig{ChainSelector: sel, Qualifier: tt.qualifier},
			})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
