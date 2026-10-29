package adapters

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	sequences "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	ccvadapters "github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/adapters"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	aphbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/advanced_pool_hooks"
	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	scval "github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	aphops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/advanced_pool_hooks"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	tpoolops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_pool"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ownership"
)

// sorobanWriteRouter routes owner-gated Soroban writes on one contract
// between direct exec and MCMS proposals.
type sorobanWriteRouter struct {
	chainSelector uint64
	contractID    string
	contractType  string
	owner         string // the contract's current owner
	deployerAddr  string
	timelock      string // empty unless the owner is the CLL timelock
	writes        []evmcontract.WriteOutput
	executed      bool
}

// route applies one owner-gated entrypoint: the deployer executes directly
// when it holds the required identity (authorizer), the CLL timelock gets an
// MCMS proposal carrying fn + args (the timelock is the invoking ancestor, so
// the contract's require_auth on it passes). Returns whether the write
// executed directly (false when it was queued as a proposal).
func (r *sorobanWriteRouter) route(authorizer string, directExec func() error, fn string, args []xdr.ScVal) (bool, error) {
	switch {
	case authorizer == r.deployerAddr:
		if err := directExec(); err != nil {
			return false, err
		}
		r.executed = true
		return true, nil
	case r.timelock != "" && authorizer == r.timelock:
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload(fn, args)
		if err != nil {
			return false, err
		}
		r.writes = append(r.writes, evmcontract.WriteOutput{
			ChainSelector: r.chainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{ContractType: r.contractType},
				To:                r.contractID,
				Data:              data,
				AdditionalFields:  stellarPoolMCMSTxAdditionalFields(),
			},
		})
		return false, nil
	default:
		return false, fmt.Errorf(
			"contract %s (%s) on chain %d requires authorizer %q, which is neither deployer %q nor timelock %q",
			r.contractID, r.contractType, r.chainSelector, authorizer, r.deployerAddr, r.timelock)
	}
}

// output returns the direct-exec marker when anything executed directly plus
// one batch op carrying every queued proposal write — a router can mix both
// (a deployer-owned contract proposing the timelock as a per-token identity
// executes one op directly and queues the next). None when nothing changed
// (idempotent skip).
func (r *sorobanWriteRouter) output() (sequences.OnChainOutput, error) {
	var ops []mcmstypes.BatchOperation
	if r.executed {
		marker, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
			ChainSelector: r.chainSelector,
			Tx: mcmstypes.Transaction{
				OperationMetadata: mcmstypes.OperationMetadata{ContractType: r.contractType},
				To:                r.contractID,
			},
			ExecInfo: &evmcontract.ExecInfo{Hash: "stellar-direct-exec-" + r.contractType},
		}})
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		ops = append(ops, marker)
	}
	if len(r.writes) > 0 {
		batchOp, err := evmcontract.NewBatchOperationFromWrites(r.writes)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		ops = append(ops, batchOp)
	}
	return sequences.OnChainOutput{BatchOps: ops}, nil
}

// newSorobanWriteRouter builds a router for one contract, resolving its
// current owner and the chain's CLL timelock.
func newSorobanWriteRouter(
	b cldf_ops.Bundle, deps stellardeps.StellarDeps, deployerAddr string,
	ds datastore.DataStore, chainSelector uint64,
	contractID, contractType string,
) (*sorobanWriteRouter, error) {
	owner, err := ownership.ContractOwner(b.GetContext(), deps, datastore.AddressRef{
		ChainSelector: chainSelector,
		Type:          datastore.ContractType(contractType),
		Address:       contractID,
	})
	if err != nil {
		return nil, fmt.Errorf("read owner of %s %s: %w", contractType, contractID, err)
	}
	timelock, hasTimelock, err := cllTimelockStrkey(ds, chainSelector)
	if err != nil {
		return nil, err
	}
	r := &sorobanWriteRouter{
		chainSelector: chainSelector,
		contractID:    contractID,
		contractType:  contractType,
		owner:         owner,
		deployerAddr:  deployerAddr,
	}
	// The timelock routes any authorizer that IS the chain's CLL timelock —
	// not just the contract owner: accept/set_pool authorizers are per-token
	// identities read from the contract, and the timelock satisfies their
	// require_auth as the invoking ancestor.
	if hasTimelock {
		r.timelock = timelock
	}
	return r, nil
}

// ManualRegistration registers a token and its pool in the TokenAdminRegistry
// (EVM ManualRegistration parity). Divergence (documented in the parity doc):
// EVM hands the token admin role to ProposedOwner; Stellar's SAC admin
// controls minting and must stay with the pool on BnM tokens, so the SAC
// mint-authority handoff is deliberately not performed.
func (a *StellarTokenAdapter) ManualRegistration() *cldf_ops.Sequence[tokens.ManualRegistrationSequenceInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:manual-registration",
		stellarops.ContractDeploymentVersion,
		"Registers a token and its pool in the TokenAdminRegistry on a Stellar chain",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.ManualRegistrationSequenceInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.ChainSelector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.ChainSelector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.ChainSelector, err)
			}
			deps := stellardeps.FromDeployer(dep)
			ds := input.ExistingDataStore

			poolStrkey, err := resolvePoolStrkey(ds, input.ChainSelector, input.TokenPoolRef)
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			tokenStrkey, err := resolveTokenStrkey(b, deps, poolStrkey, input.TokenRef)
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			if _, err := decodeTokenStrkey(input.ProposedOwner); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("invalid proposed owner %q on chain %d: %w", input.ProposedOwner, input.ChainSelector, err)
			}
			tarStrkey, err := stellarccip.TokenAdminRegistryDatastoreRef().LookupStrkey(ds, input.ChainSelector)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("resolve TokenAdminRegistry on chain %d: %w", input.ChainSelector, err)
			}
			return registerTokenInTAR(b, deps, dep.SignerAddress(), ds, input.ChainSelector,
				tarStrkey, tokenStrkey, poolStrkey, input.ProposedOwner, true)
		},
	)
}

