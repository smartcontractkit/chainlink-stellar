package main

import (
	"strings"
	"testing"
)

// TestGenerateTypes_eventsOnlyNoImports ensures contracts whose types.go only
// contains event structs (no #[contracttype] structs/enums in the interface)
// do not import fmt/scval/xdr.
func TestGenerateTypes_eventsOnlyNoImports(t *testing.T) {
	c := &Contract{Events: []Event{
		{
			Name:   "CursedEvent",
			Topics: []string{"rmn_Cursed"},
			Fields: []EventField{{Field: Field{Name: "subjects", Type: "soroban_sdk::Vec<soroban_sdk::BytesN<16>>"}}},
		},
	}}
	out := GenerateTypes("rmn_remote", c)
	mustNotContain(t, out, "import (")
	mustContain(t, out, "type CursedEvent struct")
}

// TestGenerateEnum_IntReprEmitsU32 is a regression guard: a #[contracttype]
// enum whose every variant is a unit with an EXPLICIT `= N` discriminant (the
// soroban-sdk derive_type_enum_int path) must emit the `type X uint32` newtype
// shape with ScVal::U32 wire encoding. Real examples: MessageExecutionState,
// TransmissionState, Bound.
func TestGenerateEnum_IntReprEmitsU32(t *testing.T) {
	c := &Contract{Enums: []Enum{
		{Name: "Bound", Variants: []EnumVariant{
			{Name: "AtOrBefore", Kind: EnumVariantUnit, Value: 0, Explicit: true},
			{Name: "AtOrAfter", Kind: EnumVariantUnit, Value: 1, Explicit: true},
		}},
	}}
	out := GenerateTypes("test", c)
	mustContain(t, out,
		"type Bound uint32",
		"BoundAtOrBefore Bound = 0",
		"BoundAtOrAfter Bound = 1",
		"return scval.Uint32ToScVal(uint32(e)), nil",
	)
	mustNotContain(t, out, "type Bound struct")
}

