//go:build integration

package integration

import (
	"context"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
)

const sharedContainerName = "blockchain-stellar-integration-shared"

var (
	sharedEnv     *helpers.SharedTestEnv
	sharedEnvOnce sync.Once
	sharedEnvErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()

	// Teardown: stop the Stellar container after all tests finish
	if sharedEnv != nil && sharedEnv.Output != nil && sharedEnv.Output.Container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sharedEnv.Output.Container.Terminate(ctx); err != nil {
			log.Printf("Warning: failed to terminate Stellar container: %v", err)
		}
	}

	os.Exit(code)
}

// GetSharedTestEnv returns the shared test environment (Stellar node, deployer, etc.)
// used across all integration tests. Setup runs once on first use; teardown runs after all tests.
func GetSharedTestEnv(ctx context.Context, t *testing.T) (string, *keypair.Full, *deployment.Deployer, *rpcclient.Client, string, string) {
	sharedEnvOnce.Do(func() {
		sharedEnv, sharedEnvErr = helpers.SetupTestEnvShared(ctx, sharedContainerName)
	})
	if sharedEnvErr != nil {
		t.Fatalf("Shared test env setup failed: %v", sharedEnvErr)
	}
	return sharedEnv.ProjectRoot, sharedEnv.DeployerKP, sharedEnv.Deployer,
		sharedEnv.RPCClient, sharedEnv.NetworkPassphrase, sharedEnv.FriendbotURL
}

// GetIsolatedTestEnv returns the same values as GetSharedTestEnv but swaps the
// shared deployer for a dedicated random keypair funded via Friendbot, so
// tests that call t.Parallel cannot collide: every deterministic salt in the
// suite is deployer-scoped, so a per-test deployer gives each test its own
// contract IDs (and its own Stellar account sequence number). The underlying
// container, RPC client, passphrase, and Friendbot stay shared — only the
// signing identity is per-test. Tests needing the suite-wide deployer's
// identity (none today) must use GetSharedTestEnv instead.
func GetIsolatedTestEnv(ctx context.Context, t *testing.T) (string, *keypair.Full, *deployment.Deployer, *rpcclient.Client, string, string) {
	t.Helper()
	projectRoot, _, _, rpcClient, passphrase, friendbotURL := GetSharedTestEnv(ctx, t)

	kp, err := keypair.Random()
	if err != nil {
		t.Fatalf("generate isolated deployer keypair: %v", err)
	}
	if err := helpers.FundViaFriendbot(friendbotURL, kp.Address()); err != nil {
		t.Fatalf("fund isolated deployer %s: %v", kp.Address(), err)
	}
	return projectRoot, kp, deployment.NewDeployer(rpcClient, passphrase, kp), rpcClient, passphrase, friendbotURL
}