// ConfigureTokenForTransfersSequence configures a Stellar token pool for
// transfers with its remote chains (EVM ConfigureTokenForTransfers parity):
// per-remote chain updates with the left-padded-32 wire encoding (idempotent
// via read-before-write, safe alongside devenv PostConnect), rate limits for
// the buckets the input provides, and the TokenAdminRegistry registration
// unless suppressed. Two arms are unsupported on this adapter and error
// loudly when used (documented in the parity doc): per-remote CCV lists and
// pool-level token transfer fee configs.
func (a *StellarTokenAdapter) ConfigureTokenForTransfersSequence() *cldf_ops.Sequence[tokens.ConfigureTokenForTransfersInput, sequences.OnChainOutput, cldf_chain.BlockChains] {
	return cldf_ops.NewSequence(
		"stellar-token-adapter:configure-token-for-transfers",
		stellarops.ContractDeploymentVersion,
		"Configures a token pool on a Stellar chain for transfers with remote chains",
		func(b cldf_ops.Bundle, chains cldf_chain.BlockChains, input tokens.ConfigureTokenForTransfersInput) (sequences.OnChainOutput, error) {
			ch, ok := chains.StellarChains()[input.ChainSelector]
			if !ok {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar chain %d not found", input.ChainSelector)
			}
			dep, err := stellardeployment.NewDeployerFromChain(ch, stellardeployment.WithLogger(b.Logger))
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("stellar deployer from chain %d: %w", input.ChainSelector, err)
			}
			return configureTokenForTransfers(b, stellardeps.FromDeployer(dep), dep.SignerAddress(), input)
		},
	)
}

