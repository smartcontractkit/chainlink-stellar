package adapters

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	sequences "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	lrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	slrrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tpoolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

var (
	_ tokens.TokenPoolDynamicConfigAdapter = (*StellarTokenAdapter)(nil)
	_ tokens.TokenFeeAdapter               = (*StellarTokenAdapter)(nil)
)

// SetTokenPoolDynamicConfig implements tokens.TokenPoolDynamicConfigAdapter:
// sets the pool's router, rate-limit admin, and fee admin. EVM applies all
// three through one setDynamicConfig call; the Soroban pool exposes three
// separate owner-only entrypoints, so each changed field is applied on its own
// and an unchanged field emits no write (read-before-write). TokenRef is
// ignored: a stellar pool address alone identifies the pool.
//
// Routing: a deployer-owned pool is reconfigured directly; any other owner
// gets one MCMS proposal transaction per changed field, which the owning
// timelock must execute (the proposal is targeted from the MCMS qualifier by
// the shared changeset; the input carries no datastore to verify the owner
// against a deployed timelock here).
func (a *StellarTokenAdapter) SetTokenPoolDynamicConfig() *cldf_ops.Sequence[tokens.SetTokenPoolDynamicConfigSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:set-token-pool-dynamic-config",
		stellarops.ContractDeploymentVersion,
		"Sets the router, rate-limit admin, and fee admin on a Stellar token pool",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.SetTokenPoolDynamicConfigSequenceInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.Selector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.Selector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.Selector, err)
			}
			return setTokenPoolDynamicConfig(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), input)
		},
	)
}

// setTokenPoolDynamicConfig applies the changed dynamic-config fields with
// owner routing: deployer → direct ops plus the ExecInfo marker; any other
// owner → one proposal transaction per changed field.
func setTokenPoolDynamicConfig(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	deployerAddr string,
	input tokens.SetTokenPoolDynamicConfigSequenceInput,
) (sequences.OnChainOutput, error) {
	poolRef := normalizePoolRefType(input.TokenPoolRef)
	poolStrkey, err := ownership.NormalizeContractAddress(poolRef.Address)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("normalize pool address: %w", err)
	}
	if !isRateLimitPoolType(poolRef.Type) {
		return sequences.OnChainOutput{}, fmt.Errorf(
			"unsupported pool type %q for dynamic config on chain %d (supported: %s, %s, %s)",
			poolRef.Type, input.Selector, bmpops.ContractType, lrops.ContractType, slrrops.ContractType)
	}

	owner, err := ownership.ContractOwner(b.GetContext(), deps, poolRef)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read owner of pool %s: %w", poolStrkey, err)
	}

	var proposals []evmcontract.WriteOutput
	executed := false
	apply := func(entrypoint, addr string, directExec func() error) error {
		if owner == deployerAddr {
			if err := directExec(); err != nil {
				return err
			}
			executed = true
			return nil
		}
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload(entrypoint, []xdr.ScVal{
			scval.AddressToScVal(addr),
		})
		if err != nil {
			return err
		}
		proposals = append(proposals, poolProposalWrite(input.Selector, poolRef.Type, poolStrkey, data))
		return nil
	}

	changed := func(desired, current *string) bool {
		if desired == nil {
			return false
		}
		return current == nil || *current != *desired
	}

	if input.Router != nil {
		router, err := normalizePoolConfigAddress(*input.Router, "router", false)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		currentReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRouter, deps, tpoolops.GetRouterInput{ContractID: poolStrkey})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read router of pool %s: %w", poolStrkey, err)
		}
		if changed(&router, currentReport.Output) {
			if err := apply("set_router", router, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.SetRouter, deps, tpoolops.SetRouterInput{
					ContractID: poolStrkey,
					Router:     router,
				})
				return err
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("set router on pool %s: %w", poolStrkey, err)
			}
		}
	}

	if input.RateLimitAdmin != nil {
		admin, err := normalizePoolConfigAddress(*input.RateLimitAdmin, "rate limit admin", true)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		currentReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRateLimitAdmin, deps, tpoolops.GetRateLimitAdminInput{ContractID: poolStrkey})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read rate-limit admin of pool %s: %w", poolStrkey, err)
		}
		if changed(&admin, currentReport.Output) {
			if err := apply("set_rate_limit_admin", admin, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.SetRateLimitAdmin, deps, tpoolops.SetRateLimitAdminInput{
					ContractID: poolStrkey,
					Admin:      admin,
				})
				return err
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("set rate-limit admin on pool %s: %w", poolStrkey, err)
			}
		}
	}

	if input.FeeAdmin != nil {
		feeAdmin, err := normalizePoolConfigAddress(*input.FeeAdmin, "fee admin", true)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		currentReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetFeeAdmin, deps, tpoolops.GetFeeAdminInput{ContractID: poolStrkey})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read fee admin of pool %s: %w", poolStrkey, err)
		}
		if changed(&feeAdmin, currentReport.Output) {
			if err := apply("set_fee_admin", feeAdmin, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.SetFeeAdmin, deps, tpoolops.SetFeeAdminInput{
					ContractID: poolStrkey,
					FeeAdmin:   feeAdmin,
				})
				return err
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("set fee admin on pool %s: %w", poolStrkey, err)
			}
		}
	}

	return dynamicConfigOutput(input.Selector, poolRef.Type, poolStrkey, executed, proposals)
}

