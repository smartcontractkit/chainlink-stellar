package ccip

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/rs/zerolog"
	"github.com/stellar/go-stellar-sdk/amount"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
)

// devenvSACTargetBalance is the owner's target balance in 7-decimal base units: 10,000,000,000 whole
// tokens, far above the tens of tokens a real CCIP fee quotes (contracts/common/helpers/src/fee_math.rs).
const devenvSACTargetBalance xdr.Int64 = 10_000_000_000 * 10_000_000

// DevenvSACTokenParams configures classic-asset + SAC deploy used for CCIP fee and test tokens in devenv.
type DevenvSACTokenParams struct {
	Ctx               context.Context
	RPCClient         *rpcclient.Client
	MainDeployer      *stellardeployment.Deployer
	OwnerKeypair      *keypair.Full
	NetworkPassphrase string
	FriendbotURL      string
	IssuerSeedLabel   string // e.g. "fee-token-issuer-" + passphrase
	AssetCode         string
	Logger            *zerolog.Logger
}

// DeployDevenvSACToken creates a deterministic issuer, optional friendbot funding, trust + payment, and SAC deploy.
// Each step reads chain state first, so a rerun after any interruption repeats no completed step.
func DeployDevenvSACToken(p DevenvSACTokenParams) (contractID string, issuerKP *keypair.Full, err error) {
	if p.MainDeployer == nil || p.OwnerKeypair == nil || p.RPCClient == nil {
		return "", nil, fmt.Errorf("rpc client, main deployer, and owner keypair are required")
	}
	if p.Logger == nil {
		z := zerolog.Nop()
		p.Logger = &z
	}

	issuerSeed := sha256.Sum256([]byte(p.IssuerSeedLabel))
	issuerKP, err = keypair.FromRawSeed(issuerSeed)
	if err != nil {
		return "", nil, fmt.Errorf("create issuer keypair: %w", err)
	}

	chain := &rpcSACTokenChain{
		rpc:            p.RPCClient,
		ownerDeployer:  p.MainDeployer,
		issuerDeployer: stellardeployment.NewDeployer(p.RPCClient, p.NetworkPassphrase, issuerKP),
		friendbotURL:   p.FriendbotURL,
		logger:         p.Logger,
	}
	contractID, err = reconcileDevenvSACToken(p.Ctx, chain, sacTokenSpec{
		asset:             txnbuild.CreditAsset{Code: p.AssetCode, Issuer: issuerKP.Address()},
		owner:             p.OwnerKeypair.Address(),
		networkPassphrase: p.NetworkPassphrase,
		fundIssuer:        p.FriendbotURL != "",
		logger:            p.Logger,
	})
	if err != nil {
		return "", nil, err
	}

	p.Logger.Info().
		Str("contractID", contractID).
		Str("issuer", issuerKP.Address()).
		Str("asset", p.AssetCode).
		Msg("Devenv SAC token deployed")

	return contractID, issuerKP, nil
}

// sacTokenChain is the chain state and side effects the devenv SAC flow reconciles against.
type sacTokenChain interface {
	AccountExists(ctx context.Context, address string) (bool, error)
	TrustlineBalance(ctx context.Context, owner string, asset xdr.Asset) (balance xdr.Int64, exists bool, err error)
	ContractExists(ctx context.Context, contractID string) (bool, error)
	FundAccount(address string) error
	ChangeTrust(ctx context.Context, owner string, asset txnbuild.CreditAsset) error
	Issue(ctx context.Context, owner string, asset txnbuild.CreditAsset, units xdr.Int64) error
	DeploySAC(ctx context.Context, asset xdr.Asset) (string, error)
}

type sacTokenSpec struct {
	asset             txnbuild.CreditAsset
	owner             string
	networkPassphrase string
	fundIssuer        bool
	logger            *zerolog.Logger
}

