package txm

import (
	"context"
	"fmt"
	"sync"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// SequenceManager tracks the Stellar account sequence number (analogous to
// EVM nonce). It lazily fetches the on-chain sequence on first use and
// increments locally for each broadcast. On sequence-conflict errors the
// caller should call Sync to re-read the on-chain value.
type SequenceManager struct {
	mu        sync.Mutex
	rpc       ccvclient.RPCClient
	address   string
	seq       int64
	fetched   bool
}

// NewSequenceManager creates a SequenceManager for the given account.
func NewSequenceManager(rpc ccvclient.RPCClient, address string) *SequenceManager {
	return &SequenceManager{
		rpc:     rpc,
		address: address,
		seq:     -1,
	}
}

// NextSequence returns a SimpleAccount with the next sequence number to use.
// The caller must call Confirm after a successful broadcast, or Release on
// failure so the sequence can be reused.
func (sm *SequenceManager) NextSequence(ctx context.Context) (*txnbuild.SimpleAccount, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.fetched {
		if err := sm.fetchOnChainLocked(ctx); err != nil {
			return nil, err
		}
	}

	return &txnbuild.SimpleAccount{
		AccountID: sm.address,
		Sequence:  sm.seq,
	}, nil
}

// Confirm advances the sequence after a successful broadcast.
func (sm *SequenceManager) Confirm() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.seq++
}

// Sync re-reads the sequence number from chain. Use after a sequence
// conflict error to realign local state with the ledger.
func (sm *SequenceManager) Sync(ctx context.Context) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.fetchOnChainLocked(ctx)
}

func (sm *SequenceManager) fetchOnChainLocked(ctx context.Context) error {
	accountKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: xdr.MustAddress(sm.address),
		},
	}

	keyXDR, err := accountKey.MarshalBinaryBase64()
	if err != nil {
		return fmt.Errorf("marshal account key: %w", err)
	}

	resp, err := sm.rpc.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	})
	if err != nil {
		return fmt.Errorf("get ledger entries: %w", err)
	}

	if len(resp.Entries) == 0 {
		sm.seq = 0
		sm.fetched = true
		return nil
	}

	entryXDR := resp.Entries[0].DataXDR
	if entryXDR == "" {
		sm.seq = 0
		sm.fetched = true
		return nil
	}

	var entry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(entryXDR, &entry); err != nil {
		return fmt.Errorf("unmarshal account entry: %w", err)
	}
	account := entry.MustAccount()
	sm.seq = int64(account.SeqNum)
	sm.fetched = true
	return nil
}
