package txm

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxStore_Add(t *testing.T) {
	s := NewTxStore()

	entry, err := s.Add(TxRequest{ID: "tx-1", ContractID: "C1"})
	require.NoError(t, err)
	assert.NotNil(t, entry)
	assert.Equal(t, TxStatusPending, entry.Status)
	assert.NotNil(t, entry.Done)

	_, err = s.Add(TxRequest{ID: "tx-1"})
	assert.ErrorIs(t, err, ErrDuplicateTx)
}

func TestTxStore_Status(t *testing.T) {
	s := NewTxStore()

	_, ok := s.Status("nope")
	assert.False(t, ok)

	s.Add(TxRequest{ID: "tx-1"})
	status, ok := s.Status("tx-1")
	assert.True(t, ok)
	assert.Equal(t, TxStatusPending, status)
}

func TestTxStore_SetBroadcast(t *testing.T) {
	s := NewTxStore()
	s.Add(TxRequest{ID: "tx-1"})

	s.SetBroadcast("tx-1", "hash-abc", 42, 100)

	status, ok := s.Status("tx-1")
	require.True(t, ok)
	assert.Equal(t, TxStatusBroadcast, status)

	entry := s.GetByHash("hash-abc")
	assert.NotNil(t, entry)
	assert.Equal(t, "tx-1", entry.Request.ID)
}

func TestTxStore_SetBroadcast_NoOp(t *testing.T) {
	s := NewTxStore()
	s.SetBroadcast("nonexistent", "hash", 1, 10)
}

func TestTxStore_SetConfirmed(t *testing.T) {
	s := NewTxStore()
	entry, _ := s.Add(TxRequest{ID: "tx-1"})
	s.SetBroadcast("tx-1", "hash", 1, 10)

	meta := &xdr.TransactionMeta{}
	s.SetConfirmed("tx-1", meta, 50, "result-xdr", 1234)

	status, _ := s.Status("tx-1")
	assert.Equal(t, TxStatusConfirmed, status)

	select {
	case <-entry.Done:
	default:
		t.Fatal("Done channel should be closed after SetConfirmed")
	}
}

func TestTxStore_SetFailed(t *testing.T) {
	s := NewTxStore()
	entry, _ := s.Add(TxRequest{ID: "tx-1"})
	s.SetBroadcast("tx-1", "hash", 1, 10)

	s.SetFailed("tx-1", ErrTxFailed)

	status, _ := s.Status("tx-1")
	assert.Equal(t, TxStatusFailed, status)

	select {
	case <-entry.Done:
	default:
		t.Fatal("Done channel should be closed after SetFailed")
	}
}

func TestTxStore_SetExpired(t *testing.T) {
	s := NewTxStore()
	entry, _ := s.Add(TxRequest{ID: "tx-1"})
	s.SetBroadcast("tx-1", "hash", 1, 10)

	s.SetExpired("tx-1")

	status, _ := s.Status("tx-1")
	assert.Equal(t, TxStatusExpired, status)

	select {
	case <-entry.Done:
	default:
		t.Fatal("Done channel should be closed after SetExpired")
	}
}

func TestTxStore_IncrementRetry(t *testing.T) {
	s := NewTxStore()
	s.Add(TxRequest{ID: "tx-1"})
	s.SetBroadcast("tx-1", "hash", 1, 10)

	attempt := s.IncrementRetry("tx-1")
	assert.Equal(t, 1, attempt)

	status, _ := s.Status("tx-1")
	assert.Equal(t, TxStatusPending, status, "should reset to pending")

	assert.Equal(t, 0, s.IncrementRetry("nonexistent"))
}

func TestTxStore_BroadcastSnapshots(t *testing.T) {
	s := NewTxStore()
	s.Add(TxRequest{ID: "pending"})
	s.Add(TxRequest{ID: "bc-1"})
	s.Add(TxRequest{ID: "bc-2"})

	s.SetBroadcast("bc-1", "h1", 10, 100)
	s.SetBroadcast("bc-2", "h2", 11, 101)

	snaps := s.BroadcastSnapshots()
	assert.Len(t, snaps, 2)

	ids := map[string]bool{}
	for _, snap := range snaps {
		ids[snap.ID] = true
		assert.NotEmpty(t, snap.Hash)
		assert.NotZero(t, snap.Seq)
		assert.NotZero(t, snap.MaxLedger)
		assert.False(t, snap.Created.IsZero())
	}
	assert.True(t, ids["bc-1"])
	assert.True(t, ids["bc-2"])
}

