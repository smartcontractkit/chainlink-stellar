package txm

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"

	"github.com/smartcontractkit/chainlink-stellar/relayer/config"
)

// ErrCumulativeFeeExceeded reports that reserving another envelope's fee would
// push a transaction's total spend past its configured maximum.
var ErrCumulativeFeeExceeded = errors.New("cumulative fee budget exceeded")

// feeBudget bounds the total fee one transaction may spend across attempts.
// Fees are reserved under the envelope hash before send and settle to the
// actual FeeCharged on confirmation. Envelopes that share a sequence number can
// land at most once, so each sequence counts only its largest live reservation.
// A nil budget or a zero max disables it.
type feeBudget struct {
	mu           sync.Mutex
	max          *big.Int
	spent        *big.Int
	reservations map[string]reservation
}

// reservation is one envelope's reserved fee and the sequence it was signed for.
type reservation struct {
	seq int64
	fee *big.Int
}

func newFeeBudget(max uint64) *feeBudget {
	if max == 0 {
		return nil
	}
	return &feeBudget{
		max:          new(big.Int).SetUint64(max),
		spent:        new(big.Int),
		reservations: make(map[string]reservation),
	}
}

// newTxFeeBudget returns the budget for one request. An explicit MaxCumulativeFee
// is a hard cap. Otherwise the budget derives from the cap FeeStrategy.ResourceFee
// applies to this request: its own when set, else the node's. With neither, the
// budget is disabled.
func (s *StellarTxm) newTxFeeBudget(requestMaxResourceFee uint64) *feeBudget {
	if explicit := *s.config.MaxCumulativeFee; explicit > 0 {
		return newFeeBudget(uint64(explicit))
	}
	resourceCap := s.feeStrat.MaxResourceFee
	if requestMaxResourceFee > 0 {
		resourceCap = int64(min(requestMaxResourceFee, math.MaxInt64))
	}
	return newFeeBudget(uint64(*config.DeriveMaxCumulativeFee(resourceCap, *s.config.MaxTxRetryAttempts)))
}

// reserve records fee for hash, signed for sequence seq, failing if it would
// exceed the budget. Reserving the same envelope twice is a no-op because it is
// already counted.
func (b *feeBudget) reserve(seq int64, hash string, fee int64) error {
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
	// At most one envelope per sequence can land, so each sequence commits only its
	// largest reservation, and this envelope adds only what it raises that by.
	largest := make(map[int64]*big.Int)
	for _, r := range b.reservations {
		if cur, ok := largest[r.seq]; !ok || r.fee.Cmp(cur) > 0 {
			largest[r.seq] = r.fee
		}
	}
	committed := new(big.Int).Set(b.spent)
	for _, f := range largest {
		committed.Add(committed, f)
	}
	added := big.NewInt(fee)
	if cur, ok := largest[seq]; ok {
		added.Sub(added, cur)
		if added.Sign() < 0 {
			added.SetInt64(0)
		}
	}
	total := new(big.Int).Add(committed, added)
	if total.Cmp(b.max) > 0 {
		return fmt.Errorf("%w: committed %s plus new fee %d totals %s, max %s",
			ErrCumulativeFeeExceeded, committed, fee, total, b.max)
	}
	b.reservations[hash] = reservation{seq: seq, fee: big.NewInt(fee)}
	return nil
}

// settle charges the fee actually taken for hash. That envelope is in a ledger, so
// no other envelope at its sequence can land: their reservations are dropped too.
func (b *feeBudget) settle(hash string, fee int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.reservations[hash]; ok {
		for h, other := range b.reservations {
			if other.seq == r.seq {
				delete(b.reservations, h)
			}
		}
	}
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
