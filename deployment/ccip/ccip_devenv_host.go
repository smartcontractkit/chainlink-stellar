package ccip

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/keypair"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// CCIPDevenvHost is the runtime surface Stellar CCIP devenv deploy needs (implemented by ccv/chain).
// Kept in deployment/ccip so deployment/sequences can depend on it without importing ccv/chain.
type CCIPDevenvHost interface {
	Logger() *zerolog.Logger
	Deployer() *stellardeployment.Deployer
	DeployerKeypair() *keypair.Full
	NetworkPassphrase() string
	FriendbotURL() string

	SetOnRamp(contractID string, client *onrampbindings.OnRampClient)
	OnRampClient() *onrampbindings.OnRampClient
	FeeQuoterClient() *fqbindings.FeeQuoterClient
	SetFeeQuoter(client *fqbindings.FeeQuoterClient)
	SetTokenAdminRegistry(contractID string, client *tarbindings.TokenAdminRegistryClient)
	TokenAdminRegistryClient() *tarbindings.TokenAdminRegistryClient
	SetTokenPool(contractID string, client *tokenpoolbindings.TokenPoolClient)
	SetLegacyLockReleasePool(contractID string)
	SetTokenLockBox(contractID string)
	LatestLedgerSequence(ctx context.Context) (uint32, error)
	SetTestToken(contractID string)
	TestTokenContractID() string
	SetOffRamp(contractID string, client *offrampbindings.OffRampClient)
	OffRampClient() *offrampbindings.OffRampClient
	SetRouter(contractID string, client *routerbindings.RouterClient)
	RouterContractID() string
	SetRampRegistry(contractID string)
	RampRegistryContractID() string
	SetRmnProxy(contractID string)
	RmnProxyContractID() string
	SetVVR(contractID string)
	SetCV(contractID string)
	SetReceiver(contractID string)

	SetFeeToken(contractID string)
	FeeTokenContractID() string
	CreateFeeToken(ctx context.Context, friendbotURL string) (string, error)

	CreateTestToken(ctx context.Context, friendbotURL string) (string, error)
}
