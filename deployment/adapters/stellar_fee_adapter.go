package adapters

import (
	"fmt"

	"github.com/Masterminds/semver/v3"
	"github.com/stellar/go-stellar-sdk/strkey"

	fqopstype "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	"github.com/smartcontractkit/chainlink-ccip/deployment/fees"
	"github.com/smartcontractkit/chainlink-ccip/deployment/lanes"
	datastore_utils "github.com/smartcontractkit/chainlink-ccip/deployment/utils/datastore"
	seqcore "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

var _ fees.FeeAdapter = (*StellarFeeAdapter)(nil)
var _ fees.FeeResolver = (*StellarFeeAdapter)(nil)

// StellarFeeAdapter implements fees.FeeAdapter for the Stellar FeeQuoter.
type StellarFeeAdapter struct{}

func (a *StellarFeeAdapter) GetFeeContractRef(_ cldf_ops.Bundle, _ cldf_chain.BlockChains, ds datastore.DataStore, onRamp datastore.AddressRef, src uint64, dst uint64) (datastore.AddressRef, error) {
	toRef := func(ref datastore.AddressRef) (datastore.AddressRef, error) { return ref, nil }
	return datastore_utils.FindAndFormatRef(ds, datastore.AddressRef{
		Type:    datastore.ContractType(fqopstype.ContractType),
		Version: semver.MustParse(fqopstype.Deploy.Version()),
	}, src, toRef)
}

func (a *StellarFeeAdapter) SetTokenTransferFee(_ datastore.DataStore, fq datastore.AddressRef) *cldf_ops.Sequence[fees.SetTokenTransferFeeSequenceInput, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		stellarsequences.StellarSetTokenTransferFee.ID(),
		stellarops.ContractDeploymentVersion,
		stellarsequences.StellarSetTokenTransferFee.Description(),
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in fees.SetTokenTransferFeeSequenceInput) (seqcore.OnChainOutput, error) {
			fqStrkey, err := scval.HexToContractStrkey(fq.Address)
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("fee quoter datastore address to strkey for chain %d: %w", in.Selector, err)
			}
			report, err := cldf_ops.ExecuteSequence(b, stellarsequences.StellarSetTokenTransferFee, chains, stellarsequences.StellarSetTokenTransferFeeInput{
				SetTokenTransferFeeSequenceInput: in,
				FQContractID:                     fqStrkey,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			return report.Output, nil
		},
	)
}

func (a *StellarFeeAdapter) GetOnchainTokenTransferFeeConfig(b cldf_ops.Bundle, chains cldf_chain.BlockChains, fq datastore.AddressRef, src uint64, dst uint64, token string) (fees.TokenTransferFeeArgs, error) {
	fqStrkey, deps, err := feeQuoterReadDeps(chains, fq, src)
	if err != nil {
		return fees.TokenTransferFeeArgs{}, err
	}
	return getOnchainTokenTransferFeeConfig(b, deps, fqStrkey, src, dst, token)
}

// getOnchainTokenTransferFeeConfig reads one token's fee config from the
// FeeQuoter and maps it to the chain-agnostic fee args.
func getOnchainTokenTransferFeeConfig(b cldf_ops.Bundle, deps stellardeps.StellarDeps, fqStrkey string, src uint64, dst uint64, token string) (fees.TokenTransferFeeArgs, error) {
	if _, err := decodeTokenStrkey(token); err != nil {
		return fees.TokenTransferFeeArgs{}, fmt.Errorf("invalid token address %q for src %d and dst %d: %w", token, src, dst, err)
	}
	report, err := cldf_ops.ExecuteOperation(b, fqops.GetTokenFeeConfig, deps, fqops.GetTokenFeeConfigInput{
		ContractID:        fqStrkey,
		DestChainSelector: dst,
		Token:             token,
	})
	if err != nil {
		return fees.TokenTransferFeeArgs{}, fmt.Errorf("get token fee config for src %d, dst %d, token %s: %w", src, dst, token, err)
	}
	return tokenFeeConfigToArgs(*report.Output), nil
}

// tokenFeeConfigToArgs is the inverse of the write-side mapping in
// stellarsequences.StellarSetTokenTransferFee. MaxFeeUSDCents and DeciBps have
// no on-chain counterpart in the 2.0 FeeQuoter (EVM returns zero for both).
func tokenFeeConfigToArgs(cfg fqbindings.TokenTransferFeeConfig) fees.TokenTransferFeeArgs {
	return fees.TokenTransferFeeArgs{
		DestBytesOverhead: cfg.DestBytesOverhead,
		DestGasOverhead:   cfg.DestGasOverhead,
		MinFeeUSDCents:    cfg.FeeUsdCents,
		IsEnabled:         cfg.IsEnabled,
	}
}

