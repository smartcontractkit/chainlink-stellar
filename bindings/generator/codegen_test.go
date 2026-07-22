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

// TestGenerateEnum_UnitOnly is a regression guard: genuine C-style int enums
// (every variant unit-shaped AND at least one explicit `= N` discriminant,
// e.g. CCIPError, MessageExecutionState) must keep emitting the legacy
// `type X uint32` newtype shape so existing call sites continue to compile.
func TestGenerateEnum_UnitOnly(t *testing.T) {
	c := &Contract{Enums: []Enum{
		{Name: "MessageExecutionState", Variants: []EnumVariant{
			{Name: "Untouched", Kind: EnumVariantUnit, Value: 0, Explicit: true},
			{Name: "InProgress", Kind: EnumVariantUnit, Value: 1, Explicit: true},
		}},
	}}
	out := GenerateTypes("test", c)
	mustContain(t, out,
		"type MessageExecutionState uint32",
		"MessageExecutionStateUntouched MessageExecutionState = 0",
		"MessageExecutionStateInProgress MessageExecutionState = 1",
		"return scval.Uint32ToScVal(uint32(e)), nil",
	)
	mustNotContain(t, out, "type MessageExecutionState struct")
}

// TestGenerateEnum_BareUnitOnlyEmitsSymbolicUnion is the end-to-end
// regression test for the symbolic-union bug proven on a live devnet A/B
// test against contracts/common/interfaces/src/data_feeds_cache.rs's
// `Bound` enum (consumed by find_round): sending ScVal::U32 produces
// `Error(WasmVm, InvalidAction) — UnreachableCodeReached` on-chain; the
// correct wire value is ScVal::Vec([Symbol(<VariantName>)]).
//
// The bug was that the generator classified ANY unit-only enum (IsUnit) as
// a C-style int enum and emitted `type X uint32`, regardless of whether any
// variant actually had an explicit `= N` discriminant. Soroban itself only
// uses ScVal::U32 when a discriminant is explicit; a unit-only enum with no
// discriminant anywhere (a "symbolic union", e.g. MessageDirection here, or
// Bound in the real contract) is encoded as a discriminated union just like
// a payload-bearing enum. This test runs the real parser then runs codegen,
// exercising the same MessageDirection shape that was silently broken in
// production (contracts/common/pool/src/types.rs).
func TestGenerateEnum_BareUnitOnlyEmitsSymbolicUnion(t *testing.T) {
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
		"scval.SymbolToScVal(\"Outbound\")",
		"scval.SymbolToScVal(\"Inbound\")",
	)
	// Critical: the broken behaviour must be gone — no U32 shape anywhere.
	mustNotContain(t, out,
		"type MessageDirection uint32",
		"MessageDirectionOutbound MessageDirection = 0",
		"MessageDirectionInbound MessageDirection = 1",
		"scval.Uint32ToScVal",
	)
}

// TestGenerateEnum_Bound mirrors the exact enum that was proven broken
// on-chain (contracts/common/interfaces/src/data_feeds_cache.rs's Bound,
// consumed by find_round): a unit-only enum with no explicit discriminant.
// Must emit the Vec[Symbol] discriminated-union shape, never U32.
func TestGenerateEnum_Bound(t *testing.T) {
	src := `
#[soroban_sdk::contracttype(export = false)]
pub enum Bound {
    AtOrBefore,
    AtOrAfter,
}
`
	c := &Contract{Enums: parseEnums(src)}
	out := GenerateTypes("test", c)
	mustContain(t, out,
		"type Bound struct {",
		"AtOrBefore *BoundAtOrBefore",
		"AtOrAfter *BoundAtOrAfter",
		"scval.SymbolToScVal(\"AtOrBefore\")",
		"scval.SymbolToScVal(\"AtOrAfter\")",
		"scval.VecToScVal(items)",
	)
	mustNotContain(t, out,
		"type Bound uint32",
		"scval.Uint32ToScVal",
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
		"MessageExecutionState": true,  // int enum (explicit discriminants)
		"ReplayKey":             false, // union
	}
	if got := zeroValue("MessageExecutionState"); got != "0" {
		t.Errorf("int enum zero: got %q want \"0\"", got)
	}
	if got := zeroValue("ReplayKey"); got != "ReplayKey{}" {
		t.Errorf("union enum zero: got %q want \"ReplayKey{}\"", got)
	}
}