// configureTokenForTransfers is the pool-configuration core (unit-tested with
// a recording invoker): per-remote chain updates, rate limits, allowed
// finality, and the TokenAdminRegistry registration.
func configureTokenForTransfers(b cldf_ops.Bundle, deps stellardeps.StellarDeps, deployerAddr string, input tokens.ConfigureTokenForTransfersInput) (sequences.OnChainOutput, error) {
	ds := input.ExistingDataStore

	poolRef := normalizePoolRefType(datastore.AddressRef{
		ChainSelector: input.ChainSelector,
		Type:          datastore.ContractType(input.PoolType),
	})
	if !isRateLimitPoolType(poolRef.Type) {
		return sequences.OnChainOutput{}, fmt.Errorf(
			"unsupported pool type %q on chain %d (supported: burn-mint, lock-release, siloed)", input.PoolType, input.ChainSelector)
	}
	poolStrkey, err := ownership.NormalizeContractAddress(input.TokenPoolAddress)
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("normalize pool address on chain %d: %w", input.ChainSelector, err)
	}
	poolRef.Address = poolStrkey
	tokenStrkey, err := resolveTokenStrkey(b, deps, poolStrkey, input.TokenRef)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	registryPoolStrkey := poolStrkey
	if input.RegistryTokenPoolAddress != "" {
		registryPoolStrkey, err = ownership.NormalizeContractAddress(input.RegistryTokenPoolAddress)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("normalize registry pool address on chain %d: %w", input.ChainSelector, err)
		}
	}
	// Input validation first: the unsupported arm fails fast before any
	// on-chain read.
	for remoteSel, rc := range input.RemoteChains {
		if rc.TokenTransferFeeConfig != nil {
			return sequences.OnChainOutput{}, fmt.Errorf(
				"pool-level token transfer fee config is not supported by the stellar adapter (remote %d); use the fee changesets instead", remoteSel)
		}
	}

	decimalsReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetTokenDecimals, deps, tpoolops.GetTokenDecimalsInput{ContractID: poolStrkey})
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read local decimals of pool %s: %w", poolStrkey, err)
	}
	poolRouter, err := newSorobanWriteRouter(b, deps, deployerAddr, ds, input.ChainSelector, poolStrkey, string(poolRef.Type))
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	// CCV lists apply to the pool's AdvancedPoolHooks, not the pool — like
	// EVM, the arm is skipped entirely when no hooks are wired.
	hooksReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetAdvancedPoolHooks, deps, tpoolops.GetAdvancedPoolHooksInput{ContractID: poolStrkey})
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read AdvancedPoolHooks of pool %s: %w", poolStrkey, err)
	}
	var aphRouter *sorobanWriteRouter
	if hooksReport.Output != nil && *hooksReport.Output != "" {
		aphRouter, err = newSorobanWriteRouter(b, deps, deployerAddr, ds, input.ChainSelector, *hooksReport.Output, aphops.ContractType)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
	}

	var batchOps []mcmstypes.BatchOperation
	// Dependent rate-limit writes, emitted after the lane writes they depend
	// on: a queued lane write must execute first, or its dependent
	// set_rate_limit_config fails ChainNotSupported or is deleted by the
	// replacement's removal.
	var rateOps []mcmstypes.BatchOperation

	// Per-remote configuration: chain updates and rate limits.
	for remoteSel, rc := range input.RemoteChains {
		defBucket, ffBucket, err := resolveRemoteRateLimitBuckets(input, rc, uint8(decimalsReport.Output))
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("resolve rate limit buckets for remote %d: %w", remoteSel, err)
		}
		if aphRouter != nil {
			if err := applyPoolCCVConfig(b, deps, aphRouter, remoteSel, rc); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("apply CCV config for remote %d: %w", remoteSel, err)
			}
		}

		supportedReport, err := cldf_ops.ExecuteOperation(b, tpoolops.IsSupportedChain, deps, tpoolops.IsSupportedChainInput{
			ContractID: poolStrkey, RemoteChainSelector: remoteSel,
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read remote support of pool %s for %d: %w", poolStrkey, remoteSel, err)
		}

		if !supportedReport.Output {
			// New lane: one ChainUpdate — the pool address in the
			// left-padded-32 wire form and the token in the remote family's
			// native form (the same forms the raw PostConnect writer uses,
			// so the two writers cannot double-apply). A fresh lane starts
			// unlimited unless the input supplies the default bucket.
			add, err := poolChainUpdate(remoteSel, rc, bucketOrZero(defBucket))
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			laneExecuted, err := poolRouter.route(poolRouter.owner, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.ApplyChainUpdates, deps, tpoolops.ApplyChainUpdatesInput{
					ContractID: poolStrkey, Adds: []tpoolbindings.ChainUpdate{add},
				})
				return err
			}, "apply_chain_updates", []xdr.ScVal{
				scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{add}),
				scval.Uint64SliceToScVal(nil),
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("apply chain updates for remote %d on pool %s: %w", remoteSel, poolStrkey, err)
			}
			// Fast-finality bucket (if provided) as a separate call, ordered
			// after the lane write — staged (only proposed) when the lane
			// write itself is.
			if ffBucket != nil {
				rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, !laneExecuted, tokens.TPRLRemotes{
					ChainSelector:       input.ChainSelector,
					RemoteChainSelector: remoteSel,
					TokenPoolRef:        poolRef,
					ExistingDataStore:   ds,
					RateLimitBuckets:    []tokens.TPRLRateLimitBucket{*ffBucket},
				})
				if err != nil {
					return sequences.OnChainOutput{}, fmt.Errorf("set fast-finality rate limits for remote %d: %w", remoteSel, err)
				}
				rateOps = append(rateOps, rlOut.BatchOps...)
			}
			continue
		}

		// Already supported: idempotent updates.
		remoteTokenReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRemoteToken, deps, tpoolops.GetRemoteTokenInput{
			ContractID: poolStrkey, RemoteChainSelector: remoteSel,
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read remote token of pool %s for %d: %w", poolStrkey, remoteSel, err)
		}
		wantRemoteToken, err := remoteTokenWireBytes(remoteSel, rc.RemoteToken)
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("remote chain %d: %w", remoteSel, err)
		}
		if !bytes.Equal(remoteTokenReport.Output, wantRemoteToken) {
			// Remote token changed: replace the lane atomically — one
			// apply_chain_updates carrying the removal and the re-add, so a
			// failed re-add cannot leave the lane removed. The replacement
			// destroys the lane's rate-limit buckets, so each omitted bucket
			// is preserved from the live lane (EVM preserves the default
			// bucket; preserving fast-finality too is a deliberate divergence
			// — EVM drops it here).
			liveDefault, liveFF, err := readLiveBuckets(b, deps, poolStrkey, remoteSel)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("read live rate limits for remote %d: %w", remoteSel, err)
			}
			if defBucket == nil {
				defBucket = liveDefault
			}
			if ffBucket == nil {
				ffBucket = liveFF
			}
			add, err := poolChainUpdate(remoteSel, rc, bucketOrZero(defBucket))
			if err != nil {
				return sequences.OnChainOutput{}, err
			}
			laneExecuted, err := poolRouter.route(poolRouter.owner, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.ApplyChainUpdates, deps, tpoolops.ApplyChainUpdatesInput{
					ContractID: poolStrkey, Adds: []tpoolbindings.ChainUpdate{add}, Removes: []uint64{remoteSel},
				})
				return err
			}, "apply_chain_updates", []xdr.ScVal{
				scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{add}),
				scval.Uint64SliceToScVal([]uint64{remoteSel}),
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("replace changed remote token lane %d on pool %s: %w", remoteSel, poolStrkey, err)
			}
			// A preserved or supplied fast-finality bucket needs its own
			// call (the ChainUpdate only carries the default bucket), ordered
			// after the replacement — staged when the replacement itself is
			// (a direct call would be deleted by the later removal).
			if ffBucket != nil {
				rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, !laneExecuted, tokens.TPRLRemotes{
					ChainSelector:       input.ChainSelector,
					RemoteChainSelector: remoteSel,
					TokenPoolRef:        poolRef,
					ExistingDataStore:   ds,
					RateLimitBuckets:    []tokens.TPRLRateLimitBucket{*ffBucket},
				})
				if err != nil {
					return sequences.OnChainOutput{}, fmt.Errorf("restore fast-finality rate limits for remote %d: %w", remoteSel, err)
				}
				rateOps = append(rateOps, rlOut.BatchOps...)
			}
			continue
		} else {
			// Same token: add the remote pool if missing.
			remotePoolsReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRemotePools, deps, tpoolops.GetRemotePoolsInput{
				ContractID: poolStrkey, RemoteChainSelector: remoteSel,
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("read remote pools of pool %s for %d: %w", poolStrkey, remoteSel, err)
			}
			if len(rc.RemotePool) > 0 {
				padded, err := remotePoolWireBytes(remoteSel, rc.RemotePool)
				if err != nil {
					return sequences.OnChainOutput{}, fmt.Errorf("remote pool (remote %d): %w", remoteSel, err)
				}
				if len(matchingRemotePoolEntries(remotePoolsReport.Output, rc.RemotePool)) == 0 {
					if _, err := poolRouter.route(poolRouter.owner, func() error {
						_, err := cldf_ops.ExecuteOperation(b, tpoolops.AddRemotePool, deps, tpoolops.AddRemotePoolInput{
							ContractID: poolStrkey, RemoteChainSelector: remoteSel, RemotePoolAddress: padded,
						})
						return err
					}, "add_remote_pool", []xdr.ScVal{
						scval.Uint64ToScVal(remoteSel), scval.BytesToScVal(padded),
					}); err != nil {
						return sequences.OnChainOutput{}, fmt.Errorf("add remote pool for %d on pool %s: %w", remoteSel, poolStrkey, err)
					}
				}
			}
		}

		// Rate limit updates for the buckets the input supplies only: an
		// omitted bucket keeps its on-chain config (EVM reuses the on-chain
		// default the same way — no write), never a synthesized disabled one.
		var rlBuckets []tokens.TPRLRateLimitBucket
		if defBucket != nil {
			rlBuckets = append(rlBuckets, *defBucket)
		}
		if ffBucket != nil {
			rlBuckets = append(rlBuckets, *ffBucket)
		}
		if len(rlBuckets) > 0 {
			rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, false, tokens.TPRLRemotes{
				ChainSelector:       input.ChainSelector,
				RemoteChainSelector: remoteSel,
				TokenPoolRef:        poolRef,
				ExistingDataStore:   ds,
				RateLimitBuckets:    rlBuckets,
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("set rate limits for remote %d: %w", remoteSel, err)
			}
			rateOps = append(rateOps, rlOut.BatchOps...)
		}
	}

	// Allowed finality (pool-global, read-before-write).
	if !input.AllowedFinalityConfig.IsZero() {
		var anyRemote uint64
		for remoteSel := range input.RemoteChains {
			anyRemote = remoteSel
			break
		}
		rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, false, tokens.TPRLRemotes{
			ChainSelector:         input.ChainSelector,
			RemoteChainSelector:   anyRemote,
			TokenPoolRef:          poolRef,
			ExistingDataStore:     ds,
			AllowedFinalityConfig: input.AllowedFinalityConfig,
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("set allowed finality on pool %s: %w", poolStrkey, err)
		}
		rateOps = append(rateOps, rlOut.BatchOps...)
	}

	poolOut, err := poolRouter.output()
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	batchOps = append(batchOps, poolOut.BatchOps...)
	if aphRouter != nil {
		aphOut, err := aphRouter.output()
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		batchOps = append(batchOps, aphOut.BatchOps...)
	}
	batchOps = append(batchOps, rateOps...)

	// TokenAdminRegistry registration (EVM RegisterToken parity).
	if !input.SkipTokenAdminRegistrySetup {
		// The changeset passes RegistryAddress in the datastore hex form;
		// the ops need the strkey.
		tarStrkey := input.RegistryAddress
		if tarStrkey == "" {
			tarStrkey, err = stellarccip.TokenAdminRegistryDatastoreRef().LookupStrkey(ds, input.ChainSelector)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("resolve TokenAdminRegistry on chain %d: %w", input.ChainSelector, err)
			}
		} else {
			tarStrkey, err = ownership.NormalizeContractAddress(tarStrkey)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("normalize registry address on chain %d: %w", input.ChainSelector, err)
			}
		}
		regOut, err := registerTokenInTAR(b, deps, deployerAddr, ds, input.ChainSelector,
			tarStrkey, tokenStrkey, registryPoolStrkey, input.ExternalAdmin, false)
		if err != nil {
			return sequences.OnChainOutput{}, err
		}
		batchOps = append(batchOps, regOut.BatchOps...)
	}

	return sequences.OnChainOutput{BatchOps: batchOps}, nil
}

