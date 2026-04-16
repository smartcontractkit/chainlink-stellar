package txm

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- TxStore: GetNextSequence ---

func TestTxStore_GetNextSequence_Basic(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	assert.Equal(t, int64(10), store.GetNextSequence())
}

func TestTxStore_GetNextSequence_AdvancesAfterAddUnconfirmed(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "hash-a", 100, nil))
	assert.Equal(t, int64(11), store.GetNextSequence())

	require.NoError(t, store.AddUnconfirmed(11, "hash-b", 100, nil))
	assert.Equal(t, int64(12), store.GetNextSequence())
}

func TestTxStore_GetNextSequence_WithFailedRecycling(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	// Use sequences 10, 11, 12
	require.NoError(t, store.AddUnconfirmed(10, "hash-10", 100, nil))
	require.NoError(t, store.AddUnconfirmed(11, "hash-11", 100, nil))
	require.NoError(t, store.AddUnconfirmed(12, "hash-12", 100, nil))
	assert.Equal(t, int64(13), store.GetNextSequence())

	// Sequence 10 expires (not consumed on-chain) — recycle it
	require.NoError(t, store.Confirm(10, "hash-10", true))

	// GetNextSequence should return min(13, 10) = 10
	assert.Equal(t, int64(10), store.GetNextSequence())

	// Use the recycled sequence
	require.NoError(t, store.AddUnconfirmed(10, "hash-10-retry", 200, nil))

	// 10 is no longer in failedSequences, so next should be 13 again
	assert.Equal(t, int64(13), store.GetNextSequence())
}

func TestTxStore_GetNextSequence_MultipleFailedPicksSmallest(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store.AddUnconfirmed(11, "h11", 100, nil))
	require.NoError(t, store.AddUnconfirmed(12, "h12", 100, nil))

	// Both 10 and 12 fail
	require.NoError(t, store.Confirm(10, "h10", true))
	require.NoError(t, store.Confirm(12, "h12", true))

	// Should return min(13, 10, 12) = 10
	assert.Equal(t, int64(10), store.GetNextSequence())
}

// --- TxStore: AddUnconfirmed ---

func TestTxStore_AddUnconfirmed_DuplicateReject(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "hash-a", 100, nil))
	err := store.AddUnconfirmed(10, "hash-b", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sequence used")
}

func TestTxStore_AddUnconfirmed_RejectsOldSequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	err := store.AddUnconfirmed(9, "h9", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old sequence")
}

func TestTxStore_AddUnconfirmed_RejectsFutureSequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	err := store.AddUnconfirmed(12, "h12", 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "future sequence")
}

func TestTxStore_AddUnconfirmed_AcceptsRecycledSequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store.Confirm(10, "h10", true)) // failed -> recycled

	// The recycled sequence should be accepted even though nextSequence is 11
	require.NoError(t, store.AddUnconfirmed(10, "h10-retry", 200, nil))
	assert.Equal(t, 1, store.InflightCount())
}

// --- TxStore: Confirm ---

func TestTxStore_Confirm_SuccessRemovesUnconfirmed(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	assert.Equal(t, 1, store.InflightCount())

	require.NoError(t, store.Confirm(10, "h10", false))
	assert.Equal(t, 0, store.InflightCount())
}

func TestTxStore_Confirm_FailedAddsToRecycling(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store.AddUnconfirmed(11, "h11", 100, nil))

	// Seq 10 fails -> goes to failedSequences
	require.NoError(t, store.Confirm(10, "h10", true))
	assert.Equal(t, 1, store.InflightCount()) // only 11 is unconfirmed

	// GetNextSequence should recycle 10
	assert.Equal(t, int64(10), store.GetNextSequence())
}

func TestTxStore_Confirm_FailedBelowOnchainIsNotRecycled(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))

	// Simulate resync that advances on-chain state past seq 10
	store.ResyncNonce(12)

	// Confirm seq 10 as failed — but it's below lastOnchainSequence (12)
	// so it should NOT be recycled
	require.NoError(t, store.Confirm(10, "h10", true))

	// nextSequence should be 12 (from resync), not 10
	assert.Equal(t, int64(12), store.GetNextSequence())
}

func TestTxStore_Confirm_NonExistentSequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	err := store.Confirm(10, "h10", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such unconfirmed sequence")
}