func (a *StellarFeeAdapter) GetDefaultTokenTransferFeeConfig(_, _ uint64) fees.TokenTransferFeeArgs {
	return fees.TokenTransferFeeArgs{
		MinFeeUSDCents:    50,
		MaxFeeUSDCents:    500,
		DestBytesOverhead: 0,
		DestGasOverhead:   50_000,
		DeciBps:           0,
		IsEnabled:         true,
	}
}

// Validate validates the FeeQuoter address reference for Stellar.
// Stellar contract IDs should be 32 bytes when decoded from hex.
func (a *StellarFeeAdapter) Validate(_ cldf.Environment, feeRef datastore.AddressRef) error {
	if feeRef.Address == "" {
		return fmt.Errorf("fee quoter address is empty")
	}
	// Try to convert to strkey to validate format
	_, err := scval.HexToContractStrkey(feeRef.Address)
	if err != nil {
		return fmt.Errorf("invalid fee quoter address %q: %w", feeRef.Address, err)
	}
	return nil
}

func (a *StellarFeeAdapter) ApplyDestChainConfigUpdates(_ datastore.DataStore, feeRef datastore.AddressRef) *cldf_ops.Sequence[fees.ApplyDestChainConfigSequenceInput, seqcore.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		stellarsequences.StellarApplyDestChainConfig.ID(),
		stellarops.ContractDeploymentVersion,
		stellarsequences.StellarApplyDestChainConfig.Description(),
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, in fees.ApplyDestChainConfigSequenceInput) (seqcore.OnChainOutput, error) {
			fqStrkey, err := scval.HexToContractStrkey(feeRef.Address)
			if err != nil {
				return seqcore.OnChainOutput{}, fmt.Errorf("fee quoter datastore address to strkey for chain %d: %w", in.Selector, err)
			}
			report, err := cldf_ops.ExecuteSequence(b, stellarsequences.StellarApplyDestChainConfig, chains, stellarsequences.StellarApplyDestChainConfigInput{
				ApplyDestChainConfigSequenceInput: in,
				FQContractID:                      fqStrkey,
			})
			if err != nil {
				return seqcore.OnChainOutput{}, err
			}
			return report.Output, nil
		},
	)
}

func (a *StellarFeeAdapter) GetOnchainDestChainConfig(b cldf_ops.Bundle, chains cldf_chain.BlockChains, fq datastore.AddressRef, src uint64, dst uint64) (lanes.FeeQuoterDestChainConfig, error) {
	fqStrkey, deps, err := feeQuoterReadDeps(chains, fq, src)
	if err != nil {
		return lanes.FeeQuoterDestChainConfig{}, err
	}
	return getOnchainDestChainConfig(b, deps, fqStrkey, src, dst)
}

// getOnchainDestChainConfig reads the FeeQuoter dest chain config and maps it
// to the chain-agnostic lane config.
func getOnchainDestChainConfig(b cldf_ops.Bundle, deps stellardeps.StellarDeps, fqStrkey string, src uint64, dst uint64) (lanes.FeeQuoterDestChainConfig, error) {
	report, err := cldf_ops.ExecuteOperation(b, fqops.GetDestChainConfig, deps, fqops.GetDestChainConfigInput{
		ContractID:        fqStrkey,
		DestChainSelector: dst,
	})
	if err != nil {
		return lanes.FeeQuoterDestChainConfig{}, fmt.Errorf("get dest chain config for src %d, dst %d: %w", src, dst, err)
	}
	// DestinationChainNotEnabled: not configured, callers apply fresh defaults
	// (EVM zero-struct semantics).
	if report.Output == nil {
		return lanes.FeeQuoterDestChainConfig{}, nil
	}
	return destChainConfigToLanes(*report.Output), nil
}

// destChainConfigToLanes is the inverse of the write-side mapping in
// stellarsequences.StellarApplyDestChainConfig. The write side widens the
// uint8/uint16 lane fields, so the round trip is lossless.
func destChainConfigToLanes(cfg fqbindings.DestChainConfig) lanes.FeeQuoterDestChainConfig {
	return lanes.FeeQuoterDestChainConfig{
		IsEnabled:                   cfg.IsEnabled,
		MaxDataBytes:                cfg.MaxDataBytes,
		MaxPerMsgGasLimit:           cfg.MaxPerMsgGasLimit,
		DestGasOverhead:             cfg.DestGasOverhead,
		DestGasPerPayloadByteBase:   uint8(cfg.DestGasPerPayloadByte),
		DefaultTokenFeeUSDCents:     uint16(cfg.DefaultTokenFeeUsd),
		DefaultTokenDestGasOverhead: cfg.DefaultTokenDestGas,
		DefaultTxGasLimit:           cfg.DefaultTxGasLimit,
		NetworkFeeUSDCents:          uint16(cfg.NetworkFeeUsdCents),
		V2Params: &lanes.FeeQuoterV2Params{
			LinkFeeMultiplierPercent: uint8(cfg.LinkPremiumPercent),
		},
	}
}

