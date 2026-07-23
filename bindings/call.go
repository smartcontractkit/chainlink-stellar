package bindings

import (
	"context"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Void is the result type of calls to contract functions that return nothing.
type Void struct{}

// Call is a built, not-yet-executed contract invocation. It is returned by
// every method of a two-step-style generated client. The caller chooses how
// to execute it:
//
//   - Result runs the call as a local simulation: free, no signature, no
//     transaction. Any state changes are computed and then DISCARDED by the
//     network — never use it for a call that must take effect on-chain.
//   - SignAndSend submits the call as a fee-paying transaction through the
//     invoker (which signs it) and decodes the return value from the
//     transaction result.
//
// Soroban's contract spec carries no view/mutability marker, so the choice
// of read versus write cannot be made at generation time; Call makes it
// explicit at each call site instead.
type Call[T any] struct {
	invoker    Invoker
	contractID string
	function   string
	args       []xdr.ScVal
	decode     func(*xdr.ScVal) (T, error)
}

// NewCall builds a Call. It is intended for use by generated bindings.
func NewCall[T any](invoker Invoker, contractID, function string, args []xdr.ScVal, decode func(*xdr.ScVal) (T, error)) *Call[T] {
	return &Call[T]{
		invoker:    invoker,
		contractID: contractID,
		function:   function,
		args:       args,
		decode:     decode,
	}
}

// Function returns the contract function name this call targets.
func (c *Call[T]) Function() string { return c.function }

// Result executes the call as a free local simulation and decodes its return
// value. State changes are discarded by the network; use SignAndSend for any
// call that must take effect on-chain.
func (c *Call[T]) Result(ctx context.Context) (T, error) {
	result, err := c.invoker.SimulateContract(ctx, c.contractID, c.function, c.args)
	if err != nil {
		var zero T
		return zero, err
	}
	return c.decode(result)
}

// SignAndSend submits the call as a signed, fee-paying transaction and
// decodes its return value from the transaction result.
func (c *Call[T]) SignAndSend(ctx context.Context) (T, error) {
	result, err := c.invoker.InvokeContract(ctx, c.contractID, c.function, c.args)
	if err != nil {
		var zero T
		return zero, err
	}
	return c.decode(result)
}
