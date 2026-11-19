package staging_tests

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	chainsel "github.com/smartcontractkit/chain-selectors"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
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

	// Token transfers (both directions) over a burn-mint pool pair.
	envStellarToken        = "STAGING_STELLAR_TOKEN"         // required: C... (or 0x 32-byte hex) BnM token on Stellar
	envStellarTokenPool    = "STAGING_STELLAR_TOKEN_POOL"    // required: C... (or 0x 32-byte hex) BurnMintTokenPool for it
	envEVMToken            = "STAGING_EVM_TOKEN"             // required: 0x... BurnMintERC20WithDrip token on the EVM chain
	envEVMTokenPool        = "STAGING_EVM_TOKEN_POOL"        // required: 0x... BurnMintTokenPool for it
	envTokenTransferAmount = "STAGING_TOKEN_TRANSFER_AMOUNT" // default: 1000, in the Stellar token's base units

	// Ephemeral-sender mode (CI): STAGING_EPHEMERAL=true generates a fresh,
	// zero-privilege sender key per test instead of requiring a long-lived funded
	// sender secret. XLM comes from the public testnet Friendbot and the BnM
	// transfer token from its permissionless drip, but the FEE TOKEN has no known
	// permissionless faucet and Sepolia ETH has no CI-friendly one, so
	// low-privilege TREASURY keys — plain holders of the fee token / testnet ETH,
	// nothing more — dispense to the ephemeral sender at run start.
	envEphemeral         = "STAGING_EPHEMERAL"                // optional: true enables ephemeral senders
	envStellarTreasury   = "STAGING_STELLAR_TREASURY_SECRET"  // required in ephemeral mode: S... seed of a key holding the fee token
	envFriendbotURL      = "STAGING_FRIENDBOT_URL"            // default: public testnet Friendbot
	envFeeTokenFundWhole = "STAGING_FEE_TOKEN_FUND_WHOLE"     // default: 10, whole fee-token units transferred to the ephemeral sender
	envEVMTreasuryKey    = "STAGING_EVM_TREASURY_PRIVATE_KEY" // required in ephemeral mode for EVM tests: hex key holding Sepolia ETH
	envEVMFundETH        = "STAGING_EVM_FUND_ETH"             // default: 0.1 ETH transferred to the ephemeral EVM sender
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

	// Ephemeral mode: Sender was generated in-process (not parsed from
	// STAGING_STELLAR_SENDER_SECRET) and must be funded before use — XLM via
	// FriendbotURL, fee token via a transfer from Treasury (a low-privilege key
	// that merely holds the fee token; LINK has no faucet).
	Ephemeral    bool
	Treasury     *keypair.Full // nil outside ephemeral mode
	FriendbotURL string
	FeeFundWhole int64 // whole fee-token units Treasury transfers to the ephemeral Sender

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

	ephemeral := mustBool(t, envEphemeral, false)

	// In ephemeral mode the sender key is generated below; a treasury is only
	// needed when the fee token is NOT the devenv FEE SAC (whose deterministic
	// issuer needs no secret — see fundEphemeralStellarSender).
	var seed, treasurySeed string
	if ephemeral {
		if v := strings.TrimSpace(os.Getenv(envStellarTreasury)); v != "" {
			treasurySeed = v
		}
	} else {
		seed = required(envStellarSenderSeed)
	}
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
		Ephemeral:           ephemeral,
		FriendbotURL:        envOr(envFriendbotURL, "https://friendbot.stellar.org"),
		FeeFundWhole:        int64(mustUint(t, envFeeTokenFundWhole, 10)),
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

	if ephemeral {
		kp, err := keypair.Random()
		if err != nil {
			t.Fatalf("generate ephemeral Stellar sender: %v", err)
		}
		cfg.Sender = kp
		if treasurySeed != "" {
			treasury, err := keypair.ParseFull(treasurySeed)
			if err != nil {
				t.Fatalf("%s: invalid Stellar secret seed: %v", envStellarTreasury, err)
			}
			cfg.Treasury = treasury
		}
	} else {
		kp, err := keypair.ParseFull(seed)
		if err != nil {
			t.Fatalf("%s: invalid Stellar secret seed: %v", envStellarSenderSeed, err)
		}
		cfg.Sender = kp
	}

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

	// Ephemeral mode: EVMSender was generated in-process and funded with ETH
	// from EVMTreasury (a low-privilege key that merely holds testnet ETH);
	// StellarEphemeral means the Stellar account was also generated in-process
	// (read-only sims + TTL extensions only, so Friendbot XLM is all it needs).
	EVMephemeral bool
	EVMTreasury  *ecdsa.PrivateKey // nil outside ephemeral mode
	EVMFundETH   *big.Int          // wei transferred from the treasury to the ephemeral sender

	StellarRPCURL     string
	NetworkPassphrase string
	// StellarAccount only signs read-only simulations (Router/OffRamp/receiver views);
	// it never submits a transaction in these tests.
	StellarAccount   *keypair.Full
	StellarEphemeral bool
	FriendbotURL     string
	StellarRouterID  string
	OffRampID        string // resolved via Router.get_offramps when empty
	ReceiverID       string

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

	ephemeral := mustBool(t, envEphemeral, false)

	var evmKey string
	if ephemeral {
		// The EVM sender key is generated below; what ephemeral mode requires
		// instead is the ETH treasury that will fund it (helpers_test.go).
		evmKey = required(envEVMTreasuryKey)
	} else {
		evmKey = required(envEVMSenderKey)
	}
	evmRPC := required(envEVMRPCURL)
	evmRouter := required(envEVMRouter)
	var stellarSeed string
	if ephemeral {
		// Read-only simulations + TTL extensions only: a generated account
		// with Friendbot XLM suffices; no funded sender secret needed.
		stellarSeed = "" // generated below
	} else {
		stellarSeed = required(envStellarSenderSeed)
	}
	stellarRouter := required(envStellarRouter)
	receiver := required(envStellarReceiver)
	if len(missing) > 0 {
		t.Skipf("EVM → Stellar staging env not configured, missing: %s (see tests/staging/README.md)", strings.Join(missing, ", "))
	}

	cfg := &evmToStellarConfig{
		EVMRPCURL:            evmRPC,
		EVMRouter:            mustEVMAddress(t, envEVMRouter, evmRouter),
		EVMephemeral:         ephemeral,
		StellarEphemeral:     ephemeral,
		FriendbotURL:         envOr(envFriendbotURL, "https://friendbot.stellar.org"),
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

	if ephemeral {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("generate ephemeral EVM sender: %v", err)
		}
		cfg.EVMSender = key

		treasury, err := crypto.HexToECDSA(strings.TrimPrefix(evmKey, "0x"))
		if err != nil {
			t.Fatalf("%s: invalid secp256k1 private key: %v", envEVMTreasuryKey, err)
		}
		cfg.EVMTreasury = treasury

		fundETH := strings.TrimSpace(os.Getenv(envEVMFundETH))
		if fundETH == "" {
			fundETH = "0.1"
		}
		amount, ok := new(big.Float).SetString(fundETH)
		if !ok {
			t.Fatalf("%s: %q is not a decimal ETH amount", envEVMFundETH, fundETH)
		}
		wei, _ := new(big.Float).Mul(amount, big.NewFloat(1e18)).Int(nil)
		cfg.EVMFundETH = wei

		kp, err := keypair.Random()
		if err != nil {
			t.Fatalf("generate ephemeral Stellar account: %v", err)
		}
		cfg.StellarAccount = kp
	} else {
		key, err := crypto.HexToECDSA(strings.TrimPrefix(evmKey, "0x"))
		if err != nil {
			t.Fatalf("%s: invalid secp256k1 private key: %v", envEVMSenderKey, err)
		}
		cfg.EVMSender = key

		kp, err := keypair.ParseFull(stellarSeed)
		if err != nil {
			t.Fatalf("%s: invalid Stellar secret seed: %v", envStellarSenderSeed, err)
		}
		cfg.StellarAccount = kp
	}

	if v := strings.TrimSpace(os.Getenv(envEVMFeeToken)); v != "" {
		cfg.EVMFeeToken = mustEVMAddress(t, envEVMFeeToken, v)
	}
	if v := strings.TrimSpace(os.Getenv(envStellarOffRamp)); v != "" {
		cfg.OffRampID = mustContractID(t, envStellarOffRamp, v)
	}

	return cfg
}

