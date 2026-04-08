package txm

import (
	"math/big"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// BroadcastSnapshot is an immutable point-in-time copy of a broadcast
// txEntry's fields. The confirmer works with snapshots to avoid data races
// from reading live *txEntry pointers after the store lock is released.
type BroadcastSnapshot struct {
	ID        string
	Hash      string
	Seq       int64
	MaxLedger uint32
	Created   time.Time
}

// TxStore is an in-memory store tracking transactions through their lifecycle.
// Thread-safe via mutex. Modeled after Solana's PendingTxContext.
//
// Callers must not read mutable fields from *txEntry pointers returned by Add
// without proper synchronization. Use the typed accessor methods (Status,
// GetResult, GetFee, BroadcastSnapshots) which read under lock.
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

// BroadcastSnapshots returns point-in-time copies of all broadcast entries.
// Callers may safely read the returned snapshots without holding any lock.
func (s *TxStore) BroadcastSnapshots() []BroadcastSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var snaps []BroadcastSnapshot
	for _, e := range s.byID {
		if e.Status == TxStatusBroadcast {
			snaps = append(snaps, BroadcastSnapshot{
				ID:        e.Request.ID,
				Hash:      e.Hash,
				Seq:       e.Seq,
				MaxLedger: e.MaxLedger,
				Created:   e.Created,
			})
		}
	}
	return snaps
}

// GetEntryForRetry returns the live *txEntry for re-enqueuing on the
// broadcast channel. The returned pointer is only safe to send on a channel
// (the channel send/receive provides happens-before synchronization).
func (s *TxStore) GetEntryForRetry(id string) *txEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

// GetResult builds a TxResult snapshot for a terminal transaction under
// the read lock. Returns nil if the entry is not found or not yet terminal.
func (s *TxStore) GetResult(id string) *TxResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.byID[id]
	if !ok || !entry.Status.Terminal() {
		return nil
	}
	return &TxResult{
		Status:     entry.Status,
		Hash:       entry.Hash,
		ResultMeta: entry.Meta,
		ResultXDR:  entry.ResultXDR,
		FeeCharged: entry.FeeCharged,
		Error:      entry.Error,
		LedgerNum:  entry.Ledger,
	}
}

// GetFee returns the fee charged for a confirmed transaction, reading under
// the lock. Returns (nil, false) if the tx is not found or not confirmed.
func (s *TxStore) GetFee(id string) (*big.Int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.byID[id]
	if !ok || entry.Status != TxStatusConfirmed {
		return nil, false
	}
	return big.NewInt(entry.FeeCharged), true
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
