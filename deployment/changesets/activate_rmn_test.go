package changesets

import (
	"testing"

	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/changesets"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
)

func TestActivateRMN_VerifyPreconditions(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	rmn := testRef(stellarccip.RMNRemoteDatastoreRef(), sel)
	gov := mcmsutil.StellarTimelockDatastoreRef(sel, cciputils.RMNTimelockQualifier, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK24")
	fast := mcmsutil.StellarTimelockDatastoreRef(sel, cciputils.UltraFastCurseMCMSQualifier, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK54")
	custom := mcmsutil.StellarTimelockDatastoreRef(sel, "custom-gov", "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAK24")

	for _, tt := range []struct {
		name      string
		withChain bool
		refs      []datastore.AddressRef
		cfg       ActivateRMNConfig
		wantErr   string
	}{
		{name: "missing chain", refs: []datastore.AddressRef{rmn, gov, fast}, wantErr: "stellar chain"},
		{name: "missing rmn remote", withChain: true, refs: []datastore.AddressRef{gov, fast}, wantErr: "deploy RMN Remote first"},
		{name: "missing governance timelock", withChain: true, refs: []datastore.AddressRef{rmn, fast}, wantErr: "deploy the RMNMCMS MCMS stack"},
		{name: "missing fast-curse timelock", withChain: true, refs: []datastore.AddressRef{rmn, gov}, wantErr: "deploy the UltraFastCurse MCMS stack"},
		{name: "custom governance qualifier not deployed", withChain: true, refs: []datastore.AddressRef{rmn, gov, fast}, cfg: ActivateRMNConfig{GovernanceQualifier: "custom-gov"}, wantErr: "deploy the custom-gov MCMS stack"},
		{name: "default qualifiers", withChain: true, refs: []datastore.AddressRef{rmn, gov, fast}},
		{name: "custom governance qualifier", withChain: true, refs: []datastore.AddressRef{rmn, custom, fast}, cfg: ActivateRMNConfig{GovernanceQualifier: "custom-gov"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnvironment(t, sel, tt.withChain, tt.refs...)
			cfg := tt.cfg
			cfg.ChainSelector = sel
			err := ActivateRMN(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[ActivateRMNConfig]{Cfg: cfg})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