// poolChainUpdate builds the ChainUpdate for one remote: the pool address in
// the left-padded-32 wire form counterpart pools compare by (exact bytes),
// and the token in the remote family's native form — the stellar OnRamp
// carries remote_token_address verbatim into the message, and an EVM
// destination OffRamp rejects a dest token that is not its native 20 bytes
// (see remoteTokenWireBytes).
func poolChainUpdate(remoteSel uint64, rc tokens.RemoteChainConfig[[]byte, string], bucket tokens.TPRLRateLimitBucket) (tpoolbindings.ChainUpdate, error) {
	outbound, err := rateLimiterConfigToBinding(bucket.OutboundRateLimiterConfig)
	if err != nil {
		return tpoolbindings.ChainUpdate{}, fmt.Errorf("outbound rate limit (remote %d): %w", remoteSel, err)
	}
	inbound, err := rateLimiterConfigToBinding(bucket.InboundRateLimiterConfig)
	if err != nil {
		return tpoolbindings.ChainUpdate{}, fmt.Errorf("inbound rate limit (remote %d): %w", remoteSel, err)
	}
	remoteToken, err := remoteTokenWireBytes(remoteSel, rc.RemoteToken)
	if err != nil {
		return tpoolbindings.ChainUpdate{}, fmt.Errorf("remote token (remote %d): %w", remoteSel, err)
	}
	remotePool, err := remotePoolWireBytes(remoteSel, rc.RemotePool)
	if err != nil {
		return tpoolbindings.ChainUpdate{}, fmt.Errorf("remote pool (remote %d): %w", remoteSel, err)
	}
	return tpoolbindings.ChainUpdate{
		RemoteChainSelector:       remoteSel,
		RemotePoolAddresses:       [][]byte{remotePool},
		RemoteTokenAddress:        remoteToken,
		OutboundRateLimiterConfig: outbound,
		InboundRateLimiterConfig:  inbound,
	}, nil
}