// GetOnRampRef implements fees.FeeResolver: resolves the OnRamp serving the
// (src, dst) lane by reading the Router's get_onramp live, then returns the
// canonical OnRamp datastore ref. The shared fee changesets use the ref's
// version to pick the fee adapter.
func (a *StellarFeeAdapter) GetOnRampRef(b cldf_ops.Bundle, chains cldf_chain.BlockChains, ds datastore.DataStore, src uint64, dst uint64) (datastore.AddressRef, error) {
	routerStrkey, err := stellarccip.RouterDatastoreRef().LookupStrkey(ds, src)
	if err != nil {
		return datastore.AddressRef{}, fmt.Errorf("resolve Router ref for chain %d: %w", src, err)
	}
	deps, err := stellarReadDeps(chains, src)
	if err != nil {
		return datastore.AddressRef{}, err
	}
	return getOnRampRef(b, deps, routerStrkey, src, dst)
}

// getOnRampRef reads the Router's onramp for the (src, dst) lane and builds
// the canonical OnRamp AddressRef for it.
func getOnRampRef(b cldf_ops.Bundle, deps stellardeps.StellarDeps, routerStrkey string, src uint64, dst uint64) (datastore.AddressRef, error) {
	report, err := cldf_ops.ExecuteOperation(b, routerops.GetOnramp, deps, routerops.GetOnrampInput{
		ContractID:        routerStrkey,
		DestChainSelector: dst,
	})
	if err != nil {
		return datastore.AddressRef{}, fmt.Errorf("router get_onramp for src %d, dst %d: %w", src, dst, err)
	}
	// Not routed (UnsupportedDestinationChain #63): the op maps it to nil.
	if report.Output == nil || *report.Output == "" {
		return datastore.AddressRef{}, fmt.Errorf("no OnRamp contract found for src %d and dst %d", src, dst)
	}
	hexAddr, err := stellarutil.StrkeyToHex(*report.Output)
	if err != nil {
		return datastore.AddressRef{}, fmt.Errorf("onramp strkey to hex for src %d, dst %d: %w", src, dst, err)
	}
	return stellarccip.OnRampDatastoreRef().FullAddressRef(src, hexAddr), nil
}

// feeQuoterReadDeps resolves the FeeQuoter strkey and the operation deps for a
// live read on the source chain's FeeQuoter.
func feeQuoterReadDeps(chains cldf_chain.BlockChains, fq datastore.AddressRef, src uint64) (string, stellardeps.StellarDeps, error) {
	fqStrkey, err := scval.HexToContractStrkey(fq.Address)
	if err != nil {
		return "", stellardeps.StellarDeps{}, fmt.Errorf("fee quoter datastore address to strkey for chain %d: %w", src, err)
	}
	deps, err := stellarReadDeps(chains, src)
	if err != nil {
		return "", stellardeps.StellarDeps{}, err
	}
	return fqStrkey, deps, nil
}

// stellarReadDeps resolves the operation deps for a live read on one Stellar
// chain.
func stellarReadDeps(chains cldf_chain.BlockChains, src uint64) (stellardeps.StellarDeps, error) {
	ch, ok := chains.StellarChains()[src]
	if !ok {
		return stellardeps.StellarDeps{}, fmt.Errorf("stellar chain %d not found", src)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch)
	if err != nil {
		return stellardeps.StellarDeps{}, fmt.Errorf("stellar deployer from chain %d: %w", src, err)
	}
	return stellardeps.FromDeployer(dep), nil
}

// decodeTokenStrkey validates a token address as a C…/G… strkey.
func decodeTokenStrkey(token string) ([]byte, error) {
	if token == "" {
		return nil, fmt.Errorf("empty token address")
	}
	if token[0] == 'G' {
		decoded, err := strkey.Decode(strkey.VersionByteAccountID, token)
		if err != nil {
			return nil, fmt.Errorf("decode account strkey: %w", err)
		}
		return decoded, nil
	}
	decoded, err := strkey.Decode(strkey.VersionByteContract, token)
	if err != nil {
		return nil, fmt.Errorf("decode contract strkey: %w", err)
	}
	return decoded, nil
}

func (a *StellarFeeAdapter) GetDefaultDestChainConfig(_, _ uint64) lanes.FeeQuoterDestChainConfig {
	return (&StellarChainFamilyAdapter{}).GetFeeQuoterDestChainConfig()
}
