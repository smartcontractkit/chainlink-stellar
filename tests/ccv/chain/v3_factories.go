package ccvchain

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
)

var _ cciptestinterfaces.V3Destination = (*Chain)(nil)

// NewV3Destination is the chainreg.V3DestinationFactory for Stellar. *Chain
// already implements the full V3Destination surface (BuildChainMessage etc.
// live in composable.go, ConfirmExecOnDest in chain.go), so it delegates to
// the shared ImplFactory constructor.
//
// No V3SourceFactory is registered yet: *Chain lacks BuildV3ExtraArgs, and
// Stellar-as-source tcapi cases are additionally blocked on EVM-side executor
// wiring for Stellar-sourced messages and the aggregator rejecting
// token-transfer CCV data (NONEVM-3946).
func NewV3Destination(ctx context.Context, lggr zerolog.Logger, env *deployment.Environment, chainSelector uint64) (cciptestinterfaces.V3Destination, error) {
	chain, err := NewImplFactory().New(ctx, lggr, env, chainSelector)
	if err != nil {
		return nil, fmt.Errorf("create stellar chain %d: %w", chainSelector, err)
	}
	v3Dst, ok := chain.(*Chain)
	if !ok {
		return nil, fmt.Errorf("unexpected stellar chain implementation %T for selector %d", chain, chainSelector)
	}
	return v3Dst, nil
}
