package chainwriter

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/smartcontractkit/chainlink-common/pkg/types/core"
)

type TransmitterSelector interface {
	Next() (string, error)
}

type roundRobinSelector struct {
	accounts []string
	counter  uint64
}

func NewRoundRobinSelector(ctx context.Context, ks core.Keystore) (TransmitterSelector, error) {
	accounts, err := ks.Accounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch accounts from keystore: %w", err)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("no accounts found in keystore")
	}

	return &roundRobinSelector{
		accounts: accounts,
		counter:  0,
	}, nil
}

func (s *roundRobinSelector) Next() (string, error) {
	if len(s.accounts) == 0 {
		return "", fmt.Errorf("no accounts available")
	}
	idx := atomic.AddUint64(&s.counter, 1) - 1
	return s.accounts[idx%uint64(len(s.accounts))], nil
}