// dynamicConfigOutput assembles the sequence output: the direct-exec marker
// when any pool executed directly (labeled with the last executed pool's
// identity), plus the proposal batch.
func dynamicConfigOutput(
	chainSelector uint64,
	markerType datastore.ContractType,
	markerTo string,
	executed bool,
	proposals []evmcontract.WriteOutput,
) (sequences.OnChainOutput, error) {
	if !executed && len(proposals) == 0 {
		return sequences.OnChainOutput{}, nil
	}
	var ops []mcmstypes.BatchOperation
	if executed {
		marker, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
			ChainSelector: chainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{ContractType: string(markerType)},
				To:                markerTo,
			},
			ExecInfo: &evmcontract.ExecInfo{Hash: "stellar-set-token-pool-dynamic-config-direct-exec"},
		}})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("build batch operation for chain %d: %w", chainSelector, err)
		}
		ops = append(ops, marker)
	}
	if len(proposals) > 0 {
		batch, err := evmcontract.NewBatchOperationFromWrites(proposals)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("build batch operation for chain %d: %w", chainSelector, err)
		}
		ops = append(ops, batch)
	}
	return sequences.OnChainOutput{BatchOps: ops}, nil
}

// SetAllowedFinalityConfig implements tokens.TokenFeeAdapter: sets the
// allowed-finality config on one or more pools, keyed by the pool's address in
// either recorded form. The Settings map is keyed by the pool counterpart
// address the changeset derives, so the full pool ref (and its type) is
// recovered from the datastore by address.
func (a *StellarTokenAdapter) SetAllowedFinalityConfig(e *deployment.Environment) *cldf_ops.Sequence[tokens.SetAllowedFinalityConfigSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:set-allowed-finality-config",
		stellarops.ContractDeploymentVersion,
		"Sets the allowed finality config on Stellar token pools",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.SetAllowedFinalityConfigSequenceInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.Selector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.Selector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.Selector, err)
			}
			return setAllowedFinalityConfigForPools(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), e.DataStore, input)
		},
	)
}

// setAllowedFinalityConfigForPools applies the per-pool allowed-finality
// writes with owner routing via applyPoolAllowedFinality (deployer → direct;
// CLL timelock → proposal; anything else → fail closed).
func setAllowedFinalityConfigForPools(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	deployerAddr string,
	ds datastore.DataStore,
	input tokens.SetAllowedFinalityConfigSequenceInput,
) (sequences.OnChainOutput, error) {
	timelockStrkey, hasTimelock, err := cllTimelockStrkey(ds, input.Selector)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}

	var writes []evmcontract.WriteOutput
	executed := false
	execPoolType := datastore.ContractType("")
	execPoolStrkey := ""
	for poolAddr, finalityConfig := range input.Settings {
		if finalityConfig.IsZero() {
			b.Logger.Warnf("skipping finality configuration for pool %s on chain %d since finality config is zero", poolAddr, input.Selector)
			continue
		}
		if err := finalityConfig.Validate(); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("invalid finality config for pool %s on chain %d: %w", poolAddr, input.Selector, err)
		}
		poolRef, err := findPoolRefByAddress(ds, input.Selector, poolAddr)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		poolStrkey, err := ownership.NormalizeContractAddress(poolRef.Address)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("normalize pool address: %w", err)
		}
		owner, err := ownership.ContractOwner(b.GetContext(), deps, poolRef)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read owner of pool %s: %w", poolStrkey, err)
		}
		raw := finalityConfig.Raw()
		want := binary.BigEndian.Uint32(raw[:])
		exec, proposal, err := applyPoolAllowedFinality(b, deps, owner, deployerAddr, timelockStrkey, hasTimelock, input.Selector, poolRef.Type, poolStrkey, want)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		if exec {
			executed = true
			execPoolType = poolRef.Type
			execPoolStrkey = poolStrkey
		}
		if proposal != nil {
			writes = append(writes, *proposal)
		}
	}
	return dynamicConfigOutput(input.Selector, execPoolType, execPoolStrkey, executed, writes)
}

