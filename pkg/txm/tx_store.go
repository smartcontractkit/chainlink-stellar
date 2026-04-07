package txm

import (
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// TxStore is an in-memory store tracking transactions through their lifecycle.
// Thread-safe via mutex. Modeled after Solana's PendingTxContext.
type TxStore struct {
	mu     sync.RWMutex
	byID   map[string]*txEntry
	byHash map[string]string // hash → ID
}

// NewTxStore creates an empty TxStore.
func NewTxStore() *TxStore {
	return &TxStore{
		byID:   make(map[string]*txEntry),
		byHash: make(map[string]string),
	}
}

// Add inserts a new pending transaction. Returns ErrDuplicateTx if the ID
// already exists.
func (s *TxStore) Add(req TxRequest) (*txEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byID[req.ID]; exists {
		return nil, ErrDuplicateTx
	}

	entry := &txEntry{
		Request: req,
		Status:  TxStatusPending,
		Created: time.Now(),
		Updated: time.Now(),
		Done:    make(chan struct{}),
	}
	s.byID[req.ID] = entry
	return entry, nil
}

// Get returns the entry for a given ID, or nil.
func (s *TxStore) Get(id string) *txEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

// Status returns the current status of a transaction under the read lock.
func (s *TxStore) Status(id string) (TxStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.byID[id]
	if !ok {
		return 0, false
	}
	return entry.Status, true
}

// GetByHash returns the entry for a given tx hash, or nil.
func (s *TxStore) GetByHash(hash string) *txEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHash[hash]
	if !ok {
		return nil
	}
	return s.byID[id]
}

// SetBroadcast moves a pending tx to the broadcast state and records its hash.
func (s *TxStore) SetBroadcast(id, hash string, seq int64, maxLedger uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byID[id]
	if !ok {
		return
	}
	entry.Status = TxStatusBroadcast
	entry.Hash = hash
	entry.Seq = seq
	entry.MaxLedger = maxLedger
	entry.Updated = time.Now()
	s.byHash[hash] = id
}

// SetConfirmed moves a broadcast tx to confirmed.
func (s *TxStore) SetConfirmed(id string, meta *xdr.TransactionMeta, ledger uint32, resultXDR string, feeCharged int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byID[id]
	if !ok {
		return
	}
	entry.Status = TxStatusConfirmed
	entry.Meta = meta
	entry.Ledger = ledger
	entry.ResultXDR = resultXDR
	entry.FeeCharged = feeCharged
	entry.Updated = time.Now()
	s.closeDone(entry)
}

// SetFailed moves a tx to the failed state.
func (s *TxStore) SetFailed(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byID[id]
	if !ok {
		return
	}
	entry.Status = TxStatusFailed
	entry.Error = err
	entry.Updated = time.Now()
	s.closeDone(entry)
}

// SetExpired moves a broadcast tx to expired.
func (s *TxStore) SetExpired(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byID[id]
	if !ok {
		return
	}
	entry.Status = TxStatusExpired
	entry.Error = ErrTxExpired
	entry.Updated = time.Now()
	s.closeDone(entry)
}

// IncrementRetry bumps the retry counter and returns the new value.
func (s *TxStore) IncrementRetry(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.byID[id]
	if !ok {
		return 0
	}
	entry.Attempt++
	entry.Updated = time.Now()
	entry.Status = TxStatusPending
	entry.Hash = ""
	return entry.Attempt
}

// BroadcastEntries returns all entries in the broadcast state.
func (s *TxStore) BroadcastEntries() []*txEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var entries []*txEntry
	for _, e := range s.byID {
		if e.Status == TxStatusBroadcast {
			entries = append(entries, e)
		}
	}
	return entries
}

// UnconfirmedCount returns the number of broadcast (unconfirmed) entries.
func (s *TxStore) UnconfirmedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, e := range s.byID {
		if e.Status == TxStatusBroadcast {
			count++
		}
	}
	return count
}

// Reap removes terminal entries older than the given threshold.
func (s *TxStore) Reap(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	reaped := 0
	for id, e := range s.byID {
		if e.Status.Terminal() && e.Updated.Before(cutoff) {
			if e.Hash != "" {
				delete(s.byHash, e.Hash)
			}
			delete(s.byID, id)
			reaped++
		}
	}
	return reaped
}

func (s *TxStore) closeDone(entry *txEntry) {
	select {
	case <-entry.Done:
	default:
		close(entry.Done)
	}
}
