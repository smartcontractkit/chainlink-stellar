package operations

import (
	"encoding/json"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Void is the output type for on-chain calls that return no structured payload.
type Void struct{}

// DeployInput is the standard input for uploading Soroban WASM with a salt.
// ConstructorArgs are passed to __constructor in the creating transaction.
type DeployInput struct {
	WasmPath        string          `json:"wasm_path"`
	Salt            [32]byte        `json:"salt"`
	ConstructorArgs ConstructorArgs `json:"constructor_args,omitempty"`
}

// ConstructorArgs serializes as base64 XDR strings so stored reports stay
// compact and their hashes do not depend on the SDK's Go struct layout.
type ConstructorArgs []xdr.ScVal

func (a ConstructorArgs) MarshalJSON() ([]byte, error) {
	out := make([]string, len(a))
	for i, v := range a {
		s, err := xdr.MarshalBase64(v)
		if err != nil {
			return nil, fmt.Errorf("constructor arg %d: %w", i, err)
		}
		out[i] = s
	}
	return json.Marshal(out)
}

func (a *ConstructorArgs) UnmarshalJSON(data []byte) error {
	var encoded []string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	args := make(ConstructorArgs, len(encoded))
	for i, s := range encoded {
		if err := xdr.SafeUnmarshalBase64(s, &args[i]); err != nil {
			return fmt.Errorf("constructor arg %d: %w", i, err)
		}
	}
	*a = args
	return nil
}

// DeployOutput carries the deployed contract address (StrKey).
type DeployOutput struct {
	ContractID string `json:"contract_id"`
}
