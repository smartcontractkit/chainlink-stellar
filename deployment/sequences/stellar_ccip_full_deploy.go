package sequences

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Masterminds/semver/v3"
	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v1_2_0/operations/router"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/committee_verifier"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/executor"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/fee_quoter"
	offrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/offramp"
	onrampoperations "github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/onramp"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/operations/proxy"
	"github.com/smartcontractkit/chainlink-ccip/chains/evm/deployment/v2_0_0/versioned_verifier_resolver"
	seq_core "github.com/smartcontractkit/chainlink-ccip/deployment/utils/sequences"
	"github.com/smartcontractkit/chainlink-ccip/deployment/v2_0_0/offchain"
	devenvcommon "github.com/smartcontractkit/chainlink-ccv/build/devenv/common"
	"github.com/smartcontractkit/chainlink-deployments-framework/datastore"
	cldf_ops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cvbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	offrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/offramp"
	onrampbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/onramp"
	rampregistrybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/ramp_registry"
	routerbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/router"
	tarbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/token_admin_registry"
	vvrbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/versioned_verifier_resolver"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	stellardeployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarccip "github.com/smartcontractkit/chainlink-stellar/deployment/ccip"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	recvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ccip_receiver"
	cvops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/committee_verifier"
	fqops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/fee_quoter"
	offrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/offramp"
	onrampops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/onramp"
	rrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/ramp_registry"
	rmnproxyops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_proxy"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	routerops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/router"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	tarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/token_admin_registry"
	vvrops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/versioned_verifier_resolver"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func execStellarCCIPOp[IN, OUT any](
	b cldf_ops.Bundle,
	deps stellardeps.StellarDeps,
	op *cldf_ops.Operation[IN, OUT, stellardeps.StellarDeps],
	in IN,
) (OUT, error) {
	rep, err := cldf_ops.ExecuteOperation(b, op, deps, in)
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
// → ramps → receiver + cross-family datastore refs).
//
// topology must be non-nil with a non-nil NOP graph: committee verifier signature quorum configuration
// (ApplySignatureConfigs) is derived from offchain NOP/committee data. CCV supplies this via
// RegisterStellarDeployOffchainTopologyForSelector → Take in [StellarDeployChainContracts], or
// [RunStellarCCIPFullDeployForCCV] which converts CCV topology before calling this function.
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
	if topology == nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("RunStellarCCIPFullDeploy: offchain EnvironmentTopology is nil (use CCV pre-deploy stash + StellarDeployChainContracts or RunStellarCCIPFullDeployForCCV with a non-nil CCV topology)")
	}
	// topology is retained for the stash contract and future deploy-time needs; committee
	// signer quorums are applied later, at lane-configuration time (see the comment below).

	ds := datastore.NewMemoryDataStore()
	if err := stellarccip.MergeExistingAddressRefs(ds, in.ExistingAddresses); err != nil {
		return seq_core.OnChainOutput{}, err
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

	// --- Foundation (OnRamp deploy first; initialize after TAR + FeeQuoter) ---
	onrampWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "onramp.wasm")
	if err := statReleaseWasm(onrampWasmPath, "OnRamp"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", onrampWasmPath).Msg("Deploying OnRamp contract...")
	onrampSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "onramp")
	onrampOut, err := execStellarCCIPOp(b, deps, onrampops.Deploy, stellarops.DeployInput{WasmPath: onrampWasmPath, Salt: onrampSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy OnRamp: %w", err)
	}
	onrampContractID = onrampOut.ContractID
	if err := stellarccip.RecordOnRamp(ds, selector, onrampContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	onRampClient := onrampbindings.NewOnRampClient(h.Deployer(), onrampContractID)
	h.SetOnRamp(onrampContractID, onRampClient)
	h.Logger().Info().Str("contractID", onrampContractID).Msg("OnRamp contract deployed")

	rmnRemoteWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	if err := statReleaseWasm(rmnRemoteWasmPath, "RMN Remote"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", rmnRemoteWasmPath).Msg("Deploying RMN Remote contract...")
	rmnRemoteSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "rmn-remote")
	rmnRemoteOut, err := execStellarCCIPOp(b, deps, rmnremoteops.Deploy, stellarops.DeployInput{WasmPath: rmnRemoteWasmPath, Salt: rmnRemoteSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy RMN Remote: %w", err)
	}
	rmnRemoteContractID = rmnRemoteOut.ContractID
	if err := stellarccip.RecordRMNRemote(ds, selector, rmnRemoteContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	if _, err := execStellarCCIPOp(b, deps, rmnremoteops.Initialize, rmnremoteops.InitializeInput{
		ContractID:  rmnRemoteContractID,
		Owner:       h.DeployerKeypair().Address(),
		CurseAdmins: nil,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize RMN Remote: %w", err)
	}
	h.Logger().Info().Str("rmnRemoteContractID", rmnRemoteContractID).Msg("RMN Remote initialized")

	rmnProxyWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "rmn_proxy.wasm")
	if err := statReleaseWasm(rmnProxyWasmPath, "RMN Proxy"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", rmnProxyWasmPath).Msg("Deploying RMN Proxy contract...")
	rmnProxySalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "rmn-proxy")
	rmnProxyOut, err := execStellarCCIPOp(b, deps, rmnproxyops.Deploy, stellarops.DeployInput{WasmPath: rmnProxyWasmPath, Salt: rmnProxySalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy RMN Proxy: %w", err)
	}
	rmnProxyContractID = rmnProxyOut.ContractID
	if _, err := execStellarCCIPOp(b, deps, rmnproxyops.Initialize, rmnproxyops.InitializeInput{
		ContractID: rmnProxyContractID,
		Owner:      h.DeployerKeypair().Address(),
		RmnRemote:  rmnRemoteContractID,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize RMN Proxy: %w", err)
	}
	h.Logger().Info().Str("rmnProxyContractID", rmnProxyContractID).Msg("RMN Proxy initialized")

	feeQuoterWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "fee_quoter.wasm")
	if err := statReleaseWasm(feeQuoterWasmPath, "FeeQuoter"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", feeQuoterWasmPath).Msg("Deploying FeeQuoter contract...")
	feeQuoterSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "fee-quoter")
	feeQuoterOut, err := execStellarCCIPOp(b, deps, fqops.Deploy, stellarops.DeployInput{WasmPath: feeQuoterWasmPath, Salt: feeQuoterSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy FeeQuoter: %w", err)
	}
	feeQuoterContractID = feeQuoterOut.ContractID
	if err := stellarccip.RecordFeeQuoter(ds, selector, feeQuoterContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
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

	feeQuoterClient := fqbindings.NewFeeQuoterClient(h.Deployer(), feeQuoterContractID)
	h.SetFeeQuoter(feeQuoterClient)
	if _, err := execStellarCCIPOp(b, deps, fqops.Initialize, fqops.InitializeInput{
		ContractID: feeQuoterContractID,
		Owner:      h.DeployerKeypair().Address(),
		StaticConfig: fqbindings.StaticConfig{
			LinkToken:         feeTokenContractID,
			MaxFeeJuelsPerMsg: 1_000_000_000_000_000_000,
		},
		AuthorizedCallers: []string{h.DeployerKeypair().Address()},
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize FeeQuoter: %w", err)
	}
	h.Logger().Info().Str("feeQuoterContractID", feeQuoterContractID).Msg("FeeQuoter initialized")

	tarWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "token_admin_registry.wasm")
	if err := statReleaseWasm(tarWasmPath, "TokenAdminRegistry"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", tarWasmPath).Msg("Deploying TokenAdminRegistry contract...")
	tarSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "token-admin-registry")
	tarOut, err := execStellarCCIPOp(b, deps, tarops.Deploy, stellarops.DeployInput{WasmPath: tarWasmPath, Salt: tarSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy TokenAdminRegistry: %w", err)
	}
	tarContractID = tarOut.ContractID
	if err := stellarccip.RecordTokenAdminRegistry(ds, selector, tarContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	tarClient := tarbindings.NewTokenAdminRegistryClient(h.Deployer(), tarContractID)
	h.SetTokenAdminRegistry(tarContractID, tarClient)
	if _, err := execStellarCCIPOp(b, deps, tarops.Initialize, tarops.InitializeInput{
		ContractID: tarContractID,
		Owner:      h.DeployerKeypair().Address(),
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize TokenAdminRegistry: %w", err)
	}
	h.Logger().Info().Str("contractID", tarContractID).Msg("TokenAdminRegistry deployed and initialized")

	// Fee aggregator receivable account: devenv uses the deployer account (real on-chain identity, not a synthetic C… mock).
	feeAggregatorAddr := h.DeployerKeypair().Address()
	if _, err := execStellarCCIPOp(b, deps, onrampops.Initialize, onrampops.InitializeInput{
		ContractID: onrampContractID,
		Owner:      h.DeployerKeypair().Address(),
		StaticConfig: onrampbindings.StaticConfig{
			ChainSelector:         selector,
			TokenAdminRegistry:    tarContractID,
			RmnProxy:              rmnProxyContractID,
			MaxUsdCentsPerMessage: 10000,
		},
		DynamicConfig: onrampbindings.DynamicConfig{
			FeeQuoter:     feeQuoterContractID,
			FeeAggregator: feeAggregatorAddr,
		},
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize OnRamp: %w", err)
	}
	h.Logger().Info().Str("onRampContractID", onrampContractID).Msg("OnRamp client initialized")

	// --- Verification + FeeQuoter config ---
	vvrWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccvs_versioned_verifier_resolver.wasm")
	if err := statReleaseWasm(vvrWasmPath, "VVR"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", vvrWasmPath).Msg("Deploying Versioned Verifier Resolver contract...")
	vvrSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "versioned-verifier-resolver")
	vvrOut, err := execStellarCCIPOp(b, deps, vvrops.Deploy, stellarops.DeployInput{WasmPath: vvrWasmPath, Salt: vvrSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy VVR: %w", err)
	}
	vvrContractID = vvrOut.ContractID
	if err := stellarccip.RecordVVR(ds, selector, vvrContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("contractID", vvrContractID).Msg("VVR contract deployed")
	h.SetVVR(vvrContractID)

	if _, err := execStellarCCIPOp(b, deps, vvrops.Initialize, vvrops.InitializeInput{
		ContractID:    vvrContractID,
		Owner:         h.DeployerKeypair().Address(),
		FeeAggregator: feeAggregatorAddr,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize VVR: %w", err)
	}
	h.Logger().Info().Str("vvrContractID", vvrContractID).Msg("VVR client initialized")

	cvWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccvs_committee_verifier.wasm")
	if err := statReleaseWasm(cvWasmPath, "Committee Verifier"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", cvWasmPath).Msg("Deploying Committee Verifier contract...")
	cvSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "committee-verifier")
	cvOut, err := execStellarCCIPOp(b, deps, cvops.Deploy, stellarops.DeployInput{WasmPath: cvWasmPath, Salt: cvSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy Committee Verifier: %w", err)
	}
	cvContractID = cvOut.ContractID
	if err := stellarccip.RecordCommitteeVerifier(ds, selector, cvContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("contractID", cvContractID).Msg("Committee Verifier contract deployed")

	allowlistAdmin := h.DeployerKeypair().Address()
	cvFeeAgg := feeAggregatorAddr
	mockStorageLocation := stellarutil.GenerateContractAddress("storage-location", h.NetworkPassphrase())
	if _, err := execStellarCCIPOp(b, deps, cvops.Initialize, cvops.InitializeInput{
		ContractID: cvContractID,
		Owner:      h.DeployerKeypair().Address(),
		DynamicConfig: cvbindings.DynamicConfig{
			AllowlistAdmin: &allowlistAdmin,
			FeeAggregator:  &cvFeeAgg,
		},
		StorageLocations: [][]byte{mockStorageLocation},
		RmnProxy:         rmnProxyContractID,
		VersionTag:       stellarutil.DefaultCommitteeVerifierVersionTag(),
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize Committee Verifier: %w", err)
	}
	h.SetCV(cvContractID)
	h.Logger().Info().Str("cvContractID", cvContractID).Msg("Committee Verifier client initialized")

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
					Token:       feeTokenContractID,
					UsdPerToken: scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 1_000_000_000_000_000_000}),
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
					Token:       testToken,
					UsdPerToken: scval.U128(xdr.UInt128Parts{Hi: 0, Lo: 1_000_000_000_000_000_000}),
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
	if err := statReleaseWasm(offRampWasmPath, "OffRamp"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", offRampWasmPath).Msg("Deploying OffRamp contract...")
	offRampSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "offramp")
	offRampOut, err := execStellarCCIPOp(b, deps, offrampops.Deploy, stellarops.DeployInput{WasmPath: offRampWasmPath, Salt: offRampSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy OffRamp: %w", err)
	}
	offRampContractID = offRampOut.ContractID
	if err := stellarccip.RecordOffRamp(ds, selector, offRampContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	offRampClient := offrampbindings.NewOffRampClient(h.Deployer(), offRampContractID)
	h.SetOffRamp(offRampContractID, offRampClient)

	if _, err := execStellarCCIPOp(b, deps, offrampops.Initialize, offrampops.InitializeInput{
		ContractID: offRampContractID,
		Owner:      h.DeployerKeypair().Address(),
		Config: offrampbindings.StaticConfig{
			ChainSelector:      selector,
			RmnProxy:           rmnProxyContractID,
			TokenAdminRegistry: tarContractID,
		},
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize OffRamp: %w", err)
	}
	h.Logger().Info().Str("offRampContractID", offRampContractID).Msg("OffRamp initialized")

	routerWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "router.wasm")
	if err := statReleaseWasm(routerWasmPath, "Router"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", routerWasmPath).Msg("Deploying Router contract...")
	routerSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "router")
	routerOut, err := execStellarCCIPOp(b, deps, routerops.Deploy, stellarops.DeployInput{WasmPath: routerWasmPath, Salt: routerSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy Router: %w", err)
	}
	routerContractID = routerOut.ContractID
	if err := stellarccip.RecordRouter(ds, selector, routerContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	routerClient := routerbindings.NewRouterClient(h.Deployer(), routerContractID)
	if _, err := execStellarCCIPOp(b, deps, routerops.Initialize, routerops.InitializeInput{
		ContractID: routerContractID,
		Owner:      h.DeployerKeypair().Address(),
		RmnProxy:   rmnProxyContractID,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize Router: %w", err)
	}
	h.SetRouter(routerContractID, routerClient)
	h.Logger().Info().Str("routerContractID", routerContractID).Msg("Router initialized")

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

	contractHexAddr := func(name string) string {
		return hexutil.Encode(stellarutil.GenerateContractAddress(name, h.NetworkPassphrase()))
	}
	executorProxyHex := contractHexAddr("stellar-executor-proxy")
	executorContractID, err := scval.HexToContractStrkey(executorProxyHex)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert executor proxy placeholder address: %w", err)
	}
	onRampDestConfigs, err := stellarccip.BuildOnRampDestConfigs(ds.Seal(), remoteSelectors, executorContractID, false, vvrContractID, routerContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("build provisional onramp dest configs: %w", err)
	}
	if _, err := execStellarCCIPOp(b, deps, onrampops.ApplyDestChainConfigUpdates, onrampops.ApplyDestChainConfigUpdatesInput{
		ContractID: onrampContractID,
		Updates:    onRampDestConfigs,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply dest chain config updates on OnRamp: %w", err)
	}

	offRampSourceConfigs, err := stellarccip.BuildOffRampSourceConfigs(ds.Seal(), remoteSelectors, false, vvrContractID, routerContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("build provisional offramp source configs: %w", err)
	}
	if _, err := execStellarCCIPOp(b, deps, offrampops.ApplySourceChainCfgUpdates, offrampops.ApplySourceChainCfgUpdatesInput{
		ContractID: offRampContractID,
		Updates:    offRampSourceConfigs,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("apply source chain config updates on OffRamp: %w", err)
	}

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
	if err := statReleaseWasm(rampRegistryWasmPath, "RampRegistry"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", rampRegistryWasmPath).Msg("Deploying RampRegistry contract...")
	rampRegistrySalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "ramp-registry")
	rrOut, err := execStellarCCIPOp(b, deps, rrops.Deploy, stellarops.DeployInput{WasmPath: rampRegistryWasmPath, Salt: rampRegistrySalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy RampRegistry: %w", err)
	}
	rampRegistryContractID := rrOut.ContractID
	if err := stellarccip.RecordRampRegistry(ds, selector, rampRegistryContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	if _, err := execStellarCCIPOp(b, deps, rrops.Initialize, rrops.InitializeInput{
		ContractID: rampRegistryContractID,
		Owner:      h.DeployerKeypair().Address(),
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize RampRegistry: %w", err)
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

	// --- Receiver + EVM-typed datastore refs for CCIP v2 tooling ---
	receiverWasmPath := filepath.Join(stellarRoot, "target", "wasm32v1-none", "release", "ccip_receiver_example.wasm")
	if err := statReleaseWasm(receiverWasmPath, "ccip_receiver_example"); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	h.Logger().Info().Str("wasmPath", receiverWasmPath).Msg("Deploying CCIP receiver example contract...")
	receiverSalt := stellardeployment.GenerateDeterministicSalt(h.DeployerKeypair().Address(), "ccip-receiver-example")
	recvOut, err := execStellarCCIPOp(b, deps, recvops.Deploy, stellarops.DeployInput{WasmPath: receiverWasmPath, Salt: receiverSalt})
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("deploy ccip_receiver_example: %w", err)
	}
	receiverContractID := recvOut.ContractID
	if err := stellarccip.RecordCCIPReceiver(ds, selector, receiverContractID); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	if _, err := execStellarCCIPOp(b, deps, recvops.Initialize, recvops.InitializeInput{
		ContractID: receiverContractID,
		Owner:      h.DeployerKeypair().Address(),
		Router:     routerContractID,
	}); err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("initialize ccip_receiver_example: %w", err)
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

	receiverHex, err := stellarutil.StrkeyToHex(receiverContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert receiver address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       receiverHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(stellarccip.CcipReceiverContractType),
		Version:       stellarops.ContractDeploymentVersion,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	onrampHex, err := stellarutil.StrkeyToHex(onrampContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert OnRamp address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       onrampHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(onrampoperations.ContractType),
		Version:       semver.MustParse(onrampoperations.Deploy.Version()),
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	offRampHex, err := stellarutil.StrkeyToHex(offRampContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert OffRamp address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       offRampHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(offrampoperations.ContractType),
		Version:       semver.MustParse(offrampoperations.Deploy.Version()),
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	routerHex, err := stellarutil.StrkeyToHex(routerContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert Router address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       routerHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(router.ContractType),
		Version:       semver.MustParse(router.Deploy.Version()),
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	tarHex, err := stellarutil.StrkeyToHex(tarContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert TokenAdminRegistry address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       tarHex,
		ChainSelector: selector,
		Type:          datastore.ContractType(stellarccip.TokenAdminRegistryContractType),
		Version:       stellarops.ContractDeploymentVersion,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	vvrHex, err := stellarutil.StrkeyToHex(vvrContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert VVR address: %w", err)
	}
	for _, qualifier := range []string{devenvcommon.DefaultCommitteeVerifierQualifier} {
		if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
			Address:       vvrHex,
			Type:          datastore.ContractType(versioned_verifier_resolver.CommitteeVerifierResolverType),
			Version:       versioned_verifier_resolver.Version,
			Qualifier:     qualifier,
			ChainSelector: selector,
		}); err != nil {
			return seq_core.OnChainOutput{}, err
		}
	}

	cvHex, err := stellarutil.StrkeyToHex(cvContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert Committee Verifier address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       cvHex,
		Type:          datastore.ContractType(committee_verifier.ContractType),
		Version:       committee_verifier.Version,
		Qualifier:     devenvcommon.DefaultCommitteeVerifierQualifier,
		ChainSelector: selector,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       contractHexAddr("stellar-executor"),
		Type:          datastore.ContractType(executor.ContractType),
		Version:       executor.Version,
		Qualifier:     devenvcommon.DefaultExecutorQualifier,
		ChainSelector: selector,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       contractHexAddr("stellar-executor-proxy"),
		Type:          datastore.ContractType(proxy.ContractType),
		Version:       proxy.Version,
		Qualifier:     devenvcommon.DefaultExecutorQualifier,
		ChainSelector: selector,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	rmnRemoteHex, err := stellarutil.StrkeyToHex(rmnRemoteContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert RMN Remote address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       rmnRemoteHex,
		Type:          datastore.ContractType(stellarccip.RMNRemoteContractType),
		Version:       semver.MustParse(stellarccip.RMNRemoteContractVersion),
		ChainSelector: selector,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	feeQuoterHex, err := stellarutil.StrkeyToHex(feeQuoterContractID)
	if err != nil {
		return seq_core.OnChainOutput{}, fmt.Errorf("convert FeeQuoter address: %w", err)
	}
	if err := ds.AddressRefStore.Upsert(datastore.AddressRef{
		Address:       feeQuoterHex,
		Type:          datastore.ContractType(fee_quoter.ContractType),
		Version:       semver.MustParse(fee_quoter.Deploy.Version()),
		ChainSelector: selector,
	}); err != nil {
		return seq_core.OnChainOutput{}, err
	}

	addrs, err := ds.AddressRefStore.Fetch()
	if err != nil {
		return seq_core.OnChainOutput{}, err
	}
	return seq_core.OnChainOutput{Addresses: addrs}, nil
}
