package mcmsutil

import (
	"testing"

	"github.com/smartcontractkit/chainlink-stellar/bindings/scval"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestEncodeSorobanMCMSInvokePayload_acceptOwnership(t *testing.T) {
	b, err := EncodeSorobanMCMSInvokePayload("accept_ownership", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty XDR payload")
	}
	var sc xdr.ScVal
	if err := sc.UnmarshalBinary(b); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	vec, ok := sc.GetVec()
	if !ok || vec == nil || len(*vec) < 1 {
		t.Fatalf("expected ScVal vec, got %+v", sc)
	}
}

func TestEncodeSorobanMCMSInvokePayload_transferOwnership(t *testing.T) {
	b, err := EncodeSorobanMCMSInvokePayload("transfer_ownership", []xdr.ScVal{
		scval.AddressToScVal("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var sc xdr.ScVal
	if err := sc.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	vec, ok := sc.GetVec()
	if !ok || vec == nil || len(*vec) != 2 {
		t.Fatalf("expected 2-element vec, got %v", vec)
	}
}

// Decode as xdr.ScVal, not xdr.ScVec: args_xdr is the full ScVal::Vec encoding that
// soroban_sdk Vec::<Val>::from_xdr consumes. Unmarshalling into xdr.ScVec reads the SCV_VEC
// discriminant as an element count, so it accepts wrapper-less output and rejects correct output.
func TestEncodeSorobanInvokeArgs_empty(t *testing.T) {
	b, err := EncodeSorobanInvokeArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	var sc xdr.ScVal
	if err := sc.UnmarshalBinary(b); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if sc.Type != xdr.ScValTypeScvVec {
		t.Fatalf("decoded type = %v, want ScvVec", sc.Type)
	}
	vec, ok := sc.GetVec()
	if !ok || vec == nil {
		t.Fatalf("expected ScVal vec, got %+v", sc)
	}
	if len(*vec) != 0 {
		t.Fatalf("expected empty vec, got %d elements", len(*vec))
	}
}

func TestEncodeSorobanInvokeArgs_transferOwnership(t *testing.T) {
	b, err := EncodeSorobanInvokeArgs([]xdr.ScVal{
		scval.AddressToScVal("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var sc xdr.ScVal
	if err := sc.UnmarshalBinary(b); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if sc.Type != xdr.ScValTypeScvVec {
		t.Fatalf("decoded type = %v, want ScvVec", sc.Type)
	}
	vec, ok := sc.GetVec()
	if !ok || vec == nil || len(*vec) != 1 {
		t.Fatalf("expected 1-element vec, got %v", vec)
	}
}