func TestTxStore_Confirm_HashMismatch(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	err := store.Confirm(10, "wrong-hash", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected tx hash")
}

// --- TxStore: Release ---

func TestTxStore_Release_RecyclesSequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	// Simulate: allocate seq 10 via GetNextSequence, but simulation fails before SendTransaction
	seq := store.GetNextSequence()
	assert.Equal(t, int64(10), seq)

	// We must manually add it as unconfirmed first to advance nextSequence,
	// OR we can test Release independently on a sequence that was just allocated.
	// Release can be called even without AddUnconfirmed — it just puts the seq into failedSequences.
	store.Release(10)

	// Next call should recycle seq 10
	assert.Equal(t, int64(10), store.GetNextSequence())
}

func TestTxStore_Release_DoesNotRecycleBelowOnchain(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	// Advance on-chain state past 10
	store.ResyncNonce(15)

	// Release seq 10 — should NOT be recycled because it's below lastOnchainSequence
	store.Release(10)

	// Next sequence should be 15, not 10
	assert.Equal(t, int64(15), store.GetNextSequence())
}

func TestTxStore_Release_CleansUpUnconfirmedEntry(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	assert.Equal(t, 1, store.InflightCount())

	// Release removes from unconfirmed and adds to failed
	store.Release(10)
	assert.Equal(t, 0, store.InflightCount())

	// Sequence 10 should be recyclable
	assert.Equal(t, int64(10), store.GetNextSequence())
}

// --- TxStore: ResyncNonce ---

func TestTxStore_ResyncNonce_AdvancesNextSequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	store.ResyncNonce(15)
	assert.Equal(t, int64(15), store.GetNextSequence())
	assert.Equal(t, int64(15), store.GetLastResyncedNonce())
}

func TestTxStore_ResyncNonce_DoesNotGoBackwards(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	// nextSequence is now 11

	// Resync to 5 — should not go backwards
	store.ResyncNonce(5)
	assert.Equal(t, int64(11), store.GetNextSequence())

	// But lastOnchainSequence should still be updated
	assert.Equal(t, int64(5), store.GetLastResyncedNonce())
}

func TestTxStore_ResyncNonce_CleansStaleFailedSequences(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store.AddUnconfirmed(11, "h11", 100, nil))

	// Both fail
	require.NoError(t, store.Confirm(10, "h10", true))
	require.NoError(t, store.Confirm(11, "h11", true))

	// Resync to 11 — seq 10 should be removed from failedSequences
	// because it's below the on-chain state
	store.ResyncNonce(11)

	// Only seq 11 should remain in failedSequences (11 >= 11)
	assert.Equal(t, int64(11), store.GetNextSequence())
}

func TestTxStore_ResyncNonce_StellarPlusOneOffset(t *testing.T) {
	t.Parallel()

	// Simulate the Stellar-specific pattern:
	// On-chain account.SeqNum = 99 (last USED sequence).
	// The next valid sequence is 100.
	// Caller must pass onchainSeq + 1.
	onchainSeq := int64(99)
	store := NewTxStore(onchainSeq + 1)

	assert.Equal(t, int64(100), store.GetNextSequence(),
		"Stellar: next sequence should be on-chain seq + 1")
}

// --- TxStore: GetUnconfirmed ---

func TestTxStore_GetUnconfirmed_ReturnsSortedBySequence(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store.AddUnconfirmed(11, "h11", 110, nil))
	require.NoError(t, store.AddUnconfirmed(12, "h12", 120, nil))

	unconfirmed := store.GetUnconfirmed()
	require.Len(t, unconfirmed, 3)
	assert.Equal(t, int64(10), unconfirmed[0].Sequence)
	assert.Equal(t, int64(11), unconfirmed[1].Sequence)
	assert.Equal(t, int64(12), unconfirmed[2].Sequence)
}

func TestTxStore_GetUnconfirmed_ReturnsShallowCopy(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	require.NoError(t, store.AddUnconfirmed(10, "h10", 100, nil))

	unconfirmed := store.GetUnconfirmed()
	require.Len(t, unconfirmed, 1)

	// Mutating the returned slice should not affect the store
	unconfirmed[0].Hash = "mutated"

	fresh := store.GetUnconfirmed()
	assert.Equal(t, "h10", fresh[0].Hash)
}

func TestTxStore_GetUnconfirmed_Empty(t *testing.T) {
	t.Parallel()
	store := NewTxStore(10)

	unconfirmed := store.GetUnconfirmed()
	assert.Empty(t, unconfirmed)
}

// --- AccountStore ---

func TestAccountStore_CreateAndGet(t *testing.T) {
	t.Parallel()
	as := NewAccountStore()

	store, err := as.CreateTxStore("GABC123", 100)
	require.NoError(t, err)
	require.NotNil(t, store)

	retrieved := as.GetTxStore("GABC123")
	assert.Equal(t, store, retrieved)
}

