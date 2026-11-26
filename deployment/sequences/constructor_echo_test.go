package sequences

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

// echoGetters maps a release wasm name and a getter to the constructor argument
// the real contract stores and that getter returns. owner() is argument 0 for all.
var echoGetters = map[string]map[string]int{
	"onramp.wasm":                           {"get_static_config": 1, "get_dynamic_config": 2},
	"offramp.wasm":                          {"get_static_config": 1},
	"fee_quoter.wasm":                       {"get_static_config": 1, "get_authorized_callers": 2},
	"executor.wasm":                         {"get_max_ccvs_per_message": 1, "get_dynamic_config": 2},
	"ccvs_committee_verifier.wasm":          {"get_dynamic_config": 1, "get_storage_locations": 2, "get_rmn_proxy": 3, "version_tag": 4},
	"ccvs_versioned_verifier_resolver.wasm": {"get_fee_aggregator": 1},
	"rmn_remote.wasm":                       {"get_curse_admins": 1},
	"rmn_proxy.wasm":                        {"get_rmn": 1},
	"ccip_receiver_example.wasm":            {"get_router": 1},
	"ccip_ramp_registry.wasm":               {},
	"router.wasm":                           {},
	"token_admin_registry.wasm":             {},
}

type echoedContract struct {
	wasm string
	args []xdr.ScVal
}

// constructorEcho fakes a ledger that ran each created contract's constructor:
// readbacks return the constructor arguments the fake deployer received.
// Contracts it did not create fall through to the recording invoker.
type constructorEcho struct {
	*operationstest.RecordingInvoker
	deployer stellardeps.SorobanContractDeployer

	mu   sync.Mutex
	byID map[string]echoedContract
}

func newConstructorEcho(inv *operationstest.RecordingInvoker) *constructorEcho {
	return &constructorEcho{RecordingInvoker: inv, deployer: newCharacterizationDeployer(), byID: map[string]echoedContract{}}
}

func (e *constructorEcho) deps() stellardeps.StellarDeps {
	return stellardeps.StellarDeps{Deploy: e, Invoker: e}
}

func (e *constructorEcho) DeployContractWithArgs(ctx context.Context, wasmPath string, salt [32]byte, args []xdr.ScVal) (string, error) {
	id, err := e.deployer.DeployContractWithArgs(ctx, wasmPath, salt, args)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.byID[id] = echoedContract{wasm: filepath.Base(wasmPath), args: args}
	return id, nil
}

func (e *constructorEcho) SimulateContract(ctx context.Context, contractID, fn string, args []xdr.ScVal) (*xdr.ScVal, error) {
	res, err := e.RecordingInvoker.SimulateContract(ctx, contractID, fn, args)
	e.mu.Lock()
	c, ok := e.byID[contractID]
	e.mu.Unlock()
	if !ok {
		return res, err
	}
	if v, ok := echoReadback(c, fn); ok {
		return &v, nil
	}
	return res, err
}

func echoReadback(c echoedContract, fn string) (xdr.ScVal, bool) {
	getters, known := echoGetters[c.wasm]
	if !known {
		return xdr.ScVal{}, false
	}
	switch {
	case fn == "owner":
		return c.args[0], true
	case c.wasm == "router.wasm" && fn == "get_config":
		v, err := scval.BuildStructScVal(map[string]xdr.ScVal{"rmn_proxy": c.args[1]})
		return v, err == nil
	case c.wasm == "token_admin_registry.wasm" && fn == "get_all_configured_tokens":
		return scval.VecToScVal(nil), true
	case c.wasm == "fee_quoter.wasm" && (fn == "get_token_price" || fn == "get_dest_chain_gas_price"):
		v, err := scval.BuildStructScVal(map[string]xdr.ScVal{
			"timestamp": scval.Uint64ToScVal(0),
			"value":     scval.U128ToScVal(xdr.UInt128Parts{}),
		})
		return v, err == nil
	}
	if i, ok := getters[fn]; ok {
		return c.args[i], true
	}
	return xdr.ScVal{}, false
}

// releaseWasm writes a fixture wasm under a release file name next to
// wasmPath, so the echo knows which contract a test creates.
func releaseWasm(t *testing.T, wasmPath, name string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(wasmPath), name)
	require.NoError(t, os.WriteFile(p, []byte("wasm-bytes"), 0o644))
	return p
}
