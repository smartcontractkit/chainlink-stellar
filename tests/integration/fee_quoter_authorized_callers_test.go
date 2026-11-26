//go:build integration

package integration

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	fqbindings "github.com/smartcontractkit/chainlink-stellar/bindings/contracts/fee_quoter"
	deployment "github.com/smartcontractkit/chainlink-stellar/deployment"
	helpers "github.com/smartcontractkit/chainlink-stellar/tests/testutils"
	"github.com/stellar/go-stellar-sdk/keypair"
)

// TestFeeQuoterAuthorizedCallerUpdates exercises the owner-gated
// price-updater rotation against the live wasm (EVM
// `FeeQuoter.applyAuthorizedCallerUpdates` parity). Regression for the
// `DefaultOwnable`/`AUTHOWN` stand-in: its owner key was never written by any
// contract, so every owner-gated update failed with `NotOwner` and the
// updater set was frozen at `initialize` — each case below would have failed
// before the fix.
func TestFeeQuoterAuthorizedCallerUpdates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectRoot, deployerKP, deployer, rpcClient, networkPassphrase, friendbotURL :=
		GetIsolatedTestEnv(ctx, t)
	deployerAddr := deployerKP.Address()

	// Two funded price-updater accounts, each with its own deployer so they
	// can sign their own update_prices calls (the updaters are NOT the owner).
	updaterAKP, err := keypair.Random()
	if err != nil {
		t.Fatalf("updaterA keypair: %v", err)
	}
	updaterBKP, err := keypair.Random()
	if err != nil {
		t.Fatalf("updaterB keypair: %v", err)
	}
	for _, kp := range []*keypair.Full{updaterAKP, updaterBKP} {
		if err := helpers.FundViaFriendbot(friendbotURL, kp.Address()); err != nil {
			t.Fatalf("fund updater %s: %v", kp.Address(), err)
		}
	}
	updaterADeployer := deployment.NewDeployer(rpcClient, networkPassphrase, updaterAKP)
	updaterBDeployer := deployment.NewDeployer(rpcClient, networkPassphrase, updaterBKP)

	// Owner = the stack deployer; updaterA is the sole initial price updater.
	linkToken := helpers.GenerateMockContractID(t, deployerAddr, "fq-authcallers-link-token")
	fqID := deployConstructed(ctx, t, projectRoot, deployer, deployerAddr, "fee_quoter.wasm", "fq-authcallers-fee-quoter",
		mustCtor(t)(fqbindings.ConstructorArgs(deployerAddr, fqbindings.StaticConfig{
			LinkToken:         linkToken,
			MaxFeeJuelsPerMsg: big.NewInt(1_000_000_000_000_000_000),
		}, []string{updaterAKP.Address()})))
	client := fqbindings.NewFeeQuoterClient(deployer, fqID)

	updaterAClient := fqbindings.NewFeeQuoterClient(updaterADeployer, fqID)
	updaterBClient := fqbindings.NewFeeQuoterClient(updaterBDeployer, fqID)

	requireCallerNotAuthorized := func(t *testing.T, err error, what string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected CallerNotAuthorized, got nil", what)
		}
		if !strings.Contains(err.Error(), "Error(Contract, #6)") {
			t.Fatalf("%s: want CallerNotAuthorized (#6), got: %v", what, err)
		}
	}

	// Case 1 — the owner gate reads the embedding contract's own owner, so the
	// real owner can add an updater, and the new updater can update prices.
	t.Run("owner adds updater and it can update prices", func(t *testing.T) {
		if err := client.ApplyAuthorizedCallerUpdates(ctx, deployerAddr,
			[]string{updaterBKP.Address()}, nil); err != nil {
			t.Fatalf("ApplyAuthorizedCallerUpdates (add): %v", err)
		}

		callers, err := client.GetAuthorizedCallers(ctx)
		if err != nil {
			t.Fatalf("GetAuthorizedCallers: %v", err)
		}
		if len(callers) != 2 || callers[0] != updaterAKP.Address() || callers[1] != updaterBKP.Address() {
			t.Fatalf("authorized callers: want [updaterA, updaterB], got %v", callers)
		}

		if err := updaterBClient.UpdatePrices(ctx, updaterBKP.Address(), fqbindings.PriceUpdates{
			TokenPriceUpdates: []fqbindings.TokenPriceUpdate{},
			GasPriceUpdates:   []fqbindings.GasPriceUpdate{},
		}); err != nil {
			t.Fatalf("UpdatePrices as newly added updater: %v", err)
		}
	})

	// Case 2 — being an authorized price updater does NOT grant rotation
	// rights: updaterA (in the set, not the owner) cannot change it.
	t.Run("non-owner rotation rejected without state change", func(t *testing.T) {
		intruderKP, err := keypair.Random()
		if err != nil {
			t.Fatalf("intruder keypair: %v", err)
		}
		intruder := intruderKP.Address()
		err = updaterAClient.ApplyAuthorizedCallerUpdates(ctx, updaterAKP.Address(),
			[]string{intruder}, nil)
		requireCallerNotAuthorized(t, err, "updater adds a caller")

		err = updaterAClient.ApplyAuthorizedCallerUpdates(ctx, updaterAKP.Address(),
			nil, []string{updaterBKP.Address()})
		requireCallerNotAuthorized(t, err, "updater removes a caller")

		callers, err := client.GetAuthorizedCallers(ctx)
		if err != nil {
			t.Fatalf("GetAuthorizedCallers: %v", err)
		}
		if len(callers) != 2 {
			t.Fatalf("rejected rotations must not change the set; got %v", callers)
		}
	})

	// Case 3 — the owner removes updaterA, whose update_prices now fails.
	t.Run("owner removes updater and it loses access", func(t *testing.T) {
		if err := client.ApplyAuthorizedCallerUpdates(ctx, deployerAddr,
			nil, []string{updaterAKP.Address()}); err != nil {
			t.Fatalf("ApplyAuthorizedCallerUpdates (remove): %v", err)
		}

		callers, err := client.GetAuthorizedCallers(ctx)
		if err != nil {
			t.Fatalf("GetAuthorizedCallers: %v", err)
		}
		if len(callers) != 1 || callers[0] != updaterBKP.Address() {
			t.Fatalf("authorized callers: want [updaterB], got %v", callers)
		}

		err = updaterAClient.UpdatePrices(ctx, updaterAKP.Address(), fqbindings.PriceUpdates{
			TokenPriceUpdates: []fqbindings.TokenPriceUpdate{},
			GasPriceUpdates:   []fqbindings.GasPriceUpdate{},
		})
		requireCallerNotAuthorized(t, err, "removed updater updates prices")
	})
}
