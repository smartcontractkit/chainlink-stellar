package staging_tests

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// Environment variables consumed by the staging tests. Every test skips (rather
// than fails) when a required variable is unset, so `go test ./...` stays green
// on machines that are not pointed at the staging environment.
const (
	// Stellar (source) side.
	envStellarRPCURL      = "STAGING_STELLAR_RPC_URL"            // default: SDF testnet Soroban RPC
	envStellarPassphrase  = "STAGING_STELLAR_NETWORK_PASSPHRASE" // default: testnet passphrase
	envStellarSenderSeed  = "STAGING_STELLAR_SENDER_SECRET"      // required: S... seed of the funded sender account
	envStellarRouter      = "STAGING_STELLAR_ROUTER"             // required: C... Router contract
	envStellarOnRamp      = "STAGING_STELLAR_ONRAMP"             // optional: C... OnRamp; resolved via Router.get_onramp when unset
	envStellarFeeToken    = "STAGING_STELLAR_FEE_TOKEN"          // required: C... fee token (e.g. LINK or native XLM SAC)
	envStellarCCVs        = "STAGING_STELLAR_CCVS"               // required: comma-separated C... CCV addresses (e.g. the VersionedVerifierResolver)
	envStellarExecutor    = "STAGING_STELLAR_EXECUTOR"           // optional: C... executor; defaults to the lane's default executor sentinel
	envRouterLegacyFeeArg = "STAGING_ROUTER_LEGACY_FEE_ARG"      // optional: true for Routers built before bbc2d2b4, whose ccip_send takes a trailing fee_token_amount
	envSourceChainSel     = "STAGING_SOURCE_CHAIN_SELECTOR"      // default: stellar-testnet
	envDestChainSel       = "STAGING_DEST_CHAIN_SELECTOR"        // default: ethereum-testnet-sepolia
	envEVMGasLimit        = "STAGING_EVM_GAS_LIMIT"              // default: 0 (EOA receiver, no callback)
	envSendBatchSize      = "STAGING_SEND_BATCH_SIZE"            // default: 3
	envSentEventTimeout   = "STAGING_SENT_EVENT_TIMEOUT"         // default: 2m
	envEVMReceiver        = "STAGING_EVM_RECEIVER"               // required: 0x... receiver on the destination chain
	envEVMRPCURL          = "STAGING_EVM_RPC_URL"                // optional: enables destination execution checks
	envEVMOffRamp         = "STAGING_EVM_OFFRAMP"                // optional: 0x... OffRamp; required with STAGING_EVM_RPC_URL
	envEVMExecTimeout     = "STAGING_EVM_EXEC_TIMEOUT"           // default: 30m
	envEVMLogLookbackBlks = "STAGING_EVM_LOG_LOOKBACK_BLOCKS"    // default: 50 blocks before the send
)

type stagingConfig struct {
	StellarRPCURL     string
	NetworkPassphrase string
	Sender            *keypair.Full
	RouterID          string
	OnRampID          string
	FeeTokenID        string
	CCVs              []string
	Executor          string
	// RouterLegacyFeeArg selects the pre-bbc2d2b4 ccip_send(sender, dest, message, fee_token_amount) ABI.
	RouterLegacyFeeArg bool

	SourceChainSelector uint64
	DestChainSelector   uint64
	EVMGasLimit         uint32
	BatchSize           int
	SentEventTimeout    time.Duration

	EVMReceiver common.Address

	// Destination execution checks are enabled only when both are set.
	EVMRPCURL         string
	EVMOffRamp        common.Address
	EVMExecTimeout    time.Duration
	EVMLookbackBlocks uint64
}

// ExecCheckEnabled reports whether the test should wait for execution on the EVM OffRamp.
func (c *stagingConfig) ExecCheckEnabled() bool {
	return c.EVMRPCURL != ""
}

