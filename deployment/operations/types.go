package operations

// Void is the output type for on-chain calls that return no structured payload.
type Void struct{}

// DeployInput is the standard input for uploading Soroban WASM with a salt.
type DeployInput struct {
	WasmPath string   `json:"wasm_path"`
	Salt     [32]byte `json:"salt"`
}

// DeployOutput carries the deployed contract address (StrKey).
type DeployOutput struct {
	ContractID string `json:"contract_id"`
}
