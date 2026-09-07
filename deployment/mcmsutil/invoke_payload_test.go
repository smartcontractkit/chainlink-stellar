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

func TestEncodeSorobanInvokeArgs_empty(t *testing.T) {
	b, err := EncodeSorobanInvokeArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	var vec xdr.ScVec
	if err := vec.UnmarshalBinary(b); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if len(vec) != 0 {
		t.Fatalf("expected empty vec, got %d elements", len(vec))
	}
}

func TestEncodeSorobanInvokeArgs_transferOwnership(t *testing.T) {
	b, err := EncodeSorobanInvokeArgs([]xdr.ScVal{
		scval.AddressToScVal("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var vec xdr.ScVec
	if err := vec.UnmarshalBinary(b); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if len(vec) != 1 {
		t.Fatalf("expected 1-element vec, got %d", len(vec))
	}
}
