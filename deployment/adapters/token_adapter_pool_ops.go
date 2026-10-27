package adapters

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-ccip/deployment/deploy"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	cciputils "github.com/smartcontractkit/chainlink-ccip/deployment/utils"
	sequences "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf "github.com/smartcontractkit/chainlink-deployments-framework/deployment"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink-stellar/bindings"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	aphops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks"
	bmpops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/burn_mint_pool"
	lrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/lock_release_pool"
	slrrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/siloed_lock_release_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tlbops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_lock_box"
	tpoolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
	stellarsequences "github.com/smartcontractkit/chainlink-stellar/deployment/sequences"
)

// UpdateAuthorities transfers ownership of the pool — plus per-type lockboxes
// and the optional AdvancedPoolHooks — to the CLL timelock, mirroring EVM
// TokenAdapter.UpdateAuthorities: ref collection per pool type, then the
// transfer + accept ownership sequences.
func (a *StellarTokenAdapter) UpdateAuthorities() *cldf_ops.Sequence[tokens.UpdateAuthoritiesInput, sequences.OnChainOutput, *cldf.Environment] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:update-authorities",
		stellarops.ContractDeploymentVersion,
		"Transfers pool, lockbox, and AdvancedPoolHooks ownership to the CLL timelock on a Stellar chain",
		func(b cldf_ops.Bundle, e *cldf.Environment, input tokens.UpdateAuthoritiesInput) (sequences.OnChainOutput, error) {
			ch, ok := e.BlockChains.StellarChains()[input.ChainSelector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found in environment", input.ChainSelector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.ChainSelector, err)
			}

			govStrkey, ok, err := cllTimelockStrkey(e.DataStore, input.ChainSelector)
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf(
					"no RBACTimelock (CLL qualifier) deployed on chain %d; cannot transfer pool ownership", input.ChainSelector)
			}

			contractRefs, err := collectPoolOwnershipRefs(b, stellardeps.FromDeployer(dep), e.DataStore, input)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("collect ownership refs for chain %d: %w", input.ChainSelector, err)
			}

			ownershipInput := stellarsequences.StellarTransferOwnershipInput{
				TransferOwnershipPerChainInput: deploy.TransferOwnershipPerChainInput{
					ChainSelector: input.ChainSelector,
					CurrentOwner:  dep.SignerAddress(),
					ProposedOwner: govStrkey,
					ContractRef:   contractRefs,
				},
				GovernanceAddr: govStrkey,
			}

			var result sequences.OnChainOutput
			result, err = sequences.RunAndMergeSequence(b, e.BlockChains, stellarsequences.StellarTransferOwnershipViaMCMS, ownershipInput, result)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("transfer ownership on chain %d: %w", input.ChainSelector, err)
			}
			result, err = sequences.RunAndMergeSequence(b, e.BlockChains, stellarsequences.StellarAcceptOwnership, ownershipInput, result)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("accept ownership on chain %d: %w", input.ChainSelector, err)
			}
			return result, nil
		},
	)
}