// remoteTokenWireBytes returns the remote token bytes a stellar pool must
// store: the remote family's native form. A 32-byte zero-left-padded input
// (the form the shared changeset supplies) is trimmed to the native tail;
// any other wrong length is an error, never a silent reshape — a stored
// token no destination accepts fails every send.
func remoteTokenWireBytes(remoteSel uint64, token []byte) ([]byte, error) {
	if len(token) == 0 {
		return nil, fmt.Errorf("empty remote token address")
	}
	native, err := remoteFamilyAddressBytesLength(remoteSel)
	if err != nil {
		return nil, fmt.Errorf("get chain family for remote chain %d: %w", remoteSel, err)
	}
	if len(token) == int(native) {
		return token, nil
	}
	if len(token) == 32 && int(native) < 32 && bytes.Trim(token[:32-int(native)], "\x00") == nil {
		return token[32-int(native):], nil
	}
	return nil, fmt.Errorf("remote token address is %d bytes, want the family-native %d", len(token), native)
}

// remotePoolWireBytes validates a remote pool address as the remote family's
// native length or the 32-byte left-padded wire form, and returns the padded
// form the pool stores. Any other length (including empty) is an error, never
// a silent pad — a malformed stored entry matches no counterpart pool.
func remotePoolWireBytes(remoteSel uint64, pool []byte) ([]byte, error) {
	if len(pool) == 0 {
		return nil, fmt.Errorf("empty remote pool address")
	}
	native, err := remoteFamilyAddressBytesLength(remoteSel)
	if err != nil {
		return nil, fmt.Errorf("get chain family for remote chain %d: %w", remoteSel, err)
	}
	if len(pool) == int(native) {
		return common.LeftPadBytes(pool, 32), nil
	}
	if len(pool) == 32 && int(native) < 32 && bytes.Trim(pool[:32-int(native)], "\x00") == nil {
		return pool, nil
	}
	return nil, fmt.Errorf("remote pool address is %d bytes, want the family-native %d", len(pool), native)
}

// remoteFamilyAddressBytesLength resolves the remote family's native address
// length from its own chain-family adapter (EVM 20, Solana 32, ...). The local
// AddressBytesLength helper hard-codes 20 for every non-stellar family, which
// rejects or truncates the 32-byte token addresses of non-EVM families.
func remoteFamilyAddressBytesLength(remoteSel uint64) (uint32, error) {
	family, err := chainsel.GetSelectorFamily(remoteSel)
	if err != nil {
		return 0, err
	}
	if family == chainsel.FamilyStellar {
		return stellarccip.StellarAddressByteLen, nil
	}
	if fam, ok := ccvadapters.GetChainFamilyRegistry().GetChainFamily(family); ok {
		return uint32(fam.GetAddressBytesLength()), nil
	}
	// EVM remotes must resolve even where the EVM family adapter package is not
	// linked in (its registration lives in the ccip EVM adapters package init).
	if family == chainsel.FamilyEVM {
		return 20, nil
	}
	return 0, fmt.Errorf("no chain-family adapter registered for family %q; cannot resolve its native address length", family)
}

// resolveRemoteRateLimitBuckets resolves the per-remote TPRL buckets the
// input supplies (EVM precedence minus migration metadata): paired default
// buckets and paired fast-finality buckets, each scaled to token amounts; a
// nil bucket means the input omitted it. A new lane initializes an omitted
// default to unlimited; an existing lane never writes an omitted bucket (its
// on-chain config stands, EVM on-chain-reuse parity).
func resolveRemoteRateLimitBuckets(input tokens.ConfigureTokenForTransfersInput, rc tokens.RemoteChainConfig[[]byte, string], localDecimals uint8) (*tokens.TPRLRateLimitBucket, *tokens.TPRLRateLimitBucket, error) {
	outbounds := rc.GetOutboundRateLimitBuckets()
	inbounds := rc.GetInboundRateLimitBuckets()
	defaultOut, outOK := outbounds.DefaultBucket()
	defaultIn, inOK := inbounds.DefaultBucket()
	if outOK != inOK {
		return nil, nil, fmt.Errorf(
			"default outbound and inbound rate limits must both be specified together or fully omitted")
	}
	var defBucket *tokens.TPRLRateLimitBucket
	if outOK {
		outbound, inbound := tokens.GenerateTPRLConfigs(
			defaultOut.RateLimit, defaultIn.RateLimit, localDecimals, rc.RemoteDecimals,
			chainsel.FamilyStellar, stellarops.ContractDeploymentVersion, input.PoolType)
		defBucket = &tokens.TPRLRateLimitBucket{OutboundRateLimiterConfig: outbound, InboundRateLimiterConfig: inbound}
	}

	ffOut, ffOutOK := outbounds.FastFinalityBucket()
	ffIn, ffInOK := inbounds.FastFinalityBucket()
	if ffOutOK != ffInOK {
		return nil, nil, fmt.Errorf(
			"fast-finality rate limits require both outbound and inbound buckets with fastFinality=true, or neither")
	}
	var ffBucket *tokens.TPRLRateLimitBucket
	if ffOutOK {
		ffOutbound, ffInbound := tokens.GenerateTPRLConfigs(
			ffOut.RateLimit, ffIn.RateLimit, localDecimals, rc.RemoteDecimals,
			chainsel.FamilyStellar, stellarops.ContractDeploymentVersion, input.PoolType)
		ffBucket = &tokens.TPRLRateLimitBucket{
			OutboundRateLimiterConfig: ffOutbound, InboundRateLimiterConfig: ffInbound, FastFinality: true,
		}
	}
	return defBucket, ffBucket, nil
}

