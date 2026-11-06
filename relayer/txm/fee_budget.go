package txm

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
)

// ErrCumulativeFeeExceeded reports that reserving another envelope's fee would
// push a transaction's total spend past its configured maximum.
var ErrCumulativeFeeExceeded = errors.New("cumulative fee budget exceeded")

// feeBudget bounds the total fee one transaction may spend across attempts.
// Fees are reserved under the envelope hash before send and settle to the
// actual FeeCharged on confirmation. A nil budget or a zero max disables it.
type feeBudget struct {
	mu           sync.Mutex
	max          *big.Int
	spent        *big.Int
	reservations map[string]*big.Int
}

func newFeeBudget(max uint64) *feeBudget {
	if max == 0 {
		return nil
	}
	return &feeBudget{
		max:          new(big.Int).SetUint64(max),
		spent:        new(big.Int),
		reservations: make(map[string]*big.Int),
	}
}

// reserve records fee for hash, failing if it would exceed the budget.
// Reserving the same envelope twice is a no-op because it is already counted.
func (b *feeBudget) reserve(hash string, fee int64) error {
	if b == nil {
		return nil
	}
	if fee < 0 {
		return fmt.Errorf("negative fee %d for envelope %s", fee, hash)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.reservations[hash]; ok {
		return nil
	}
	committed := new(big.Int).Set(b.spent)
	for _, r := range b.reservations {
		committed.Add(committed, r)
	}
	total := new(big.Int).Add(committed, big.NewInt(fee))
	if total.Cmp(b.max) > 0 {
		return fmt.Errorf("%w: committed %s plus new fee %d totals %s, max %s",
			ErrCumulativeFeeExceeded, committed, fee, total, b.max)
	}
	b.reservations[hash] = big.NewInt(fee)
	return nil
}

// settle replaces hash's reservation with the fee actually charged.
func (b *feeBudget) settle(hash string, fee int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.reservations, hash)
	b.spent.Add(b.spent, big.NewInt(fee))
}

// release drops hash's reservation without charging it, freeing headroom.
func (b *feeBudget) release(hash string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.reservations, hash)
}
