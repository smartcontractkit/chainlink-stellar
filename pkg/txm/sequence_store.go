package txm

import (
	"context"
	"fmt"
	"sort"
	"sync"

	ccvclient "github.com/smartcontractkit/chainlink-stellar/ccv/client"
	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// SequenceStore tracks per-account Stellar sequence numbers with failed-sequence
// reuse. Modeled after the Aptos TxStore nonce tracking pattern.
//
// Key behaviors:
//   - Lazily fetches the on-chain sequence on first use.
//   - NextSequence prefers reusing failed sequences over advancing the counter.
//   - Confirm(seq, consumed) handles both on-chain outcomes and expired txs.
//   - Release returns a locally-allocated sequence that was never broadcast.
//   - Sync re-reads the on-chain value and prunes stale failed sequences.
type SequenceStore struct {
	mu             sync.Mutex
	rpc            ccvclient.RPCClient
	address        string
	nextOnChainSeq int64            // Next on-chain sequence to allocate
	lastOnChainSeq int64            // Highest sequence confirmed on-chain
	fetched        bool             //
	unconfirmed    map[int64]string // on-chain seq -> tx hash (in-flight)
	failedSeqs     []int64          // sorted ascending, available for reuse
}

// NewSequenceStore creates a SequenceStore for the given account.
func NewSequenceStore(rpc ccvclient.RPCClient, address string) *SequenceStore {
	return &SequenceStore{
		rpc:         rpc,
		address:     address,
		unconfirmed: make(map[int64]string),
	}
}

// NextSequence returns a SimpleAccount for building a transaction and the
// on-chain sequence number that will be used (after IncrementSequenceNum).
// Failed sequences are reused before advancing the counter.
func (ss *SequenceStore) NextSequence(ctx context.Context) (*txnbuild.SimpleAccount, int64, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if !ss.fetched {
		if err := ss.fetchOnChainLocked(ctx); err != nil {
			return nil, 0, err
		}
	}

	var allocated int64
	if len(ss.failedSeqs) > 0 {
		allocated = ss.failedSeqs[0]
		ss.failedSeqs = ss.failedSeqs[1:]
	} else {
		allocated = ss.nextOnChainSeq
		ss.nextOnChainSeq++
	}

	// txnbuild.NewTransaction with IncrementSequenceNum:true adds 1 to
	// SimpleAccount.Sequence, so we set base = allocated - 1.
	return &txnbuild.SimpleAccount{
		AccountID: ss.address,
		Sequence:  allocated - 1,
	}, allocated, nil
}

// AddUnconfirmed records an on-chain sequence as in-flight after a successful
// SendTransaction call.
func (ss *SequenceStore) AddUnconfirmed(seq int64, hash string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.unconfirmed[seq] = hash
}

// Confirm processes the on-chain outcome of a transaction.
//
//   - consumed=true: the sequence was included in a ledger (SUCCESS or FAILED).
//     Advances lastOnChainSeq so the sequence won't be reused.
//   - consumed=false: the sequence was never included (expired / dropped).
//     Adds it to failedSeqs so NextSequence can reuse it.
func (ss *SequenceStore) Confirm(seq int64, consumed bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	delete(ss.unconfirmed, seq)

	if consumed {
		if seq > ss.lastOnChainSeq {
			ss.lastOnChainSeq = seq
		}
	} else {
		if seq > ss.lastOnChainSeq {
			ss.insertFailedLocked(seq)
		}
	}
}

// Release returns an allocated sequence that was never broadcast (e.g. build
// or simulation error). The sequence is added to failedSeqs for reuse.
func (ss *SequenceStore) Release(seq int64) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	delete(ss.unconfirmed, seq)
	if seq > ss.lastOnChainSeq {
		ss.insertFailedLocked(seq)
	}
}

// Sync re-reads the account sequence from the ledger and prunes failed
// sequences that the chain has already consumed. Call after tx_bad_seq errors.
func (ss *SequenceStore) Sync(ctx context.Context) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if err := ss.fetchOnChainLocked(ctx); err != nil {
		return err
	}

	pruned := ss.failedSeqs[:0]
	for _, s := range ss.failedSeqs {
		if s > ss.lastOnChainSeq {
			pruned = append(pruned, s)
		}
	}
	ss.failedSeqs = pruned
	return nil
}

// UnconfirmedCount returns the number of sequences currently in-flight.
func (ss *SequenceStore) UnconfirmedCount() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return len(ss.unconfirmed)
}

func (ss *SequenceStore) insertFailedLocked(seq int64) {
	i := sort.Search(len(ss.failedSeqs), func(i int) bool {
		return ss.failedSeqs[i] >= seq
	})
	if i < len(ss.failedSeqs) && ss.failedSeqs[i] == seq {
		return
	}
	ss.failedSeqs = append(ss.failedSeqs, 0)
	copy(ss.failedSeqs[i+1:], ss.failedSeqs[i:])
	ss.failedSeqs[i] = seq
}

func (ss *SequenceStore) fetchOnChainLocked(ctx context.Context) error {
	accountKey := xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{
			AccountId: xdr.MustAddress(ss.address),
		},
	}

	keyXDR, err := accountKey.MarshalBinaryBase64()
	if err != nil {
		return fmt.Errorf("marshal account key: %w", err)
	}

	resp, err := ss.rpc.GetLedgerEntries(ctx, protocolrpc.GetLedgerEntriesRequest{
		Keys: []string{keyXDR},
	})
	if err != nil {
		return fmt.Errorf("get ledger entries: %w", err)
	}

	var seqNum int64
	if len(resp.Entries) > 0 && resp.Entries[0].DataXDR != "" {
		var entry xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &entry); err != nil {
			return fmt.Errorf("unmarshal account entry: %w", err)
		}
		seqNum = int64(entry.MustAccount().SeqNum)
	}

	ss.lastOnChainSeq = seqNum
	if !ss.fetched || seqNum+1 > ss.nextOnChainSeq {
		ss.nextOnChainSeq = seqNum + 1
	}
	ss.fetched = true
	return nil
}