// collectPoolOwnershipRefs builds the ownership-transfer ref list for one pool
// (EVM parity): the pool itself, per-type lockboxes (lock-release → the
// matching-qualifier TokenLockBox; siloed → every configured lockbox, deduped),
// and the optional AdvancedPoolHooks read live from the pool (nil means none
// configured — no zero address exists on Soroban).
func collectPoolOwnershipRefs(b cldf_ops.Bundle, deps stellardeps.StellarDeps, ds datastore.DataStore, input tokens.UpdateAuthoritiesInput) ([]datastore.AddressRef, error) {
	poolStrkey, err := ownership.NormalizeContractAddress(input.TokenPoolRef.Address)
	if err != nil {
		return nil, fmt.Errorf("normalize pool address: %w", err)
	}
	refs := []datastore.AddressRef{normalizePoolRefType(input.TokenPoolRef)}
	switch string(refs[0].Type) {
	case lrops.ContractType:
		lockboxRef, err := stellarccip.TokenLockBoxDatastoreRef(input.TokenPoolRef.Qualifier).LookupAddressRef(ds, input.ChainSelector)
		if err != nil {
			return nil, fmt.Errorf("find TokenLockBox for lock-release pool qualifier %q on chain %d: %w",
				input.TokenPoolRef.Qualifier, input.ChainSelector, err)
		}
		refs = append(refs, lockboxRef)
	case slrrops.ContractType:
		report, err := cldf_ops.ExecuteOperation(b, slrrops.GetAllLockBoxConfigs, deps, slrrops.GetAllLockBoxConfigsInput{
			ContractID: poolStrkey,
		})
		if err != nil {
			return nil, fmt.Errorf("get lockbox configs from siloed pool %s on chain %d: %w", poolStrkey, input.ChainSelector, err)
		}
		seen := make(map[string]struct{})
		for _, entry := range report.Output {
			if entry.LockBox == "" {
				continue
			}
			if _, dup := seen[entry.LockBox]; dup {
				continue
			}
			seen[entry.LockBox] = struct{}{}
			refs = append(refs, datastore.AddressRef{
				ChainSelector: input.ChainSelector,
				Type:          datastore.ContractType(tlbops.ContractType),
				Version:       stellarops.ContractDeploymentVersion,
				Address:       entry.LockBox,
			})
		}
		if len(refs) == 1 {
			return nil, fmt.Errorf("no lockboxes configured on siloed pool %s on chain %d", poolStrkey, input.ChainSelector)
		}
	}

	hooksReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetAdvancedPoolHooks, deps, tpoolops.GetAdvancedPoolHooksInput{
		ContractID: poolStrkey,
	})
	if err != nil {
		return nil, fmt.Errorf("get AdvancedPoolHooks for pool %s on chain %d: %w", poolStrkey, input.ChainSelector, err)
	}
	if hooksReport.Output != nil && *hooksReport.Output != "" {
		refs = append(refs, datastore.AddressRef{
			ChainSelector: input.ChainSelector,
			Type:          datastore.ContractType(aphops.ContractType),
			Version:       stellarops.ContractDeploymentVersion,
			Address:       *hooksReport.Output,
		})
	}
	return refs, nil
}

// SetTokenPoolRateLimits sets rate limits (and the optional allowed finality
// config) on a Stellar token pool for one remote chain, applying every
// finality bucket as a separate call (EVM v2 parity).
func (a *StellarTokenAdapter) SetTokenPoolRateLimits() *cldf_ops.Sequence[tokens.TPRLRemotes, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:set-token-pool-rate-limits",
		stellarops.ContractDeploymentVersion,
		"Sets rate limits and optional allowed finality on a Stellar token pool",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.TPRLRemotes) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.ChainSelector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.ChainSelector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.ChainSelector, err)
			}
			return setTokenPoolRateLimits(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), false, input)
		},
	)
}

