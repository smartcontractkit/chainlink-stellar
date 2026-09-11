package ccvchain

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/keypair"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
)

// stellarCCIPDeployHost adapts *Chain to [stellarccip.CCIPDevenvHost] without an import cycle.
type stellarCCIPDeployHost struct {
	c *Chain
}

var _ stellarccip.CCIPDevenvHost = (*stellarCCIPDeployHost)(nil)

func (h *stellarCCIPDeployHost) Logger() *zerolog.Logger { return &h.c.logger }
func (h *stellarCCIPDeployHost) Deployer() *stellardeployment.Deployer {
	return h.c.deployer
}
func (h *stellarCCIPDeployHost) DeployerKeypair() *keypair.Full { return h.c.deployerKeypair }
func (h *stellarCCIPDeployHost) NetworkPassphrase() string      { return h.c.networkPassphrase }
func (h *stellarCCIPDeployHost) FriendbotURL() string           { return h.c.friendbotURL }

func (h *stellarCCIPDeployHost) SetOnRamp(contractID string, client *onrampbindings.OnRampClient) {
	h.c.onRampContractID = contractID
	h.c.onRampClient = client
}
func (h *stellarCCIPDeployHost) OnRampClient() *onrampbindings.OnRampClient { return h.c.onRampClient }

func (h *stellarCCIPDeployHost) FeeQuoterClient() *fqbindings.FeeQuoterClient {
	return h.c.feeQuoterClient
}
func (h *stellarCCIPDeployHost) SetFeeQuoter(client *fqbindings.FeeQuoterClient) {
	h.c.feeQuoterClient = client
}

func (h *stellarCCIPDeployHost) SetTokenAdminRegistry(contractID string, client *tarbindings.TokenAdminRegistryClient) {
	h.c.tokenAdminRegistryContractID = contractID
	h.c.tokenAdminRegistryClient = client
}

func (h *stellarCCIPDeployHost) TokenAdminRegistryClient() *tarbindings.TokenAdminRegistryClient {
	return h.c.tokenAdminRegistryClient
}

func (h *stellarCCIPDeployHost) SetTokenPool(contractID string, client *tokenpoolbindings.TokenPoolClient) {
	h.c.tokenPoolContractID = contractID
	h.c.tokenPoolClient = client
}

func (h *stellarCCIPDeployHost) SetLegacyLockReleasePool(contractID string) {
	h.c.legacyLockReleasePoolID = contractID
}

func (h *stellarCCIPDeployHost) SetTokenLockBox(contractID string) {
	h.c.tokenLockBoxContractID = contractID
}

func (h *stellarCCIPDeployHost) LatestLedgerSequence(ctx context.Context) (uint32, error) {
	if h.c.rpcClient == nil {
		return 0, fmt.Errorf("rpc client is nil")
	}
	ledger, err := h.c.rpcClient.GetLatestLedger(ctx)
	if err != nil {
		return 0, err
	}
	return ledger.Sequence, nil
}

func (h *stellarCCIPDeployHost) SetTestToken(contractID string) { h.c.testTokenContractID = contractID }
func (h *stellarCCIPDeployHost) TestTokenContractID() string    { return h.c.testTokenContractID }

func (h *stellarCCIPDeployHost) SetFeeToken(contractID string) { h.c.feeTokenContractID = contractID }
func (h *stellarCCIPDeployHost) FeeTokenContractID() string    { return h.c.feeTokenContractID }
func (h *stellarCCIPDeployHost) CreateFeeToken(ctx context.Context, friendbotURL string) (string, error) {
	return h.c.createFeeToken(ctx, friendbotURL)
}

func (h *stellarCCIPDeployHost) SetOffRamp(contractID string, client *offrampbindings.OffRampClient) {
	h.c.offRampContractID = contractID
	h.c.offRampClient = client
}
func (h *stellarCCIPDeployHost) OffRampClient() *offrampbindings.OffRampClient {
	return h.c.offRampClient
}

func (h *stellarCCIPDeployHost) SetRouter(contractID string, client *routerbindings.RouterClient) {
	h.c.routerContractID = contractID
	h.c.routerClient = client
}

func (h *stellarCCIPDeployHost) RouterContractID() string { return h.c.routerContractID }

func (h *stellarCCIPDeployHost) SetRampRegistry(contractID string) {
	h.c.rampRegistryContractID = contractID
}
func (h *stellarCCIPDeployHost) RampRegistryContractID() string { return h.c.rampRegistryContractID }

func (h *stellarCCIPDeployHost) SetVVR(contractID string) { h.c.vvrContractID = contractID }
func (h *stellarCCIPDeployHost) SetCV(contractID string)  { h.c.cvContractID = contractID }

func (h *stellarCCIPDeployHost) SetReceiver(contractID string) { h.c.receiverContractID = contractID }

func (h *stellarCCIPDeployHost) CreateTestToken(ctx context.Context, friendbotURL string) (string, error) {
	return h.c.createTestToken(ctx, friendbotURL)
}

// CCIPDevenvHost returns the Soroban devenv host used by [github.com/smartcontractkit/chainlink-stellar/deployment/sequences.DeployStellarCCIPContracts], [github.com/smartcontractkit/chainlink-stellar/deployment/ccip.DeployLockReleaseTestTokenPool], and sequences when building from CLDF BlockChains.
func (c *Chain) CCIPDevenvHost() stellarccip.CCIPDevenvHost {
	return &stellarCCIPDeployHost{c: c}
}