func reconcileDevenvSACToken(ctx context.Context, chain sacTokenChain, s sacTokenSpec) (string, error) {
	xdrAsset, err := s.asset.ToXDR()
	if err != nil {
		return "", fmt.Errorf("convert asset to XDR: %w", err)
	}
	contractID, err := stellardeployment.ComputeSACContractID(s.networkPassphrase, xdrAsset)
	if err != nil {
		return "", fmt.Errorf("compute SAC contract ID: %w", err)
	}

	if s.fundIssuer {
		issuerExists, err := chain.AccountExists(ctx, s.asset.Issuer)
		if err != nil {
			return "", fmt.Errorf("read issuer account: %w", err)
		}
		if !issuerExists {
			if err := chain.FundAccount(s.asset.Issuer); err != nil {
				return "", fmt.Errorf("fund issuer: %w", err)
			}
		}
	}

	balance, trusted, err := chain.TrustlineBalance(ctx, s.owner, xdrAsset)
	if err != nil {
		return "", fmt.Errorf("read owner trustline: %w", err)
	}
	if !trusted {
		if err := chain.ChangeTrust(ctx, s.owner, s.asset); err != nil {
			return "", fmt.Errorf("establish trustline: %w", err)
		}
		balance = 0
	}

	if balance < devenvSACTargetBalance {
		if err := chain.Issue(ctx, s.owner, s.asset, devenvSACTargetBalance-balance); err != nil {
			return "", fmt.Errorf("issue tokens: %w", err)
		}
	} else {
		s.logger.Debug().Str("balance", amount.String(balance)).Msg("Owner already holds target SAC token balance")
	}

	deployed, err := chain.ContractExists(ctx, contractID)
	if err != nil {
		return "", fmt.Errorf("read SAC instance: %w", err)
	}
	if !deployed {
		if _, err := chain.DeploySAC(ctx, xdrAsset); err != nil {
			return "", fmt.Errorf("deploy SAC: %w", err)
		}
	}
	return contractID, nil
}

type rpcSACTokenChain struct {
	rpc            *rpcclient.Client
	ownerDeployer  *stellardeployment.Deployer
	issuerDeployer *stellardeployment.Deployer
	friendbotURL   string
	logger         *zerolog.Logger
}

func (c *rpcSACTokenChain) ledgerEntry(ctx context.Context, key xdr.LedgerKey) (xdr.LedgerEntryData, bool, error) {
	keyXDR, err := key.MarshalBinaryBase64()
	if err != nil {
		return xdr.LedgerEntryData{}, false, fmt.Errorf("marshal ledger key: %w", err)
	}
	resp, err := c.rpc.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{Keys: []string{keyXDR}})
	if err != nil {
		return xdr.LedgerEntryData{}, false, fmt.Errorf("get ledger entries: %w", err)
	}
	if len(resp.Entries) == 0 || resp.Entries[0].DataXDR == "" {
		return xdr.LedgerEntryData{}, false, nil
	}
	var data xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &data); err != nil {
		return xdr.LedgerEntryData{}, false, fmt.Errorf("decode ledger entry: %w", err)
	}
	return data, true, nil
}

func (c *rpcSACTokenChain) AccountExists(ctx context.Context, address string) (bool, error) {
	accountID, err := xdr.AddressToAccountId(address)
	if err != nil {
		return false, fmt.Errorf("account id from %s: %w", address, err)
	}
	_, found, err := c.ledgerEntry(ctx, xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: accountID},
	})
	return found, err
}

func (c *rpcSACTokenChain) TrustlineBalance(ctx context.Context, owner string, asset xdr.Asset) (xdr.Int64, bool, error) {
	accountID, err := xdr.AddressToAccountId(owner)
	if err != nil {
		return 0, false, fmt.Errorf("account id from %s: %w", owner, err)
	}
	data, found, err := c.ledgerEntry(ctx, xdr.LedgerKey{
		Type:      xdr.LedgerEntryTypeTrustline,
		TrustLine: &xdr.LedgerKeyTrustLine{AccountId: accountID, Asset: asset.ToTrustLineAsset()},
	})
	if err != nil || !found {
		return 0, false, err
	}
	trustline, ok := data.GetTrustLine()
	if !ok {
		return 0, false, fmt.Errorf("ledger entry for %s trustline is %s", owner, data.Type)
	}
	return trustline.Balance, true, nil
}

func (c *rpcSACTokenChain) ContractExists(ctx context.Context, contractID string) (bool, error) {
	key, err := stellardeployment.ContractInstanceLedgerKey(contractID)
	if err != nil {
		return false, err
	}
	_, found, err := c.ledgerEntry(ctx, key)
	return found, err
}

func (c *rpcSACTokenChain) FundAccount(address string) error {
	return FundAccountViaFriendbot(c.friendbotURL, address, c.logger)
}

func (c *rpcSACTokenChain) ChangeTrust(ctx context.Context, owner string, asset txnbuild.CreditAsset) error {
	return c.ownerDeployer.SubmitClassicOperation(ctx, &txnbuild.ChangeTrust{
		Line:          asset.MustToChangeTrustAsset(),
		SourceAccount: owner,
	})
}

func (c *rpcSACTokenChain) Issue(ctx context.Context, owner string, asset txnbuild.CreditAsset, units xdr.Int64) error {
	return c.issuerDeployer.SubmitClassicOperation(ctx, &txnbuild.Payment{
		Destination:   owner,
		Amount:        amount.String(units),
		Asset:         asset,
		SourceAccount: asset.Issuer,
	})
}

func (c *rpcSACTokenChain) DeploySAC(ctx context.Context, asset xdr.Asset) (string, error) {
	return c.ownerDeployer.DeploySACToken(ctx, asset)
}
