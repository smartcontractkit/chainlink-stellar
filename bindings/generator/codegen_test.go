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
			Fields: []Field{{Name: "subjects", Type: "soroban_sdk::Vec<soroban_sdk::BytesN<16>>"}},
		},
	}}
	out := GenerateTypes("rmn_remote", c)
	mustNotContain(t, out, "import (")
	mustContain(t, out, "type CursedEvent struct")
}

// TestGenerateEnum_UnitOnly is a regression guard: pre-existing unit-only
// enums (CCIPError, MessageDirection, ...) must keep emitting the legacy
// `type X uint32` newtype shape so existing call sites continue to compile.
func TestGenerateEnum_UnitOnly(t *testing.T) {
	c := &Contract{Enums: []Enum{
		{Name: "MessageDirection", Variants: []EnumVariant{
			{Name: "Outbound", Kind: EnumVariantUnit, Value: 0},
			{Name: "Inbound", Kind: EnumVariantUnit, Value: 1},
		}},
	}}
	out := GenerateTypes("test", c)
	mustContain(t, out,
		"type MessageDirection uint32",
		"MessageDirectionOutbound MessageDirection = 0",
		"MessageDirectionInbound MessageDirection = 1",
		"return scval.Uint32ToScVal(uint32(e)), nil",
	)
	mustNotContain(t, out, "type MessageDirection struct")
}

// TestGenerateEnum_BareUnitsHaveDistinctDiscriminants is the end-to-end
// regression test for the MessageDirection bug:
//
//	const (
//	    MessageDirectionOutbound MessageDirection = 0
//	    MessageDirectionInbound  MessageDirection = 0  // <- BUG
//	)
//
// The bug was that bare-identifier unit variants (no explicit `= N`) all
// got Go's zero value 0, so MessageDirectionInbound serialised as the
// same on-chain ScVal::U32 as MessageDirectionOutbound. The fix tracks an
// auto-incrementing counter in parseEnumVariants. This test runs the
// real parser then runs codegen, so a future regression in either step
// would be caught here even if the unit test of codegen above still
// passes (because that one bypasses the parser).
func TestGenerateEnum_BareUnitsHaveDistinctDiscriminants(t *testing.T) {
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
		"MessageDirectionOutbound MessageDirection = 0",
		"MessageDirectionInbound MessageDirection = 1",
	)
	// The exact symptom of the bug: Inbound = 0. Refuse to accept it.
	mustNotContain(t, out,
		"MessageDirectionInbound MessageDirection = 0",
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
		"Amount int64",
		// Struct-variant fields are passed positionally in the same order
		// they appear in Rust, after the discriminant symbol.
		"scval.AddressToScVal(e.Mint.To)",
		"scval.I128ToScVal(e.Mint.Amount)",
	)
}

// TestGenerateEnum_ZeroValue makes sure tuple/return-position uses pick the
// correct Go zero literal: `0` for unit-only, `T{}` for unions. Without
// this, a tuple-returning function whose tuple contains a discriminated
// union would emit `return 0, ...` and fail to compile.
func TestGenerateEnum_ZeroValue(t *testing.T) {
	knownEnumNames = map[string]bool{
		"MessageDirection": true,  // unit-only
		"ReplayKey":        false, // union
	}
	if got := zeroValue("MessageDirection"); got != "0" {
		t.Errorf("unit enum zero: got %q want \"0\"", got)
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

func TestParseEvents_DataFormat(t *testing.T) {
	src := `
#[contractevent(topics = ["forwarder_ReportProcessed"], data_format = "single-value")]
#[derive(Clone)]
pub struct ReportProcessedEvent {
    #[topic]
    pub receiver: Address,
    pub success: bool,
}

#[contractevent(topics = ["forwarder_ConfigSet"])]
#[derive(Clone)]
pub struct ConfigSetEvent {
    pub f: u32,
}
`
	events := parseEvents(src)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].DataFormat != "single-value" {
		t.Errorf("ReportProcessedEvent DataFormat = %q, want single-value", events[0].DataFormat)
	}
	if events[1].DataFormat != "" {
		t.Errorf("ConfigSetEvent DataFormat = %q, want empty (map default)", events[1].DataFormat)
	}
}

func TestGenerateClient_SingleValueEventParser(t *testing.T) {
	c := &Contract{Name: "Forwarder", Events: []Event{{
		Name:       "ReportProcessedEvent",
		Topics:     []string{"forwarder_ReportProcessed"},
		DataFormat: "single-value",
		Fields: []Field{
			{Name: "receiver", Type: "soroban_sdk::Address", Topic: true},
			{Name: "workflow_execution_id", Type: "soroban_sdk::BytesN<32>", Topic: true},
			{Name: "success", Type: "bool"},
		},
	}}}
	out := GenerateClient("cre", c)
	mustContain(t, out,
		"func ParseReportProcessedEvent(e protocolrpc.EventInfo) (*ReportProcessedEvent, error)",
		"v, ok := eventVal.GetB()",
		"result.Success = v",
	)
	mustNotContain(t, out, "event is not a map")
}

func TestGenerateClient_UnknownDataFormatPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for unknown data_format")
		}
	}()
	c := &Contract{Name: "X", Events: []Event{{
		Name:       "E",
		Topics:     []string{"t"},
		DataFormat: "vec",
		Fields:     []Field{{Name: "a", Type: "u32"}},
	}}}
	GenerateClient("x", c)
}