// TestGenerateAliasTypes covers `pub type X = soroban_sdk::BytesN<N>;`
// aliases: each must emit a defined `[N]byte` Go type with ToScVal/FromScVal
// converters, matching the shape existing default struct-field cases expect.
func TestGenerateAliasTypes(t *testing.T) {
	c := &Contract{Aliases: []TypeAlias{
		{Name: "DataId", Target: "soroban_sdk::BytesN<16>"},
		{Name: "WorkflowName", Target: "soroban_sdk::BytesN<10>"},
	}}
	out := GenerateTypes("data_feeds_cache", c)
	mustContain(t, out,
		"type DataId [16]byte",
		"func (v DataId) ToScVal() (xdr.ScVal, error)",
		"scval.Bytes16ToScVal([16]byte(v))",
		"func DataIdFromScVal(val xdr.ScVal) (*DataId, error)",
		"type WorkflowName [10]byte",
		"scval.Bytes10ToScVal([10]byte(v))",
	)
}

// TestGenerateI256Field covers `soroban_sdk::I256` struct fields: they must
// map to Go `*big.Int` and encode/decode via the scval.I256ToScVal /
// scval.I256FromScVal helpers, with "math/big" imported.
func TestGenerateI256Field(t *testing.T) {
	c := &Contract{Structs: []Struct{{
		Name:   "RoundData",
		Fields: []Field{{Name: "answer", Type: "soroban_sdk::I256"}, {Name: "round_id", Type: "u64"}},
	}}}
	out := GenerateTypes("data_feeds_cache", c)
	mustContain(t, out,
		"Answer *big.Int",
		"scval.MustToScVal(scval.I256ToScVal(s.Answer))",
		"scval.I256FromScVal(entry.Val)",
		`"math/big"`,
	)
}

// TestGenerateEventI256Field covers `soroban_sdk::I256` event fields (e.g.
// ReportUpdated.answer in the real contract inventory): the generated event
// parser must decode via the qualified scval.I256FromScVal helper and assign
// the already-*big.Int result directly, not deref a struct pointer.
func TestGenerateEventI256Field(t *testing.T) {
	c := &Contract{Events: []Event{
		{
			Name:   "ReportUpdated",
			Topics: []string{"report_updated"},
			Fields: []Field{
				{Name: "data_id", Type: "DataId"},
				{Name: "round_id", Type: "u64"},
				{Name: "timestamp", Type: "u64"},
				{Name: "answer", Type: "soroban_sdk::I256"},
				{Name: "ledger_seq", Type: "u32"},
				{Name: "primary", Type: "bool"},
			},
		},
	}}
	out := GenerateClient("data_feeds_cache", c, nil)
	mustContain(t, out,
		"v, err := scval.I256FromScVal(entry.Val)",
		"result.Answer = v\n",
	)
	mustNotContain(t, out,
		"v, err := I256FromScVal(entry.Val)",
		"result.Answer = *v\n",
	)
}

// TestGenerateAliasU256Panics covers `pub type X = soroban_sdk::U256;`
// aliases: U256 has no mapping anywhere else in the pipeline
// (rustTypeToGo/getToScValConverter/generateFromScValField), so
// generateAlias must panic loudly instead of silently emitting a
// type with no encode/decode support.
func TestGenerateAliasU256Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for unsupported U256 alias target, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "U256") {
			t.Fatalf("panic message %v does not mention U256", r)
		}
	}()
	c := &Contract{Aliases: []TypeAlias{{Name: "BigThing", Target: "soroban_sdk::U256"}}}
	GenerateTypes("data_feeds_cache", c)
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