// TestGenerateEnum_BareUnitsEmitUnion is the end-to-end regression test for the
// MessageDirection wire-encoding bug. A #[contracttype] enum with bare unit
// variants (no explicit `= N`) is encoded by soroban-sdk as
// ScVal::Vec([Symbol(<VariantName>)]) (derive_type_enum), NOT ScVal::U32. The
// old generator emitted U32, so the contract decoded the argument as a Vec<Val>
// and trapped (HostError WasmVm InvalidAction / UnreachableCodeReached). This
// test runs the real parser then codegen, so a regression in either step is
// caught here. The "distinct encoding per variant" intent is now satisfied by
// distinct discriminant Symbols, not numeric values.
func TestGenerateEnum_BareUnitsEmitUnion(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
pub enum MessageDirection {
    Outbound,
    Inbound,
}
`
	c := &Contract{Enums: parseEnums(src)}
	out := GenerateTypes("test", c)
	mustContain(t, out,
		"type MessageDirection struct {",
		"Outbound *MessageDirectionOutbound",
		"Inbound *MessageDirectionInbound",
		"type MessageDirectionOutbound struct{}",
		"type MessageDirectionInbound struct{}",
		// Each variant encodes as a one-element vec holding its name Symbol.
		`scval.SymbolToScVal("Outbound")`,
		`scval.SymbolToScVal("Inbound")`,
		"scval.VecToScVal(items)",
	)
	// The broken U32 encoding must be gone.
	mustNotContain(t, out,
		"type MessageDirection uint32",
		"return scval.Uint32ToScVal(uint32(e)), nil",
	)
}

// TestGenerateEnum_TupleEmitsUnion is the core regression test for the
// reviewer's report: an enum with a tuple variant must emit a discriminated
// union, must use ScVec(Symbol+payloads), and must NOT use Uint32ToScVal.
func TestGenerateEnum_TupleEmitsUnion(t *testing.T) {
	c := &Contract{Enums: []Enum{
		{Name: "ReplayKey", Variants: []EnumVariant{
			{Name: "SeenHash", Kind: EnumVariantTuple, Payload: []Field{
				{Type: "soroban_sdk::BytesN<32>"},
			}},
		}},
	}}
	out := GenerateTypes("test", c)

	mustContain(t, out,
		"type ReplayKey struct {",
		"SeenHash *ReplayKeySeenHash",
		"type ReplayKeySeenHash struct {",
		"Field0 [32]byte",
		// ToScVal: discriminant symbol + Bytes32 payload, returned as a vec.
		"scval.SymbolToScVal(\"SeenHash\")",
		"scval.Bytes32ToScVal(e.SeenHash.Field0)",
		"scval.VecToScVal(items)",
		// FromScVal: parse vec, dispatch on tag symbol, decode payload.
		"vecPtr, ok := val.GetVec()",
		"tag, err := scval.SymbolFromScVal(vec[0])",
		"case \"SeenHash\":",
		"scval.Bytes32FromScVal(vec[1])",
	)
	// Critical: the broken behaviour must be gone.
	mustNotContain(t, out,
		"type ReplayKey uint32",
		"return scval.Uint32ToScVal(uint32(e)), nil",
	)
}

// TestGenerateEnum_MixedUnitAndTuple covers PoolDataKey: the union path must
// handle unit variants alongside tuple variants without losing the variant.
func TestGenerateEnum_MixedUnitAndTuple(t *testing.T) {
	c := &Contract{Enums: []Enum{
		{Name: "PoolDataKey", Variants: []EnumVariant{
			{Name: "Token", Kind: EnumVariantUnit},
			{Name: "RemoteChainConfig", Kind: EnumVariantTuple, Payload: []Field{{Type: "u64"}}},
			{Name: "SupportedChains", Kind: EnumVariantUnit},
		}},
	}}
	out := GenerateTypes("test", c)

	mustContain(t, out,
		"type PoolDataKey struct {",
		"Token *PoolDataKeyToken",
		"RemoteChainConfig *PoolDataKeyRemoteChainConfig",
		"SupportedChains *PoolDataKeySupportedChains",
		"type PoolDataKeyToken struct{}",
		"type PoolDataKeyRemoteChainConfig struct {",
		"Field0 uint64",
		"type PoolDataKeySupportedChains struct{}",
		// Each variant's ToScVal branch should emit a vec with the right tag.
		"scval.SymbolToScVal(\"Token\")",
		"scval.SymbolToScVal(\"RemoteChainConfig\")",
		"scval.SymbolToScVal(\"SupportedChains\")",
		// FromScVal must dispatch all three variants.
		"case \"Token\":",
		"case \"RemoteChainConfig\":",
		"case \"SupportedChains\":",
		// Payload-bearing variant must check the right element count.
		"PoolDataKey::RemoteChainConfig: expected 2 elements",
	)
	// Unit variants must accept exactly 1 element (just the symbol).
	mustContain(t, out,
		"PoolDataKey::Token: expected 1 elements",
		"PoolDataKey::SupportedChains: expected 1 elements",
	)
}

// TestGenerateEnum_StructVariant covers the struct-variant payload shape.
// We exercise it via codegen even though no current contract enum uses it,
// because the parser supports it and we want symmetric encode/decode.
func TestGenerateEnum_StructVariant(t *testing.T) {
	c := &Contract{Enums: []Enum{
		{Name: "Op", Variants: []EnumVariant{
			{Name: "Mint", Kind: EnumVariantStruct, Payload: []Field{
				{Name: "to", Type: "soroban_sdk::Address"},
				{Name: "amount", Type: "i128"},
			}},
		}},
	}}
	out := GenerateTypes("test", c)

	mustContain(t, out,
		"type Op struct {",
		"Mint *OpMint",
		"type OpMint struct {",
		"To string",
		"Amount *big.Int",
		// An i128 payload used solely in an enum variant must still pull in
		// math/big; the import scan covers enum variant payloads, not just
		// struct/event fields.
		"\"math/big\"",
		// Struct-variant fields are passed positionally in the same order
		// they appear in Rust, after the discriminant symbol.
		"scval.AddressToScVal(e.Mint.To)",
		"scval.I128ToScVal(e.Mint.Amount)",
	)
}

// TestGenerateEnum_ZeroValue makes sure tuple/return-position uses pick the
// correct Go zero literal: `0` for int-repr (U32 newtype) enums, `T{}` for
// discriminated-union enums. Without this, a tuple-returning function whose
// tuple contains a discriminated union would emit `return 0, ...` and fail to
// compile.
func TestGenerateEnum_ZeroValue(t *testing.T) {
	knownEnumNames = map[string]bool{
		"Bound":            true,  // int-repr (U32 newtype)
		"MessageDirection": false, // bare unit -> union (struct)
		"ReplayKey":        false, // tuple-variant -> union (struct)
	}
	if got := zeroValue("Bound"); got != "0" {
		t.Errorf("int-repr enum zero: got %q want \"0\"", got)
	}
	if got := zeroValue("MessageDirection"); got != "MessageDirection{}" {
		t.Errorf("bare-unit union enum zero: got %q want \"MessageDirection{}\"", got)
	}
	if got := zeroValue("ReplayKey"); got != "ReplayKey{}" {
		t.Errorf("union enum zero: got %q want \"ReplayKey{}\"", got)
	}
}

// helpers

func mustContain(t *testing.T, s string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(s, n) {
			t.Errorf("output missing required snippet:\n  %q", n)
		}
	}
}

func mustNotContain(t *testing.T, s string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(s, n) {
			t.Errorf("output unexpectedly contains:\n  %q", n)
		}
	}
}