// SetTokenTransferFee implements tokens.TokenFeeAdapter: sets per-remote token
// transfer fee configs on one or more pools. A nil or disabled fee config for
// a remote disables the pool's override for it (EVM parity). The pool's
// apply_token_fee_config_updates is owner-only on chain, so the same
// deployer-direct vs CLL-timelock-proposal routing as the finality arm
// applies.
func (a *StellarTokenAdapter) SetTokenTransferFee(e *deployment.Environment) *cldf_ops.Sequence[tokens.SetTokenTransferFeeSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:set-token-transfer-fee",
		stellarops.ContractDeploymentVersion,
		"Sets per-remote token transfer fee configs on Stellar token pools",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.SetTokenTransferFeeSequenceInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.Selector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.Selector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.Selector, err)
			}
			return setTokenTransferFeeForPools(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), e.DataStore, input)
		},
	)
}

// setTokenTransferFeeForPools applies the per-pool fee updates with owner
// routing: deployer → direct op plus the marker; CLL timelock → one proposal
// transaction per pool; anything else → fail closed.
func setTokenTransferFeeForPools(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	deployerAddr string,
	ds datastore.DataStore,
	input tokens.SetTokenTransferFeeSequenceInput,
) (sequences.OnChainOutput, error) {
	timelockStrkey, hasTimelock, err := cllTimelockStrkey(ds, input.Selector)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}

	var writes []evmcontract.WriteOutput
	executed := false
	execPoolType := datastore.ContractType("")
	execPoolStrkey := ""
	for poolAddr, cfg := range input.Settings {
		poolRef, err := findPoolRefByAddress(ds, input.Selector, poolAddr)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		poolStrkey, err := ownership.NormalizeContractAddress(poolRef.Address)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("normalize pool address: %w", err)
		}

		adds := make([]tpoolbindings.TokenTransferFeeConfigArgs, 0, len(cfg))
		disables := make([]uint64, 0, len(cfg))
		for dst, fee := range cfg {
			if fee == nil || !fee.IsEnabled {
				disables = append(disables, dst)
				continue
			}
			adds = append(adds, tpoolbindings.TokenTransferFeeConfigArgs{
				DestChainSelector: dst,
				Config:            tokenTransferFeeConfigToBinding(*fee),
			})
		}
		if len(adds) == 0 && len(disables) == 0 {
			continue
		}

		owner, err := ownership.ContractOwner(b.GetContext(), deps, poolRef)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read owner of pool %s: %w", poolStrkey, err)
		}
		switch {
		case owner == deployerAddr:
			if _, err := cldf_ops.ExecuteOperation(b, tpoolops.ApplyTokenFeeConfigUpdates, deps, tpoolops.ApplyTokenFeeConfigUpdatesInput{
				ContractID: poolStrkey,
				Adds:       adds,
				Disables:   disables,
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("apply token fee config updates on pool %s: %w", poolStrkey, err)
			}
			executed = true
			execPoolType = poolRef.Type
			execPoolStrkey = poolStrkey
		case hasTimelock && owner == timelockStrkey:
			data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("apply_token_fee_config_updates", []xdr.ScVal{
				scval.StructSliceToScVal(adds),
				scval.Uint64SliceToScVal(disables),
			})
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			writes = append(writes, poolProposalWrite(input.Selector, poolRef.Type, poolStrkey, data))
		default:
			return sequences.OnChainOutput{}, fmt.Errorf(
				"cannot set token transfer fee on pool %s: owner %q is neither deployer %q nor timelock %q",
				poolStrkey, owner, deployerAddr, timelockStrkey)
		}
	}
	return dynamicConfigOutput(input.Selector, execPoolType, execPoolStrkey, executed, writes)
}

