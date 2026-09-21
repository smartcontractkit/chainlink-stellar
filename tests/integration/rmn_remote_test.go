//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	cldfops "github.com/smartcontractkit/chainlink-deployments-framework/operations"
	cldflogger "github.com/smartcontractkit/chainlink-deployments-framework/pkg/logger"

	rmnbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/rmn_remote"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	stellarops "github.com/smartcontractkit/chainlink-stellar/deployment/operations"
	rmnremoteops "github.com/smartcontractkit/chainlink-stellar/deployment/operations/rmn_remote"
	"github.com/smartcontractkit/chainlink-stellar/deployment/operations/stellardeps"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/keypair"
)

var globalCurseSubject = [16]byte{
	0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
}

func TestRmnRemote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, _, _, _ := GetSharedTestEnv(ctx, t)

	t.Log("Deploying RmnRemote contract...")
	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "rmn-remote")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")

	contractID, err := deployer.DeployContract(ctx, wasmPath, salt)
	if err != nil {
		t.Fatalf("Failed to deploy RmnRemote: %v", err)
	}
	t.Logf("RmnRemote deployed at: %s", contractID)

	client := rmnbindings.NewRmnRemoteClient(deployer, contractID)

	t.Run("initialize", func(t *testing.T) {
		err := client.Initialize(ctx, deployerKP.Address(), nil)
		if err != nil {
			t.Fatalf("Failed to initialize RmnRemote: %v", err)
		}
		t.Log("RmnRemote initialized successfully")
	})

	t.Run("double initialize fails", func(t *testing.T) {
		err := client.Initialize(ctx, deployerKP.Address(), nil)
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

		err := client.Curse(ctx, deployerKP.Address(), [][16]byte{laneSubject})
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

	t.Run("curse already cursed subject silent skip", func(t *testing.T) {
		laneSubject := [16]byte{0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02,
			0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02, 0x02}

		err := client.Curse(ctx, deployerKP.Address(), [][16]byte{laneSubject})
		if err != nil {
			t.Fatalf("Re-curse should succeed (silent skip): %v", err)
		}
		subjects, err := client.GetCursedSubjects(ctx)
		if err != nil {
			t.Fatalf("Failed to get cursed subjects: %v", err)
		}
		if len(subjects) != 1 {
			t.Fatalf("Expected 1 cursed subject after re-curse, got %d", len(subjects))
		}
		t.Log("Re-curse on already-cursed subject is a no-op (EVM-aligned)")
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

		err := client.Curse(ctx, deployerKP.Address(), [][16]byte{s1, s2, s3})
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

		err = client.Uncurse(ctx, [][16]byte{s1, s3})
		if err != nil {
			t.Fatalf("Failed to uncurse remaining subjects: %v", err)
		}
	})

	t.Run("global curse subject", func(t *testing.T) {
		err := client.Curse(ctx, deployerKP.Address(), [][16]byte{globalCurseSubject})
		if err != nil {
			t.Fatalf("Failed to curse global subject: %v", err)
		}

		isCursed, err := client.IsCursed(ctx)
		if err != nil {
			t.Fatalf("IsCursed failed after global curse: %v", err)
		}
		if !isCursed {
			t.Fatal("IsCursed should return true after global curse")
		}
		t.Log("Global curse applied and IsCursed returned successfully")

		arbitrary := [16]byte{0xFF}
		isCursedBySubject, err := client.IsCursedBySubject(ctx, arbitrary)
		if err != nil {
			t.Fatalf("IsCursedBySubject failed after global curse: %v", err)
		}
		if !isCursedBySubject {
			t.Fatal("IsCursedBySubject should return true after global curse")
		}
		t.Log("IsCursedBySubject correctly responds after global curse")

		err = client.Uncurse(ctx, [][16]byte{globalCurseSubject})
		if err != nil {
			t.Fatalf("Failed to uncurse global subject: %v", err)
		}
		t.Log("Global curse removed")
	})

	t.Log("RmnRemote integration test passed!")
}

// TestRmnRemoteCurseAdmins exercises owner vs curse-admin authorization for curse,
// uncurse, and apply_curse_admin_updates on a fresh RMN Remote deployment.
func TestRmnRemoteCurseAdmins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, ownerDep, rpcClient, passphrase, friendbotURL := GetSharedTestEnv(ctx, t)

	b := cldfops.NewBundle(
		func() context.Context { return ctx },
		cldflogger.Test(t),
		cldfops.NewMemoryReporter(),
	)
	ownerDeps := stellardeps.FromDeployer(ownerDep)

	initialAdmin1 := keypair.MustRandom()
	initialAdmin2 := keypair.MustRandom()
	newAdmin := keypair.MustRandom()
	for _, kp := range []*keypair.Full{initialAdmin1, initialAdmin2, newAdmin} {
		if err := helpers.FundViaFriendbot(friendbotURL, kp.Address()); err != nil {
			t.Fatalf("Friendbot fund %s: %v", kp.Address(), err)
		}
	}
	admin1Deps := stellardeps.FromDeployer(deployment.NewDeployer(rpcClient, passphrase, initialAdmin1))
	admin2Deps := stellardeps.FromDeployer(deployment.NewDeployer(rpcClient, passphrase, initialAdmin2))
	newAdminDeps := stellardeps.FromDeployer(deployment.NewDeployer(rpcClient, passphrase, newAdmin))

	salt := deployment.GenerateDeterministicSalt(deployerKP.Address(), "rmn-remote-curse-admins")
	wasmPath := filepath.Join(projectRoot, "target", "wasm32v1-none", "release", "rmn_remote.wasm")

	deployReport, err := cldfops.ExecuteOperation(b, rmnremoteops.Deploy, ownerDeps, stellarops.DeployInput{
		WasmPath: wasmPath,
		Salt:     salt,
	})
	if err != nil {
		t.Fatalf("deploy RmnRemote: %v", err)
	}
	contractID := deployReport.Output.ContractID

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Initialize, ownerDeps, rmnremoteops.InitializeInput{
		ContractID:  contractID,
		Owner:       deployerKP.Address(),
		CurseAdmins: []string{initialAdmin1.Address(), initialAdmin2.Address()},
	}); err != nil {
		t.Fatalf("initialize with curse admins: %v", err)
	}

	admins := getCurseAdmins(t, b, ownerDeps, contractID)
	if !curseAdminsEqual(admins, initialAdmin1.Address(), initialAdmin2.Address()) {
		t.Fatalf("unexpected curse admins after init: %v", admins)
	}

	// get_curse_admins returns the raw stored list: explicitly adding the owner
	// as a curse admin succeeds and is then listed (nothing filters the owner out).
	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.ApplyCurseAdminUpdates, ownerDeps, rmnremoteops.ApplyCurseAdminUpdatesInput{
		ContractID:  contractID,
		AddedAdmins: []string{deployerKP.Address()},
	}); err != nil {
		t.Fatalf("apply_curse_admin_updates adding owner as admin: %v", err)
	}
	admins = getCurseAdmins(t, b, ownerDeps, contractID)
	if !curseAdminsEqual(admins, initialAdmin1.Address(), initialAdmin2.Address(), deployerKP.Address()) {
		t.Fatalf("owner must be listed after being added as a curse admin: %v", admins)
	}
	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.ApplyCurseAdminUpdates, ownerDeps, rmnremoteops.ApplyCurseAdminUpdatesInput{
		ContractID:    contractID,
		RemovedAdmins: []string{deployerKP.Address()},
	}); err != nil {
		t.Fatalf("apply_curse_admin_updates removing owner from admins: %v", err)
	}

	subjectByAdmin2 := [16]byte{0xA1}
	subjectByNewAdmin := [16]byte{0xA2}

	ownerClient := rmnbindings.NewRmnRemoteClient(ownerDep, contractID)

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Curse, admin2Deps, rmnremoteops.CurseInput{
		ContractID: contractID,
		Caller:     initialAdmin2.Address(),
		Subjects:   [][16]byte{subjectByAdmin2},
	}); err != nil {
		t.Fatalf("curse as initial curse admin (not owner): %v", err)
	}
	cursed, err := ownerClient.IsCursedBySubject(ctx, subjectByAdmin2)
	if err != nil || !cursed {
		t.Fatalf("subject should be cursed after admin curse: cursed=%v err=%v", cursed, err)
	}

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Uncurse, admin2Deps, rmnremoteops.UncurseInput{
		ContractID: contractID,
		Subjects:   [][16]byte{subjectByAdmin2},
	}); err == nil {
		t.Fatal("uncurse as curse admin should fail (owner-only)")
	}

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.ApplyCurseAdminUpdates, ownerDeps, rmnremoteops.ApplyCurseAdminUpdatesInput{
		ContractID:    contractID,
		AddedAdmins:   []string{newAdmin.Address()},
		RemovedAdmins: []string{initialAdmin1.Address()},
	}); err != nil {
		t.Fatalf("apply_curse_admin_updates: %v", err)
	}

	admins = getCurseAdmins(t, b, ownerDeps, contractID)
	if !curseAdminsEqual(admins, initialAdmin2.Address(), newAdmin.Address()) {
		t.Fatalf("unexpected curse admins after update: %v", admins)
	}
	if slices.Contains(admins, initialAdmin1.Address()) {
		t.Fatalf("removed curse admin still listed: %v", admins)
	}

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Curse, admin1Deps, rmnremoteops.CurseInput{
		ContractID: contractID,
		Caller:     initialAdmin1.Address(),
		Subjects:   [][16]byte{subjectByNewAdmin},
	}); err == nil {
		t.Fatal("curse as removed curse admin should fail")
	}

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Curse, newAdminDeps, rmnremoteops.CurseInput{
		ContractID: contractID,
		Caller:     newAdmin.Address(),
		Subjects:   [][16]byte{subjectByNewAdmin},
	}); err != nil {
		t.Fatalf("curse as new curse admin: %v", err)
	}
	cursed, err = ownerClient.IsCursedBySubject(ctx, subjectByNewAdmin)
	if err != nil || !cursed {
		t.Fatalf("subject should be cursed after new admin curse: cursed=%v err=%v", cursed, err)
	}

	if _, err := cldfops.ExecuteOperation(b, rmnremoteops.Uncurse, ownerDeps, rmnremoteops.UncurseInput{
		ContractID: contractID,
		Subjects:   [][16]byte{subjectByAdmin2, subjectByNewAdmin},
	}); err != nil {
		t.Fatalf("uncurse as owner: %v", err)
	}
	for _, subject := range [][16]byte{subjectByAdmin2, subjectByNewAdmin} {
		cursed, err := ownerClient.IsCursedBySubject(ctx, subject)
		if err != nil {
			t.Fatalf("IsCursedBySubject after owner uncurse: %v", err)
		}
		if cursed {
			t.Fatalf("subject %x should be uncursed", subject)
		}
	}

	t.Log("RmnRemote curse admin integration test passed!")
}

func getCurseAdmins(t *testing.T, b cldfops.Bundle, deps stellardeps.StellarDeps, contractID string) []string {
	t.Helper()
	// Force execution: the operations framework caches by (op, input) pair, so a
	// repeat read with the same input would otherwise return the stale cached list.
	report, err := cldfops.ExecuteOperation(b, rmnremoteops.GetCurseAdmins, deps, rmnremoteops.GetCurseAdminsInput{
		ContractID: contractID,
	}, cldfops.WithForceExecute[rmnremoteops.GetCurseAdminsInput, stellardeps.StellarDeps]())
	if err != nil {
		t.Fatalf("get curse admins: %v", err)
	}
	return report.Output.Admins
}

func curseAdminsEqual(admins []string, want ...string) bool {
	if len(admins) != len(want) {
		return false
	}
	for _, addr := range want {
		if !slices.Contains(admins, addr) {
			return false
		}
	}
	return true
}
