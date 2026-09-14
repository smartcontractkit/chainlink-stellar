package ccip

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	cldfstellar "github.com/smartcontractkit/chainlink-deployments-framework/chain/stellar"
	"github.com/stellar/go-stellar-sdk/keypair"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// DefaultStellarDeployZerolog returns a stderr console logger for Stellar deploy sequences
// when no environment-specific logger is wired.
func DefaultStellarDeployZerolog() zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()
}

// CLDFStellarCCIPDevenvHost implements [CCIPDevenvHost] from a CLDF [cldfstellar.Chain] (BlockChains entry),
// so DeployChainContracts does not need a side-registered *ccvchain.Chain.
type CLDFStellarCCIPDevenvHost struct {
	ch      cldfstellar.Chain
	lg      zerolog.Logger
	dep     *stellardeployment.Deployer
	ownerKp *keypair.Full

	onRampContractID        string
	onRampClient            *onrampbindings.OnRampClient
	feeQuoterClient         *fqbindings.FeeQuoterClient
	tarContractID           string
	tarClient               *tarbindings.TokenAdminRegistryClient
	tokenPoolContractID     string
	tokenPoolClient         *tokenpoolbindings.TokenPoolClient
	legacyLockReleasePoolID string
	tokenLockBoxContractID  string
	testTokenContractID     string
	offRampContractID       string
	offRampClient           *offrampbindings.OffRampClient
	routerContractID        string
	routerClient            *routerbindings.RouterClient
	rampRegistryContractID  string
	vvrContractID           string
	cvContractID            string
	receiverContractID      string
	feeTokenContractID      string
}

var _ CCIPDevenvHost = (*CLDFStellarCCIPDevenvHost)(nil)

// NewCLDFStellarCCIPDevenvHost builds a devenv host backed by CLDF Stellar chain metadata and RPC.
func NewCLDFStellarCCIPDevenvHost(ch cldfstellar.Chain, lg zerolog.Logger, dep *stellardeployment.Deployer) (*CLDFStellarCCIPDevenvHost, error) {
	if dep == nil {
		return nil, fmt.Errorf("deployer is nil")
	}
	if ch.Signer == nil {
		return nil, fmt.Errorf("stellar chain Signer is nil")
	}
	kp := ch.Signer.KeypairFull()
	if kp == nil {
		return nil, fmt.Errorf("chain signer KeypairFull is nil")
	}
	return &CLDFStellarCCIPDevenvHost{ch: ch, lg: lg, dep: dep, ownerKp: kp}, nil
}

func (h *CLDFStellarCCIPDevenvHost) Logger() *zerolog.Logger { return &h.lg }
func (h *CLDFStellarCCIPDevenvHost) Deployer() *stellardeployment.Deployer {
	return h.dep
}
func (h *CLDFStellarCCIPDevenvHost) DeployerKeypair() *keypair.Full { return h.ownerKp }
func (h *CLDFStellarCCIPDevenvHost) NetworkPassphrase() string {
	return h.ch.NetworkPassphrase
}
func (h *CLDFStellarCCIPDevenvHost) FriendbotURL() string { return h.ch.FriendbotURL }

func (h *CLDFStellarCCIPDevenvHost) SetOnRamp(contractID string, client *onrampbindings.OnRampClient) {
	h.onRampContractID = contractID
	h.onRampClient = client
}
func (h *CLDFStellarCCIPDevenvHost) OnRampClient() *onrampbindings.OnRampClient {
	return h.onRampClient
}

func (h *CLDFStellarCCIPDevenvHost) FeeQuoterClient() *fqbindings.FeeQuoterClient {
	return h.feeQuoterClient
}
func (h *CLDFStellarCCIPDevenvHost) SetFeeQuoter(client *fqbindings.FeeQuoterClient) {
	h.feeQuoterClient = client
}

func (h *CLDFStellarCCIPDevenvHost) SetTokenAdminRegistry(contractID string, client *tarbindings.TokenAdminRegistryClient) {
	h.tarContractID = contractID
	h.tarClient = client
}
func (h *CLDFStellarCCIPDevenvHost) TokenAdminRegistryClient() *tarbindings.TokenAdminRegistryClient {
	return h.tarClient
}