// setTokenPoolRateLimits applies the rate-limit buckets: a pool the deployer
// owns (or is rate-limit admin of) executes directly (an ExecInfo-marked
// empty batch op); a timelock-owned pool gets one MCMS proposal transaction
// per call with caller = timelock (the contract require_auths the caller,
// which the timelock satisfies as the invoking ancestor). prerequisiteStaged
// forces the proposal path: the lane this update depends on is only proposed,
// so a direct call would run before the lane exists (ChainNotSupported) or be
// deleted by a later replacement removal.
func setTokenPoolRateLimits(b cldf_ops.Bundle, deps stellardeps.StellarDeps, deployerAddr string, prerequisiteStaged bool, input tokens.TPRLRemotes) (sequences.OnChainOutput, error) {
	poolRef := normalizePoolRefType(input.TokenPoolRef)
	poolStrkey, err := ownership.NormalizeContractAddress(poolRef.Address)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("normalize pool address: %w", err)
	}
	if !isRateLimitPoolType(poolRef.Type) {
		return sequences.OnChainOutput{}, fmt.Errorf(
			"unsupported pool type %q for rate limits on chain %d (supported: %s, %s, %s)",
			poolRef.Type, poolRef.ChainSelector, bmpops.ContractType, lrops.ContractType, slrrops.ContractType)
	}

	timelockStrkey, hasTimelock, err := cllTimelockStrkey(input.ExistingDataStore, input.ChainSelector)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}

	owner, err := ownership.ContractOwner(b.GetContext(), deps, poolRef)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read owner of pool %s: %w", poolStrkey, err)
	}
	rlAdminReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRateLimitAdmin, deps, tpoolops.GetRateLimitAdminInput{
		ContractID: poolStrkey,
	})
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read rate-limit admin of pool %s: %w", poolStrkey, err)
	}
	rlAdmin := ""
	if rlAdminReport.Output != nil {
		rlAdmin = *rlAdminReport.Output
	}

	caller := ""
	switch {
	case prerequisiteStaged:
		if owner != timelockStrkey {
			return sequences.OnChainOutput{}, fmt.Errorf(
				"a staged prerequisite implies a timelock-owned lane, but pool %s owner is %q", poolStrkey, owner)
		}
		caller = timelockStrkey
	case owner == deployerAddr || rlAdmin == deployerAddr:
		caller = deployerAddr
	case hasTimelock && (owner == timelockStrkey || rlAdmin == timelockStrkey):
		caller = timelockStrkey
	case input.SkipIfMissingPermissions:
		b.Logger.Warnf(
			"skipping rate limit update on pool %s (chain %d): owner %q and rate-limit admin %q are neither deployer %q nor timelock %q",
			poolStrkey, input.ChainSelector, owner, rlAdmin, deployerAddr, timelockStrkey)
		return sequences.OnChainOutput{}, nil
	default:
		return sequences.OnChainOutput{}, fmt.Errorf(
			"pool %s owner %q and rate-limit admin %q are neither deployer %q nor timelock %q; cannot set rate limits",
			poolStrkey, owner, rlAdmin, deployerAddr, timelockStrkey)
	}
	direct := caller == deployerAddr

	var writes []evmcontract.WriteOutput
	executed := false

	if !input.AllowedFinalityConfig.IsZero() {
		raw := input.AllowedFinalityConfig.Raw()
		want := binary.BigEndian.Uint32(raw[:])
		exec, proposal, err := applyPoolAllowedFinality(b, deps, owner, deployerAddr, timelockStrkey, hasTimelock, input.ChainSelector, input.TokenPoolRef.Type, poolStrkey, want)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		if exec {
			executed = true
		}
		if proposal != nil {
			writes = append(writes, *proposal)
		}
	}

	for _, bucket := range input.RateLimitBuckets {
		outbound, err := rateLimiterConfigToBinding(bucket.OutboundRateLimiterConfig)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("outbound rate limit (remote %d, fastFinality %t): %w",
				input.RemoteChainSelector, bucket.FastFinality, err)
		}
		inbound, err := rateLimiterConfigToBinding(bucket.InboundRateLimiterConfig)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("inbound rate limit (remote %d, fastFinality %t): %w",
				input.RemoteChainSelector, bucket.FastFinality, err)
		}
		if direct {
			if _, err := cldf_ops.ExecuteOperation(b, tpoolops.SetRateLimitConfig, deps, tpoolops.SetRateLimitConfigInput{
				ContractID:          poolStrkey,
				Caller:              deployerAddr,
				RemoteChainSelector: input.RemoteChainSelector,
				OutboundConfig:      outbound,
				InboundConfig:       inbound,
				FastFinality:        bucket.FastFinality,
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf(
					"set rate limit config on pool %s for remote %d (fastFinality %t): %w",
					poolStrkey, input.RemoteChainSelector, bucket.FastFinality, err)
			}
			executed = true
			continue
		}
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("set_rate_limit_config", []xdr.ScVal{
			scval.AddressToScVal(caller),
			scval.Uint64ToScVal(input.RemoteChainSelector),
			scval.MustToScVal(outbound.ToScVal()),
			scval.MustToScVal(inbound.ToScVal()),
			scval.BoolToScVal(bucket.FastFinality),
		})
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		writes = append(writes, poolRateLimitProposalWrite(input, poolStrkey, data))
	}

	// Return the direct-exec marker plus every queued proposal — the
	// finality arm routes by the owner while the buckets route by the
	// owner-or-admin caller, so both kinds can occur in one call.
	var ops []mcmstypes.BatchOperation
	if executed {
		// Direct-exec marker: an ExecInfo-marked empty batch op (the txs were
		// sent directly; the shared OutputBuilder drops the empty op).
		marker, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
			ChainSelector: input.ChainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{ContractType: string(input.TokenPoolRef.Type)},
				To:                poolStrkey,
			},
			ExecInfo: &evmcontract.ExecInfo{Hash: "stellar-set-token-pool-rate-limits-direct-exec"},
		}})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("build batch operation for chain %d: %w", input.ChainSelector, err)
		}
		ops = append(ops, marker)
	}
	if len(writes) > 0 {
		batchOp, err := evmcontract.NewBatchOperationFromWrites(writes)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("build batch operation for chain %d: %w", input.ChainSelector, err)
		}
		ops = append(ops, batchOp)
	}
	return sequences.OnChainOutput{BatchOps: ops}, nil
}

