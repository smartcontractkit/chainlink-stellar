package txm

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeeBudget_Disabled(t *testing.T) {
	t.Parallel()

	var nilBudget *feeBudget
	require.NoError(t, nilBudget.reserve(1, "h", 100))
	assert.NotPanics(t, func() {
		nilBudget.settle("h", 100)
		nilBudget.release("h")
	})

	assert.Nil(t, newFeeBudget(0), "a zero max disables the budget")
}

func TestFeeBudget_ReserveUnderLimit(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.NoError(t, b.reserve(1, "a", 600))
	require.NoError(t, b.reserve(2, "b", 400))
	assert.Zero(t, b.spent.Int64(), "reservations are not spend until settled")
	assert.Len(t, b.reservations, 2)
}

func TestFeeBudget_ReserveExceedingReturnsError(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.NoError(t, b.reserve(1, "a", 600))

	err := b.reserve(2, "b", 401)
	require.ErrorIs(t, err, ErrCumulativeFeeExceeded)
	assert.Contains(t, err.Error(), "1000", "the message carries the max")
	assert.Contains(t, err.Error(), "1001", "the message carries the attempted total")
	assert.NotContains(t, b.reservations, "b", "a rejected reservation is not recorded")
}

func TestFeeBudget_ReserveSameHashIsNoOp(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.NoError(t, b.reserve(1, "a", 1_000))
	require.NoError(t, b.reserve(1, "a", 1_000), "re-reserving a counted envelope must not fail")
	assert.Equal(t, int64(1_000), b.reservations["a"].fee.Int64())
}

func TestFeeBudget_ReserveNegativeFeeIsRejected(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.Error(t, b.reserve(1, "a", -1))
	assert.Empty(t, b.reservations)
}

func TestFeeBudget_SameSequenceCountsLargestOnly(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.NoError(t, b.reserve(1, "a", 900))
	require.NoError(t, b.reserve(1, "b", 800), "a cheaper envelope at the same sequence adds nothing")
	require.NoError(t, b.reserve(1, "c", 1_000), "a dearer one adds only the difference")
	assert.Len(t, b.reservations, 3)
	require.ErrorIs(t, b.reserve(2, "d", 1), ErrCumulativeFeeExceeded, "the sequence still holds its largest fee")

	b.release("c")
	require.NoError(t, b.reserve(2, "d", 100), "after release the sequence holds the next largest, 900")
	require.ErrorIs(t, b.reserve(3, "e", 1), ErrCumulativeFeeExceeded)
}

func TestFeeBudget_SettleDropsWholeSequence(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.NoError(t, b.reserve(1, "a", 600))
	require.NoError(t, b.reserve(1, "b", 600))
	require.NoError(t, b.reserve(2, "c", 300))

	b.settle("b", 500)
	assert.Equal(t, int64(500), b.spent.Int64())
	assert.Equal(t, map[string]reservation{"c": b.reservations["c"]}, b.reservations,
		"no other envelope at a landed sequence can land")
	require.NoError(t, b.reserve(3, "d", 200))
	require.ErrorIs(t, b.reserve(4, "e", 1), ErrCumulativeFeeExceeded)
}

func TestFeeBudget_SettleRemovesReservationAndAddsSpend(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	require.NoError(t, b.reserve(1, "a", 600))
	b.settle("a", 500)

	assert.Equal(t, int64(500), b.spent.Int64())
	assert.Empty(t, b.reservations)
	require.NoError(t, b.reserve(2, "b", 500), "settled spend plus the new reservation fits exactly")
	require.ErrorIs(t, b.reserve(3, "c", 1), ErrCumulativeFeeExceeded)
}

func TestFeeBudget_SettleWithoutReservationStillAddsSpend(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(1_000)
	b.settle("never-reserved", 300)

	assert.Equal(t, int64(300), b.spent.Int64(), "a charged fee counts even if no reservation was seen")
	require.NoError(t, b.reserve(1, "a", 700))
	require.ErrorIs(t, b.reserve(2, "b", 1), ErrCumulativeFeeExceeded)
}

func TestFeeBudget_ReleaseFreesHeadroom(t *testing.T) {
	t.Parallel()

	b := newFeeBudget(100)
	require.NoError(t, b.reserve(1, "a", 100))
	require.ErrorIs(t, b.reserve(2, "b", 100), ErrCumulativeFeeExceeded)

	b.release("a")
	assert.Zero(t, b.spent.Int64(), "release is not a charge")
	require.NoError(t, b.reserve(2, "b", 100))
}

func TestFeeBudget_ConcurrentReserveIsSafe(t *testing.T) {
	t.Parallel()

	const n = 100
	b := newFeeBudget(n) // one stroop per reservation: all n must fit
	var failures atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := b.reserve(int64(i), fmt.Sprintf("h%d", i), 1); err != nil {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Zero(t, failures.Load())
	assert.Len(t, b.reservations, n)
}
