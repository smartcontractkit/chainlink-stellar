package ccvchain

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/smartcontractkit/chainlink-ccv/build/devenv/cciptestinterfaces"
	"github.com/smartcontractkit/chainlink-deployments-framework/deployment"
)

var (
	_ cciptestinterfaces.V3Destination = (*Chain)(nil)
	_ cciptestinterfaces.V3Source      = (*Chain)(nil)
)

// NewV3Destination is the chainreg.V3DestinationFactory for Stellar. *Chain
// already implements the full V3Destination surface (BuildChainMessage etc.
// live in composable.go, ConfirmExecOnDest in chain.go), so it delegates to
// the shared ImplFactory constructor.
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

// NewV3Source is the chainreg.V3SourceFactory for Stellar. *Chain implements
// the full V3Source surface: BuildV3ExtraArgs and the ChainAsSource methods
// live in composable.go. Send-side assertions beyond the message ID (receipt
// issuers) are populated in SendChainMessage from the OnRamp sent event.
func NewV3Source(ctx context.Context, lggr zerolog.Logger, env *deployment.Environment, chainSelector uint64) (cciptestinterfaces.V3Source, error) {
	chain, err := NewImplFactory().New(ctx, lggr, env, chainSelector)
	if err != nil {
		return nil, fmt.Errorf("create stellar chain %d: %w", chainSelector, err)
	}
	v3Src, ok := chain.(*Chain)
	if !ok {
		return nil, fmt.Errorf("unexpected stellar chain implementation %T for selector %d", chain, chainSelector)
	}
	return v3Src, nil
}