// poolRateLimitProposalWrite builds one MCMS proposal write for a pool
// entrypoint, marked as a Soroban invoke (same encoding the ownership
// sequences emit).
func poolRateLimitProposalWrite(input tokens.TPRLRemotes, poolStrkey string, data []byte) evmcontract.WriteOutput {
	return poolProposalWrite(input.ChainSelector, input.TokenPoolRef.Type, poolStrkey, data)
}

// poolProposalWrite builds one MCMS proposal write for a pool entrypoint,
// marked as a Soroban invoke (same encoding the ownership sequences emit).
func poolProposalWrite(chainSelector uint64, poolType datastore.ContractType, poolStrkey string, data []byte) evmcontract.WriteOutput {
	return evmcontract.WriteOutput{
		ChainSelector: chainSelector,
		Tx: mcmstypes.Transaction{
			OperationMetadata: mcmstypes.OperationMetadata{ContractType: string(poolType)},
			To:                poolStrkey,
			Data:              data,
			AdditionalFields:  stellarPoolMCMSTxAdditionalFields(),
		},
	}
}

// applyPoolAllowedFinality applies one pool's allowed-finality write with
// owner routing: an equal current value skips, the deployer executes
// directly, and the CLL timelock gets an MCMS proposal (set_allowed_finality_config
// is owner-only; the rate-limit buckets accept the admin too, so the finality
// arm routes by the owner alone).
func applyPoolAllowedFinality(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	owner, deployerAddr, timelockStrkey string,
	hasTimelock bool,
	chainSelector uint64,
	poolType datastore.ContractType,
	poolStrkey string,
	want uint32,
) (bool, *evmcontract.WriteOutput, error) {
	currentReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetAllowedFinalityConfig, deps, tpoolops.GetAllowedFinalityConfigInput{
		ContractID: poolStrkey,
	})
	if err != nil {
		return false, nil, fmt.Errorf("read allowed finality config of pool %s: %w", poolStrkey, err)
	}
	if currentReport.Output == want {
		return false, nil, nil
	}
	switch {
	case owner == deployerAddr:
		if _, err := cldf_ops.ExecuteOperation(b, tpoolops.SetAllowedFinalityConfig, deps, tpoolops.SetAllowedFinalityConfigInput{
			ContractID:      poolStrkey,
			AllowedFinality: want,
		}); err != nil {
			return false, nil, fmt.Errorf("set allowed finality config on pool %s: %w", poolStrkey, err)
		}
		return true, nil, nil
	case hasTimelock && owner == timelockStrkey:
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("set_allowed_finality_config", []xdr.ScVal{
			scval.Uint32ToScVal(want),
		})
		if err != nil {
			return false, nil, err
		}
		write := poolProposalWrite(chainSelector, poolType, poolStrkey, data)
		return false, &write, nil
	default:
		return false, nil, fmt.Errorf(
			"cannot set allowed finality on pool %s: owner %q is neither deployer %q nor timelock %q",
			poolStrkey, owner, deployerAddr, timelockStrkey)
	}
}

