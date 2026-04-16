package txm

import (
	"fmt"
	"sort"
	"sync"

	"golang.org/x/exp/maps"
)

// UnconfirmedTx tracks a transaction that has been sent to the network
// but has not yet been confirmed (or rejected) by a ledger.
type UnconfirmedTx struct {
	Sequence  int64
	Hash      string
	MaxLedger uint32 // LedgerBounds.MaxLedger — primary timeout mechanism
	Tx        *StellarTx
}

// TxStore tracks sequence numbers and in-flight transactions for a single Stellar account.
// Sequence numbers are strictly sequential: a gap blocks all subsequent transactions.
// The failed-sequence recycling logic ensures gaps are plugged.
type TxStore struct {
	lock sync.RWMutex

	nextSequence         int64
	unconfirmedSequences map[int64]*UnconfirmedTx
	failedSequences      map[int64]struct{}
	lastOnchainSequence  int64
}

func NewTxStore(initialSequence int64) *TxStore {
	return &TxStore{
		nextSequence:         initialSequence,
		unconfirmedSequences: make(map[int64]*UnconfirmedTx),
		failedSequences:      make(map[int64]struct{}),
		lastOnchainSequence:  initialSequence,
	}
}

// ResyncNonce updates the TxStore's view of on-chain state.
//
// IMPORTANT: On Stellar, the on-chain account.SeqNum is the LAST USED sequence.
// The caller must pass onchainSeq+1 (the next expected sequence number).
//
// This must not be called between GetNextSequence() and AddUnconfirmed(), as it
// mutates nextSequence.
func (s *TxStore) ResyncNonce(nextExpectedSequence int64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	// Remove failed sequences that are now behind the on-chain state.
	badFailedSeqs := []int64{}
	for failedSeq := range s.failedSequences {
		if failedSeq >= nextExpectedSequence {
			continue
		}
		badFailedSeqs = append(badFailedSeqs, failedSeq)
	}
	for _, failedSeq := range badFailedSeqs {
		delete(s.failedSequences, failedSeq)
	}

	if s.nextSequence < nextExpectedSequence {
		s.nextSequence = nextExpectedSequence
	}

	s.lastOnchainSequence = nextExpectedSequence
}

func (s *TxStore) GetLastResyncedNonce() int64 {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.lastOnchainSequence
}

// GetNextSequence returns the next sequence number to use.
// If there are failed (recycled) sequences, it returns the smallest of
// (nextSequence, min(failedSequences)) to plug gaps.
func (s *TxStore) GetNextSequence() int64 {
	s.lock.Lock()
	defer s.lock.Unlock()

	next := s.nextSequence
	for seq := range s.failedSequences {
		next = min(next, seq)
	}

	return next
}

// AddUnconfirmed records a transaction that has been submitted to the network.
// The sequence must match the value returned by the preceding GetNextSequence() call.
func (s *TxStore) AddUnconfirmed(seq int64, hash string, maxLedger uint32, tx *StellarTx) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if existing, exists := s.unconfirmedSequences[seq]; exists {
		return fmt.Errorf("sequence used: tried to use sequence (%d) for tx (%s), already used by (%s)", seq, hash, existing.Hash)
	}

	if _, isFailedSeq := s.failedSequences[seq]; !isFailedSeq {
		if seq < s.nextSequence {
			return fmt.Errorf("tried to add an unconfirmed tx at an old sequence: expected %d, got %d", s.nextSequence, seq)
		}
		if seq > s.nextSequence {
			return fmt.Errorf("tried to add an unconfirmed tx at a future sequence: expected %d, got %d", s.nextSequence, seq)
		}
		s.nextSequence++
	} else {
		delete(s.failedSequences, seq)
	}

	s.unconfirmedSequences[seq] = &UnconfirmedTx{
		Sequence:  seq,
		Hash:      hash,
		MaxLedger: maxLedger,
		Tx:        tx,
	}

	return nil
}

// Confirm removes a transaction from the unconfirmed set.
// If failed is true and the sequence is still ahead of the last known on-chain
// sequence, it is added to failedSequences for recycling.
func (s *TxStore) Confirm(seq int64, hash string, failed bool) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	unconfirmed, exists := s.unconfirmedSequences[seq]
	if !exists {
		return fmt.Errorf("no such unconfirmed sequence: %d", seq)
	}
	if unconfirmed.Hash != hash {
		return fmt.Errorf("unexpected tx hash: expected %s, got %s", unconfirmed.Hash, hash)
	}
	delete(s.unconfirmedSequences, seq)

	if failed && seq >= s.lastOnchainSequence {
		s.failedSequences[seq] = struct{}{}
	}
	return nil
}

// Release returns an allocated-but-never-broadcast sequence to the failed pool
// for reuse. The broadcaster must call this at every early-return error path
// between GetNextSequence() and SendTransaction (e.g., simulation fails,
// assembly errors, signing errors). Without this, a pre-broadcast failure
// would permanently leak a sequence number.
func (s *TxStore) Release(seq int64) {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.unconfirmedSequences, seq)
	if seq >= s.lastOnchainSequence {
		s.failedSequences[seq] = struct{}{}
	}
}

// GetUnconfirmed returns a sorted (by sequence) snapshot of all unconfirmed transactions.
func (s *TxStore) GetUnconfirmed() []*UnconfirmedTx {
	s.lock.RLock()
	defer s.lock.RUnlock()

	unconfirmed := maps.Values(s.unconfirmedSequences)
	result := make([]*UnconfirmedTx, len(unconfirmed))

	for i, tx := range unconfirmed {
		result[i] = &UnconfirmedTx{
			Sequence:  tx.Sequence,
			Hash:      tx.Hash,
			MaxLedger: tx.MaxLedger,
			Tx:        tx.Tx,
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Sequence < result[j].Sequence
	})

	return result
}

func (s *TxStore) InflightCount() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return len(s.unconfirmedSequences)
}

// AccountStore holds a TxStore per Stellar account address.
type AccountStore struct {
	store map[string]*TxStore
	lock  sync.RWMutex
}

func NewAccountStore() *AccountStore {
	return &AccountStore{
		store: map[string]*TxStore{},
	}
}

// CreateTxStore initializes a TxStore for a new account. Returns an error if
// a store already exists for this address.
func (c *AccountStore) CreateTxStore(accountAddress string, initialSequence int64) (*TxStore, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, ok := c.store[accountAddress]; ok {
		return nil, fmt.Errorf("TxStore already exists: %s", accountAddress)
	}
	store := NewTxStore(initialSequence)
	c.store[accountAddress] = store
	return store, nil
}

func (c *AccountStore) GetTxStore(accountAddress string) *TxStore {
	c.lock.RLock()
	defer c.lock.RUnlock()
	store, ok := c.store[accountAddress]
	if !ok {
		return nil
	}
	return store
}

func (c *AccountStore) GetTotalInflightCount() int {
	c.lock.RLock()
	defer c.lock.RUnlock()

	count := 0
	for _, store := range c.store {
		count += store.InflightCount()
	}
	return count
}

func (c *AccountStore) GetAllUnconfirmed() map[string][]*UnconfirmedTx {
	c.lock.RLock()
	defer c.lock.RUnlock()

	allUnconfirmed := map[string][]*UnconfirmedTx{}
	for addr, store := range c.store {
		allUnconfirmed[addr] = store.GetUnconfirmed()
	}
	return allUnconfirmed
}
