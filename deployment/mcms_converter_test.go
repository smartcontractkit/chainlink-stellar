package deployment

import (
	"testing"

	chainsel "github.com/smartcontractkit/chain-selectors"
	mcmschainwrappers "github.com/smartcontractkit/mcms/chainwrappers"
	mcmtypes "github.com/smartcontractkit/mcms/types"
	"github.com/stretchr/testify/require"
)

// TestMCMSBuildConverterStellar guards the mcms pin at a version with Stellar
// timelock-converter support: without it, Stellar TimelockProposals cannot be
// converted, signed or executed through the mcms SDK.
func TestMCMSBuildConverterStellar(t *testing.T) {
	converter, err := mcmschainwrappers.BuildConverter(
		mcmtypes.ChainSelector(chainsel.STELLAR_TESTNET.Selector),
		mcmtypes.ChainMetadata{},
	)
	require.NoError(t, err)
	require.NotNil(t, converter, "mcms must provide a timelock converter for the stellar family")
}