// defaultTokenTransferAmount is the transfer size, in Stellar token base units,
// when STAGING_TOKEN_TRANSFER_AMOUNT is unset: 0.0001 BnM at 7 decimals, so one
// Stellar drip (10^6) covers a thousand runs.
const defaultTokenTransferAmount = 1000

// tokenTransferConfig names the token and pool on each end of the lane. The
// amount is always in the Stellar token's base units; the EVM-side amount is
// derived from the two tokens' decimals so it converts exactly.
type tokenTransferConfig struct {
	StellarToken string
	StellarPool  string
	EVMToken     common.Address
	EVMPool      common.Address
	Amount       *big.Int
}

// loadTokenTransferConfig reads the token-transfer configuration from the
// environment. Missing required variables skip the test; malformed values fail it.
func loadTokenTransferConfig(t *testing.T) *tokenTransferConfig {
	t.Helper()

	var missing []string
	required := func(name string) string {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}

	stellarToken := required(envStellarToken)
	stellarPool := required(envStellarTokenPool)
	evmToken := required(envEVMToken)
	evmPool := required(envEVMTokenPool)
	if len(missing) > 0 {
		t.Skipf("token transfer staging env not configured, missing: %s (see tests/staging/README.md)", strings.Join(missing, ", "))
	}

	cfg := &tokenTransferConfig{
		StellarToken: mustStellarContract(t, envStellarToken, stellarToken),
		StellarPool:  mustStellarContract(t, envStellarTokenPool, stellarPool),
		EVMToken:     mustEVMAddress(t, envEVMToken, evmToken),
		EVMPool:      mustEVMAddress(t, envEVMTokenPool, evmPool),
		Amount:       new(big.Int).SetUint64(mustUint(t, envTokenTransferAmount, defaultTokenTransferAmount)),
	}
	if cfg.Amount.Sign() == 0 {
		t.Fatalf("%s must be > 0", envTokenTransferAmount)
	}
	return cfg
}

