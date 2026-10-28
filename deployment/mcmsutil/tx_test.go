package mcmsutil

import (
	"testing"

	mcmsstellar "github.com/smartcontractkit/mcms/sdk/stellar"
	"github.com/stretchr/testify/require"
)

func TestStellarMCMSTxAdditionalFields_isValidStellarJSON(t *testing.T) {
	t.Parallel()
	raw := StellarMCMSTxAdditionalFields()
	require.JSONEq(t, `{"family":"stellar","encodingVersion":1}`, string(raw))
	require.NoError(t, mcmsstellar.ValidateAdditionalFields(raw))
}
