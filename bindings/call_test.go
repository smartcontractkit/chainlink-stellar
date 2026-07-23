package bindings

import (
	"context"
	"testing"

	protocolrpc "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type recordingInvoker struct {
	simulated []string
	invoked   []string
	result    *xdr.ScVal
}

func (r *recordingInvoker) InvokeContract(ctx context.Context, contractID, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	r.invoked = append(r.invoked, functionName)
	return r.result, nil
}

func (r *recordingInvoker) SimulateContract(ctx context.Context, contractID, functionName string, args []xdr.ScVal) (*xdr.ScVal, error) {
	r.simulated = append(r.simulated, functionName)
	return r.result, nil
}

func (r *recordingInvoker) GetEvents(ctx context.Context, contractID string, startLedger uint32, topics []string) ([]protocolrpc.EventInfo, error) {
	return nil, nil
}

func TestCallRoutesResultToSimulateAndSignAndSendToInvoke(t *testing.T) {
	u32 := xdr.Uint32(7)
	inv := &recordingInvoker{result: &xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &u32}}
	call := NewCall(inv, "CCONTRACT", "version", nil, func(result *xdr.ScVal) (uint32, error) {
		v, _ := result.GetU32()
		return uint32(v), nil
	})

	got, err := call.Result(context.Background())
	if err != nil || got != 7 {
		t.Fatalf("Result = %d, %v; want 7, nil", got, err)
	}
	got, err = call.SignAndSend(context.Background())
	if err != nil || got != 7 {
		t.Fatalf("SignAndSend = %d, %v; want 7, nil", got, err)
	}

	if len(inv.simulated) != 1 || inv.simulated[0] != "version" {
		t.Errorf("Result routed to %v, want one simulation of version", inv.simulated)
	}
	if len(inv.invoked) != 1 || inv.invoked[0] != "version" {
		t.Errorf("SignAndSend routed to %v, want one invocation of version", inv.invoked)
	}
	if call.Function() != "version" {
		t.Errorf("Function() = %q, want version", call.Function())
	}
}
