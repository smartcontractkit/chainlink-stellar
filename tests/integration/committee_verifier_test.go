//go:build integration

package integration

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	ccvsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	rmnremotebindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/smartcontractkit/chainlink-stellar/deployment/ccip/stellarutil"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestCommitteeVerifier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL := GetSharedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	// Deploy the CommitteeVerifier contract
	t.Log("Deploying CommitteeVerifier contract...")
	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "committee-verifier")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "ccvs_committee_verifier.wasm")

	contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy CommitteeVerifier: %v", err)
	}
	t.Logf("CommitteeVerifier deployed at: %v", contractID)

	// Deploy the RMN Remote contract (required for RMN Proxy's is_cursed delegation)
	t.Log("Deploying RMN Remote contract...")
	salt = deployment.GenerateDeterministicSalt(deployerKP.Address(), "rmn-remote-"+t.Name())
	wasmPath = filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")
	rmnRemoteContractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy RMN Remote: %v", err)
	}
	t.Logf("RMN Remote deployed at: %v", rmnRemoteContractID)

	// Deploy the RMN Proxy contract
	t.Log("Deploying RMN Proxy contract...")
	salt = deployment.GenerateDeterministicSalt(deployerKP.Address(), "rmn-proxy")
	wasmPath = filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_proxy.wasm")
	rmnProxyContractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy RMN Proxy: %v", err)
	}
	t.Logf("RMN Proxy deployed at: %v", rmnProxyContractID)

	// Create client
	client := ccvsbindings.NewCommitteeVerifierClient(deployer, contractID)

	// Generate mock addresses
	mockFeeAggregator := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-aggregator")

	err = initialize(ctx, t, deployer, deployerKP, rmnRemoteContractID, rmnProxyContractID, client, mockFeeAggregator)
	if err != nil {
		t.Fatalf("Failed to contract dependencies: %v", err)
	}

	t.Run("get version tag", func(t *testing.T) {
		tag, err := client.VersionTag(ctx)
		if err != nil {
			t.Fatalf("Failed to get version tag: %v", err)
		}
		t.Logf("Version tag retrieved successfully: %v", tag)
	})

	t.Run("set allowlist", func(t *testing.T) {
		mockedAllowlistedSender := helpers.GenerateMockContractID(t, deployerKP.Address(), "allowlisted-sender")
		err := client.ApplyAllowlistUpdates(ctx, []ccvsbindings.AllowListUpdate{
			{
				DestChainSelector:         1,
				AllowlistEnabled:          true,
				AddedAllowlistedSenders:   []string{mockedAllowlistedSender},
				RemovedAllowlistedSenders: []string{},
			},
		})

		if err != nil {
			t.Fatalf("Failed to set allowlist: %v", err)
		}
		t.Log("Allowlist set successfully")

		allowlist, err := client.GetAllowlistEntry(ctx, 1)
		if err != nil {
			t.Fatalf("Failed to get allowlist: %v", err)
		}
		t.Logf("Allowlist: %v", allowlist)
	})

	t.Run("verify owner", func(t *testing.T) {
		owner, err := client.Owner(ctx)
		if err != nil {
			t.Fatalf("Failed to get owner: %v", err)
		}
		if owner == nil || *owner != deployerKP.Address() {
			t.Errorf("Owner mismatch: expected %s, got %v", deployerKP.Address(), owner)
		}
		t.Logf("Owner verified: %v", owner)
	})

	t.Run("get and set dynamic config", func(t *testing.T) {
		cfg, err := client.GetDynamicConfig(ctx)
		if err != nil {
			t.Fatalf("Failed to get dynamic config: %v", err)
		}
		if cfg == nil {
			t.Fatal("get_dynamic_config returned nil")
		}
		if cfg.FeeAggregator == nil || *cfg.FeeAggregator != mockFeeAggregator {
			t.Errorf("FeeAggregator mismatch: expected %s, got %v", mockFeeAggregator, cfg.FeeAggregator)
		}
		t.Logf("Dynamic config verified: FeeAggregator=%v", cfg.FeeAggregator)

		// Update dynamic config
		newFeeAggregator := helpers.GenerateMockContractID(t, deployerKP.Address(), "new-fee-aggregator")
		err = client.SetDynamicConfig(ctx, ccvsbindings.DynamicConfig{
			FeeAggregator: &newFeeAggregator,
		})
		if err != nil {
			t.Fatalf("Failed to set dynamic config: %v", err)
		}

		cfg, err = client.GetDynamicConfig(ctx)
		if err != nil {
			t.Fatalf("Failed to get dynamic config after update: %v", err)
		}
		if cfg.FeeAggregator == nil || *cfg.FeeAggregator != newFeeAggregator {
			t.Errorf("FeeAggregator mismatch after update: expected %s, got %v", newFeeAggregator, cfg.FeeAggregator)
		}
		t.Logf("Dynamic config updated and verified: FeeAggregator=%v", cfg.FeeAggregator)
	})

	t.Run("remote chain config and get fee", func(t *testing.T) {
		destChainSelector := uint64(12345)
		mockRouter := helpers.GenerateMockContractID(t, deployerKP.Address(), "router")

		err := client.ApplyRemoteChainCfgUpdates(ctx, []ccvsbindings.RemoteChainConfig{
			{
				RemoteChainSelector: destChainSelector,
				Router:              &mockRouter,
				AllowlistEnabled:    false,
				FeeUsdCents:         10,
				GasForVerification:  100_000,
				PayloadSizeBytes:    256,
			},
		})
		if err != nil {
			t.Fatalf("Failed to apply remote chain config: %v", err)
		}
		t.Log("Remote chain config applied")

		remoteCfg, err := client.GetRemoteChainConfig(ctx, destChainSelector)
		if err != nil {
			t.Fatalf("Failed to get remote chain config: %v", err)
		}
		if remoteCfg.RemoteChainSelector != destChainSelector {
			t.Errorf("RemoteChainSelector mismatch: expected %d, got %d", destChainSelector, remoteCfg.RemoteChainSelector)
		}
		if remoteCfg.FeeUsdCents != 10 {
			t.Errorf("FeeUsdCents mismatch: expected 10, got %d", remoteCfg.FeeUsdCents)
		}
		t.Logf("Remote chain config verified: %+v", remoteCfg)

		// fee_token is threaded into get_fee so a verifier may reject tokens it does not accept
		// at quote-time (V3 Layer 2 capability, EVM parity). The default committee-verifier
		// ignores it, so any valid strkey suffices here.
		feeToken := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-token")
		feeResp, err := client.GetFee(ctx, destChainSelector, []byte{}, []byte{}, 0, feeToken)
		if err != nil {
			t.Fatalf("Failed to get fee: %v", err)
		}
		if feeResp.Fee != 10 {
			t.Errorf("Fee mismatch: expected 10, got %d", feeResp.Fee)
		}
		if feeResp.DestGasLimit != 100_000 {
			t.Errorf("DestGasLimit mismatch: expected 100000, got %d", feeResp.DestGasLimit)
		}
		t.Logf("Fee response: %+v", feeResp)
	})

	t.Run("storage locations", func(t *testing.T) {
		// The contract bootstraps the storage-locations admin to the initializer
		// (deployer) and the locations to the empty vec passed to initialize (see
		// the initialize helper at the bottom of this file, which passes [][]byte{}).
		// So the admin reads back as the deployer and the locations start empty.
		admin, err := client.GetStorageLocationsAdmin(ctx)
		if err != nil {
			t.Fatalf("Failed to get storage locations admin: %v", err)
		}
		if admin != deployerKP.Address() {
			t.Errorf("Storage admin mismatch: expected %s, got %s", deployerKP.Address(), admin)
		}
		t.Logf("Storage locations admin: %s", admin)

		locations, err := client.GetStorageLocations(ctx)
		if err != nil {
			t.Fatalf("Failed to get storage locations: %v", err)
		}
		if len(locations) != 0 {
			t.Errorf("Expected empty storage locations, got %d", len(locations))
		}

		// update_storage_locations requires the storage-locations admin's auth
		// (committee-verifier lib.rs update_storage_locations → admin.require_auth()).
		// The admin is the deployer, which is this transaction's source account, so
		// the auth check passes.
		newLocations := [][]byte{[]byte("location1"), []byte("location2")}
		err = client.UpdateStorageLocations(ctx, newLocations)
		if err != nil {
			t.Fatalf("Failed to update storage locations: %v", err)
		}

		locations, err = client.GetStorageLocations(ctx)
		if err != nil {
			t.Fatalf("Failed to get storage locations after update: %v", err)
		}
		if len(locations) != 2 {
			t.Errorf("Expected 2 storage locations, got %d", len(locations))
		}
		t.Logf("Storage locations updated and verified: %d locations", len(locations))
	})

	t.Run("forward to verifier", func(t *testing.T) {
		mockedAllowlistedSender := helpers.GenerateMockContractID(t, deployerKP.Address(), "allowlisted-sender")
		var messageID [32]byte
		mockFeeToken := helpers.GenerateMockContractID(t, deployerKP.Address(), "fee-token")

		verifierResults, err := client.ForwardToVerifier(ctx, 1, mockedAllowlistedSender, messageID, mockFeeToken, big.NewInt(0), []byte{})
		if err != nil {
			t.Fatalf("Failed to call forward_to_verifier: %v", err)
		}
		expectedVersionTag := stellarutil.DefaultCommitteeVerifierVersionTag()
		if len(verifierResults) < 4 {
			t.Fatalf("Verifier results too short: got %d bytes", len(verifierResults))
		}
		for i := 0; i < 4; i++ {
			if verifierResults[i] != expectedVersionTag[i] {
				t.Errorf("Version tag mismatch at byte %d: expected 0x%02x, got 0x%02x", i, expectedVersionTag[i], verifierResults[i])
			}
		}
		t.Logf("ForwardToVerifier returned version tag successfully")
	})

	t.Run("is in allowlist", func(t *testing.T) {
		mockedAllowlistedSender := helpers.GenerateMockContractID(t, deployerKP.Address(), "allowlisted-sender")
		inList, err := client.IsInAllowlist(ctx, 1, mockedAllowlistedSender)
		if err != nil {
			t.Fatalf("Failed to check allowlist: %v", err)
		}
		if !inList {
			t.Error("Expected allowlisted sender to be in allowlist")
		}

		randomAddr := helpers.GenerateMockContractID(t, deployerKP.Address(), "not-allowlisted")
		inList, err = client.IsInAllowlist(ctx, 1, randomAddr)
		if err != nil {
			t.Fatalf("Failed to check allowlist for non-allowlisted: %v", err)
		}
		if inList {
			t.Error("Expected non-allowlisted sender to not be in allowlist")
		}
		t.Log("Allowlist checks passed")
	})

	t.Run("withdraw fee tokens", func(t *testing.T) {
		// Point the fee_aggregator at a REAL account (the deployer) that holds a
		// trustline to the SAC fee token. The init-time mockFeeAggregator is a bare
		// contract strkey with no on-ledger trustline, so a SAC transfer to it reverts
		// (same caveat documented in fee_distribution_test.go's sweep subtest).
		// SetDynamicConfig is owner-only; the deployer is the contract owner.
		if err := client.SetDynamicConfig(ctx, ccvsbindings.DynamicConfig{
			FeeAggregator: &deployerAddr,
		}); err != nil {
			t.Fatalf("SetDynamicConfig(fee_aggregator=deployer): %v", err)
		}

		// Real 7-decimal SAC fee token. deployIntegrationTestSAC establishes the
		// deployer's trustline and mints 1000 tokens to the deployer.
		feeToken := deployIntegrationTestSAC(ctx, t, rpcClient, deployer, deployerAddr, networkPassphrase, friendbotURL, "cv-withdraw")

		// Fund the committee-verifier contract with a fee-token balance, simulating
		// fees accrued to it. SAC transfer(deployer -> contract) is authorized by the
		// deployer as the transaction source account.
		const accrual int64 = 100_000_000 // 10 tokens at 7 decimals
		transferArgs := []xdr.ScVal{
			scval.AddressToScVal(deployerAddr),
			scval.AddressToScVal(contractID),
			scval.I128ToScVal(big.NewInt(accrual)),
		}
		if _, err := deployer.InvokeContract(ctx, feeToken, "transfer", transferArgs); err != nil {
			t.Fatalf("fund verifier via SAC transfer: %v", err)
		}

		contractBal := sacBalanceOrFatal(ctx, t, deployer, feeToken, contractID)
		if contractBal != accrual {
			t.Fatalf("verifier fee-token balance after funding: want %d, got %d", accrual, contractBal)
		}

		aggBefore := sacBalanceOrFatal(ctx, t, deployer, feeToken, deployerAddr)

		// Permissionless sweep: withdraw_fee_tokens moves the contract's FULL balance
		// of feeToken to the fee_aggregator (deployer). No require_auth on the caller —
		// the contract authorizes its own outgoing transfer as the current contract.
		if err := client.WithdrawFeeTokens(ctx, []string{feeToken}); err != nil {
			t.Fatalf("WithdrawFeeTokens: %v", err)
		}

		contractAfter := sacBalanceOrFatal(ctx, t, deployer, feeToken, contractID)
		if contractAfter != 0 {
			t.Fatalf("verifier fee-token balance must be 0 after sweep, got %d (accrual was %d)", contractAfter, accrual)
		}
		aggAfter := sacBalanceOrFatal(ctx, t, deployer, feeToken, deployerAddr)
		if got := aggAfter - aggBefore; got != accrual {
			t.Fatalf("fee_aggregator delta: want %d, got %d (before=%d after=%d)", accrual, got, aggBefore, aggAfter)
		}
		t.Logf("sweep moved %d fee-token units from the verifier to the fee_aggregator", accrual)
	})

	t.Log("CommitteeVerifier integration test passed!")
}