// bucketOrZero dereferences a supplied bucket; an omitted default initializes
// a fresh lane unlimited.
func bucketOrZero(bucket *tokens.TPRLRateLimitBucket) tokens.TPRLRateLimitBucket {
	if bucket == nil {
		return tokens.TPRLRateLimitBucket{}
	}
	return *bucket
}

// registerTokenInTAR mirrors EVM RegisterToken / ManualRegistrationSequence:
// propose the desired admin when none is set (empty desiredAdmin defaults to
// the registry owner; reassignExisting additionally transfers the role when a
// different admin is already active; a matching on-chain pending admin is
// reused rather than re-proposed), accept when the pending admin is one of
// our identities (deployer direct, timelock via MCMS — any other pending
// admin is left pending for its owner, EVM external-admin behavior), then set
// the pool while a controlled identity administers the token.
func registerTokenInTAR(
	b cldf_ops.Bundle, deps stellardeps.StellarDeps, deployerAddr string,
	ds datastore.DataStore, chainSelector uint64,
	tarStrkey, tokenStrkey, poolStrkey, desiredAdmin string, reassignExisting bool,
) (sequences.OnChainOutput, error) {
	timelock, hasTimelock, err := cllTimelockStrkey(ds, chainSelector)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	router, err := newSorobanWriteRouter(b, deps, deployerAddr, ds, chainSelector, tarStrkey, tarops.ContractType)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}

	configReport, err := cldf_ops.ExecuteOperation(b, tarops.GetTokenConfig, deps, tarops.GetTokenConfigInput{
		ContractID: tarStrkey, LocalToken: tokenStrkey,
	})
	if err != nil {
		return sequences.OnChainOutput{}, fmt.Errorf("read token config from TAR %s: %w", tarStrkey, err)
	}
	if configReport.Output == nil {
		return sequences.OnChainOutput{}, fmt.Errorf("no token config returned for %s from TAR %s", tokenStrkey, tarStrkey)
	}
	admin := derefStr(configReport.Output.Administrator)
	pending := derefStr(configReport.Output.PendingAdministrator)
	pool := derefStr(configReport.Output.TokenPool)

	// pendingStaged: this run queued (not executed) the write that sets the
	// pending admin — the on-chain pending does not exist yet, so a direct
	// accept would fail OnlyPendingAdministrator.
	pendingStaged := false
	if admin == "" {
		if desiredAdmin == "" {
			// No external admin specified: default to the registry owner
			// (the router already resolved it — the TAR owner is the
			// authorizer for the propose below).
			desiredAdmin = router.owner
		}
		if pending != desiredAdmin {
			executedDirectly, err := router.route(router.owner, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tarops.ProposeAdministrator, deps, tarops.ProposeAdministratorInput{
					ContractID: tarStrkey, Caller: deployerAddr, LocalToken: tokenStrkey, Administrator: desiredAdmin,
				})
				return err
			}, "propose_administrator", []xdr.ScVal{
				scval.AddressToScVal(router.timelock), scval.AddressToScVal(tokenStrkey), scval.AddressToScVal(desiredAdmin),
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("propose admin %s for %s: %w", desiredAdmin, tokenStrkey, err)
			}
			pendingStaged = !executedDirectly
			pending = desiredAdmin
		}
		// else: a matching pending admin is already on chain — reuse it (a
		// rerun after the propose executed) rather than proposing again.
	} else if reassignExisting && admin != desiredAdmin && desiredAdmin != "" {
		executedDirectly, err := router.route(admin, func() error {
			newAdmin := desiredAdmin
			_, err := cldf_ops.ExecuteOperation(b, tarops.TransferAdminRole, deps, tarops.TransferAdminRoleInput{
				ContractID: tarStrkey, LocalToken: tokenStrkey, NewAdmin: &newAdmin,
			})
			return err
		}, "transfer_admin_role", []xdr.ScVal{
			scval.AddressToScVal(tokenStrkey), scval.OptionalAddressToScVal(&desiredAdmin),
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("transfer admin role for %s to %s: %w", tokenStrkey, desiredAdmin, err)
		}
		pendingStaged = !executedDirectly
		pending = desiredAdmin
	}

	// Accept when the pending admin is one of our identities; any other
	// pending admin is left pending (EVM external-admin behavior). A staged
	// (queued, unexecuted) propose/transfer means the on-chain pending does
	// not exist yet: a timelock pending may queue its accept in the same batch
	// (it invokes after the prerequisite), a deployer pending waits for the
	// rerun after the proposal executes.
	switch {
	case pending == "":
	case pending != deployerAddr && !(hasTimelock && pending == timelock):
	case pendingStaged && pending == deployerAddr:
		// Nothing to do this run; the rerun after the prerequisite executes
		// accepts directly.
	default:
		if _, err := router.route(pending, func() error {
			_, err := cldf_ops.ExecuteOperation(b, tarops.AcceptAdminRole, deps, tarops.AcceptAdminRoleInput{
				ContractID: tarStrkey, LocalToken: tokenStrkey,
			})
			return err
		}, "accept_admin_role", []xdr.ScVal{scval.AddressToScVal(tokenStrkey)}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("accept admin role for %s: %w", tokenStrkey, err)
		}
		admin = pending
	}

	// Set the pool while a controlled identity administers the token —
	// set_pool authorizes the token administrator, not the registry owner.
	if (admin == deployerAddr || (hasTimelock && admin == timelock)) && pool != poolStrkey {
		if _, err := router.route(admin, func() error {
			newPool := poolStrkey
			_, err := cldf_ops.ExecuteOperation(b, tarops.SetPool, deps, tarops.SetPoolInput{
				ContractID: tarStrkey, LocalToken: tokenStrkey, Pool: &newPool,
			})
			return err
		}, "set_pool", []xdr.ScVal{
			scval.AddressToScVal(tokenStrkey), scval.OptionalAddressToScVal(&poolStrkey),
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("set pool %s for %s: %w", poolStrkey, tokenStrkey, err)
		}
	}
	return router.output()
}

// resolvePoolStrkey resolves the pool strkey: an explicit ref address wins,
// else a datastore lookup by the ref's (type, version, qualifier).
func resolvePoolStrkey(ds datastore.DataStore, chainSelector uint64, ref datastore.AddressRef) (string, error) {
	if ref.Address != "" {
		cid, err := ownership.NormalizeContractAddress(ref.Address)
		if err != nil {
			return "", fmt.Errorf("normalize pool address on chain %d: %w", chainSelector, err)
		}
		return cid, nil
	}
	if ref.Type == "" {
		return "", fmt.Errorf("pool ref has neither an address nor a type to look up on chain %d", chainSelector)
	}
	full, err := stellarccip.LookupAddressRef(ds, chainSelector, ref.Type, ref.Version, ref.Qualifier)
	if err != nil {
		return "", fmt.Errorf("resolve pool ref on chain %d: %w", chainSelector, err)
	}
	cid, err := ownership.NormalizeContractAddress(full.Address)
	if err != nil {
		return "", fmt.Errorf("normalize resolved pool address on chain %d: %w", chainSelector, err)
	}
	return cid, nil
}

// resolveTokenStrkey resolves the token strkey: an explicit ref address wins,
// else the pool's wrapped token read live.
func resolveTokenStrkey(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolStrkey string, ref datastore.AddressRef) (string, error) {
	if ref.Address != "" {
		cid, err := ownership.NormalizeContractAddress(ref.Address)
		if err != nil {
			return "", fmt.Errorf("normalize token address: %w", err)
		}
		return cid, nil
	}
	report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetToken, deps, tpoolops.GetTokenInput{ContractID: poolStrkey})
	if err != nil {
		return "", fmt.Errorf("read wrapped token of pool %s: %w", poolStrkey, err)
	}
	return report.Output, nil
}