// GetOnchainTokenTransferFeeConfig implements tokens.TokenFeeAdapter: reads
// the pool's per-remote token transfer fee config (the pool's own
// get_token_transfer_fee_config, which fronts the fee machinery the same way
// EVM v2 pools front the FeeQuoter).
func (a *StellarTokenAdapter) GetOnchainTokenTransferFeeConfig(e deployment.Environment, poolAddress string, src uint64, dst uint64) (tokens.TokenTransferFeeConfig, error) {
	deps, err := stellarReadDeps(e.BlockChains, src)
	if err != nil {
		return tokens.TokenTransferFeeConfig{}, err
	}
	return getOnchainPoolTokenTransferFeeConfig(e.OperationsBundle, deps, poolAddress, src, dst)
}

// getOnchainPoolTokenTransferFeeConfig reads one pool's fee config for one
// destination and maps it to the chain-agnostic form. The bps fields narrow
// from the on-chain u32 to the uint16 lane fields; unrepresentable values are
// rejected so a read/modify/write cannot persist truncated settings.
func getOnchainPoolTokenTransferFeeConfig(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolAddress string, src uint64, dst uint64) (tokens.TokenTransferFeeConfig, error) {
	poolStrkey, err := ownership.NormalizeContractAddress(poolAddress)
	if err != nil {
		return tokens.TokenTransferFeeConfig{}, fmt.Errorf("normalize pool address: %w", err)
	}
	report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetTokenTransferFeeConfig, deps, tpoolops.GetTokenTransferFeeConfigInput{
		ContractID:        poolStrkey,
		DestChainSelector: dst,
	})
	if err != nil {
		return tokens.TokenTransferFeeConfig{}, fmt.Errorf(
			"get on-chain token transfer fee config for pool %s on chain selector %d for dest chain selector %d: %w",
			poolStrkey, src, dst, err)
	}
	cfg := report.Output
	if cfg == nil {
		return tokens.TokenTransferFeeConfig{}, fmt.Errorf("no fee config returned for pool %s dest chain %d", poolStrkey, dst)
	}
	if cfg.FinalityTransferFeeBps > math.MaxUint16 || cfg.FastFinalityTransferFeeBps > math.MaxUint16 {
		return tokens.TokenTransferFeeConfig{}, fmt.Errorf(
			"pool %s dest chain %d: bps values (%d, %d) exceed the uint16 lane fields",
			poolStrkey, dst, cfg.FinalityTransferFeeBps, cfg.FastFinalityTransferFeeBps)
	}
	return bindingToTokenTransferFeeConfig(*cfg), nil
}

// GetDefaultTokenTransferFeeConfig implements tokens.TokenFeeAdapter.
func (a *StellarTokenAdapter) GetDefaultTokenTransferFeeConfig(src uint64, dst uint64) tokens.TokenTransferFeeConfig {
	return tokens.GetDefaultChainAgnosticTokenTransferFeeConfig(src, dst)
}

// tokenTransferFeeConfigToBinding maps the chain-agnostic fee config to the
// pool binding form (EVM parity mapping; u16 bps widen to the on-chain u32).
func tokenTransferFeeConfigToBinding(fee tokens.TokenTransferFeeConfig) tpoolbindings.TokenTransferFeeConfig {
	return tpoolbindings.TokenTransferFeeConfig{
		DestGasOverhead:            fee.DestGasOverhead,
		DestBytesOverhead:          fee.DestBytesOverhead,
		FinalityFeeUsdCents:        fee.DefaultFinalityFeeUSDCents,
		FastFinalityFeeUsdCents:    fee.CustomFinalityFeeUSDCents,
		FinalityTransferFeeBps:     uint32(fee.DefaultFinalityTransferFeeBps),
		FastFinalityTransferFeeBps: uint32(fee.CustomFinalityTransferFeeBps),
		IsEnabled:                  fee.IsEnabled,
	}
}