func (h *CLDFStellarCCIPDevenvHost) SetTokenPool(contractID string, client *tokenpoolbindings.TokenPoolClient) {
	h.tokenPoolContractID = contractID
	h.tokenPoolClient = client
}

func (h *CLDFStellarCCIPDevenvHost) SetLegacyLockReleasePool(contractID string) {
	h.legacyLockReleasePoolID = contractID
}

func (h *CLDFStellarCCIPDevenvHost) SetTokenLockBox(contractID string) {
	h.tokenLockBoxContractID = contractID
}

func (h *CLDFStellarCCIPDevenvHost) LatestLedgerSequence(ctx context.Context) (uint32, error) {
	if h.ch.Client == nil {
		return 0, fmt.Errorf("stellar rpc client is nil")
	}
	ledger, err := h.ch.Client.GetLatestLedger(ctx)
	if err != nil {
		return 0, err
	}
	return ledger.Sequence, nil
}

func (h *CLDFStellarCCIPDevenvHost) SetTestToken(contractID string) {
	h.testTokenContractID = contractID
}
func (h *CLDFStellarCCIPDevenvHost) TestTokenContractID() string { return h.testTokenContractID }

func (h *CLDFStellarCCIPDevenvHost) SetOffRamp(contractID string, client *offrampbindings.OffRampClient) {
	h.offRampContractID = contractID
	h.offRampClient = client
}
func (h *CLDFStellarCCIPDevenvHost) OffRampClient() *offrampbindings.OffRampClient {
	return h.offRampClient
}

func (h *CLDFStellarCCIPDevenvHost) SetRouter(contractID string, client *routerbindings.RouterClient) {
	h.routerContractID = contractID
	h.routerClient = client
}
func (h *CLDFStellarCCIPDevenvHost) RouterContractID() string { return h.routerContractID }

func (h *CLDFStellarCCIPDevenvHost) SetRampRegistry(contractID string) {
	h.rampRegistryContractID = contractID
}
func (h *CLDFStellarCCIPDevenvHost) RampRegistryContractID() string {
	return h.rampRegistryContractID
}

func (h *CLDFStellarCCIPDevenvHost) SetVVR(contractID string) { h.vvrContractID = contractID }
func (h *CLDFStellarCCIPDevenvHost) SetCV(contractID string)  { h.cvContractID = contractID }

func (h *CLDFStellarCCIPDevenvHost) SetReceiver(contractID string) { h.receiverContractID = contractID }

func (h *CLDFStellarCCIPDevenvHost) SetFeeToken(contractID string) { h.feeTokenContractID = contractID }
func (h *CLDFStellarCCIPDevenvHost) FeeTokenContractID() string    { return h.feeTokenContractID }

func (h *CLDFStellarCCIPDevenvHost) CreateFeeToken(ctx context.Context, friendbotURL string) (string, error) {
	id, _, err := DeployDevenvSACToken(DevenvSACTokenParams{
		Ctx:               ctx,
		RPCClient:         h.ch.Client,
		MainDeployer:      h.dep,
		OwnerKeypair:      h.ownerKp,
		NetworkPassphrase: h.ch.NetworkPassphrase,
		FriendbotURL:      friendbotURL,
		IssuerSeedLabel:   fmt.Sprintf("fee-token-issuer-%s", h.ch.NetworkPassphrase),
		AssetCode:         "FEE",
		Logger:            h.Logger(),
	})
	return id, err
}

func (h *CLDFStellarCCIPDevenvHost) CreateTestToken(ctx context.Context, friendbotURL string) (string, error) {
	id, _, err := DeployDevenvSACToken(DevenvSACTokenParams{
		Ctx:               ctx,
		RPCClient:         h.ch.Client,
		MainDeployer:      h.dep,
		OwnerKeypair:      h.ownerKp,
		NetworkPassphrase: h.ch.NetworkPassphrase,
		FriendbotURL:      friendbotURL,
		IssuerSeedLabel:   fmt.Sprintf("test-token-issuer-%s", h.ch.NetworkPassphrase),
		AssetCode:         "TEST",
		Logger:            h.Logger(),
	})
	return id, err
}
