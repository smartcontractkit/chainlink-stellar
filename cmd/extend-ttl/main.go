// Command extend-ttl pre-extends persistent ledger entries (typically WASM
// code entries shared by deployed contracts) to a chosen remaining TTL, paid
// by the Stellar deployer key.
//
// Extending is permissionless: it sends ExtendFootprintTtl transactions that
// no contract needs to authorize. Keeping a WASM code entry above the
// contracts' LEDGER_THRESHOLD also keeps their instance().extend_ttl from
// paying code rent on every mutating call.
//
// Usage:
//
//	extend-ttl -target 1000000 -contract <ID> [-contract <ID>...] [-wasm <hex>...]
//	           [-rpc URL] [-passphrase PHRASE] [-key-env NAME] [-dry-run]
//
// The deployer secret key (S... strkey) is read from the environment variable
// named by -key-env (default ONCHAIN_STELLAR_DEPLOYER_KEY). -dry-run simulates
// every extension and prints the fees without submitting anything.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/smartcontractkit/chainlink-stellar/deployment"
)

// multiFlag collects repeated occurrences of one string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func main() {
	rpcURL := flag.String("rpc", "https://rpcs.cldev.sh/stellar/testnet", "Soroban RPC URL")
	passphrase := flag.String("passphrase", "Test SDF Network ; September 2015", "Stellar network passphrase")
	target := flag.Uint("target", 0, "target remaining TTL in ledgers (required, must stay below the network maximum)")
	var contracts, wasms multiFlag
	flag.Var(&contracts, "contract", "deployed contract ID whose WASM code entry to extend (repeatable)")
	flag.Var(&wasms, "wasm", "WASM code entry hash in hex (repeatable)")
	keyEnv := flag.String("key-env", "ONCHAIN_STELLAR_DEPLOYER_KEY", "environment variable holding the deployer secret key (S... strkey)")
	dryRun := flag.Bool("dry-run", false, "simulate and print fees without submitting anything")
	flag.Parse()

	if *target == 0 || (len(contracts) == 0 && len(wasms) == 0) {
		flag.Usage()
		os.Exit(2)
	}

	seed := os.Getenv(*keyEnv)
	if seed == "" {
		fatalf("environment variable %s is not set", *keyEnv)
	}
	kp, err := keypair.ParseFull(seed)
	if err != nil {
		fatalf("parse deployer key from %s: %v", *keyEnv, err)
	}

	client := rpcclient.NewClient(*rpcURL, http.DefaultClient)
	d := deployment.NewDeployer(client, *passphrase, kp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Resolve every contract's WASM hash, keeping first-seen order.
	hashes := make([]xdr.Hash, 0, len(contracts)+len(wasms))
	seen := map[xdr.Hash]bool{}
	for _, id := range contracts {
		h, err := d.ContractWasmHash(ctx, id)
		if err != nil {
			fatalf("resolve WASM of %s: %v", id, err)
		}
		if !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}
	for _, w := range wasms {
		raw, err := hex.DecodeString(strings.TrimPrefix(w, "0x"))
		if err != nil || len(raw) != len(xdr.Hash{}) {
			fatalf("invalid WASM hash %q: expected 32-byte hex", w)
		}
		var h xdr.Hash
		copy(h[:], raw)
		if !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}

	latest, err := client.GetLatestLedger(ctx)
	if err != nil {
		fatalf("get latest ledger: %v", err)
	}
	balance, err := accountBalance(ctx, client, kp.Address())
	if err != nil {
		fatalf("get deployer balance: %v", err)
	}
	fmt.Printf("deployer %s balance %.2f XLM, latest ledger %d, target TTL %d\n",
		kp.Address(), float64(balance)/1e7, latest.Sequence, *target)

	type pending struct {
		hash xdr.Hash
		fee  int64
	}
	var todo []pending
	var totalFee int64
	for _, h := range hashes {
		key := deployment.ContractCodeLedgerKey(h)
		fee, atTarget, err := d.SimulateExtendTTL(ctx, []xdr.LedgerKey{key}, uint32(*target))
		if err != nil {
			fatalf("simulate extend of wasm %x: %v", h, err)
		}
		if atTarget {
			fmt.Printf("wasm %x: already at target, nothing to do\n", h)
			continue
		}
		fmt.Printf("wasm %x: extend to target costs ~%.2f XLM (sim fee %d stroops; the submitted fee adds the deployer buffer)\n",
			h, float64(fee)/1e7, fee)
		if int64(float64(fee)*1.25)+int64(txnbuild.MinBaseFee) > math.MaxUint32 {
			fmt.Printf("wasm %x: WARNING buffered fee exceeds the uint32 transaction fee cap; this extension will be rejected, lower the target or extend fewer entries per transaction\n", h)
		}
		todo = append(todo, pending{hash: h, fee: fee})
		totalFee += fee
	}
	// 1.25 is the deployer's default fee bump; used only for the estimate.
	fmt.Printf("total simulated cost ~%.2f XLM (~%.2f XLM with the 1.25 fee buffer)\n",
		float64(totalFee)/1e7, float64(totalFee)*1.25/1e7)
	if balance < int64(float64(totalFee)*1.25) {
		fmt.Printf("WARNING: deployer balance %.2f XLM may not cover the buffered cost\n", float64(balance)/1e7)
	}

	if *dryRun {
		fmt.Println("dry run: nothing submitted")
		return
	}
	if len(todo) == 0 {
		return
	}

	for _, p := range todo {
		key := deployment.ContractCodeLedgerKey(p.hash)
		liveUntil, err := d.ExtendTTLTo(ctx, []xdr.LedgerKey{key}, uint32(*target))
		if err != nil {
			fatalf("extend wasm %x: %v", p.hash, err)
		}
		fmt.Printf("wasm %x: extended, live until ledger %d\n", p.hash, liveUntil[0])
	}
}

func accountBalance(ctx context.Context, client *rpcclient.Client, address string) (int64, error) {
	key := xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: xdr.MustAddress(address)},
	}
	keyXDR, err := key.MarshalBinaryBase64()
	if err != nil {
		return 0, err
	}
	resp, err := client.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{Keys: []string{keyXDR}})
	if err != nil {
		return 0, err
	}
	if len(resp.Entries) == 0 {
		return 0, fmt.Errorf("account %s not found", address)
	}
	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &entry); err != nil {
		return 0, err
	}
	return int64(entry.MustAccount().Balance), nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "extend-ttl: "+format+"\n", args...)
	os.Exit(1)
}
