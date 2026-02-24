package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	ccvsbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/committee_verifier"
	rmnproxybindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_proxy"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

func TestCommitteeVerifier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _ := helpers.SetupTestEnv(ctx, t)

	// Deploy the CommitteeVerifier contract
	t.Log("Deploying CommitteeVerifier contract...")
	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "committee-verifier")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "ccvs_committee_verifier.wasm")

	contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy CommitteeVerifier: %v", err)
	}
	t.Logf("CommitteeVerifier deployed at: %v", contractID)

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

	t.Run("initialize", func(t *testing.T) {
		// Initialize RMN Proxy
		rmnProxyClient := rmnproxybindings.NewRmnProxyClient(deployer, rmnProxyContractID)
		mockRmnRemote := helpers.GenerateMockContractID(t, deployerKP.Address(), "rmn-remote")
		err := rmnProxyClient.Initialize(ctx, deployerKP.Address(), mockRmnRemote)
		if err != nil {
			t.Fatalf("Failed to initialize RMN Proxy: %v", err)
		}
		t.Log("RMN Proxy initialized successfully")

		// Initialize CommitteeVerifier
		err = client.Initialize(ctx, deployerKP.Address(), ccvsbindings.DynamicConfig{
			FeeAggregator: &mockFeeAggregator,
		}, [][]byte{}, rmnProxyContractID)
		if err != nil {
			t.Fatalf("Failed to initialize CommitteeVerifier: %v", err)
		}

		t.Log("CommitteeVerifier initialized successfully with RMN Proxy")
	})

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

}
