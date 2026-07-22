package main

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseEnums_PureUnit covers the real-world MessageDirection shape: a
// unit-only enum with NO explicit discriminant on any variant. Structurally
// IsUnit() is true, but Soroban does not encode this as ScVal::U32 — with no
// `= N` anywhere, it's a "symbolic union" that Soroban encodes as
// ScVal::Vec([Symbol(<VariantName>)]). IsIntEnum() must report false here;
// only HasExplicitDiscriminant()==true would flip it to the U32 encoding.
//
// This also pins the Rust auto-numbering rule: bare unit variants get
// sequential Value starting at 0 in declaration order (used only for
// internal bookkeeping / IsUnit-derived consistency now that this shape
// no longer emits a Go const block keyed on Value).
func TestParseEnums_PureUnit(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
#[derive(Debug, Clone)]
pub enum MessageDirection {
    Outbound,
    Inbound,
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	got := enums[0]
	if got.Name != "MessageDirection" {
		t.Fatalf("name: %q", got.Name)
	}
	if !got.IsUnit() {
		t.Fatalf("expected IsUnit=true")
	}
	if got.HasExplicitDiscriminant() {
		t.Fatalf("expected HasExplicitDiscriminant=false: no variant has `= N`")
	}
	if got.IsIntEnum() {
		t.Fatalf("expected IsIntEnum=false: symbolic union with no explicit discriminant must not encode as U32")
	}
	want := []EnumVariant{
		{Name: "Outbound", Kind: EnumVariantUnit, Value: 0},
		{Name: "Inbound", Kind: EnumVariantUnit, Value: 1},
	}
	if !reflect.DeepEqual(got.Variants, want) {
		t.Fatalf("variants: got %+v want %+v", got.Variants, want)
	}
}

// TestParseEnums_ExplicitDiscriminantIsIntEnum covers the genuine C-style
// int enum case (e.g. MessageExecutionState on offramp): every variant is
// unit-shaped AND at least one carries an explicit `= N`. This is the only
// shape that should encode as ScVal::U32; IsIntEnum() must report true.
func TestParseEnums_ExplicitDiscriminantIsIntEnum(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
pub enum MessageExecutionState {
    Untouched = 0,
    InProgress = 1,
    Success = 2,
    Failure = 3,
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	got := enums[0]
	if !got.IsUnit() {
		t.Fatalf("expected IsUnit=true")
	}
	if !got.HasExplicitDiscriminant() {
		t.Fatalf("expected HasExplicitDiscriminant=true: every variant has `= N`")
	}
	if !got.IsIntEnum() {
		t.Fatalf("expected IsIntEnum=true: explicit-discriminant unit-only enum must encode as U32")
	}
	for _, v := range got.Variants {
		if !v.Explicit {
			t.Fatalf("variant %q: expected Explicit=true", v.Name)
		}
	}
}

// TestParseEnums_Bound covers the exact enum shape that was proven broken
// on-chain: contracts/common/interfaces/src/data_feeds_cache.rs's `Bound`,
// consumed by find_round. No variant has an explicit discriminant, so this
// must classify as a symbolic union (IsIntEnum=false), not an int enum.
// Sending ScVal::U32 for this enum produces
// `Error(WasmVm, InvalidAction) — UnreachableCodeReached` on a live devnet;
// the correct wire value is ScVal::Vec([Symbol("AtOrBefore")]) etc.
func TestParseEnums_Bound(t *testing.T) {
	src := `
#[soroban_sdk::contracttype(export = false)]
pub enum Bound {
    AtOrBefore,
    AtOrAfter,
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	got := enums[0]
	if got.Name != "Bound" {
		t.Fatalf("name: %q", got.Name)
	}
	if !got.IsUnit() {
		t.Fatalf("expected IsUnit=true")
	}
	if got.IsIntEnum() {
		t.Fatalf("expected IsIntEnum=false: Bound has no explicit discriminant and must encode as Vec[Symbol], not U32")
	}
}

// TestParseEnums_ImplicitDiscriminants covers the full Rust auto-numbering
// rule including discriminant resets:
//   - bare variants get +1 from the previous variant
//   - an explicit `= N` resets the counter so the next bare gets `N+1`
//
// This is the exact behaviour Soroban's #[contracttype] derive uses for
// the on-chain wire value, so anything that diverges from this leaks
// wrong discriminants into Go bindings.
func TestParseEnums_ImplicitDiscriminants(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
pub enum E {
    A,
    B,
    C = 10,
    D,
    Reset = 0,
    F,
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	want := []EnumVariant{
		{Name: "A", Kind: EnumVariantUnit, Value: 0},
		{Name: "B", Kind: EnumVariantUnit, Value: 1},
		{Name: "C", Kind: EnumVariantUnit, Value: 10, Explicit: true},
		{Name: "D", Kind: EnumVariantUnit, Value: 11},
		{Name: "Reset", Kind: EnumVariantUnit, Value: 0, Explicit: true},
		{Name: "F", Kind: EnumVariantUnit, Value: 1},
	}
	if !reflect.DeepEqual(enums[0].Variants, want) {
		t.Fatalf("variants:\n  got %+v\n  want %+v", enums[0].Variants, want)
	}
	if !enums[0].IsIntEnum() {
		t.Fatalf("expected IsIntEnum=true: E has explicit discriminants (C, Reset)")
	}
}

// TestParseEnums_Tuple covers tuple-variant enums: a BytesN<32> payload must be
// detected as non-unit so codegen emits the discriminated-union shape.
func TestParseEnums_Tuple(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
#[derive(Debug, Clone)]
pub enum ReplayKey {
    SeenHash(soroban_sdk::BytesN<32>),
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	got := enums[0]
	if got.IsUnit() {
		t.Fatalf("expected IsUnit=false for tuple-variant enum")
	}
	if len(got.Variants) != 1 {
		t.Fatalf("variants: got %d", len(got.Variants))
	}
	v := got.Variants[0]
	if v.Name != "SeenHash" || v.Kind != EnumVariantTuple {
		t.Fatalf("variant header: %+v", v)
	}
	if len(v.Payload) != 1 || v.Payload[0].Type != "soroban_sdk::BytesN<32>" {
		t.Fatalf("payload: %+v", v.Payload)
	}
	if v.Payload[0].Name != "" {
		t.Fatalf("expected anonymous tuple field, got name=%q", v.Payload[0].Name)
	}
}

// TestParseEnums_Mixed exercises the real-world PoolDataKey shape:
// unit and tuple variants in one enum. Bare-identifier and discriminant-less
// unit variants must coexist with parameterised ones.
func TestParseEnums_Mixed(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
pub enum PoolDataKey {
    Token,
    RemoteChainConfig(u64),
    SupportedChains,
    OutboundRateLimit(u64),
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	got := enums[0]
	if got.IsUnit() {
		t.Fatalf("expected IsUnit=false for mixed enum")
	}
	// Discriminants for unit variants follow Rust's auto-numbering even when
	// interleaved with tuple variants (the non-unit codegen path doesn't
	// consult Value, but the parser pins this for consistency and so that
	// IsUnit-derived behaviour stays predictable if the shape ever changes).
	want := []EnumVariant{
		{Name: "Token", Kind: EnumVariantUnit, Value: 0},
		{Name: "RemoteChainConfig", Kind: EnumVariantTuple, Payload: []Field{{Type: "u64"}}},
		{Name: "SupportedChains", Kind: EnumVariantUnit, Value: 1},
		{Name: "OutboundRateLimit", Kind: EnumVariantTuple, Payload: []Field{{Type: "u64"}}},
	}
	if !reflect.DeepEqual(got.Variants, want) {
		t.Fatalf("variants: got %+v want %+v", got.Variants, want)
	}
}

// TestParseEnums_Struct covers struct-variant payloads. These are valid in
// #[contracttype] enums and currently produced by no project enums, but the
// parser must not silently mis-classify them as unit when they appear.
func TestParseEnums_Struct(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
pub enum Op {
    Mint { to: soroban_sdk::Address, amount: i128 },
    Burn,
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	got := enums[0]
	if got.IsUnit() {
		t.Fatalf("expected IsUnit=false for struct-variant enum")
	}
	if len(got.Variants) != 2 {
		t.Fatalf("variants: got %d", len(got.Variants))
	}
	mint := got.Variants[0]
	if mint.Name != "Mint" || mint.Kind != EnumVariantStruct {
		t.Fatalf("Mint header: %+v", mint)
	}
	wantPayload := []Field{
		{Name: "to", Type: "soroban_sdk::Address"},
		{Name: "amount", Type: "i128"},
	}
	if !reflect.DeepEqual(mint.Payload, wantPayload) {
		t.Fatalf("Mint payload: got %+v want %+v", mint.Payload, wantPayload)
	}
	burn := got.Variants[1]
	if burn.Name != "Burn" || burn.Kind != EnumVariantUnit || len(burn.Payload) != 0 {
		t.Fatalf("Burn: %+v", burn)
	}
}

// TestParseEnums_ExportFalseStillEmitted documents the intentional choice
// that `#[contracttype(export = false)]` does NOT cause us to skip Go
// binding generation. That attribute controls whether the type appears in
// the on-chain contract schema, which is orthogonal to whether Go callers
// need to encode it for invocations or for RestoreFootprintOp ledger keys.
//
// Several public ABI surfaces (MessageDirection on pools,
// MessageExecutionState on offramp) are tagged `export = false` and still
// appear as function parameters / return types. Skipping them would break
// every package that uses them.
func TestParseEnums_ExportFalseStillEmitted(t *testing.T) {
	src := `
#[soroban_sdk::contracttype(export = false)]
pub enum MessageDirection {
    Outbound,
    Inbound,
}

#[soroban_sdk::contracttype]
pub enum PublicKey {
    Bar(u32),
}
`
	enums := parseEnums(src)
	if len(enums) != 2 {
		t.Fatalf("expected both enums to be emitted, got %d", len(enums))
	}
	names := []string{enums[0].Name, enums[1].Name}
	if names[0] != "MessageDirection" || names[1] != "PublicKey" {
		t.Fatalf("unexpected enums: %v", names)
	}
}

// TestParseEnums_GenericCommas guards the top-level comma splitter:
// a payload like `Vec<u32, u64>` must not split mid-generic.
func TestParseEnums_GenericCommas(t *testing.T) {
	src := `
#[soroban_sdk::contracttype]
pub enum X {
    A(soroban_sdk::Map<u32, soroban_sdk::Address>),
    B,
}
`
	enums := parseEnums(src)
	if len(enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(enums))
	}
	v := enums[0].Variants[0]
	if v.Kind != EnumVariantTuple || len(v.Payload) != 1 {
		t.Fatalf("A should have one payload field: %+v", v)
	}
	if v.Payload[0].Type != "soroban_sdk::Map<u32, soroban_sdk::Address>" {
		t.Fatalf("payload type wrong: %q", v.Payload[0].Type)
	}
}

// TestSplitTopLevel directly exercises the splitter that all three parsers
// rely on. Failing here would silently corrupt every shape above.
func TestSplitTopLevel(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a, b, c", []string{"a", " b", " c"}},
		{"Vec<u32, u64>, X", []string{"Vec<u32, u64>", " X"}},
		{"Foo { a: T, b: T }, Bar", []string{"Foo { a: T, b: T }", " Bar"}},
		{"Foo(T1, T2), Bar", []string{"Foo(T1, T2)", " Bar"}},
	}
	for _, tc := range cases {
		got := splitTopLevel(tc.in, ',')
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitTopLevel(%q):\n  got  %#v\n  want %#v", tc.in, got, tc.want)
		}
	}
}