// bindingToTokenTransferFeeConfig is the inverse of
// tokenTransferFeeConfigToBinding.
func bindingToTokenTransferFeeConfig(cfg tpoolbindings.TokenTransferFeeConfig) tokens.TokenTransferFeeConfig {
	return tokens.TokenTransferFeeConfig{
		DefaultFinalityTransferFeeBps: uint16(cfg.FinalityTransferFeeBps),
		CustomFinalityTransferFeeBps:  uint16(cfg.FastFinalityTransferFeeBps),
		DefaultFinalityFeeUSDCents:    cfg.FinalityFeeUsdCents,
		CustomFinalityFeeUSDCents:     cfg.FastFinalityFeeUsdCents,
		DestBytesOverhead:             cfg.DestBytesOverhead,
		DestGasOverhead:               cfg.DestGasOverhead,
		IsEnabled:                     cfg.IsEnabled,
	}
}

// findPoolRefByAddress recovers the full pool ref for a bare pool address
// key. The fee and finality changesets key their Settings maps by the pool's
// counterpart-address string rather than the ref, and ownership reads plus
// proposal metadata need the ref's pool type, so the ref is looked up by
// address in either recorded form.
func findPoolRefByAddress(ds datastore.DataStore, chainSelector uint64, poolAddr string) (datastore.AddressRef, error) {
	refs, err := ds.Addresses().Fetch()
	if err != nil {
		return datastore.AddressRef{}, fmt.Errorf("fetch datastore addresses: %w", err)
	}
	for _, ref := range refs {
		if ref.ChainSelector != chainSelector {
			continue
		}
		normalized := normalizePoolRefType(ref)
		if !isRateLimitPoolType(normalized.Type) {
			continue
		}
		same, err := sameStellarAddress(ref.Address, poolAddr)
		if err != nil {
			return datastore.AddressRef{}, err
		}
		if same {
			return normalized, nil
		}
	}
	return datastore.AddressRef{}, fmt.Errorf("no token pool ref found for address %s on chain %d", poolAddr, chainSelector)
}

// sameStellarAddress reports whether two stellar address strings decode to the
// same raw 32 bytes.
func sameStellarAddress(a, b string) (bool, error) {
	rawA, err := stellarAddressStringToBytes(a)
	if err != nil {
		return false, err
	}
	rawB, err := stellarAddressStringToBytes(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(rawA, rawB), nil
}

// normalizePoolConfigAddress normalizes a desired router/admin address in
// either recorded form to the strkey the pool entrypoints take. G… account
// strkeys pass as-is when allowed (the deployer can hold an admin slot); the
// router must be a non-zero contract (the family's zero address is rejected
// per the shared input contract).
func normalizePoolConfigAddress(addr, what string, allowAccount bool) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("empty %s address", what)
	}
	switch {
	case strings.HasPrefix(addr, "0x"):
		normalized, err := ownership.NormalizeContractAddress(addr)
		if err != nil {
			return "", fmt.Errorf("normalize %s address %q: %w", what, addr, err)
		}
		if !allowAccount && isZeroContractStrkey(normalized) {
			return "", fmt.Errorf("%s address %q is the zero address", what, addr)
		}
		return normalized, nil
	case strings.HasPrefix(addr, "G"):
		if !allowAccount {
			return "", fmt.Errorf("%s address %q: an account strkey is not valid here", what, addr)
		}
		if _, err := strkey.Decode(strkey.VersionByteAccountID, addr); err != nil {
			return "", fmt.Errorf("%s address %q is not a valid account strkey: %w", what, addr, err)
		}
		return addr, nil
	case strings.HasPrefix(addr, "C"):
		if _, err := strkey.Decode(strkey.VersionByteContract, addr); err != nil {
			return "", fmt.Errorf("%s address %q is not a valid contract strkey: %w", what, addr, err)
		}
		if !allowAccount && isZeroContractStrkey(addr) {
			return "", fmt.Errorf("%s address %q is the zero address", what, addr)
		}
		return addr, nil
	}
	return "", fmt.Errorf("%s address %q is neither a strkey nor 0x-hex", what, addr)
}

// isZeroContractStrkey reports whether a contract strkey encodes all-zero
// bytes.
func isZeroContractStrkey(cid string) bool {
	raw, err := strkey.Decode(strkey.VersionByteContract, cid)
	if err != nil {
		return false
	}
	return bytes.Trim(raw, "\x00") == nil
}
