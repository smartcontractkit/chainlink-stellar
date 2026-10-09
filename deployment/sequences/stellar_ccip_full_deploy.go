package sequences

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/offchain"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	executorbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/executor"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rampregistrybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	recvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	execops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/executor"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	rrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func execStellarCCIPOp[IN, OUT any](
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	op *cldf_ops.Operation[IN, OUT, stellardeps.StellarDeps],
	in IN,
	opts ...cldf_ops.ExecuteOption[IN, stellardeps.StellarDeps],
) (OUT, error) {
	rep, err := cldf_ops.ExecuteOperation(b, op, deps, in, opts...)
	if err != nil {
		var z OUT
		return z, err
	}
	return rep.Output, nil
}

// statReleaseWasm checks that a release-profile WASM path exists and is a regular file.
func statReleaseWasm(path, displayName string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s WASM not found at %s. Run 'make build'", displayName, path)
		}
		return fmt.Errorf("stat %s WASM at %s: %w", displayName, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s WASM at %s is not a regular file (mode %s)", displayName, path, info.Mode())
	}
	return nil
}

// RunStellarCCIPFullDeploy deploys and configures the full Stellar CCIP Soroban stack for devenv using CLDF
// operations on the given bundle. It mirrors the phased devenv pipeline (foundation → verification/fees
// → ramps → receiver). Each component deploys and initializes through its own
// rerun-safe sequence; this orchestrator passes dependencies, applies the config
// ops and updates the host.
//
// topology is optional: committee verifier signature quorums are applied at lane-configuration
// time, not during the deploy, so the deploy itself needs no NOP/committee data. It is kept as a
// parameter for callers that hold one (CCV's stash → [StellarDeployChainContracts], or
// [RunStellarCCIPFullDeployForCCV] which converts CCV topology) and for future deploy-time needs.
func RunStellarCCIPFullDeploy(
	ctx context.Context,
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	h stellarccip.CCIPDevenvHost,
	topology *offchain.EnvironmentTopology,
	in DeployStellarCCIPInnerInput,
) (seq_core.OnChainOutput, error) {
	if h == nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("RunStellarCCIPFullDeploy: CCIPDevenvHost is nil")
	}
	if deps.Deploy == nil || deps.Invoker == nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("RunStellarCCIPFullDeploy: incomplete StellarDeps")
	}
	// topology (possibly nil) is retained for the stash contract and future deploy-time needs; committee
	// signer quorums are applied later, at lane-configuration time (see the comment below).

	ds := datastore.NewMemoryDataStore()
	if err := stellarccip.MergeExistingAddressRefs(ds, in.ExistingAddresses); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	// Component deps for the per-component sequences: plain op deps plus the
	// skip-if-exists inputs that must stay out of the sequence input hash.
	componentDeps := ComponentDeps{
		StellarDeps:       deps,
		NetworkPassphrase: h.NetworkPassphrase(),
		DeployerAddress:   h.DeployerKeypair().Address(),
		Ledger:            h.Deployer(),
	}

	stellarRoot, err := stellarutil.FindStellarRoot()
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("locate chainlink-stellar root: %w", err)
	}

	selector := in.ChainSelector
	allSelectors := in.AllSelectors
	remoteSelectors := stellarutil.FilterRemoteSelectors(allSelectors, selector)

	var (
		feeTokenContractID  string
		onrampContractID    string
		rmnRemoteContractID string
		rmnProxyContractID  string
		feeQuoterContractID string
		tarContractID       string
		vvrContractID       string
		cvContractID        string
		offRampContractID   string
		routerContractID    string
	)

	// --- Foundation → verification/fees → ramps → receiver, via component sequences ---
	rmnRemoteWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	currentRefs, err := ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	rmnRemoteOut, err := execComponentSequence(b, componentDeps, DeployRMNRemote, DeployRMNRemoteInput{
		ChainSelector:      selector,
		WasmPath:           rmnRemoteWasmPath,
		CurseAdmins:        in.CurseAdmins,
		EnableFastCurse:    in.EnableFastCurse,
		FastCurseQualifier: in.FastCurseQualifier,
		ExistingAddresses:  currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy RMN Remote: %w", err)
	}
	rmnRemoteContractID = rmnRemoteOut.ContractID
	if err := upsertComponentRefs(ds, rmnRemoteOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("rmnRemoteContractID", rmnRemoteContractID).Msg("RMN Remote deployed and initialized")

	rmnProxyWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "rmn_proxy.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	rmnProxyOut, err := execComponentSequence(b, componentDeps, DeployRMNProxy, DeployRMNProxyInput{
		ChainSelector:     selector,
		WasmPath:          rmnProxyWasmPath,
		RmnRemote:         rmnRemoteContractID,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy RMN Proxy: %w", err)
	}
	rmnProxyContractID = rmnProxyOut.ContractID
	if err := upsertComponentRefs(ds, rmnProxyOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("rmnProxyContractID", rmnProxyContractID).Msg("RMN Proxy deployed and initialized")
	// Record the RMN proxy on the host so post-deploy pool initialization can pass it
	// into each pool's initialize (EVM `immutable i_rmnProxy` parity — pools store it once
	// and consult it directly for curse checks, not via the Router).
	h.SetRmnProxy(rmnProxyContractID)

	feeQuoterWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "fee_quoter.wasm")
	if h.FriendbotURL() != "" {
		feeTokenID, feeTokenErr := h.CreateFeeToken(ctx, h.FriendbotURL())
		if feeTokenErr != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("create fee token: %w", feeTokenErr)
		}
		h.SetFeeToken(feeTokenID)
		feeTokenContractID = feeTokenID
		h.Logger().Info().Str("contractID", feeTokenID).Msg("Fee token SAC deployed for CCIP fee payments")
	} else {
		h.Logger().Warn().Msg("Friendbot URL not available; using mock fee token ID (fee transfers will not work)")
		feeTokenContractID = stellarutil.MustGenerateMockContractID(h.DeployerKeypair().Address(), "fee-token")
	}
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	feeQuoterOut, err := execComponentSequence(b, componentDeps, DeployFeeQuoter, DeployFeeQuoterInput{
		ChainSelector:     selector,
		WasmPath:          feeQuoterWasmPath,
		FeeToken:          feeTokenContractID,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy FeeQuoter: %w", err)
	}
	feeQuoterContractID = feeQuoterOut.ContractID
	if err := upsertComponentRefs(ds, feeQuoterOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	feeQuoterClient := fqbindings.NewFeeQuoterClient(h.Deployer(), feeQuoterContractID)
	h.SetFeeQuoter(feeQuoterClient)
	h.Logger().Info().Str("feeQuoterContractID", feeQuoterContractID).Msg("FeeQuoter deployed and initialized")

	tarWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "token_admin_registry.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	tarOut, err := execComponentSequence(b, componentDeps, DeployTokenAdminRegistry, DeployTokenAdminRegistryInput{
		ChainSelector:     selector,
		WasmPath:          tarWasmPath,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy TokenAdminRegistry: %w", err)
	}
	tarContractID = tarOut.ContractID
	if err := upsertComponentRefs(ds, tarOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	tarClient := tarbindings.NewTokenAdminRegistryClient(h.Deployer(), tarContractID)
	h.SetTokenAdminRegistry(tarContractID, tarClient)
	h.Logger().Info().Str("contractID", tarContractID).Msg("TokenAdminRegistry deployed and initialized")

	// Fee aggregator receivable account: devenv uses the deployer account (real on-chain identity, not a synthetic C… mock).
	feeAggregatorAddr := h.DeployerKeypair().Address()
	// OnRamp deploys and initializes in one step, after TAR (the monolith
	// deployed it first for no functional reason; addresses are
	// salt-deterministic, so only the transaction order changes — the one
	// reviewed op-order move of this refactor).
	onrampWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "onramp.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	onrampOut, err := execComponentSequence(b, componentDeps, DeployOnRamp, DeployOnRampInput{
		ChainSelector:      selector,
		WasmPath:           onrampWasmPath,
		TokenAdminRegistry: tarContractID,
		RmnProxy:           rmnProxyContractID,
		FeeQuoter:          feeQuoterContractID,
		FeeAggregator:      feeAggregatorAddr,
		ExistingAddresses:  currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy OnRamp: %w", err)
	}
	onrampContractID = onrampOut.ContractID
	if err := upsertComponentRefs(ds, onrampOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	onRampClient := onrampbindings.NewOnRampClient(h.Deployer(), onrampContractID)
	h.SetOnRamp(onrampContractID, onRampClient)
	h.Logger().Info().Str("onRampContractID", onrampContractID).Msg("OnRamp deployed and initialized")

	// --- Verification + FeeQuoter config ---
	vvrWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccvs_versioned_verifier_resolver.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	vvrOut, err := execComponentSequence(b, componentDeps, DeployVVR, DeployVVRInput{
		ChainSelector:     selector,
		WasmPath:          vvrWasmPath,
		FeeAggregator:     feeAggregatorAddr,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy VVR: %w", err)
	}
	vvrContractID = vvrOut.ContractID
	if err := upsertComponentRefs(ds, vvrOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.SetVVR(vvrContractID)
	h.Logger().Info().Str("vvrContractID", vvrContractID).Msg("VVR deployed and initialized")

	cvWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccvs_committee_verifier.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	mockStorageLocation := stellarutil.GenerateContractAddress("storage-location", h.NetworkPassphrase())
	cvOut, err := execComponentSequence(b, componentDeps, DeployCommitteeVerifier, DeployCommitteeVerifierInput{
		ChainSelector:     selector,
		WasmPath:          cvWasmPath,
		StorageLocations:  [][]byte{mockStorageLocation},
		RmnProxy:          rmnProxyContractID,
		FeeAggregator:     feeAggregatorAddr,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy Committee Verifier: %w", err)
	}
	cvContractID = cvOut.ContractID
	if err := upsertComponentRefs(ds, cvOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.SetCV(cvContractID)
	h.Logger().Info().Str("cvContractID", cvContractID).Msg("Committee Verifier deployed and initialized")

	execWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "executor.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	// The source-side fee/policy Executor (EVM Executor.sol parity). The OnRamp's
	// get_fee path cross-calls Executor::get_fee on default_executor, so the
	// contract must be deployed+initialized with the dest chain enabled and an
	// allowed_finality_config that permits the requested finality the lane flows
	// send (the stellar family default; EVM deploy defaults apply the same to
	// executors — see FamilyDefaultAllowedFinality).
	execOut, err := execComponentSequence(b, componentDeps, DeployExecutor, DeployExecutorInput{
		ChainSelector:         selector,
		WasmPath:              execWasmPath,
		FeeAggregator:         feeAggregatorAddr,
		AllowedFinalityConfig: FamilyDefaultAllowedFinality,
		ExistingAddresses:     currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy Executor: %w", err)
	}
	executorContractID := execOut.ContractID
	if err := upsertComponentRefs(ds, execOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("contractID", executorContractID).Msg("Executor deployed and initialized")

	execDestChainAdds := make([]executorbindings.RemoteChainConfigArgs, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		execDestChainAdds = append(execDestChainAdds, executorbindings.RemoteChainConfigArgs{
			DestChainSelector: rs,
			Config: executorbindings.RemoteChainConfig{
				Enabled:     true,
				UsdCentsFee: 0,
			},
		})
	}
	if _, err := execStellarCCIPOp(b, deps, execops.ApplyDestChainUpdates, execops.ApplyDestChainUpdatesInput{
		ContractID: executorContractID,
		ToAdd:      execDestChainAdds,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply Executor dest chain updates: %w", err)
	}
	h.Logger().Info().Str("contractID", executorContractID).Msg("Executor initialized and dest chains enabled")

	outboundImplUpdates := []vvrbindings.OutboundImplementationUpdate{}
	for _, remoteSelector := range allSelectors {
		outboundImplUpdates = append(outboundImplUpdates, vvrbindings.OutboundImplementationUpdate{
			DestChainSelector: remoteSelector,
			Verifier:          &cvContractID,
		})
	}
	if _, err := execStellarCCIPOp(b, deps, vvrops.ApplyOutboundImplUpdates, vvrops.ApplyOutboundImplUpdatesInput{
		ContractID:      vvrContractID,
		Implementations: outboundImplUpdates,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply outbound implementation updates: %w", err)
	}

	inboundImplUpdates := []vvrbindings.InboundImplementationUpdate{
		{
			Version:  stellarutil.DefaultCommitteeVerifierVersionTag(),
			Verifier: &cvContractID,
		},
	}
	if _, err := execStellarCCIPOp(b, deps, vvrops.ApplyInboundImplUpdates, vvrops.ApplyInboundImplUpdatesInput{
		ContractID:      vvrContractID,
		Implementations: inboundImplUpdates,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply inbound implementation updates: %w", err)
	}

	// Signature quorum configs are deliberately NOT applied here. Since the
	// chainlink-ccv 2026-06-26 signing-key sync cutover, topology enrichment skips
	// families whose bootstrappers push signing keys to JD on connect, so signer
	// addresses are not reliably present at deploy time. They are resolved from JD
	// (with topology fallback) and applied during the lane-configuration phase by
	// StellarConfigureChainForLanes via ConfigureChainsForLanesFromTopology.

	fqDestChainConfigs := []fqbindings.DestChainConfigArgs{}
	for _, rs := range allSelectors {
		fqDestChainConfigs = append(fqDestChainConfigs, fqbindings.DestChainConfigArgs{
			DestChainSelector: rs,
			Config: fqbindings.DestChainConfig{
				IsEnabled:             true,
				MaxDataBytes:          50000,
				MaxPerMsgGasLimit:     4_000_000,
				DestGasOverhead:       350_000,
				DestGasPerPayloadByte: 16,
				DefaultTokenFeeUsd:    50,
				DefaultTokenDestGas:   50_000,
				DefaultTxGasLimit:     200_000,
				NetworkFeeUsdCents:    100,
				LinkPremiumPercent:    90,
			},
		})
	}
	if _, err := execStellarCCIPOp(b, deps, fqops.ApplyDestChainConfigs, fqops.ApplyDestChainConfigsInput{
		ContractID: feeQuoterContractID,
		Configs:    fqDestChainConfigs,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply dest chain configs on FeeQuoter: %w", err)
	}

	gasPriceUpdates := make([]fqbindings.GasPriceUpdate, 0, len(allSelectors))
	for _, rs := range allSelectors {
		gasPriceUpdates = append(gasPriceUpdates, fqbindings.GasPriceUpdate{
			DestChainSelector: rs,
			UsdPerUnitGas:     scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 100_000_000_000_000}),
		})
	}
	if _, err := execStellarCCIPOp(b, deps, fqops.UpdatePrices, fqops.UpdatePricesInput{
		ContractID: feeQuoterContractID,
		Updater:    h.DeployerKeypair().Address(),
		PriceUpdates: fqbindings.PriceUpdates{
			TokenPriceUpdates: []fqbindings.TokenPriceUpdate{
				{
					Token: feeTokenContractID,
					// Devenv fee token is a 7-decimal SAC. USDPriceWith18Decimals
					// convention (EVM Internal.Price.usdPerToken) = "USD × 1e18 per
					// 1e18 smallest units", scaled by token decimals:
					// $1 × 10^(36-7) = 1e29. Using 1e18 (only valid for 18-dec tokens)
					// makes a 50¢ fee quote as 5e17 base units (50B tokens) — unpayable.
					// See contracts/common/helpers/src/fee_math.rs.
					UsdPerToken: scval.U128(xdr.UInt128Parts{ // 1e29 = $1 × 10^(36-7); split into u64 limbs (Lo alone overflows u64).
						Hi: 5421010862, Lo: 7886392056514347008,
					}),
				},
			},
			GasPriceUpdates: gasPriceUpdates,
		},
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("update prices on FeeQuoter: %w", err)
	}

	if testToken := h.TestTokenContractID(); testToken != "" {
		if _, err := execStellarCCIPOp(b, deps, fqops.UpdatePrices, fqops.UpdatePricesInput{
			ContractID: feeQuoterContractID,
			Updater:    h.DeployerKeypair().Address(),
			PriceUpdates: fqbindings.PriceUpdates{
				TokenPriceUpdates: []fqbindings.TokenPriceUpdate{{
					Token: testToken,
					// 7-decimal SAC at $1 → 1e29 (see feeToken comment + fee_math.rs).
					UsdPerToken: scval.U128(xdr.UInt128Parts{ // 1e29 = $1 × 10^(36-7); split into u64 limbs (Lo alone overflows u64).
						Hi: 5421010862, Lo: 7886392056514347008,
					}),
				}},
			},
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("set test token price on FeeQuoter: %w", err)
		}
		tokenFeeConfigs := make([]fqbindings.TokenFeeConfigArgs, 0, len(allSelectors))
		for _, rs := range allSelectors {
			tokenFeeConfigs = append(tokenFeeConfigs, fqbindings.TokenFeeConfigArgs{
				Token:             testToken,
				DestChainSelector: rs,
				Config: fqbindings.TokenTransferFeeConfig{
					FeeUsdCents:       25,
					DestGasOverhead:   90_000,
					DestBytesOverhead: 32,
					IsEnabled:         true,
				},
			})
		}
		if _, err := execStellarCCIPOp(b, deps, fqops.ApplyTokenFeeConfigs, fqops.ApplyTokenFeeConfigsInput{
			ContractID: feeQuoterContractID,
			AddConfigs: tokenFeeConfigs,
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("apply token fee configs on FeeQuoter: %w", err)
		}
	}

	// --- Ramps, router, registry, provisional lanes ---
	offRampWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "offramp.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	offRampOut, err := execComponentSequence(b, componentDeps, DeployOffRamp, DeployOffRampInput{
		ChainSelector:      selector,
		WasmPath:           offRampWasmPath,
		RmnProxy:           rmnProxyContractID,
		TokenAdminRegistry: tarContractID,
		ExistingAddresses:  currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy OffRamp: %w", err)
	}
	offRampContractID = offRampOut.ContractID
	if err := upsertComponentRefs(ds, offRampOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	offRampClient := offrampbindings.NewOffRampClient(h.Deployer(), offRampContractID)
	h.SetOffRamp(offRampContractID, offRampClient)
	h.Logger().Info().Str("offRampContractID", offRampContractID).Msg("OffRamp deployed and initialized")

	routerWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "router.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	routerOut, err := execComponentSequence(b, componentDeps, DeployRouter, DeployRouterInput{
		ChainSelector:     selector,
		WasmPath:          routerWasmPath,
		RmnProxy:          rmnProxyContractID,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy Router: %w", err)
	}
	routerContractID = routerOut.ContractID
	if err := upsertComponentRefs(ds, routerOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	routerClient := routerbindings.NewRouterClient(h.Deployer(), routerContractID)
	h.SetRouter(routerContractID, routerClient)
	h.Logger().Info().Str("routerContractID", routerContractID).Msg("Router deployed and initialized")

	// CommitteeVerifier RemoteChainConfig.Router must be this chain's Router contract (not the deployer);
	// router exists only after deploy + Initialize above (matches tests/integration/contract_deploy_helpers_test.go).
	routerRef := routerContractID
	remoteChainConfigs := make([]cvbindings.RemoteChainConfig, 0, len(allSelectors))
	for _, rs := range allSelectors {
		remoteChainConfigs = append(remoteChainConfigs, cvbindings.RemoteChainConfig{
			RemoteChainSelector: rs,
			FeeUsdCents:         0,
			GasForVerification:  10000,
			PayloadSizeBytes:    0,
			AllowlistEnabled:    false,
			Router:              &routerRef,
		})
	}
	if _, err := execStellarCCIPOp(b, deps, cvops.ApplyRemoteChainCfgUpdates, cvops.ApplyRemoteChainCfgUpdatesInput{
		ContractID: cvContractID,
		Configs:    remoteChainConfigs,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply remote chain config updates on committee verifier: %w", err)
	}

	// No OnRamp DestChainConfig / OffRamp SourceChainConfig wiring happens here.
	// EVM parity (and Solana parity): `DeployChainContracts` deploys and
	// initializes only — ramp lane configs are lane-configuration-time wiring,
	// applied with the remote chain's real ramp addresses resolved from the
	// datastore. Stellar's lane-time wiring has two homes: the shared CLD path
	// (StellarConfigureChainForLanes, invoked via the
	// ConfigureChainsForLanesFromTopology changeset) and the devenv path (ccvchain
	// Chain.PostConnect, ApplyDestChainConfigUpdates +
	// ApplySourceChainCfgUpdates, datastore backed). There is no valid
	// placeholder for a deploy-time provisional entry: the OffRamp rejects an
	// all-zero onramp encoding with `ZeroAddressNotAllowed` (#808, EVM
	// `OffRamp.applySourceChainConfigUpdates` parity), and a nonzero fake value
	// would silently corrupt the allowlist.

	onRampEntries := make([]routerbindings.OnRampEntry, 0, len(remoteSelectors))
	offRampEntries := make([]routerbindings.OffRampEntry, 0, len(remoteSelectors))
	for _, rs := range remoteSelectors {
		onRampEntries = append(onRampEntries, routerbindings.OnRampEntry{
			DestChainSelector: rs,
			Onramp:            onrampContractID,
		})
		offRampEntries = append(offRampEntries, routerbindings.OffRampEntry{
			SourceChainSelector: rs,
			Offramp:             offRampContractID,
		})
	}
	if _, err := execStellarCCIPOp(b, deps, routerops.ApplyRampUpdates, routerops.ApplyRampUpdatesInput{
		ContractID:     routerContractID,
		OnRampUpdates:  onRampEntries,
		OffRampRemoves: []routerbindings.OffRampEntry{},
		OffRampAdds:    offRampEntries,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply ramp updates on Router: %w", err)
	}

	rampRegistryWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccip_ramp_registry.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	rrOut, err := execComponentSequence(b, componentDeps, DeployRampRegistry, DeployRampRegistryInput{
		ChainSelector:     selector,
		WasmPath:          rampRegistryWasmPath,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy RampRegistry: %w", err)
	}
	rampRegistryContractID := rrOut.ContractID
	if err := upsertComponentRefs(ds, rrOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	rrOnRamp := make([]rampregistrybindings.OnRampUpdate, len(onRampEntries))
	for i, e := range onRampEntries {
		onramp := e.Onramp
		rrOnRamp[i] = rampregistrybindings.OnRampUpdate{
			DestChainSelector: e.DestChainSelector,
			Onramp:            &onramp,
		}
	}
	if _, err := execStellarCCIPOp(b, deps, rrops.ApplyOnrampUpdates, rrops.ApplyOnrampUpdatesInput{
		ContractID: rampRegistryContractID,
		Updates:    rrOnRamp,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply onramp updates on RampRegistry: %w", err)
	}
	rrOffRamp := make([]rampregistrybindings.OffRampUpdate, len(offRampEntries))
	for i, e := range offRampEntries {
		rrOffRamp[i] = rampregistrybindings.OffRampUpdate{
			SourceChainSelector: e.SourceChainSelector,
			Offramp:             e.Offramp,
			Enabled:             true,
		}
	}
	if _, err := execStellarCCIPOp(b, deps, rrops.ApplyOfframpUpdates, rrops.ApplyOfframpUpdatesInput{
		ContractID: rampRegistryContractID,
		Updates:    rrOffRamp,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply offramp updates on RampRegistry: %w", err)
	}
	h.SetRampRegistry(rampRegistryContractID)
	h.Logger().Info().Str("contractID", rampRegistryContractID).Msg("RampRegistry deployed and ramp maps synced with Router")

	// --- Receiver (remote-chain enablement stays here: config op) ---
	receiverWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccip_receiver_example.wasm")
	currentRefs, err = ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	recvOut, err := execComponentSequence(b, componentDeps, DeployCCIPReceiver, DeployCCIPReceiverInput{
		ChainSelector:     selector,
		WasmPath:          receiverWasmPath,
		Router:            routerContractID,
		ExistingAddresses: currentRefs,
	})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy ccip_receiver_example: %w", err)
	}
	receiverContractID := recvOut.ContractID
	if err := upsertComponentRefs(ds, recvOut.Refs); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	ownerAddr := h.DeployerKeypair().Address()
	placeholderExtra := []byte{0x01}
	for _, rs := range remoteSelectors {
		if _, err := execStellarCCIPOp(b, deps, recvops.EnableRemoteChain, recvops.EnableRemoteChainInput{
			ContractID:            receiverContractID,
			Caller:                ownerAddr,
			RemoteChainSelector:   rs,
			ExtraArgs:             placeholderExtra,
			AllowedFinalityConfig: 0,
		}); err != nil {
			return seq_core.OnChainOutput{}, fmt.Errorf("ccip_receiver_example EnableRemoteChain for source chain %d: %w", rs, err)
		}
	}
	h.SetReceiver(receiverContractID)
	h.Logger().Info().Str("receiverContractID", receiverContractID).Msg("CCIP receiver example deployed and initialized")

	addrs, err := ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	return seq_core.OnChainOutput{Addresses: addrs}, nil
}
