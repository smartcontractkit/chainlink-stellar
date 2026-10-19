package adapters

import (
	"bytes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stellar/go-stellar-sdk/xdr"

	chainsel "github.com/smartcontractkit/chain-selectors"
	tokens "github.com/smartcontractkit/chainlink-ccip/deployment/tokens"
	sequences "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	cldf_chain "github.com/smartcontractkit/chainlink-deployments-framework/chain"
	evmcontract "github.com/smartcontractkit/chainlink-deployments-framework/chain/evm/operations/contract"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	mcmstypes "github.com/smartcontractkit/mcms/types"

	tpoolbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_pool"
	scval "github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/mcmsutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
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
// the contract's require_auth on it passes).
func (r *sorobanWriteRouter) route(authorizer string, directExec func() error, fn string, args []xdr.ScVal) error {
	switch {
	case authorizer == r.deployerAddr:
		if err := directExec(); err != nil {
			return err
		}
		r.executed = true
		return nil
	case r.timelock != "" && authorizer == r.timelock:
		data, err := mcmsutil.EncodeSorobanMCMSInvokePayload(fn, args)
		if err != nil {
			return err
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
		return nil
	default:
		return fmt.Errorf(
			"contract %s (%s) on chain %d requires authorizer %q, which is neither deployer %q nor timelock %q",
			r.contractID, r.contractType, r.chainSelector, authorizer, r.deployerAddr, r.timelock)
	}
}

// output returns one batch op: an ExecInfo-marked empty op when anything was
// executed directly, one op carrying every proposal write, or none when
// nothing changed (idempotent skip).
func (r *sorobanWriteRouter) output() (sequences.OnChainOutput, error) {
	if r.executed {
		batchOp, err := evmcontract.NewBatchOperationFromWrites([]evmcontract.WriteOutput{{
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
		return sequences.OnChainOutput{BatchOps: []mcmstypes.BatchOperation{batchOp}}, nil
	}
	if len(r.writes) == 0 {
		return sequences.OnChainOutput{}, nil
	}
	batchOp, err := evmcontract.NewBatchOperationFromWrites(r.writes)
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	return sequences.OnChainOutput{BatchOps: []mcmstypes.BatchOperation{batchOp}}, nil
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
			dep, err := stellardeployment.NewDeployerFromChain(ch)
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
			dep, err := stellardeployment.NewDeployerFromChain(ch)
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
	// Input validation first: unsupported arms fail fast before any
	// on-chain read.
	for remoteSel, rc := range input.RemoteChains {
		if len(rc.InboundCCVs) > 0 || len(rc.OutboundCCVs) > 0 ||
			len(rc.InboundCCVsToAddAboveThreshold) > 0 || len(rc.OutboundCCVsToAddAboveThreshold) > 0 {
			return sequences.OnChainOutput{}, fmt.Errorf(
				"per-remote CCV lists are not supported by the stellar adapter (remote %d)", remoteSel)
		}
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

	var batchOps []mcmstypes.BatchOperation

	// Per-remote configuration: chain updates and rate limits.
	for remoteSel, rc := range input.RemoteChains {
		if len(rc.InboundCCVs) > 0 || len(rc.OutboundCCVs) > 0 ||
			len(rc.InboundCCVsToAddAboveThreshold) > 0 || len(rc.OutboundCCVsToAddAboveThreshold) > 0 {
			return sequences.OnChainOutput{}, fmt.Errorf(
				"per-remote CCV lists are not supported by the stellar adapter (remote %d)", remoteSel)
		}
		if rc.TokenTransferFeeConfig != nil {
			return sequences.OnChainOutput{}, fmt.Errorf(
				"pool-level token transfer fee config is not supported by the stellar adapter (remote %d); use the fee changesets instead", remoteSel)
		}

		buckets, hasRateLimitInput, err := resolveRemoteRateLimitBuckets(input, rc, uint8(decimalsReport.Output))
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("resolve rate limit buckets for remote %d: %w", remoteSel, err)
		}

		supportedReport, err := cldf_ops.ExecuteOperation(b, tpoolops.IsSupportedChain, deps, tpoolops.IsSupportedChainInput{
			ContractID: poolStrkey, RemoteChainSelector: remoteSel,
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("read remote support of pool %s for %d: %w", poolStrkey, remoteSel, err)
		}

		if !supportedReport.Output {
			// New lane: one ChainUpdate with the left-padded-32 wire
			// encoding (the same form PostConnect writes, so the two
			// writers cannot double-apply).
			add := poolChainUpdate(remoteSel, rc, buckets[0])
			if err := poolRouter.route(poolRouter.owner, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.ApplyChainUpdates, deps, tpoolops.ApplyChainUpdatesInput{
					ContractID: poolStrkey, Adds: []tpoolbindings.ChainUpdate{add},
				})
				return err
			}, "apply_chain_updates", []xdr.ScVal{
				scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{add}),
				scval.Uint64SliceToScVal(nil),
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("apply chain updates for remote %d on pool %s: %w", remoteSel, poolStrkey, err)
			}
			// Fast-finality bucket (if provided) as a separate call.
			if hasRateLimitInput && len(buckets) > 1 {
				rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, tokens.TPRLRemotes{
					ChainSelector:       input.ChainSelector,
					RemoteChainSelector: remoteSel,
					TokenPoolRef:        poolRef,
					ExistingDataStore:   ds,
					RateLimitBuckets:    buckets[1:],
				})
				if err != nil {
					return sequences.OnChainOutput{}, fmt.Errorf("set fast-finality rate limits for remote %d: %w", remoteSel, err)
				}
				batchOps = append(batchOps, rlOut.BatchOps...)
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
		if !bytes.Equal(remoteTokenReport.Output, common.LeftPadBytes(rc.RemoteToken, 32)) {
			// Remote token changed: remove the lane and re-add it.
			if err := poolRouter.route(poolRouter.owner, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.ApplyChainUpdates, deps, tpoolops.ApplyChainUpdatesInput{
					ContractID: poolStrkey, Removes: []uint64{remoteSel},
				})
				return err
			}, "apply_chain_updates", []xdr.ScVal{
				scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{}),
				scval.Uint64SliceToScVal([]uint64{remoteSel}),
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("remove changed remote token lane %d on pool %s: %w", remoteSel, poolStrkey, err)
			}
			add := poolChainUpdate(remoteSel, rc, buckets[0])
			if err := poolRouter.route(poolRouter.owner, func() error {
				_, err := cldf_ops.ExecuteOperation(b, tpoolops.ApplyChainUpdates, deps, tpoolops.ApplyChainUpdatesInput{
					ContractID: poolStrkey, Adds: []tpoolbindings.ChainUpdate{add},
				})
				return err
			}, "apply_chain_updates", []xdr.ScVal{
				scval.StructSliceToScVal([]tpoolbindings.ChainUpdate{add}),
				scval.Uint64SliceToScVal(nil),
			}); err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("re-add changed lane %d on pool %s: %w", remoteSel, poolStrkey, err)
			}
		} else {
			// Same token: add the remote pool if missing.
			remotePoolsReport, err := cldf_ops.ExecuteOperation(b, tpoolops.GetRemotePools, deps, tpoolops.GetRemotePoolsInput{
				ContractID: poolStrkey, RemoteChainSelector: remoteSel,
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("read remote pools of pool %s for %d: %w", poolStrkey, remoteSel, err)
			}
			padded := common.LeftPadBytes(rc.RemotePool, 32)
			if len(rc.RemotePool) > 0 && !containsBytes(remotePoolsReport.Output, padded) {
				if err := poolRouter.route(poolRouter.owner, func() error {
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

		// Rate limit updates for the buckets the input provides (no
		// buckets reuses the on-chain values, EVM parity — no write).
		if hasRateLimitInput {
			rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, tokens.TPRLRemotes{
				ChainSelector:       input.ChainSelector,
				RemoteChainSelector: remoteSel,
				TokenPoolRef:        poolRef,
				ExistingDataStore:   ds,
				RateLimitBuckets:    buckets,
			})
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("set rate limits for remote %d: %w", remoteSel, err)
			}
			batchOps = append(batchOps, rlOut.BatchOps...)
		}
	}

	// Allowed finality (pool-global, read-before-write).
	if !input.AllowedFinalityConfig.IsZero() {
		var anyRemote uint64
		for remoteSel := range input.RemoteChains {
			anyRemote = remoteSel
			break
		}
		rlOut, err := setTokenPoolRateLimits(b, deps, deployerAddr, tokens.TPRLRemotes{
			ChainSelector:         input.ChainSelector,
			RemoteChainSelector:   anyRemote,
			TokenPoolRef:          poolRef,
			ExistingDataStore:     ds,
			AllowedFinalityConfig: input.AllowedFinalityConfig,
		})
		if err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("set allowed finality on pool %s: %w", poolStrkey, err)
		}
		batchOps = append(batchOps, rlOut.BatchOps...)
	}

	poolOut, err := poolRouter.output()
	if err != nil {
		return sequences.OnChainOutput{}, err
	}
	batchOps = append(batchOps, poolOut.BatchOps...)

	// TokenAdminRegistry registration (EVM RegisterToken parity).
	if !input.SkipTokenAdminRegistrySetup {
		tarStrkey := input.RegistryAddress
		if tarStrkey == "" {
			tarStrkey, err = stellarccip.TokenAdminRegistryDatastoreRef().LookupStrkey(ds, input.ChainSelector)
			if err != nil {
				return sequences.OnChainOutput{}, fmt.Errorf("resolve TokenAdminRegistry on chain %d: %w", input.ChainSelector, err)
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

// poolChainUpdate builds the ChainUpdate for one remote with the
// left-padded-32 wire encoding both writers (this sequence and devenv
// PostConnect) use.
func poolChainUpdate(remoteSel uint64, rc tokens.RemoteChainConfig[[]byte, string], bucket tokens.TPRLRateLimitBucket) tpoolbindings.ChainUpdate {
	return tpoolbindings.ChainUpdate{
		RemoteChainSelector:       remoteSel,
		RemotePoolAddresses:       [][]byte{common.LeftPadBytes(rc.RemotePool, 32)},
		RemoteTokenAddress:        common.LeftPadBytes(rc.RemoteToken, 32),
		OutboundRateLimiterConfig: rateLimiterConfigToBinding(bucket.OutboundRateLimiterConfig),
		InboundRateLimiterConfig:  rateLimiterConfigToBinding(bucket.InboundRateLimiterConfig),
	}
}

// resolveRemoteRateLimitBuckets builds the per-remote TPRL buckets (EVM
// precedence minus migration metadata): paired default buckets from the input
// scaled to token amounts; no input buckets → disabled defaults (a new lane
// starts unlimited); paired fast-finality buckets appended after the default.
// The second return reports whether the input carried any rate limit config.
func resolveRemoteRateLimitBuckets(input tokens.ConfigureTokenForTransfersInput, rc tokens.RemoteChainConfig[[]byte, string], localDecimals uint8) ([]tokens.TPRLRateLimitBucket, bool, error) {
	outbounds := rc.GetOutboundRateLimitBuckets()
	inbounds := rc.GetInboundRateLimitBuckets()
	defaultOut, outOK := outbounds.DefaultBucket()
	defaultIn, inOK := inbounds.DefaultBucket()
	if outOK != inOK {
		return nil, false, fmt.Errorf(
			"default outbound and inbound rate limits must both be specified together or fully omitted")
	}
	var outbound, inbound tokens.RateLimiterConfig
	if outOK {
		outbound, inbound = tokens.GenerateTPRLConfigs(
			defaultOut.RateLimit, defaultIn.RateLimit, localDecimals, rc.RemoteDecimals,
			chainsel.FamilyStellar, stellarops.ContractDeploymentVersion, input.PoolType)
	}
	buckets := []tokens.TPRLRateLimitBucket{{OutboundRateLimiterConfig: outbound, InboundRateLimiterConfig: inbound}}

	ffOut, ffOutOK := outbounds.FastFinalityBucket()
	ffIn, ffInOK := inbounds.FastFinalityBucket()
	if ffOutOK != ffInOK {
		return nil, false, fmt.Errorf(
			"fast-finality rate limits require both outbound and inbound buckets with fastFinality=true, or neither")
	}
	if ffOutOK {
		ffOutbound, ffInbound := tokens.GenerateTPRLConfigs(
			ffOut.RateLimit, ffIn.RateLimit, localDecimals, rc.RemoteDecimals,
			chainsel.FamilyStellar, stellarops.ContractDeploymentVersion, input.PoolType)
		buckets = append(buckets, tokens.TPRLRateLimitBucket{
			OutboundRateLimiterConfig: ffOutbound, InboundRateLimiterConfig: ffInbound, FastFinality: true,
		})
	}
	return buckets, outOK || ffOutOK, nil
}

// registerTokenInTAR mirrors EVM RegisterToken / ManualRegistrationSequence:
// propose the desired admin when none is set (empty desiredAdmin defaults to
// the registry owner; reassignExisting additionally transfers the role when a
// different admin is already active), accept when the pending admin is one of
// our identities (deployer direct, timelock via MCMS — any other pending
// admin is left pending for its owner, EVM external-admin behavior), then set
// the pool while the registry owner administers the token.
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

	if admin == "" {
		if desiredAdmin == "" {
			// No external admin specified: default to the registry owner
			// (the router already resolved it — the TAR owner is the
			// authorizer for the propose below).
			desiredAdmin = router.owner
		}
		if err := router.route(router.owner, func() error {
			_, err := cldf_ops.ExecuteOperation(b, tarops.ProposeAdministrator, deps, tarops.ProposeAdministratorInput{
				ContractID: tarStrkey, Caller: deployerAddr, LocalToken: tokenStrkey, Administrator: desiredAdmin,
			})
			return err
		}, "propose_administrator", []xdr.ScVal{
			scval.AddressToScVal(router.timelock), scval.AddressToScVal(tokenStrkey), scval.AddressToScVal(desiredAdmin),
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("propose admin %s for %s: %w", desiredAdmin, tokenStrkey, err)
		}
		pending = desiredAdmin
	} else if reassignExisting && admin != desiredAdmin && desiredAdmin != "" {
		if err := router.route(admin, func() error {
			newAdmin := desiredAdmin
			_, err := cldf_ops.ExecuteOperation(b, tarops.TransferAdminRole, deps, tarops.TransferAdminRoleInput{
				ContractID: tarStrkey, LocalToken: tokenStrkey, NewAdmin: &newAdmin,
			})
			return err
		}, "transfer_admin_role", []xdr.ScVal{
			scval.AddressToScVal(tokenStrkey), scval.OptionalAddressToScVal(&desiredAdmin),
		}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("transfer admin role for %s to %s: %w", tokenStrkey, desiredAdmin, err)
		}
	}

	// Accept when the pending admin is one of our identities; any other
	// pending admin is left pending (EVM external-admin behavior).
	if pending != "" && (pending == deployerAddr || (hasTimelock && pending == timelock)) {
		if err := router.route(pending, func() error {
			_, err := cldf_ops.ExecuteOperation(b, tarops.AcceptAdminRole, deps, tarops.AcceptAdminRoleInput{
				ContractID: tarStrkey, LocalToken: tokenStrkey,
			})
			return err
		}, "accept_admin_role", []xdr.ScVal{scval.AddressToScVal(tokenStrkey)}); err != nil {
			return sequences.OnChainOutput{}, fmt.Errorf("accept admin role for %s: %w", tokenStrkey, err)
		}
		admin = pending
	}

	// Set the pool while the registry-owner administers the token.
	if admin == router.owner && pool != poolStrkey {
		if err := router.route(admin, func() error {
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

// containsBytes reports whether the list contains an equal byte slice.
func containsBytes(list [][]byte, want []byte) bool {
	for _, b := range list {
		if bytes.Equal(b, want) {
			return true
		}
	}
	return false
}
