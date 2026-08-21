package bindings

import (
	"testing"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestSorobanInvokePayloadRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []xdr.ScVal
	}{
		{name: "empty"},
		{name: "non-empty", args: []xdr.ScVal{scval.BoolToScVal(true), scval.Uint64ToScVal(42)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := EncodeSorobanInvokePayload("accept_ownership", tt.args)
			if err != nil {
				t.Fatal(err)
			}
			function, args, err := DecodeSorobanInvokePayload(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if function != "accept_ownership" {
				t.Fatalf("function = %q", function)
			}
			if len(args) != len(tt.args) {
				t.Fatalf("args length = %d, want %d", len(args), len(tt.args))
			}
			for i := range args {
				got, err := args[i].MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				want, err := tt.args[i].MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Fatalf("arg %d did not round trip", i)
				}
			}
		})
	}
}

func TestSorobanInvokePayloadRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	_, err := EncodeSorobanInvokePayload("not a symbol", nil)
	if err == nil {
		t.Fatal("expected invalid symbol error")
	}

	_, _, err = DecodeSorobanInvokePayload([]byte{0xff})
	if err == nil {
		t.Fatal("expected malformed payload error")
	}

	empty := xdr.ScVec{}
	emptyPtr := &empty
	encoded, err := (xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &emptyPtr}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = DecodeSorobanInvokePayload(encoded)
	if err == nil {
		t.Fatal("expected empty vector error")
	}

	notSymbol := xdr.ScVec{scval.BoolToScVal(true)}
	notSymbolPtr := &notSymbol
	encoded, err = (xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &notSymbolPtr}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = DecodeSorobanInvokePayload(encoded)
	if err == nil {
		t.Fatal("expected non-symbol function error")
	}
}