// derefStr dereferences an optional string.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applyPoolCCVConfig mirrors EVM's makeCCVUpdates with a stored-config
// comparison: provided CCV lists replace the stored ones, empty lists keep the
// stored values (EVM keeps the on-chain effective values), and the
// include-defaults flags are preserved. Nothing is applied when the resolved
// arg matches the stored config.
func applyPoolCCVConfig(
	b cldf_ops.Bundle, deps stellardeps.StellarDeps,
	aphRouter *sorobanWriteRouter, remoteSel uint64,
	rc tokens.RemoteChainConfig[[]byte, string],
) error {
	storedReport, err := cldf_ops.ExecuteOperation(b, aphops.GetCcvConfig, deps, aphops.GetCcvConfigInput{
		ContractID: aphRouter.contractID, RemoteChainSelector: remoteSel,
	})
	if err != nil {
		return fmt.Errorf("read stored CCV config: %w", err)
	}
	stored := aphbindings.CCVConfig{}
	hasStored := storedReport.Output != nil
	if hasStored {
		stored = *storedReport.Output
	}

	arg := aphbindings.CCVConfigArg{RemoteChainSelector: remoteSel}
	arg.OutboundCcvs, arg.OutboundIncludeDefaults = ccvListOrStored(rc.OutboundCCVs, stored.OutboundCcvs, stored.OutboundIncludeDefaults)
	arg.InboundCcvs, arg.InboundIncludeDefaults = ccvListOrStored(rc.InboundCCVs, stored.InboundCcvs, stored.InboundIncludeDefaults)
	arg.ThresholdOutboundCcvs = ccvListOrStoredList(rc.OutboundCCVsToAddAboveThreshold, stored.ThresholdOutboundCcvs)
	arg.ThresholdInboundCcvs = ccvListOrStoredList(rc.InboundCCVsToAddAboveThreshold, stored.ThresholdInboundCcvs)

	// Skip when nothing changes; skip a would-be-removal of an unconfigured
	// remote (an empty arg on a fresh remote is a no-op either way).
	if ccvArgMatchesStored(arg, stored, hasStored) {
		return nil
	}
	if !hasStored && len(arg.OutboundCcvs) == 0 && len(arg.InboundCcvs) == 0 &&
		len(arg.ThresholdOutboundCcvs) == 0 && len(arg.ThresholdInboundCcvs) == 0 {
		return nil
	}

	_, routeErr := aphRouter.route(aphRouter.owner, func() error {
		_, err := cldf_ops.ExecuteOperation(b, aphops.ApplyCCVConfigUpdates, deps, aphops.ApplyCCVConfigUpdatesInput{
			ContractID: aphRouter.contractID, Configs: []aphbindings.CCVConfigArg{arg},
		})
		return err
	}, "apply_ccv_config_updates", []xdr.ScVal{
		scval.StructSliceToScVal([]aphbindings.CCVConfigArg{arg}),
	})
	return routeErr
}