func TestParseTypeAliases(t *testing.T) {
	input := `
pub type DataId = soroban_sdk::BytesN<16>;
pub type WorkflowOwner = BytesN<20>;
pub type Answer = I256;
pub trait Contract {
    fn decimals(env: soroban_sdk::Env, data_id: DataId) -> Result<u32, CacheError>;
}
`
	c, err := ParseRustBindings(input)
	require.NoError(t, err)
	require.Len(t, c.Aliases, 3)
	require.Equal(t, TypeAlias{Name: "DataId", Target: "soroban_sdk::BytesN<16>"}, c.Aliases[0])
	require.Equal(t, TypeAlias{Name: "WorkflowOwner", Target: "soroban_sdk::BytesN<20>"}, c.Aliases[1])
	require.Equal(t, TypeAlias{Name: "Answer", Target: "soroban_sdk::I256"}, c.Aliases[2])
}

func TestParseVoidAndConstructorFunctions(t *testing.T) {
	input := `
pub trait Contract {
    fn upgrade(env: soroban_sdk::Env, new_wasm_hash: WasmHash);
    fn accept_ownership(env: soroban_sdk::Env);
    fn __constructor(
        env: soroban_sdk::Env,
        owner: soroban_sdk::Address,
        retention_ttl_ledgers: u32,
    );
    fn version(env: soroban_sdk::Env) -> u32;
}
`
	c, err := ParseRustBindings(input)
	require.NoError(t, err)
	names := map[string]Function{}
	for _, f := range c.Functions {
		names[f.Name] = f
	}
	require.Contains(t, names, "upgrade")
	require.Equal(t, "", names["upgrade"].Returns)
	require.Len(t, names["upgrade"].Inputs, 1)
	require.Contains(t, names, "accept_ownership")
	require.Contains(t, names, "version")
	require.NotContains(t, names, "__constructor")
}

