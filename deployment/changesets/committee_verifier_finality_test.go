package changesets

import (
	"testing"

	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-ccip/deployment/finality"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	mcmsutils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/mcms"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
)

func TestSetCommitteeVerifierFinality_VerifyPreconditions(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	cv := testRef(stellarccip.CommitteeVerifierDatastoreRefWithQualifier("mcms-governance-test"), sel)
	cll := mcmsutil.StellarTimelockDatastoreRef(sel, cciputils.CLLQualifier, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK24")
	safe := finality.Config{WaitForSafe: true}

	for _, tt := range []struct {
		name      string
		withChain bool
		refs      []datastore.AddressRef
		qualifier string
		mcmsQual  string
		finality  finality.Config
		wantErr   string
	}{
		{name: "missing chain", refs: []datastore.AddressRef{cv, cll}, qualifier: "mcms-governance-test", finality: safe, wantErr: "stellar chain"},
		{name: "missing verifier", withChain: true, refs: []datastore.AddressRef{cll}, qualifier: "mcms-governance-test", finality: safe, wantErr: "deploy Committee Verifier first"},
		{name: "default verifier not deployed", withChain: true, refs: []datastore.AddressRef{cv, cll}, finality: safe, wantErr: "deploy Committee Verifier first"},
		{name: "empty finality", withChain: true, refs: []datastore.AddressRef{cv, cll}, qualifier: "mcms-governance-test", wantErr: "finality config is empty"},
		{name: "missing timelock", withChain: true, refs: []datastore.AddressRef{cv}, qualifier: "mcms-governance-test", finality: safe, wantErr: "deploy the CLLCCIP MCMS stack"},
		{name: "custom mcms qualifier not deployed", withChain: true, refs: []datastore.AddressRef{cv, cll}, qualifier: "mcms-governance-test", mcmsQual: "RMNMCMS", finality: safe, wantErr: "deploy the RMNMCMS MCMS stack"},
		{name: "ok", withChain: true, refs: []datastore.AddressRef{cv, cll}, qualifier: "mcms-governance-test", finality: safe},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnvironment(t, sel, tt.withChain, tt.refs...)
			err := SetCommitteeVerifierFinality(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[SetCommitteeVerifierFinalityConfig]{
				MCMS: mcmsutils.Input{Qualifier: tt.mcmsQual},
				Cfg: SetCommitteeVerifierFinalityConfig{
					ChainSelector:   sel,
					Qualifier:       tt.qualifier,
					AllowedFinality: tt.finality,
				},
			})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