// stellarPoolMCMSTxAdditionalFields marks proposal transactions as Soroban
// invokes (same encoding the ownership sequences emit).
func stellarPoolMCMSTxAdditionalFields() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"family":"stellar","encodingVersion":%d}`, bindings.SorobanInvokeEncodingVersion))
}

// isRateLimitPoolType reports whether the pool type is served by the shared
// TokenPool client (burn-mint, lock-release, siloed).
func isRateLimitPoolType(t datastore.ContractType) bool {
	switch string(t) {
	case bmpops.ContractType, lrops.ContractType, slrrops.ContractType:
		return true
	}
	return false
}

// normalizePoolRefType maps a pool ref's type to the ops-package type that
// type-keyed dispatch (ownership reads, pool-type checks) keys on: the shared
// pipeline's pool refs carry the devenv catalog names "BurnMintTokenPool" /
// "LockReleaseTokenPool" while the ops packages name the same pools
// "BurnMintPool" / "LockReleasePool".
func normalizePoolRefType(ref datastore.AddressRef) datastore.AddressRef {
	switch string(ref.Type) {
	case stellarccip.DevenvCatalogBurnMintTokenPoolType:
		ref.Type = datastore.ContractType(bmpops.ContractType)
	case stellarccip.DevenvCatalogLockReleaseTokenPoolType:
		ref.Type = datastore.ContractType(lrops.ContractType)
	}
	return ref
}

// rateLimiterConfigToBinding maps the chain-agnostic rate limiter config to
// the on-chain U128 form. Negative and wider-than-128-bit amounts are
// rejected before any write: a truncated limit would apply on chain (directly
// or via proposal) as a different limit than the one requested.
func rateLimiterConfigToBinding(cfg tokens.RateLimiterConfig) (tpoolbindings.RateLimitConfig, error) {
	capacity, err := bigIntToU128(cfg.Capacity)
	if err != nil {
		return tpoolbindings.RateLimitConfig{}, fmt.Errorf("capacity: %w", err)
	}
	rate, err := bigIntToU128(cfg.Rate)
	if err != nil {
		return tpoolbindings.RateLimitConfig{}, fmt.Errorf("rate: %w", err)
	}
	return tpoolbindings.RateLimitConfig{
		IsEnabled: cfg.IsEnabled,
		Capacity:  capacity,
		Rate:      rate,
	}, nil
}

// bigIntToU128 converts a big.Int to the U128 hi/lo form, rejecting negative
// values and values above 2^128-1.
func bigIntToU128(v *big.Int) (scval.U128, error) {
	if v == nil {
		return scval.U128{}, nil
	}
	if v.Sign() < 0 {
		return scval.U128{}, fmt.Errorf("negative amount %s", v.String())
	}
	if v.BitLen() > 128 {
		return scval.U128{}, fmt.Errorf("amount %s exceeds the 128-bit limit", v.String())
	}
	lo := new(big.Int).And(v, new(big.Int).SetUint64(math.MaxUint64))
	hi := new(big.Int).Rsh(v, 64)
	return scval.U128(xdr.UInt128Parts{Hi: xdr.Uint64(hi.Uint64()), Lo: xdr.Uint64(lo.Uint64())}), nil
}

// cllTimelockStrkey resolves the CLL-qualifier RBACTimelock strkey from the
// datastore; ok is false when the chain has none (EOA lanes deploy no
// timelock).
func cllTimelockStrkey(ds datastore.DataStore, chainSelector uint64) (string, bool, error) {
	refs, err := ds.Addresses().Fetch()
	if err != nil {
		return "", false, fmt.Errorf("fetch datastore addresses: %w", err)
	}
	tl, ok := mcmsutil.FindExistingStellarTimelock(refs, chainSelector, cciputils.CLLQualifier)
	return tl, ok, nil
}

var _ tokens.RateLimitReaderAdapter = (*StellarTokenAdapter)(nil)

// GetOnchainRateLimits implements tokens.RateLimitReaderAdapter: reads the
// pool's current outbound and inbound rate-limiter buckets for one remote lane
// and finality bucket. Used by the outbound-only arm of
// SetTokenPoolRateLimits to pass the current inbound through unchanged (the
// stellar setter is atomic over both directions), and to validate a
// counterpart's inbound against a new outbound. TokenRef is ignored: a
// stellar pool address alone identifies the lane config. An unconfigured lane
// reads as the contract's default zero-value buckets with no error.
func (a *StellarTokenAdapter) GetOnchainRateLimits(
	b cldf_ops.Bundle,
	chains cldf_chain.BlockChains,
	_ datastore.DataStore,
	chainSelector uint64,
	poolRef datastore.AddressRef,
	_ datastore.AddressRef,
	remoteSelector uint64,
	fastFinality bool,
) (tokens.OnchainRateLimits, error) {
	return getOnchainRateLimits(b, chains, chainSelector, poolRef, remoteSelector, fastFinality)
}

// getOnchainRateLimits resolves the pool and reads its current buckets.
func getOnchainRateLimits(
	b cldf_ops.Bundle,
	chains cldf_chain.BlockChains,
	chainSelector uint64,
	poolRef datastore.AddressRef,
	remoteSelector uint64,
	fastFinality bool,
) (tokens.OnchainRateLimits, error) {
	deps, err := stellarReadDeps(chains, chainSelector)
	if err != nil {
		return tokens.OnchainRateLimits{}, err
	}
	poolStrkey, err := ownership.NormalizeContractAddress(poolRef.Address)
	if err != nil {
		return tokens.OnchainRateLimits{}, fmt.Errorf("normalize pool address: %w", err)
	}
	return readOnchainRateLimits(b, deps, poolStrkey, remoteSelector, fastFinality)
}

// readOnchainRateLimits reads the pool's current rate-limiter state and maps
// both buckets to the chain-agnostic configs.
func readOnchainRateLimits(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	poolStrkey string,
	remoteSelector uint64,
	fastFinality bool,
) (tokens.OnchainRateLimits, error) {
	report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetCurrentRateLimiterState, deps, tpoolops.GetCurrentRateLimiterStateInput{
		ContractID:          poolStrkey,
		RemoteChainSelector: remoteSelector,
		FastFinality:        fastFinality,
	})
	if err != nil {
		return tokens.OnchainRateLimits{}, fmt.Errorf(
			"get current rate limiter state for pool %s remote chain %d (fastFinality %t): %w",
			poolStrkey, remoteSelector, fastFinality, err)
	}
	if report.Output == nil {
		return tokens.OnchainRateLimits{}, fmt.Errorf("no rate limiter state returned for pool %s remote chain %d", poolStrkey, remoteSelector)
	}
	return tokens.OnchainRateLimits{
		Outbound: tokenBucketToRateLimiterConfig(report.Output.Outbound),
		Inbound:  tokenBucketToRateLimiterConfig(report.Output.Inbound),
	}, nil
}

// tokenBucketToRateLimiterConfig maps one on-chain bucket to the
// chain-agnostic config (the inverse of rateLimiterConfigToBinding).
func tokenBucketToRateLimiterConfig(bucket tpoolbindings.TokenBucket) tokens.RateLimiterConfig {
	return tokens.RateLimiterConfig{
		IsEnabled: bucket.IsEnabled,
		Capacity:  u128ToBigInt(bucket.Capacity),
		Rate:      u128ToBigInt(bucket.Rate),
	}
}

var _ tokens.TokenPoolOwnershipChecker = (*StellarTokenAdapter)(nil)

// IsPoolExternallyOwned implements tokens.TokenPoolOwnershipChecker: the pool
// is externally owned when its on-chain owner is neither the chain's CLL
// timelock (EOA lanes deploy none) nor the chain deployer. tokenRef is ignored
// — a stellar pool contract alone identifies the pool (EVM parity). A failed
// owner read is an error and must be treated as fatal by the caller, never a
// skip.
func (a *StellarTokenAdapter) IsPoolExternallyOwned(e cldf.Environment, chainSelector uint64, poolRef datastore.AddressRef, _ datastore.AddressRef) (bool, error) {
	ch, ok := e.BlockChains.StellarChains()[chainSelector]
	if !ok {
		return false, fmt.Errorf("stellar chain %d not found", chainSelector)
	}
	dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(e.Logger))
	if err != nil {
		return false, fmt.Errorf("stellar deployer from chain %d: %w", chainSelector, err)
	}
	return isPoolExternallyOwned(context.Background(), stellardeps.FromDeployer(dep), dep.SignerAddress(), e.DataStore, chainSelector, poolRef)
}

// isPoolExternallyOwned is the ownership-check core (unit-tested with a
// recording invoker).
func isPoolExternallyOwned(ctx context.Context, deps stellardeps.StellarDeps, deployerAddr string, ds datastore.DataStore, chainSelector uint64, poolRef datastore.AddressRef) (bool, error) {
	poolStrkey, err := resolvePoolStrkey(ds, chainSelector, poolRef)
	if err != nil {
		return false, err
	}
	owner, err := ownership.ContractOwner(ctx, deps, normalizePoolRefType(datastore.AddressRef{
		ChainSelector: chainSelector,
		Type:          poolRef.Type,
		Address:       poolStrkey,
	}))
	if err != nil {
		return false, fmt.Errorf("read owner of pool %s on chain %d: %w", poolStrkey, chainSelector, err)
	}
	if owner == deployerAddr {
		return false, nil
	}
	timelock, hasTimelock, err := cllTimelockStrkey(ds, chainSelector)
	if err != nil {
		return false, err
	}
	if hasTimelock && owner == timelock {
		return false, nil
	}
	return true, nil
}

var _ tokens.RemotePoolRemover = (*StellarTokenAdapter)(nil)

// RemoveRemotePools implements tokens.RemotePoolRemover: each requested remote
// pool is matched against the pool's current on-chain list and removed by its
// exact stored bytes; a remote pool that is not currently configured is skipped
// with a warning so re-runs are idempotent. The input carries no datastore, so
// routing follows setTokenPoolDynamicConfig: a deployer-owned pool executes the
// removals directly, any other owner gets one MCMS proposal transaction per
// removal, which the owning timelock must execute.
func (a *StellarTokenAdapter) RemoveRemotePools() *cldf_ops.Sequence[tokens.RemoveRemotePoolsSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:remove-remote-pools",
		stellarops.ContractDeploymentVersion,
		"Removes remote pool entries from a token pool on a Stellar chain",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.RemoveRemotePoolsSequenceInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.Selector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.Selector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.Selector, err)
			}
			return removeRemotePools(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), input)
		},
	)
}

// removeRemotePools is the removal core (unit-tested with a recording
// invoker). Removals are per-entry exact-equality writes, never whole-list
// rewrites, so several can share one proposal without ordering hazards.
func removeRemotePools(
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	deployerAddr string,
	input tokens.RemoveRemotePoolsSequenceInput,
) (sequences.OnChainOutput, error) {
	poolRef := normalizePoolRefType(input.TokenPoolRef)
	poolStrkey, err := ownership.NormalizeContractAddress(poolRef.Address)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("normalize pool address: %w", err)
	}
	if !isRateLimitPoolType(poolRef.Type) {
		return sequences.OnChainOutput{}, fmt.Errorf(
			"unsupported pool type %q for remote pool removal on chain %d (supported: %s, %s, %s)",
			poolRef.Type, input.Selector, bmpops.ContractType, lrops.ContractType, slrrops.ContractType)
	}
	owner, err := ownership.ContractOwner(b.GetContext(), deps, poolRef)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read owner of pool %s: %w", poolStrkey, err)
	}

	var proposals []evmcontract.WriteOutput
	executed := false
	removed := make(map[string]struct{})
	for _, toRemove := range input.RemotePoolsToRemove {
		want, err := remotePoolAddressBytes(toRemove.Remote.Address)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("remote pool address for remote chain %d: %w", toRemove.Selector, err)
		}
		report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRemotePools, deps, tpoolops.GetRemotePoolsInput{
			ContractID: poolStrkey, RemoteChainSelector: toRemove.Selector,
		}, cldf_ops.WithForceExecute[tpoolops.GetRemotePoolsInput, stellardeps.StellarDeps]())
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read remote pools of pool %s for remote chain %d: %w", poolStrkey, toRemove.Selector, err)
		}
		matches := matchingRemotePoolEntries(report.Output, want)
		if len(matches) == 0 {
			b.Logger.Warnf(
				"skipping removal of remote pool %s for remote chain %d on pool %s (chain %d): pairing already absent",
				toRemove.Remote.Address, toRemove.Selector, poolStrkey, input.Selector)
			continue
		}
		for _, stored := range matches {
			key := fmt.Sprintf("%d:%x", toRemove.Selector, stored)
			if _, dup := removed[key]; dup {
				continue
			}
			removed[key] = struct{}{}
			if owner == deployerAddr {
				if _, err := cldf_ops.ExecuteOperation(b, tpoolops.RemoveRemotePool, deps, tpoolops.RemoveRemotePoolInput{
					ContractID: poolStrkey, RemoteChainSelector: toRemove.Selector, RemotePoolAddress: stored,
				}); err != nil {
					return sequences.OnChainOutput{}, fmt.Errorf("remove remote pool %x for remote chain %d on pool %s: %w", stored, toRemove.Selector, poolStrkey, err)
				}
				executed = true
				continue
			}
			data, err := mcmsutil.EncodeSorobanMCMSInvokePayload("remove_remote_pool", []xdr.ScVal{
				scval.Uint64ToScVal(toRemove.Selector), scval.BytesToScVal(stored),
			})
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			proposals = append(proposals, poolProposalWrite(input.Selector, poolRef.Type, poolStrkey, data))
		}
	}
	return dynamicConfigOutput(input.Selector, poolRef.Type, poolStrkey, "stellar-remove-remote-pools-direct-exec", executed, proposals)
}

// matchingRemotePoolEntries returns the stored remote-pool entries equal to the
// requested address in either wire form — the raw bytes, or the 32-byte
// left-padded form counterpart pools are conventionally wired with. Matching
// the raw form first keeps a configured unpadded entry removable; each match
// is removed by its exact stored bytes.
func matchingRemotePoolEntries(stored [][]byte, want []byte) [][]byte {
	var matches [][]byte
	for _, entry := range stored {
		if bytes.Equal(entry, want) || bytes.Equal(entry, common.LeftPadBytes(want, 32)) {
			matches = append(matches, entry)
		}
	}
	return matches
}

// remotePoolAddressBytes decodes a remote pool address in the remote family's
// recorded form: 0x-hex at the family's native length (EVM 20, stellar 32) or
// a stellar strkey.
func remotePoolAddressBytes(addr string) ([]byte, error) {
	if strings.HasPrefix(addr, "0x") {
		raw, err := hexutil.Decode(addr)
		if err != nil {
			return nil, fmt.Errorf("remote pool address %q is not valid hex: %w", addr, err)
		}
		if len(raw) != 20 && len(raw) != 32 {
			return nil, fmt.Errorf("remote pool address %q decodes to %d bytes, want the family-native 20 or 32", addr, len(raw))
		}
		return raw, nil
	}
	raw, err := decodeTokenStrkey(addr)
	if err != nil {
		return nil, fmt.Errorf("remote pool address %q: %w", addr, err)
	}
	return raw, nil
}
