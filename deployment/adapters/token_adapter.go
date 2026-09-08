package adapters

import (
	"encoding/hex"
	"fmt"
	"strings"

	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	datastore_utils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	"github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
)

var _ tokens.TokenAdapter = (*StellarTokenAdapter)(nil)

// StellarTokenAdapter implements tokens.TokenAdapter for Stellar token pools.
// Pool deployment and TAR registration are handled by the Stellar deploy
// pipeline (PostDeployContractsForSelector), so most sequences are no-ops.
// The adapter's primary role is to provide address encoding so that the
// shared ConfigureTokensForTransfers changeset can resolve Stellar pool/token
// addresses when configuring EVM pools with Stellar remotes.
type StellarTokenAdapter struct{}

func (a *StellarTokenAdapter) AddressRefToBytes(ref datastore.AddressRef) ([]byte, error) {
	addr := strings.TrimPrefix(ref.Address, "0x")
	b, err := hex.DecodeString(addr)
	if err != nil {
		return nil, fmt.Errorf("decode stellar hex address %q: %w", ref.Address, err)
	}
	return b, nil
}

func (a *StellarTokenAdapter) DeriveTokenAddress(e deployment.Environment, chainSelector uint64, poolRef datastore.AddressRef) (string, error) {
	qualifier := poolRef.Qualifier
	if qualifier == "" {
		qualifier = stellarccip.DevenvTestTokenPoolQualifier
	}
	tokenRef, err := datastore_utils.FindAndFormatRef(e.DataStore, datastore.AddressRef{
		Type:      datastore.ContractType(stellarccip.TestTokenContractType),
		Version:   stellarops.ContractDeploymentVersion,
		Qualifier: qualifier,
	}, chainSelector, datastore_utils.FullRef)
	if err != nil {
		return "", fmt.Errorf("find test token ref for chain %d: %w", chainSelector, err)
	}
	return tokenRef.Address, nil
}

const stellarTestTokenDecimals = 7

func (a *StellarTokenAdapter) DeriveTokenDecimals(_ deployment.Environment, _ uint64, _ datastore.AddressRef, _ []byte) (uint8, error) {
	return stellarTestTokenDecimals, nil
}

func (a *StellarTokenAdapter) DeriveTokenPoolCounterpart(_ deployment.Environment, _ uint64, tokenPool []byte, _ []byte) ([]byte, error) {
	return tokenPool, nil
}

var stellarConfigureTokenNoOp = cldf_ops.NewSequence(
	"StellarConfigureTokenForTransfers",
	stellarops.ContractDeploymentVersion,
	"No-op: Stellar token pool chain updates are applied during PostConnect",
	func(_ cldf_ops.Bundle, _ cldf_chain.BlockChains, _ tokens.ConfigureTokenForTransfersInput) (sequences.OnChainOutput, error) {
		return sequences.OnChainOutput{}, nil
	},
)

func (a *StellarTokenAdapter) ConfigureTokenForTransfersSequence() *cldf_ops.Sequence[tokens.ConfigureTokenForTransfersInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return stellarConfigureTokenNoOp
}

func (a *StellarTokenAdapter) ManualRegistration() *cldf_ops.Sequence[tokens.ManualRegistrationSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}

func (a *StellarTokenAdapter) SetTokenPoolRateLimits() *cldf_ops.Sequence[tokens.TPRLRemotes, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}

func (a *StellarTokenAdapter) DeployToken() *cldf_ops.Sequence[tokens.DeployTokenInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}

func (a *StellarTokenAdapter) DeployTokenVerify(_ deployment.Environment, _ tokens.DeployTokenInput) error {
	return nil
}

func (a *StellarTokenAdapter) DeployTokenPoolForToken() *cldf_ops.Sequence[tokens.DeployTokenPoolInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}

func (a *StellarTokenAdapter) UpdateAuthorities() *cldf_ops.Sequence[tokens.UpdateAuthoritiesInput, sequences.OnChainOutput, *deployment.Environment] {
	return nil
}

func (a *StellarTokenAdapter) MigrateLockReleasePoolLiquiditySequence() *cldf_ops.Sequence[tokens.MigrateLockReleasePoolLiquidityInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return nil
}
