package sequences

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/operationstest"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
)

var updateGoldens = flag.Bool("update", false, "rewrite characterization golden files")

const (
	characterizationPassphrase = "Chainlink Stellar CCIP Characterization Network ; 2026-09-29"
)

// Registered selectors, because BuildOnRampDestConfigs/BuildOffRampSourceConfigs
// resolve each remote selector's chain family from the chain-selectors registry.
var (
	characterizationSelector  = chainsel.STELLAR_LOCALNET.Selector
	characterizationRemoteSel = chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector
)

// characterizationKeypair returns a fixed deployer keypair so every op input
// (owner, salts, fee aggregator) is byte-stable across runs.
func characterizationKeypair() *keypair.Full {
	seed := sha256.Sum256([]byte("stellar-ccip-full-deploy-characterization"))
	kp, err := keypair.FromRawSeed(seed)
	if err != nil {
		panic(err)
	}
	return kp
}

// characterizationDeployer deploys each WASM to a valid, distinct, deterministic
// C… strkey (sha256 of the wasm file name), unlike operationstest.FakeDeployer's
// checksum-less placeholder: the monolith tail converts every contract ID to hex
// via StrkeyToHex, which rejects invalid strkeys.
type characterizationDeployer struct{}

func (characterizationDeployer) DeployContract(_ context.Context, wasmPath string, _ [32]byte) (string, error) {
	sum := sha256.Sum256([]byte(filepath.Base(wasmPath)))
	return strkey.Encode(strkey.VersionByteContract, sum[:])
}

// fakeCCIPDevenvHost is a CCIPDevenvHost with no friendbot (the monolith then
// takes the mock fee-token path), no test token, and nothing but stores. It is
// the characterization fixture: RunStellarCCIPFullDeploy runs end to end against
// operationstest fakes through it.
type fakeCCIPDevenvHost struct {
	log     *zerolog.Logger
	dep     *stellardeployment.Deployer
	kp      *keypair.Full
	netPass string

	onRamp       *onrampbindings.OnRampClient
	feeQuoter    *fqbindings.FeeQuoterClient
	tar          *tarbindings.TokenAdminRegistryClient
	tokenPool    *tokenpoolbindings.TokenPoolClient
	offRamp      *offrampbindings.OffRampClient
	routerID     string
	router       *routerbindings.RouterClient
	rampRegistry string
	rmnProxyID   string
	vvrID        string
	cvID         string
	receiverID   string
	testTokenID  string
	feeTokenID   string
}

var _ stellarccip.CCIPDevenvHost = (*fakeCCIPDevenvHost)(nil)

func newFakeCCIPDevenvHost(t *testing.T) *fakeCCIPDevenvHost {
	t.Helper()
	log := zerolog.Nop()
	kp := characterizationKeypair()
	// The orchestrator reads skip-if-exists state through h.Deployer(), so the
	// host deployer needs a live-shaped RPC: an httptest server whose ledger
	// always answers "entry not found" (every component deploys) and whose other
	// methods are never reached. The response must echo the request id — jrpc2
	// matches replies by id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"entries": []any{}, "latestLedger": 1},
		})
	}))
	t.Cleanup(srv.Close)
	dep := stellardeployment.NewDeployer(rpcclient.NewClient(srv.URL, srv.Client()), characterizationPassphrase, kp)
	return &fakeCCIPDevenvHost{log: &log, dep: dep, kp: kp, netPass: characterizationPassphrase}
}

