package common

import (
	"fmt"

	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
)

// EncodeExtraArgsV3 converts a GenericExtraArgsV3 to XDR bytes suitable for
// the OnRamp contract's ExtraArgs field (parsed via GenericExtraArgsV3::from_xdr).
func EncodeExtraArgsV3(args onrampbindings.GenericExtraArgsV3) ([]byte, error) {
	scVal, err := args.ToScVal()
	if err != nil {
		return nil, fmt.Errorf("failed to convert extra args to ScVal: %w", err)
	}
	return scVal.MarshalBinary()
}