func TestAccountStore_DuplicateCreate(t *testing.T) {
	t.Parallel()
	as := NewAccountStore()

	_, err := as.CreateTxStore("GABC123", 100)
	require.NoError(t, err)

	_, err = as.CreateTxStore("GABC123", 200)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestAccountStore_GetNonExistent(t *testing.T) {
	t.Parallel()
	as := NewAccountStore()

	store := as.GetTxStore("GNONEXIST")
	assert.Nil(t, store)
}

func TestAccountStore_GetTotalInflightCount(t *testing.T) {
	t.Parallel()
	as := NewAccountStore()

	store1, _ := as.CreateTxStore("GACC1", 10)
	store2, _ := as.CreateTxStore("GACC2", 20)

	require.NoError(t, store1.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store1.AddUnconfirmed(11, "h11", 100, nil))
	require.NoError(t, store2.AddUnconfirmed(20, "h20", 100, nil))

	assert.Equal(t, 3, as.GetTotalInflightCount())
}

func TestAccountStore_GetAllUnconfirmed(t *testing.T) {
	t.Parallel()
	as := NewAccountStore()

	store1, _ := as.CreateTxStore("GACC1", 10)
	store2, _ := as.CreateTxStore("GACC2", 20)

	require.NoError(t, store1.AddUnconfirmed(10, "h10", 100, nil))
	require.NoError(t, store2.AddUnconfirmed(20, "h20", 100, nil))
	require.NoError(t, store2.AddUnconfirmed(21, "h21", 100, nil))

	all := as.GetAllUnconfirmed()
	assert.Len(t, all["GACC1"], 1)
	assert.Len(t, all["GACC2"], 2)
}

// --- Integration-style scenario ---

func TestTxStore_FullLifecycle(t *testing.T) {
	t.Parallel()

	// Simulate: Stellar account with on-chain SeqNum = 99 (last used).
	// Next expected = 100.
	store := NewTxStore(100)

	// Broadcast 3 transactions
	for i := int64(0); i < 3; i++ {
		seq := store.GetNextSequence()
		assert.Equal(t, int64(100+i), seq)
		require.NoError(t, store.AddUnconfirmed(seq, fmt.Sprintf("hash-%d", seq), uint32(200+i), nil))
	}
	assert.Equal(t, 3, store.InflightCount())

	// Seq 100 confirms successfully
	require.NoError(t, store.Confirm(100, "hash-100", false))
	assert.Equal(t, 2, store.InflightCount())

	// Seq 101 expires (failed) — sequence recycled
	require.NoError(t, store.Confirm(101, "hash-101", true))
	assert.Equal(t, 1, store.InflightCount())

	// Next sequence should recycle 101 (not 103)
	next := store.GetNextSequence()
	assert.Equal(t, int64(101), next)

	// Retry with recycled sequence
	require.NoError(t, store.AddUnconfirmed(101, "hash-101-retry", 300, nil))
	assert.Equal(t, 2, store.InflightCount())

	// Seq 102 confirms, seq 101 retry confirms
	require.NoError(t, store.Confirm(102, "hash-102", false))
	require.NoError(t, store.Confirm(101, "hash-101-retry", false))
	assert.Equal(t, 0, store.InflightCount())

	// Next fresh sequence
	assert.Equal(t, int64(103), store.GetNextSequence())
}

func TestTxStore_ReleaseAndRetryLifecycle(t *testing.T) {
	t.Parallel()
	store := NewTxStore(50)

	// Tx A: seq 50 broadcasts successfully, advancing nextSequence to 51
	require.NoError(t, store.AddUnconfirmed(50, "hash-50", 200, nil))
	assert.Equal(t, int64(51), store.GetNextSequence())

	// Tx B: gets seq 51, but simulation fails before SendTransaction.
	// Without Release, nextSequence is still 51 (GetNextSequence is read-only),
	// so a retry would naturally get 51 again. However, Release is needed
	// when the sequence was tracked in unconfirmedSequences before the failure.
	// Simulate that case:
	require.NoError(t, store.AddUnconfirmed(51, "hash-51", 200, nil))
	// nextSequence is now 52

	// Assembly/signing fails — Release the sequence
	store.Release(51)
	assert.Equal(t, 1, store.InflightCount()) // only seq 50 remains

	// Next call should recycle 51 (not 52)
	seq := store.GetNextSequence()
	assert.Equal(t, int64(51), seq)

	// Retry succeeds
	require.NoError(t, store.AddUnconfirmed(51, "hash-51-retry", 300, nil))
	assert.Equal(t, 2, store.InflightCount())
	assert.Equal(t, int64(52), store.GetNextSequence())
}