func (h *fakeCCIPDevenvHost) Logger() *zerolog.Logger               { return h.log }
func (h *fakeCCIPDevenvHost) Deployer() *stellardeployment.Deployer { return h.dep }
func (h *fakeCCIPDevenvHost) DeployerKeypair() *keypair.Full        { return h.kp }
func (h *fakeCCIPDevenvHost) NetworkPassphrase() string             { return h.netPass }
func (h *fakeCCIPDevenvHost) FriendbotURL() string                  { return "" }
func (h *fakeCCIPDevenvHost) SetOnRamp(_ string, c *onrampbindings.OnRampClient) {
	h.onRamp = c
}
func (h *fakeCCIPDevenvHost) OnRampClient() *onrampbindings.OnRampClient { return h.onRamp }
func (h *fakeCCIPDevenvHost) FeeQuoterClient() *fqbindings.FeeQuoterClient {
	return h.feeQuoter
}
func (h *fakeCCIPDevenvHost) SetFeeQuoter(c *fqbindings.FeeQuoterClient) { h.feeQuoter = c }
func (h *fakeCCIPDevenvHost) SetTokenAdminRegistry(_ string, c *tarbindings.TokenAdminRegistryClient) {
	h.tar = c
}
func (h *fakeCCIPDevenvHost) TokenAdminRegistryClient() *tarbindings.TokenAdminRegistryClient {
	return h.tar
}
func (h *fakeCCIPDevenvHost) SetTokenPool(_ string, c *tokenpoolbindings.TokenPoolClient) {
	h.tokenPool = c
}
func (h *fakeCCIPDevenvHost) SetLegacyLockReleasePool(string) {}
func (h *fakeCCIPDevenvHost) SetTokenLockBox(string)          {}
func (h *fakeCCIPDevenvHost) LatestLedgerSequence(context.Context) (uint32, error) {
	return 0, nil
}
func (h *fakeCCIPDevenvHost) SetTestToken(id string)      { h.testTokenID = id }
func (h *fakeCCIPDevenvHost) TestTokenContractID() string { return h.testTokenID }
func (h *fakeCCIPDevenvHost) SetOffRamp(_ string, c *offrampbindings.OffRampClient) {
	h.offRamp = c
}
func (h *fakeCCIPDevenvHost) OffRampClient() *offrampbindings.OffRampClient { return h.offRamp }
func (h *fakeCCIPDevenvHost) SetRouter(id string, c *routerbindings.RouterClient) {
	h.routerID = id
	h.router = c
}
func (h *fakeCCIPDevenvHost) RouterContractID() string       { return h.routerID }
func (h *fakeCCIPDevenvHost) SetRampRegistry(id string)      { h.rampRegistry = id }
func (h *fakeCCIPDevenvHost) RampRegistryContractID() string { return h.rampRegistry }
func (h *fakeCCIPDevenvHost) SetRmnProxy(id string)          { h.rmnProxyID = id }
func (h *fakeCCIPDevenvHost) RmnProxyContractID() string     { return h.rmnProxyID }
func (h *fakeCCIPDevenvHost) SetVVR(id string)               { h.vvrID = id }
func (h *fakeCCIPDevenvHost) SetCV(id string)                { h.cvID = id }
func (h *fakeCCIPDevenvHost) SetReceiver(id string)          { h.receiverID = id }
func (h *fakeCCIPDevenvHost) SetFeeToken(id string)          { h.feeTokenID = id }
func (h *fakeCCIPDevenvHost) FeeTokenContractID() string     { return h.feeTokenID }
func (h *fakeCCIPDevenvHost) CreateFeeToken(context.Context, string) (string, error) {
	return "", fmt.Errorf("fakeCCIPDevenvHost: no friendbot, fee token is mocked")
}
func (h *fakeCCIPDevenvHost) CreateTestToken(context.Context, string) (string, error) {
	return "", fmt.Errorf("fakeCCIPDevenvHost: no test token")
}

// characterizationWasms writes one dummy release WASM per component and points
// CHAINLINK_STELLAR_ROOT at a temp root declaring the root module, so
// FindStellarRoot resolves it.
func characterizationWasms(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/smartcontractkit/chainlink-stellar\n"), 0o644))
	release := filepath.Join(root, "target", "wasm32v1-none", "release")
	require.NoError(t, os.MkdirAll(release, 0o755))
	for _, name := range []string{
		"onramp.wasm", "rmn_remote.wasm", "rmn_proxy.wasm", "fee_quoter.wasm",
		"token_admin_registry.wasm", "ccvs_versioned_verifier_resolver.wasm",
		"ccvs_committee_verifier.wasm", "executor.wasm", "offramp.wasm", "router.wasm",
		"ccip_ramp_registry.wasm", "ccip_receiver_example.wasm",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(release, name), []byte("wasm:"+name), 0o644))
	}
	t.Setenv("CHAINLINK_STELLAR_ROOT", root)
	return root
}

