package helpers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// FindProjectRoot finds the root of the chainlink-stellar project.
func FindProjectRoot(t *testing.T) string {
	dir, err := FindProjectRootErr()
	if err != nil {
		t.Fatalf("Failed to find project root: %v", err)
	}
	return dir
}

// FindProjectRootErr finds the root of the chainlink-stellar project, returning an error on failure.
func FindProjectRootErr() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	for {
		cargoPath := filepath.Join(dir, "Cargo.toml")
		if _, err := os.Stat(cargoPath); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (Cargo.toml)")
		}
		dir = parent
	}
}

// generateMockContractID generates a deterministic mock contract ID for testing.
func GenerateMockContractID(t *testing.T, deployerAddress, contractName string) string {
	// Generate a deterministic salt
	salt := deployment.GenerateDeterministicSalt(deployerAddress, contractName)

	// Encode as a Stellar contract address
	encoded, err := strkey.Encode(strkey.VersionByteContract, salt[:])
	if err != nil {
		t.Fatalf("Failed to encode mock contract ID: %v", err)
	}
	return encoded
}

// waitForFriendbot waits for the friendbot service to be ready.
// The Stellar quickstart container starts multiple services and friendbot
// initializes after the main RPC endpoint is ready.
func WaitForFriendbot(ctx context.Context, friendbotBaseURL string, timeout time.Duration) error {
	// Generate a test address to use for the health check
	testKP, err := keypair.Random()
	if err != nil {
		return fmt.Errorf("failed to generate test keypair: %w", err)
	}
	testURL := fmt.Sprintf("%s?addr=%s", friendbotBaseURL, testKP.Address())

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
			if err != nil {
				lastErr = err
				continue
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil // Friendbot is ready
			}

			lastErr = fmt.Errorf("friendbot returned status %d", resp.StatusCode)
		}
	}

	return fmt.Errorf("friendbot not ready after %v: %w", timeout, lastErr)
}

// WaitForRPC polls the Soroban RPC getHealth endpoint until it reports a
// healthy status. Newer quickstart images can take longer to initialize the
// simulation endpoint, and friendbot readiness alone does not guarantee the
// RPC is serving simulations.
func WaitForRPC(ctx context.Context, client *rpcclient.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			health, err := client.GetHealth(ctx)
			if err != nil {
				lastErr = err
				continue
			}
			if health.Status == "healthy" {
				return nil
			}
			lastErr = fmt.Errorf("RPC status: %s", health.Status)
		}
	}

	return fmt.Errorf("RPC not ready after %v: %w", timeout, lastErr)
}

func FundViaFriendbot(friendbotURL, address string) error {
	faucetURL := fmt.Sprintf("%s?addr=%s", friendbotURL, address)

	var lastErr error
	maxRetries := 9
	retryInterval := 20 * time.Second

	for range maxRetries {
		resp, err := http.Get(faucetURL)
		if err != nil {
			lastErr = fmt.Errorf("friendbot request failed: %w", err)
			time.Sleep(retryInterval)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}

		resp.Body.Close()
		lastErr = fmt.Errorf("friendbot returned status %s", resp.Status)
		time.Sleep(retryInterval)
	}

	return fmt.Errorf("friendbot not ready after %d attempts: %w", maxRetries, lastErr)
}