// newStellarClients builds the Soroban RPC client and a Deployer (bindings.Invoker)
// that signs with kp. The Deployer logs the hash of every transaction it
// submits ("submitted stellar transaction", with contract and fn) to the test
// output, so failures can be looked up in the explorer.
func newStellarClients(t *testing.T, rpcURL, passphrase string, kp *keypair.Full) (*rpcclient.Client, *stellardeployment.Deployer) {
	rpc := rpcclient.NewClient(rpcURL, &http.Client{Timeout: 60 * time.Second})
	return rpc, stellardeployment.NewDeployer(rpc, passphrase, kp, stellardeployment.WithLogger(cldflogger.Test(t)))
}

// devenvFeeAssetCode mirrors tests/ccv/chain/chain.go's feeTokenAssetCode —
// the classic asset the staging deploy wraps as the CCIP fee token SAC via
// DeployDevenvSACToken (deployment/ccip/sac_devenv_token.go).
const devenvFeeAssetCode = "FEE"

// deriveDevenvFeeToken reproduces the staging deploy's deterministic fee-token
// recipe (sac_devenv_token.go:39-40 with the label from chain.go:1422): the
// FEE issuer seed is sha256("fee-token-issuer-"+passphrase). The issuer is
// therefore derivable by anyone from the PUBLIC network passphrase — a
// testnet fund-token dispenser, not a secret (verified: the derived SAC id
// equals the deployed fee token CDFAZQ… on testnet).
func deriveDevenvFeeToken(passphrase string) (*keypair.Full, string, error) {
	seed := sha256.Sum256([]byte("fee-token-issuer-" + passphrase))
	issuer, err := keypair.FromRawSeed(seed)
	if err != nil {
		return nil, "", err
	}
	asset := xdr.MustNewCreditAsset(devenvFeeAssetCode, issuer.Address())
	id, err := asset.ContractID(passphrase)
	if err != nil {
		return nil, "", err
	}
	cid, err := strkey.Encode(strkey.VersionByteContract, id[:])
	if err != nil {
		return nil, "", err
	}
	return issuer, cid, nil
}

