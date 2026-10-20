package staging_tests

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
	envEVMGasLimit        = "STAGING_EVM_GAS_LIMIT"              // default: 200000 (ccipReceive gas); 0 only suits EOA receivers
	envSendBatchSize      = "STAGING_SEND_BATCH_SIZE"            // default: 3
	envSentEventTimeout   = "STAGING_SENT_EVENT_TIMEOUT"         // default: 2m
	envEVMReceiver        = "STAGING_EVM_RECEIVER"               // required: 0x... receiver on the destination chain
	envEVMRPCURL          = "STAGING_EVM_RPC_URL"                // optional: enables destination execution checks
	envEVMOffRamp         = "STAGING_EVM_OFFRAMP"                // optional: 0x... OffRamp; required with STAGING_EVM_RPC_URL
	envEVMExecTimeout     = "STAGING_EVM_EXEC_TIMEOUT"           // default: 30m
	envEVMLogLookbackBlks = "STAGING_EVM_LOG_LOOKBACK_BLOCKS"    // default: 50 blocks before the send

	// EVM → Stellar direction. STAGING_SOURCE/DEST_CHAIN_SELECTOR keep naming the
	// lane's Stellar and EVM ends respectively, whichever way a test sends.
	envEVMSenderKey        = "STAGING_EVM_SENDER_PRIVATE_KEY" // required: hex secp256k1 key of the funded Sepolia sender
	envEVMRouter           = "STAGING_EVM_ROUTER"             // required: 0x... Router on the EVM chain
	envEVMFeeToken         = "STAGING_EVM_FEE_TOKEN"          // optional: 0x... ERC-20 fee token; unset pays in native ETH
	envStellarReceiver     = "STAGING_STELLAR_RECEIVER"       // required: C... (or 0x 32-byte hex) contract implementing ccip_receive
	envStellarOffRamp      = "STAGING_STELLAR_OFFRAMP"        // optional: C... OffRamp; resolved via Router.get_offramps when unset
	envStellarGasLimit     = "STAGING_STELLAR_GAS_LIMIT"      // default: 200000 (gasLimit in the EVM extra args)
	envStellarExecTimeout  = "STAGING_STELLAR_EXEC_TIMEOUT"   // default: 30m
	envEVMSentEventTimeout = "STAGING_EVM_SENT_TIMEOUT"       // default: 5m (ccipSend inclusion on the EVM chain)
)

// defaultStellarGasLimit is the gasLimit sent in the EVM-side extra args when
// STAGING_STELLAR_GAS_LIMIT is unset. Soroban routes data messages regardless,
// but the EVM OnRamp prices execution from it; matches the e2e EVM→Stellar tests.
const defaultStellarGasLimit = 200_000

// defaultEVMGasLimit is the ccipReceiveGasLimit sent when STAGING_EVM_GAS_LIMIT is
// unset; it matches CCIP's default EVM tx gas limit. A contract receiver given 0 gas
// fails on the destination with ReceiverError(0x).
const defaultEVMGasLimit = 200_000

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
		EVMGasLimit:         uint32(mustUintBits(t, envEVMGasLimit, defaultEVMGasLimit, 32)),
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

// evmToStellarConfig is the configuration for the EVM → Stellar staging tests.
type evmToStellarConfig struct {
	EVMRPCURL   string
	EVMSender   *ecdsa.PrivateKey
	EVMRouter   common.Address
	EVMFeeToken common.Address // zero address pays in native ETH

	StellarRPCURL     string
	NetworkPassphrase string
	// StellarAccount only signs read-only simulations (Router/OffRamp/receiver views);
	// it never submits a transaction in these tests.
	StellarAccount  *keypair.Full
	StellarRouterID string
	OffRampID       string // resolved via Router.get_offramps when empty
	ReceiverID      string

	StellarChainSelector uint64
	EVMChainSelector     uint64
	StellarGasLimit      uint32
	BatchSize            int
	EVMSentTimeout       time.Duration
	ExecTimeout          time.Duration
}