// loadStagingConfig reads the staging configuration from the environment.
// Missing required variables skip the test; malformed values fail it.
func loadStagingConfig(t *testing.T) *stagingConfig {
	t.Helper()

	var missing []string
	required := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}

	seed := required(envStellarSenderSeed)
	router := required(envStellarRouter)
	feeToken := required(envStellarFeeToken)
	ccvs := required(envStellarCCVs)
	receiver := required(envEVMReceiver)
	if len(missing) > 0 {
		t.Skipf("staging env not configured, missing: %s (see tests/staging/README.md)", strings.Join(missing, ", "))
	}

	cfg := &stagingConfig{
		StellarRPCURL:       envOr(envStellarRPCURL, "https://soroban-testnet.stellar.org"),
		NetworkPassphrase:   envOr(envStellarPassphrase, network.TestNetworkPassphrase),
		RouterID:            mustContractID(t, envStellarRouter, router),
		FeeTokenID:          mustContractID(t, envStellarFeeToken, feeToken),
		SourceChainSelector: mustUint(t, envSourceChainSel, chainsel.STELLAR_TESTNET.Selector),
		DestChainSelector:   mustUint(t, envDestChainSel, chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector),
		EVMGasLimit:         uint32(mustUintBits(t, envEVMGasLimit, 0, 32)),
		BatchSize:           int(mustUint(t, envSendBatchSize, 3)),
		SentEventTimeout:    mustDuration(t, envSentEventTimeout, 2*time.Minute),
		RouterLegacyFeeArg:  mustBool(t, envRouterLegacyFeeArg, false),
		EVMRPCURL:           strings.TrimSpace(os.Getenv(envEVMRPCURL)),
		EVMExecTimeout:      mustDuration(t, envEVMExecTimeout, 30*time.Minute),
		EVMLookbackBlocks:   mustUint(t, envEVMLogLookbackBlks, 50),
	}

	kp, err := keypair.ParseFull(seed)
	if err != nil {
		t.Fatalf("%s: invalid Stellar secret seed: %v", envStellarSenderSeed, err)
	}
	cfg.Sender = kp

	for _, ccv := range strings.Split(ccvs, ",") {
		if ccv = strings.TrimSpace(ccv); ccv != "" {
			cfg.CCVs = append(cfg.CCVs, mustContractID(t, envStellarCCVs, ccv))
		}
	}
	if v := strings.TrimSpace(os.Getenv(envStellarOnRamp)); v != "" {
		cfg.OnRampID = mustContractID(t, envStellarOnRamp, v)
	}
	if v := strings.TrimSpace(os.Getenv(envStellarExecutor)); v != "" {
		cfg.Executor = mustContractID(t, envStellarExecutor, v)
	}

	if !common.IsHexAddress(receiver) {
		t.Fatalf("%s: %q is not a valid EVM address", envEVMReceiver, receiver)
	}
	cfg.EVMReceiver = common.HexToAddress(receiver)

	if cfg.EVMRPCURL != "" {
		offRamp := strings.TrimSpace(os.Getenv(envEVMOffRamp))
		if !common.IsHexAddress(offRamp) {
			t.Fatalf("%s must be a valid EVM address when %s is set (got %q)", envEVMOffRamp, envEVMRPCURL, offRamp)
		}
		cfg.EVMOffRamp = common.HexToAddress(offRamp)
	}

	return cfg
}

// newStellarClients builds the Soroban RPC client and a Deployer (bindings.Invoker)
// that signs with the configured sender.
func newStellarClients(cfg *stagingConfig) (*rpcclient.Client, *stellardeployment.Deployer) {
	rpc := rpcclient.NewClient(cfg.StellarRPCURL, &http.Client{Timeout: 60 * time.Second})
	return rpc, stellardeployment.NewDeployer(rpc, cfg.NetworkPassphrase, cfg.Sender)
}

// isNativeFeeToken reports whether the fee token is the native XLM SAC, in which
// case the sender's balance also pays network fees and exact-delta checks do not apply.
func (c *stagingConfig) isNativeFeeToken() (bool, error) {
	native := xdr.MustNewNativeAsset()
	id, err := native.ContractID(c.NetworkPassphrase)
	if err != nil {
		return false, fmt.Errorf("derive native SAC contract id: %w", err)
	}
	nativeID, err := strkey.Encode(strkey.VersionByteContract, id[:])
	if err != nil {
		return false, err
	}
	return nativeID == c.FeeTokenID, nil
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func mustContractID(t *testing.T, name, v string) string {
	t.Helper()
	if _, err := strkey.Decode(strkey.VersionByteContract, v); err != nil {
		t.Fatalf("%s: %q is not a valid Stellar contract id (C...): %v", name, v, err)
	}
	return v
}

func mustUint(t *testing.T, name string, def uint64) uint64 {
	t.Helper()
	return mustUintBits(t, name, def, 64)
}

func mustUintBits(t *testing.T, name string, def uint64, bits int) uint64 {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, bits)
	if err != nil {
		t.Fatalf("%s: invalid unsigned integer %q: %v", name, v, err)
	}
	return n
}

func mustBool(t *testing.T, name string, def bool) bool {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		t.Fatalf("%s: invalid boolean %q: %v", name, v, err)
	}
	return b
}

func mustDuration(t *testing.T, name string, def time.Duration) time.Duration {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		t.Fatalf("%s: invalid duration %q: %v", name, v, err)
	}
	return d
}