// ccvListOrStored resolves one CCV list: a provided list replaces the stored
// one (dropping its include-defaults flag), an empty list keeps the stored
// list with its flag.
func ccvListOrStored(provided, stored []string, storedIncludeDefaults bool) ([]string, bool) {
	if len(provided) > 0 {
		return provided, false
	}
	return stored, storedIncludeDefaults
}

// ccvListOrStoredList resolves one threshold CCV list the same way.
func ccvListOrStoredList(provided, stored []string) []string {
	if len(provided) > 0 {
		return provided
	}
	return stored
}

// ccvArgMatchesStored reports whether the resolved arg reproduces the stored
// config (lists compare unordered, EVM comparison parity).
func ccvArgMatchesStored(arg aphbindings.CCVConfigArg, stored aphbindings.CCVConfig, hasStored bool) bool {
	if !hasStored {
		return false
	}
	return unorderedStringsEqual(arg.OutboundCcvs, stored.OutboundCcvs) &&
		arg.OutboundIncludeDefaults == stored.OutboundIncludeDefaults &&
		unorderedStringsEqual(arg.InboundCcvs, stored.InboundCcvs) &&
		arg.InboundIncludeDefaults == stored.InboundIncludeDefaults &&
		unorderedStringsEqual(arg.ThresholdOutboundCcvs, stored.ThresholdOutboundCcvs) &&
		unorderedStringsEqual(arg.ThresholdInboundCcvs, stored.ThresholdInboundCcvs)
}

// unorderedStringsEqual compares two string slices ignoring order.
func unorderedStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// readLiveBuckets reads the lane's live rate-limit buckets: the default
// always (as configured, disabled included) and the fast-finality bucket only
// when it is enabled on chain.
func readLiveBuckets(b cldf_ops.Bundle, deps stellardeps.StellarDeps, poolStrkey string, remoteSel uint64) (*tokens.TPRLRateLimitBucket, *tokens.TPRLRateLimitBucket, error) {
	read := func(fastFinality bool) (*tpoolbindings.RateLimiterState, error) {
		report, err := cldf_ops.ExecuteOperation(b, tpoolops.GetCurrentRateLimiterState, deps, tpoolops.GetCurrentRateLimiterStateInput{
			ContractID: poolStrkey, RemoteChainSelector: remoteSel, FastFinality: fastFinality,
		})
		if err != nil {
			return nil, err
		}
		return report.Output, nil
	}
	def, err := read(false)
	if err != nil {
		return nil, nil, err
	}
	ff, err := read(true)
	if err != nil {
		return nil, nil, err
	}
	defBucket := &tokens.TPRLRateLimitBucket{
		OutboundRateLimiterConfig: tokenBucketToRateLimiter(def.Outbound),
		InboundRateLimiterConfig:  tokenBucketToRateLimiter(def.Inbound),
	}
	var ffBucket *tokens.TPRLRateLimitBucket
	if ff.Outbound.IsEnabled || ff.Inbound.IsEnabled {
		ffBucket = &tokens.TPRLRateLimitBucket{
			FastFinality:              true,
			OutboundRateLimiterConfig: tokenBucketToRateLimiter(ff.Outbound),
			InboundRateLimiterConfig:  tokenBucketToRateLimiter(ff.Inbound),
		}
	}
	return defBucket, ffBucket, nil
}

// tokenBucketToRateLimiter maps an on-chain bucket's config fields to the
// chain-agnostic rate limiter config (the refill state is not configuration).
func tokenBucketToRateLimiter(tb tpoolbindings.TokenBucket) tokens.RateLimiterConfig {
	return tokens.RateLimiterConfig{
		IsEnabled: tb.IsEnabled,
		Capacity:  u128ToBigInt(tb.Capacity),
		Rate:      u128ToBigInt(tb.Rate),
	}
}

// u128ToBigInt widens the U128 hi/lo form to a big.Int.
func u128ToBigInt(v scval.U128) *big.Int {
	return new(big.Int).Or(
		new(big.Int).Lsh(new(big.Int).SetUint64(uint64(v.Hi)), 64),
		new(big.Int).SetUint64(uint64(v.Lo)),
	)
}