// loadEVMToStellarConfig reads the EVM → Stellar configuration from the
// environment. Missing required variables skip the test; malformed values fail it.
func loadEVMToStellarConfig(t *testing.T) *evmToStellarConfig {
	t.Helper()

	var missing []string
	required := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}

	evmRPC := required(envEVMRPCURL)
	evmKey := required(envEVMSenderKey)
	evmRouter := required(envEVMRouter)
	seed := required(envStellarSenderSeed)
	stellarRouter := required(envStellarRouter)
	receiver := required(envStellarReceiver)
	if len(missing) > 0 {
		t.Skipf("EVM → Stellar staging env not configured, missing: %s (see tests/staging/README.md)", strings.Join(missing, ", "))
	}

	cfg := &evmToStellarConfig{
		EVMRPCURL:            evmRPC,
		EVMRouter:            mustEVMAddress(t, envEVMRouter, evmRouter),
		StellarRPCURL:        envOr(envStellarRPCURL, "https://soroban-testnet.stellar.org"),
		NetworkPassphrase:    envOr(envStellarPassphrase, network.TestNetworkPassphrase),
		StellarRouterID:      mustContractID(t, envStellarRouter, stellarRouter),
		ReceiverID:           mustStellarContract(t, envStellarReceiver, receiver),
		StellarChainSelector: mustUint(t, envSourceChainSel, chainsel.STELLAR_TESTNET.Selector),
		EVMChainSelector:     mustUint(t, envDestChainSel, chainsel.ETHEREUM_TESTNET_SEPOLIA.Selector),
		StellarGasLimit:      uint32(mustUintBits(t, envStellarGasLimit, defaultStellarGasLimit, 32)),
		BatchSize:            int(mustUint(t, envSendBatchSize, 3)),
		EVMSentTimeout:       mustDuration(t, envEVMSentEventTimeout, 5*time.Minute),
		ExecTimeout:          mustDuration(t, envStellarExecTimeout, 30*time.Minute),
	}

	key, err := crypto.HexToECDSA(strings.TrimPrefix(evmKey, "0x"))
	if err != nil {
		t.Fatalf("%s: invalid secp256k1 private key: %v", envEVMSenderKey, err)
	}
	cfg.EVMSender = key

	kp, err := keypair.ParseFull(seed)
	if err != nil {
		t.Fatalf("%s: invalid Stellar secret seed: %v", envStellarSenderSeed, err)
	}
	cfg.StellarAccount = kp

	if v := strings.TrimSpace(os.Getenv(envEVMFeeToken)); v != "" {
		cfg.EVMFeeToken = mustEVMAddress(t, envEVMFeeToken, v)
	}
	if v := strings.TrimSpace(os.Getenv(envStellarOffRamp)); v != "" {
		cfg.OffRampID = mustContractID(t, envStellarOffRamp, v)
	}

	return cfg
}

// newStellarClients builds the Soroban RPC client and a Deployer (bindings.Invoker)
// that signs with kp.
func newStellarClients(rpcURL, passphrase string, kp *keypair.Full) (*rpcclient.Client, *stellardeployment.Deployer) {
	rpc := rpcclient.NewClient(rpcURL, &http.Client{Timeout: 60 * time.Second})
	return rpc, stellardeployment.NewDeployer(rpc, passphrase, kp)
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

// mustStellarContract accepts a contract id as a C... strkey or as the raw
// 32-byte id in hex (with or without 0x) and returns the strkey.
func mustStellarContract(t *testing.T, name, v string) string {
	t.Helper()
	if strings.HasPrefix(v, "C") {
		return mustContractID(t, name, v)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(v, "0x"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("%s: %q is neither a C... contract id nor 32-byte hex", name, v)
	}
	id, err := strkey.Encode(strkey.VersionByteContract, raw)
	if err != nil {
		t.Fatalf("%s: encode contract id: %v", name, err)
	}
	return id
}

func mustEVMAddress(t *testing.T, name, v string) common.Address {
	t.Helper()
	if !common.IsHexAddress(v) {
		t.Fatalf("%s: %q is not a valid EVM address", name, v)
	}
	return common.HexToAddress(v)
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