// fundEphemeralStellarSender funds a config-generated (zero-privilege) Stellar
// sender: XLM from the public testnet Friendbot, and fee tokens either from
// the deterministic FEE issuer (the staging env's fee token is the devenv FEE
// SAC — a classic issued asset with NO drip and NO custom entrypoints, so the
// derivable issuer is the only permissionless-style dispenser) or, for any
// other fee token, from an optional low-privilege treasury that holds it.
func fundEphemeralStellarSender(t *testing.T, ctx context.Context, cfg *stagingConfig, l *zerolog.Logger) {
	t.Helper()

	if err := helpers.FundViaFriendbot(cfg.FriendbotURL, cfg.Sender.Address()); err != nil {
		t.Fatalf("fund ephemeral sender %s via Friendbot (%s): %v", cfg.Sender.Address(), cfg.FriendbotURL, err)
	}

	if issuer, feeSAC, err := deriveDevenvFeeToken(cfg.NetworkPassphrase); err == nil && feeSAC == cfg.FeeTokenID {
		fundEphemeralSenderFromFeeIssuer(t, ctx, cfg, issuer, l)
		return
	}

	// Generic fee token (e.g. a contract token with no faucet): treasury transfer.
	if cfg.Treasury == nil {
		t.Fatalf("fee token %s is not the devenv FEE SAC and %s is unset — ephemeral mode needs either the FEE SAC (no config) or a treasury key holding the fee token",
			cfg.FeeTokenID, envStellarTreasury)
	}
	_, treasury := newStellarClients(t, cfg.StellarRPCURL, cfg.NetworkPassphrase, cfg.Treasury)
	decRes, err := treasury.SimulateContract(ctx, cfg.FeeTokenID, "decimals", nil)
	require.NoError(t, err, "fee token decimals — is %s a token contract?", cfg.FeeTokenID)
	require.NotNil(t, decRes, "fee token decimals returned no value")
	decimals, err := scval.Uint32FromScVal(*decRes)
	require.NoError(t, err, "fee token decimals shape")
	amount := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	amount = amount.Mul(amount, big.NewInt(cfg.FeeFundWhole))

	_, err = treasury.InvokeContract(ctx, cfg.FeeTokenID, "transfer", []xdr.ScVal{
		scval.AddressToScVal(cfg.Treasury.Address()),
		scval.AddressToScVal(cfg.Sender.Address()),
		scval.I128ToScVal(amount),
	})
	require.NoError(t, err, "treasury fee-token transfer of %s base units — does the treasury key hold the fee token?", amount.String())
	l.Info().
		Str("sender", cfg.Sender.Address()).
		Str("feeToken", cfg.FeeTokenID).
		Str("amount", amount.String()).
		Msg("funded ephemeral Stellar sender (Friendbot XLM + treasury fee token)")
}

// fundEphemeralSenderFromFeeIssuer funds the ephemeral sender from the
// deterministic FEE issuer: ChangeTrust (a classic issued asset needs a
// trustline before it can be received), then an issuer Payment of
// FeeFundWhole whole tokens. Amounts are decimal whole-unit strings per the
// stellar amount package convention ("101.001" = 101.001 tokens).
func fundEphemeralSenderFromFeeIssuer(t *testing.T, ctx context.Context, cfg *stagingConfig, issuer *keypair.Full, l *zerolog.Logger) {
	t.Helper()

	_, sender := newStellarClients(t, cfg.StellarRPCURL, cfg.NetworkPassphrase, cfg.Sender)
	_, issuerDeployer := newStellarClients(t, cfg.StellarRPCURL, cfg.NetworkPassphrase, issuer)
	asset := txnbuild.CreditAsset{Code: devenvFeeAssetCode, Issuer: issuer.Address()}

	// 1. Trustline: the ephemeral account must trust the FEE asset first
	// (contract tokens skip this; classic issued assets cannot).
	if err := sender.SubmitClassicOperation(ctx, &txnbuild.ChangeTrust{
		Line:          asset.MustToChangeTrustAsset(),
		SourceAccount: cfg.Sender.Address(),
		Limit:         "1000000", // 1M whole tokens — ample for fee dispensing
	}); err != nil {
		t.Fatalf("ephemeral sender ChangeTrust(%s): %v", devenvFeeAssetCode, err)
	}

	// 2. Keep the deterministic issuer alive: best-effort Friendbot top-up (a
	// no-op error on an existing funded account), and let the Payment below
	// fail loudly if the issuer truly lacks XLM.
	if err := helpers.FundViaFriendbot(cfg.FriendbotURL, issuer.Address()); err != nil {
		l.Debug().Err(err).Str("issuer", issuer.Address()).Msg("Friendbot top-up of the FEE issuer (non-fatal if already funded)")
	}

	// 3. Issuer Payment — fresh FEE units straight to the ephemeral sender.
	if err := issuerDeployer.SubmitClassicOperation(ctx, &txnbuild.Payment{
		Destination:   cfg.Sender.Address(),
		Amount:        strconv.FormatInt(cfg.FeeFundWhole, 10),
		Asset:         asset,
		SourceAccount: issuer.Address(),
	}); err != nil {
		t.Fatalf("FEE issuer payment of %d whole tokens to %s: %v (does the issuer account %s hold XLM?)",
			cfg.FeeFundWhole, cfg.Sender.Address(), err, issuer.Address())
	}
	l.Info().
		Str("sender", cfg.Sender.Address()).
		Str("feeToken", cfg.FeeTokenID).
		Int64("wholeTokens", cfg.FeeFundWhole).
		Msg("funded ephemeral Stellar sender (Friendbot XLM + deterministic FEE issuer)")
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