// opTraceEntry is one executed operation: its ID and canonical input JSON.
type opTraceEntry struct {
	ID    string          `json:"id"`
	Input json.RawMessage `json:"input"`
}

// goldenTrace is the characterization payload: the ordered op trace of the full
// deploy plus the address refs it returned.
type goldenTrace struct {
	Ops       []opTraceEntry         `json:"ops"`
	Addresses []datastore.AddressRef `json:"addresses"`
}

// buildTrace normalizes the report inputs (WasmPath gets the temp root folded
// away) and sorts the returned refs, so the golden is machine-independent.
func buildTrace(t *testing.T, reports []cldf_ops.Report[any, any], root string, refs []datastore.AddressRef) goldenTrace {
	t.Helper()
	trace := goldenTrace{Ops: make([]opTraceEntry, 0, len(reports))}
	for _, r := range reports {
		require.Nil(t, r.Err, "op %s reported an error", r.Def.ID)
		inputJSON, err := json.Marshal(r.Input)
		require.NoError(t, err)
		// Fold the temp root out of any wasm path, in op and sequence inputs alike.
		inputJSON = bytes.ReplaceAll(inputJSON, []byte(root), []byte("$CHAINLINK_STELLAR_ROOT"))
		trace.Ops = append(trace.Ops, opTraceEntry{ID: r.Def.ID, Input: inputJSON})
	}
	sorted := make([]datastore.AddressRef, len(refs))
	copy(sorted, refs)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.ChainSelector != b.ChainSelector {
			return a.ChainSelector < b.ChainSelector
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Qualifier != b.Qualifier {
			return a.Qualifier < b.Qualifier
		}
		if a.Version.String() != b.Version.String() {
			return a.Version.String() < b.Version.String()
		}
		return a.Address < b.Address
	})
	trace.Addresses = sorted
	return trace
}

// TestRunStellarCCIPFullDeploy_Characterization pins the monolith's behavior:
// the ordered (op ID, canonical input JSON) trace and the returned address refs.
// After the orchestrator rewrite, this golden must be identical except for the
// reviewed OnRamp reordering (plan.md Leg 2).
func TestRunStellarCCIPFullDeploy_Characterization(t *testing.T) {
	// Not parallel: t.Setenv.
	root := characterizationWasms(t)

	reporter := cldf_ops.NewMemoryReporter()
	b := cldf_ops.NewBundle(
		func() context.Context { return t.Context() },
		cldflogger.Nop(),
		reporter,
	)

	// owner() reads in the skip layer simulate through the invoker; void means
	// uninitialized, so every component initializes exactly once.
	inv := operationstest.NewRecordingInvoker().WithSimulateResult(&xdr.ScVal{Type: xdr.ScValTypeScvVoid})
	deps := stellardeps.StellarDeps{
		Deploy:  characterizationDeployer{},
		Invoker: inv,
	}
	host := newFakeCCIPDevenvHost(t)

	out, err := RunStellarCCIPFullDeploy(b.GetContext(), b, deps, host, nil, DeployStellarCCIPInnerInput{
		ChainSelector: characterizationSelector,
		AllSelectors:  []uint64{characterizationSelector, characterizationRemoteSel},
	})
	require.NoError(t, err)

	reports, err := reporter.GetReports()
	require.NoError(t, err)
	// 36 op call sites for this input: 24 deploy/init + 12 config, where
	// ccip-receiver:enable-remote-chain runs once per remote chain (one here).
	// 36 op call sites for this input: 24 deploy/init + 12 config, where
	// ccip-receiver:enable-remote-chain runs once per remote chain (one here).
	// The seven tier-0/1 components (RMN Remote, RMN Proxy, FeeQuoter, TAR, VVR,
	// Executor, RampRegistry) run through their component sequences, so their
	// sequence reports join the trace after their child op reports.
	require.Len(t, reports, 43)

	trace := buildTrace(t, reports, root, out.Addresses)
	got, err := json.MarshalIndent(trace, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')

	goldenPath := filepath.Join("testdata", "stellar_ccip_full_deploy_op_trace.golden.json")
	if *updateGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "golden file missing; run with -update to create it")
	require.Equal(t, string(want), string(got))
}