// TestParseEvents_AttrArgOrderIndependent pins that #[contractevent(...)]
// parses regardless of where `topics = [...]` falls among the attribute's
// key = value args. Real vendored interface files disagree on order:
// data_feeds_cache.rs (raw `stellar contract bindings rust` output) writes
// `export = false` before `topics`, while committee_verifier.rs (hand-patched
// CCIP interface) writes `topics` first. Both must parse identically.
func TestParseEvents_AttrArgOrderIndependent(t *testing.T) {
	input := `
#[soroban_sdk::contractevent(export = false, topics = ["StaleReport"])]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct StaleReport {
    pub data_id: DataId,
    pub report_ts: u64,
    pub stored_ts: u64,
}
#[soroban_sdk::contractevent(topics = ["ccv_ConfigSet"], export = false)]
#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]
pub struct ConfigSetEvent {
    pub dynamic_config: DynamicConfig,
}
`
	events := parseEvents(input)
	byName := map[string]Event{}
	for _, e := range events {
		byName[e.Name] = e
	}
	require.Len(t, events, 2, "expected both events to parse regardless of attribute arg order")

	staleReport, ok := byName["StaleReport"]
	require.True(t, ok, "topics-after-export order (data_feeds_cache.rs shape) must parse")
	require.Equal(t, []string{"StaleReport"}, staleReport.Topics)
	require.Equal(t, []Field{
		{Name: "data_id", Type: "DataId"},
		{Name: "report_ts", Type: "u64"},
		{Name: "stored_ts", Type: "u64"},
	}, staleReport.Fields)

	configSet, ok := byName["ConfigSetEvent"]
	require.True(t, ok, "topics-before-export order (committee_verifier.rs shape) must parse")
	require.Equal(t, []string{"ccv_ConfigSet"}, configSet.Topics)
	require.Equal(t, []Field{
		{Name: "dynamic_config", Type: "DynamicConfig"},
	}, configSet.Fields)
}
