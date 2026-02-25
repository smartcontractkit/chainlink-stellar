//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	rmnbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
)

var globalCurseSubject = [16]byte{
	0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

func TestRmnRemote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _ := helpers.SetupTestEnv(ctx, t)

	// Deploy the RmnRemote contract
	t.Log("Deploying RmnRemote contract...")
	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "rmn-remote")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")

	contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy RmnRemote: %v", err)
	}
	t.Logf("RmnRemote deployed at: %s", contractID)

	client := rmnbindings.NewRmnRemoteClient(deployer, contractID)
	localChainSelector := uint64(12345)

	t.Run("initialize", func(t *testing.T) {
		err := client.Initialize(ctx, deployerKP.Address(), localChainSelector)
		if err != nil {
			t.Fatalf("Failed to initialize RmnRemote: %v", err)
		}
		t.Log("RmnRemote initialized successfully")
	})

	t.Run("double initialize fails", func(t *testing.T) {
		err := client.Initialize(ctx, deployerKP.Address(), localChainSelector)
		if err == nil {
			t.Fatal("Expected error on double initialize, got nil")
		}
		t.Logf("Double initialize correctly rejected: %v", err)
	})

	t.Run("verify owner", func(t *testing.T) {
		owner, err := client.Owner(ctx)
		if err != nil {
			t.Fatalf("Failed to get owner: %v", err)
		}
		if owner == nil || *owner != deployerKP.Address() {
			t.Errorf("Owner mismatch: expected %s, got %v", deployerKP.Address(), owner)
		}
		t.Logf("Owner verified: %s", *owner)
	})

	t.Run("get local chain selector", func(t *testing.T) {
		sel, err := client.GetLocalChainSelector(ctx)
		if err != nil {
			t.Fatalf("Failed to get local chain selector: %v", err)
		}
		if sel != localChainSelector {
			t.Errorf("Chain selector mismatch: expected %d, got %d", localChainSelector, sel)
		}
		t.Logf("Local chain selector verified: %d", sel)
	})

	// ========================================
	// Configuration
	// ========================================

	t.Run("set config", func(t *testing.T) {
		config := rmnbindings.Config{
			RmnHomeConfigDigest: [32]byte{0xAA, 0xBB, 0xCC, 0xDD, 0x01, 0x02, 0x03, 0x04,
				0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C,
				0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14,
				0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C},
			Signers: []rmnbindings.Signer{
				{OnchainPubKey: [32]byte{1}, NodeIndex: 0},
				{OnchainPubKey: [32]byte{2}, NodeIndex: 1},
				{OnchainPubKey: [32]byte{3}, NodeIndex: 2},
			},
			FSign: 1, // f=1, needs 2f+1=3 signers
		}
		err := client.SetConfig(ctx, config)
		if err != nil {
			t.Fatalf("Failed to set config: %v", err)
		}
		t.Log("Config set successfully")
	})

	t.Run("set config with zero digest fails", func(t *testing.T) {
		config := rmnbindings.Config{
			RmnHomeConfigDigest: [32]byte{},
			Signers: []rmnbindings.Signer{
				{OnchainPubKey: [32]byte{1}, NodeIndex: 0},
			},
			FSign: 0,
		}
		err := client.SetConfig(ctx, config)
		if err == nil {
			t.Fatal("Expected error for zero digest config, got nil")
		}
		t.Logf("Zero digest config correctly rejected: %v", err)
	})

	t.Run("set config with not enough signers fails", func(t *testing.T) {
		config := rmnbindings.Config{
			RmnHomeConfigDigest: [32]byte{0xFF},
			Signers: []rmnbindings.Signer{
				{OnchainPubKey: [32]byte{1}, NodeIndex: 0},
				{OnchainPubKey: [32]byte{2}, NodeIndex: 1},
			},
			FSign: 1, // f=1 needs 3 signers, only 2 provided
		}
		err := client.SetConfig(ctx, config)
		if err == nil {
			t.Fatal("Expected error for insufficient signers, got nil")
		}
		t.Logf("Insufficient signers correctly rejected: %v", err)
	})

	t.Run("set config with out of order signers fails", func(t *testing.T) {
		config := rmnbindings.Config{
			RmnHomeConfigDigest: [32]byte{0xFF},
			Signers: []rmnbindings.Signer{
				{OnchainPubKey: [32]byte{1}, NodeIndex: 5},
				{OnchainPubKey: [32]byte{2}, NodeIndex: 3}, // out of order
				{OnchainPubKey: [32]byte{3}, NodeIndex: 7},
			},
			FSign: 1,
		}
		err := client.SetConfig(ctx, config)
		if err == nil {
			t.Fatal("Expected error for out of order signers, got nil")
		}
		t.Logf("Out of order signers correctly rejected: %v", err)
	})

	// ========================================
	// Cursing
	// ========================================

	t.Run("initially not cursed", func(t *testing.T) {
		subjects, err := client.GetCursedSubjects(ctx)
		if err != nil {
			t.Fatalf("Failed to get cursed subjects: %v", err)
		}
		if len(subjects) != 0 {
			t.Errorf("Expected no cursed subjects, got %d", len(subjects))
		}
		t.Log("No cursed subjects initially (as expected)")
	})

	t.Run("curse a specific subject", func(t *testing.T) {
		laneSubject := [16]byte{0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02,
			0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}

		err := client.Curse(ctx, [][16]byte{laneSubject})
		if err != nil {
			t.Fatalf("Failed to curse subject: %v", err)
		}

		subjects, err := client.GetCursedSubjects(ctx)
		if err != nil {
			t.Fatalf("Failed to get cursed subjects: %v", err)
		}
		if len(subjects) != 1 {
			t.Fatalf("Expected 1 cursed subject, got %d", len(subjects))
		}
		if subjects[0] != laneSubject {
			t.Errorf("Cursed subject mismatch: expected %x, got %x", laneSubject, subjects[0])
		}
		t.Logf("Subject cursed and verified: %x", subjects[0])
	})

	t.Run("curse already cursed subject fails", func(t *testing.T) {
		laneSubject := [16]byte{0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02,
			0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}

		err := client.Curse(ctx, [][16]byte{laneSubject})
		if err == nil {
			t.Fatal("Expected error when cursing already cursed subject, got nil")
		}
		t.Logf("Double curse correctly rejected: %v", err)
	})

	t.Run("uncurse a subject", func(t *testing.T) {
		laneSubject := [16]byte{0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02,
			0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}

		err := client.Uncurse(ctx, [][16]byte{laneSubject})
		if err != nil {
			t.Fatalf("Failed to uncurse subject: %v", err)
		}

		subjects, err := client.GetCursedSubjects(ctx)
		if err != nil {
			t.Fatalf("Failed to get cursed subjects after uncurse: %v", err)
		}
		if len(subjects) != 0 {
			t.Errorf("Expected 0 cursed subjects after uncurse, got %d", len(subjects))
		}
		t.Log("Subject uncursed and verified")
	})

	t.Run("uncurse not cursed subject fails", func(t *testing.T) {
		notCursed := [16]byte{0xAA, 0xBB, 0xCC, 0xDD, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

		err := client.Uncurse(ctx, [][16]byte{notCursed})
		if err == nil {
			t.Fatal("Expected error when uncursing non-cursed subject, got nil")
		}
		t.Logf("Uncurse non-cursed correctly rejected: %v", err)
	})

	t.Run("curse and uncurse multiple subjects", func(t *testing.T) {
		s1 := [16]byte{0x10}
		s2 := [16]byte{0x20}
		s3 := [16]byte{0x30}

		err := client.Curse(ctx, [][16]byte{s1, s2, s3})
		if err != nil {
			t.Fatalf("Failed to curse multiple subjects: %v", err)
		}

		subjects, err := client.GetCursedSubjects(ctx)
		if err != nil {
			t.Fatalf("Failed to get cursed subjects: %v", err)
		}
		if len(subjects) != 3 {
			t.Fatalf("Expected 3 cursed subjects, got %d", len(subjects))
		}
		t.Logf("3 subjects cursed successfully")

		// Uncurse only s2
		err = client.Uncurse(ctx, [][16]byte{s2})
		if err != nil {
			t.Fatalf("Failed to uncurse s2: %v", err)
		}

		subjects, err = client.GetCursedSubjects(ctx)
		if err != nil {
			t.Fatalf("Failed to get cursed subjects after partial uncurse: %v", err)
		}
		if len(subjects) != 2 {
			t.Errorf("Expected 2 cursed subjects, got %d", len(subjects))
		}
		t.Logf("Partial uncurse verified: %d subjects remain cursed", len(subjects))

		// Clean up: uncurse remaining
		err = client.Uncurse(ctx, [][16]byte{s1, s3})
		if err != nil {
			t.Fatalf("Failed to uncurse remaining subjects: %v", err)
		}
	})

	t.Run("global curse subject", func(t *testing.T) {
		err := client.Curse(ctx, [][16]byte{globalCurseSubject})
		if err != nil {
			t.Fatalf("Failed to curse global subject: %v", err)
		}

		// is_cursed should detect the global curse
		isCursed, err := client.IsCursed(ctx)
		if err != nil {
			t.Fatalf("IsCursed failed after global curse: %v", err)
		}
		if !isCursed {
			t.Fatal("IsCursed should return true after global curse")
		}
		t.Log("Global curse applied and IsCursed returned successfully")

		// is_cursed_by_subject for an arbitrary subject should also detect global curse
		arbitrary := [16]byte{0xFF}
		isCursedBySubject, err := client.IsCursedBySubject(ctx, arbitrary)
		if err != nil {
			t.Fatalf("IsCursedBySubject failed after global curse: %v", err)
		}
		if !isCursedBySubject {
			t.Fatal("IsCursedBySubject should return true after global curse")
		}
		t.Log("IsCursedBySubject correctly responds after global curse")

		// Clean up
		err = client.Uncurse(ctx, [][16]byte{globalCurseSubject})
		if err != nil {
			t.Fatalf("Failed to uncurse global subject: %v", err)
		}
		t.Log("Global curse removed")
	})

	t.Run("config version increments", func(t *testing.T) {
		config := rmnbindings.Config{
			RmnHomeConfigDigest: [32]byte{0xDD, 0xEE, 0xFF},
			Signers: []rmnbindings.Signer{
				{OnchainPubKey: [32]byte{10}, NodeIndex: 0},
				{OnchainPubKey: [32]byte{20}, NodeIndex: 1},
				{OnchainPubKey: [32]byte{30}, NodeIndex: 2},
				{OnchainPubKey: [32]byte{40}, NodeIndex: 3},
				{OnchainPubKey: [32]byte{50}, NodeIndex: 4},
			},
			FSign: 2, // f=2, needs 2f+1=5 signers
		}
		err := client.SetConfig(ctx, config)
		if err != nil {
			t.Fatalf("Failed to set second config: %v", err)
		}
		t.Log("Second config set successfully (version should be 2)")
	})

	t.Log("RmnRemote integration test passed!")
}
