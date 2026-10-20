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

func TestUpgradeOnRamp_VerifyPreconditions(t *testing.T) {
	sel := chainsel.STELLAR_LOCALNET.Selector
	onRamp := testRef(stellarccip.OnRampDatastoreRef(), sel)

	for _, tt := range []struct {
		name      string
		withChain bool
		refs      []datastore.AddressRef
		wantErr   string
	}{
		{name: "missing chain", refs: []datastore.AddressRef{onRamp}, wantErr: "stellar chain"},
		{name: "missing OnRamp", withChain: true, wantErr: "deploy OnRamp first"},
		{name: "ok", withChain: true, refs: []datastore.AddressRef{onRamp}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnvironment(t, sel, tt.withChain, tt.refs...)
			err := UpgradeOnRamp(changesets.GetRegistry()).VerifyPreconditions(env, changesets.WithMCMS[UpgradeOnRampConfig]{
				MCMS: mcmsutils.Input{},
				Cfg:  UpgradeOnRampConfig{ChainSelector: sel},
			})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
