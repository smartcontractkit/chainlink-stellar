package devenv

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/keypair"

	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	tokenpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// Host exposes chain runtime state and callbacks required for Stellar CCIP devenv deploy.
// Implemented by ccv/chain without creating an import cycle (this package does not import ccv/chain).
type Host interface {
	// Logger returns a pointer so chained Info()/Warn() calls are addressable (zerolog uses pointer receivers).
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
	SetTestToken(contractID string)
	TestTokenContractID() string
	SetOffRamp(contractID string, client *offrampbindings.OffRampClient)
	OffRampClient() *offrampbindings.OffRampClient
	SetRouter(contractID string, client *routerbindings.RouterClient)
	RouterContractID() string
	SetVVR(contractID string)
	SetCV(contractID string)
	SetReceiver(contractID string)

	SetFeeToken(contractID string)
	FeeTokenContractID() string
	CreateFeeToken(ctx context.Context, friendbotURL string) (string, error)

	BuildOnRampDestConfigs(ds datastore.DataStore, remoteSelectors []uint64, defaultExecutor string, useRemoteOffRamp bool) ([]onrampbindings.DestChainConfigArgs, error)
	BuildOffRampSourceConfigs(ds datastore.DataStore, remoteSelectors []uint64, useRemoteOnRamp bool) ([]offrampbindings.SourceChainConfigArgs, error)
	CreateTestToken(ctx context.Context, friendbotURL string) (string, error)
}