func TestTxStore_GetResult(t *testing.T) {
	s := NewTxStore()

	assert.Nil(t, s.GetResult("nope"), "not found returns nil")

	s.Add(TxRequest{ID: "tx-1"})
	assert.Nil(t, s.GetResult("tx-1"), "pending entry returns nil")

	s.SetBroadcast("tx-1", "hash", 1, 10)
	assert.Nil(t, s.GetResult("tx-1"), "broadcast entry returns nil")

	meta := &xdr.TransactionMeta{}
	s.SetConfirmed("tx-1", meta, 55, "resultxdr", 9999)

	result := s.GetResult("tx-1")
	require.NotNil(t, result)
	assert.Equal(t, TxStatusConfirmed, result.Status)
	assert.Equal(t, "hash", result.Hash)
	assert.Equal(t, "resultxdr", result.ResultXDR)
	assert.Equal(t, int64(9999), result.FeeCharged)
	assert.Equal(t, uint32(55), result.LedgerNum)
}

func TestTxStore_GetFee(t *testing.T) {
	s := NewTxStore()

	_, ok := s.GetFee("nope")
	assert.False(t, ok)

	s.Add(TxRequest{ID: "tx-1"})
	_, ok = s.GetFee("tx-1")
	assert.False(t, ok, "pending tx has no fee")

	s.SetBroadcast("tx-1", "h", 1, 10)
	s.SetConfirmed("tx-1", nil, 50, "", 42_000)

	fee, ok := s.GetFee("tx-1")
	require.True(t, ok)
	assert.Equal(t, big.NewInt(42_000), fee)
}

func TestTxStore_UnconfirmedCount(t *testing.T) {
	s := NewTxStore()
	assert.Equal(t, 0, s.UnconfirmedCount())

	s.Add(TxRequest{ID: "tx-1"})
	s.SetBroadcast("tx-1", "h1", 1, 10)
	assert.Equal(t, 1, s.UnconfirmedCount())

	s.Add(TxRequest{ID: "tx-2"})
	s.SetBroadcast("tx-2", "h2", 2, 11)
	assert.Equal(t, 2, s.UnconfirmedCount())

	s.SetConfirmed("tx-1", nil, 50, "", 0)
	assert.Equal(t, 1, s.UnconfirmedCount())
}

func TestTxStore_Reap(t *testing.T) {
	s := NewTxStore()

	s.Add(TxRequest{ID: "tx-1"})
	s.SetBroadcast("tx-1", "h1", 1, 10)
	s.SetConfirmed("tx-1", nil, 50, "", 0)

	s.Add(TxRequest{ID: "tx-2"})
	s.SetBroadcast("tx-2", "h2", 2, 11)
	s.SetFailed("tx-2", ErrTxFailed)

	assert.Equal(t, 0, s.Reap(time.Minute), "nothing old enough to reap")

	time.Sleep(5 * time.Millisecond)
	reaped := s.Reap(time.Millisecond)
	assert.Equal(t, 2, reaped)
	assert.Nil(t, s.GetByHash("h1"))
	assert.Nil(t, s.GetByHash("h2"))
}

func TestTxStore_ConcurrentAccess(t *testing.T) {
	s := NewTxStore()
	const n = 50
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := "tx-" + string(rune('a'+idx))
			s.Add(TxRequest{ID: id})
			s.SetBroadcast(id, "h-"+id, int64(idx), uint32(100+idx))
			_ = s.BroadcastSnapshots()
			_ = s.GetResult(id)
			_, _ = s.GetFee(id)
			_, _ = s.Status(id)
		}(i)
	}
	wg.Wait()
}

func TestTxStore_GetEntryForRetry(t *testing.T) {
	s := NewTxStore()
	assert.Nil(t, s.GetEntryForRetry("nope"))

	s.Add(TxRequest{ID: "tx-1"})
	entry := s.GetEntryForRetry("tx-1")
	require.NotNil(t, entry)
	assert.Equal(t, "tx-1", entry.Request.ID)
}