// Helper function to initialize the CommitteeVerifier contract
func initialize(ctx context.Context, t *testing.T, deployer *deployment.Deployer, deployerKP *keypair.Full, rmnRemoteContractID string, rmnProxyContractID string, client *ccvsbindings.CommitteeVerifierClient, mockFeeAggregator string) error {
	// Initialize RMN Remote first (RMN Proxy delegates is_cursed to it)
	rmnRemoteClient := rmnremotebindings.NewRmnRemoteClient(deployer, rmnRemoteContractID)
	err := rmnRemoteClient.Initialize(ctx, deployerKP.Address(), nil)
	if err != nil {
		t.Fatalf("Failed to initialize RMN Remote: %v", err)
		return err
	}
	t.Log("RMN Remote initialized successfully")

	// Initialize RMN Proxy with the deployed RMN Remote (not a mock)
	rmnProxyClient := rmnproxybindings.NewRmnProxyClient(deployer, rmnProxyContractID)
	err = rmnProxyClient.Initialize(ctx, deployerKP.Address(), rmnRemoteContractID)
	if err != nil {
		t.Fatalf("Failed to initialize RMN Proxy: %v", err)
		return err
	}
	t.Log("RMN Proxy initialized successfully")

	// Initialize CommitteeVerifier
	err = client.Initialize(ctx, deployerKP.Address(), ccvsbindings.DynamicConfig{
		FeeAggregator: &mockFeeAggregator,
	}, [][]byte{}, rmnProxyContractID, stellarutil.DefaultCommitteeVerifierVersionTag())
	if err != nil {
		t.Fatalf("Failed to initialize CommitteeVerifier: %v", err)
		return err
	}

	t.Log("CommitteeVerifier initialized successfully with RMN Proxy")

	return nil
}
