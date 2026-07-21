package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Contract represents a parsed Soroban contract.
type Contract struct {
	Name      string
	Structs   []Struct
	Functions []Function
	Errors    []ErrorEnum
	Events    []Event
	Enums     []Enum
}

// Struct represents a Soroban struct type.
type Struct struct {
	Name   string
	Fields []Field
}

// Field represents a struct field.
type Field struct {
	Name string
	Type string
}

// Function represents a contract function.
type Function struct {
	Name    string
	Inputs  []Field
	Returns string
}

// ErrorEnum represents a contract error enum.
type ErrorEnum struct {
	Name     string
	Variants []ErrorVariant
}

// EnumVariantKind describes the shape of a Soroban #[contracttype] enum variant.
//
// Soroban encodes a unit-only ("C-style") enum as ScVal::U32. Any enum that
// contains at least one tuple or struct variant is encoded as
// ScVal::Vec([ ScVal::Symbol(<VariantName>), <payload-fields...> ]) — the
// variant identifier is used verbatim (no case conversion) as the discriminant
// symbol. We track per-variant kind so codegen can branch correctly.
type EnumVariantKind int

const (
	EnumVariantUnit   EnumVariantKind = iota // `Foo` or `Foo = N`
	EnumVariantTuple                         // `Foo(T1, T2, ...)`
	EnumVariantStruct                        // `Foo { a: T1, b: T2 }`
)

type Enum struct {
	Name     string
	Variants []EnumVariant
}

// IsUnit reports whether every variant is a unit (C-style) variant.
// Mixed enums (any tuple/struct variant) require the discriminated-union encoding.
func (e Enum) IsUnit() bool {
	for _, v := range e.Variants {
		if v.Kind != EnumVariantUnit {
			return false
		}
	}
	return true
}

type EnumVariant struct {
	Name string
	Kind EnumVariantKind
	// Value is the C-style discriminant (only meaningful for EnumVariantUnit).
	Value int
	// Payload holds positional fields for tuple variants (Field.Name == "")
	// and named fields for struct variants. Empty for unit variants.
	Payload []Field
}

// ErrorVariant represents an error enum variant.
type ErrorVariant struct {
	Name  string
	Value int
}

// Event represents a contract event.
type Event struct {
	Name   string
	Topics []string
	Fields []Field
}

// ParseRustBindings parses Rust bindings output from stellar-cli.
func ParseRustBindings(input string) (*Contract, error) {
	contract := &Contract{}

	// Parse structs
	contract.Structs = parseStructs(input)

	// Parse trait functions
	contract.Functions = parseFunctions(input)

	// Parse error enums
	contract.Errors = parseErrors(input)

	// Parse events
	contract.Events = parseEvents(input)

	// Parse enums
	contract.Enums = parseEnums(input)

	return contract, nil
}

func parseEnums(input string) []Enum {
	// Match the enum *header* — attribute(s) + `pub enum Name {` — and then
	// walk the body with bracket-balancing because struct variants can contain
	// nested `{}`.
	//
	// We deliberately capture the full attribute block so we can detect the
	// `export = false` opt-out (PR#3 below): contract authors that mark a type
	// non-exportable on the Rust side want it omitted from Go bindings too.
	headerRe := regexp.MustCompile(`(?s)(#\[soroban_sdk::contracttype([^\]]*)\]\s*(?:#\[derive[^\]]*\]\s*)*)pub enum (\w+)\s*\{`)

	var enums []Enum
	matches := headerRe.FindAllStringSubmatchIndex(input, -1)

	for _, m := range matches {
		// Submatch indices: 0,1 = full match; 2,3 = attribute block; 4,5 = attr args; 6,7 = name; m[1] = char after `{`.
		// We capture attr args (e.g. `(export = false)`) for forward-compat,
		// but intentionally do NOT use them as a skip signal: `export = false`
		// is a Stellar SDK directive that controls whether the type is
		// emitted in the contract's on-chain interface schema. It is
		// orthogonal to whether Go callers need to encode the type for
		// invocations or for `RestoreFootprintOp` ledger-key construction.
		// Many existing public ABI surfaces use `export = false` enums
		// (MessageDirection, MessageExecutionState) and still need Go
		// bindings.
		_ = input[m[4]:m[5]]
		name := input[m[6]:m[7]]
		bodyStart := m[1]

		body, ok := extractBalancedBody(input, bodyStart)
		if !ok {
			continue
		}

		variants := parseEnumVariants(body)
		enums = append(enums, Enum{Name: name, Variants: variants})
	}

	return enums
}

// extractBalancedBody returns the text between `start` (just past the opening
// `{`) and the matching `}`, respecting nested braces. The second return value
// is false if the input is unbalanced.
func extractBalancedBody(input string, start int) (string, bool) {
	depth := 1
	for i := start; i < len(input); i++ {
		switch input[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return input[start:i], true
			}
		}
	}
	return "", false
}

// parseEnumVariants parses the raw body of a `pub enum` into structured variants.
// Handles three Rust shapes:
//   - unit (with or without an explicit discriminant): `Foo` / `Foo = 7`
//   - tuple: `Foo(T1, T2)` (commas inside generics like `Vec<u32, u64>` ignored)
//   - struct: `Foo { a: T1, b: T2 }`
func parseEnumVariants(body string) []EnumVariant {
	unitDiscRe := regexp.MustCompile(`^\s*(\w+)\s*=\s*(-?\d+)\s*$`)
	unitBareRe := regexp.MustCompile(`^\s*(\w+)\s*$`)
	tupleRe := regexp.MustCompile(`(?s)^\s*(\w+)\s*\((.+)\)\s*$`)
	structRe := regexp.MustCompile(`(?s)^\s*(\w+)\s*\{(.+)\}\s*$`)
	structFieldRe := regexp.MustCompile(`(\w+)\s*:\s*([^,]+)`)

	// Track the next implicit discriminant for bare unit variants.
	// Rust semantics: bare variants without an explicit `= N` get sequential
	// values starting at 0 in declaration order, and an explicit discriminant
	// resets the counter so the next bare variant is `discriminant + 1`.
	// This must match the on-chain ScVal::U32 wire value Soroban emits for
	// `#[contracttype]` unit-only enums; otherwise named Go constants
	// collide on the wire (e.g. MessageDirection::{Outbound,Inbound} both
	// serialising as 0).
	nextImplicit := 0

	var variants []EnumVariant
	for _, raw := range splitTopLevel(body, ',') {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		// Strip a possible doc/attribute prefix on the variant line.
		// Rust attributes on a variant aren't expected here, but be defensive.
		switch {
		case structRe.MatchString(v):
			sm := structRe.FindStringSubmatch(v)
			fieldsRaw := sm[2]
			var payload []Field
			for _, fm := range structFieldRe.FindAllStringSubmatch(fieldsRaw, -1) {
				payload = append(payload, Field{
					Name: strings.TrimSpace(fm[1]),
					Type: qualifySorobanType(strings.TrimSpace(fm[2])),
				})
			}
			variants = append(variants, EnumVariant{
				Name:    sm[1],
				Kind:    EnumVariantStruct,
				Payload: payload,
			})
		case tupleRe.MatchString(v):
			tm := tupleRe.FindStringSubmatch(v)
			payloadRaw := tm[2]
			var payload []Field
			for _, t := range splitTopLevel(payloadRaw, ',') {
				t = strings.TrimSpace(t)
				if t == "" {
					continue
				}
				payload = append(payload, Field{
					Name: "",
					Type: qualifySorobanType(t),
				})
			}
			variants = append(variants, EnumVariant{
				Name:    tm[1],
				Kind:    EnumVariantTuple,
				Payload: payload,
			})
		case unitDiscRe.MatchString(v):
			um := unitDiscRe.FindStringSubmatch(v)
			val := 0
			fmt.Sscanf(um[2], "%d", &val)
			variants = append(variants, EnumVariant{
				Name:  um[1],
				Kind:  EnumVariantUnit,
				Value: val,
			})
			nextImplicit = val + 1
		case unitBareRe.MatchString(v):
			um := unitBareRe.FindStringSubmatch(v)
			variants = append(variants, EnumVariant{
				Name:  um[1],
				Kind:  EnumVariantUnit,
				Value: nextImplicit,
			})
			nextImplicit++
		}
	}
	return variants
}

// splitTopLevel splits `s` on `sep`, respecting nesting in `<>`, `()`, and `{}`.
// This lets us handle `Vec<u32, u64>` (no split inside generics) and
// `Foo { a: T1, b: T2 }` (no split inside the struct body).
func splitTopLevel(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	angle, paren, brace := 0, 0, 0
	for _, r := range s {
		switch r {
		case '<':
			angle++
		case '>':
			if angle > 0 {
				angle--
			}
		case '(':
			paren++
		case ')':
			if paren > 0 {
				paren--
			}
		case '{':
			brace++
		case '}':
			if brace > 0 {
				brace--
			}
		}
		if r == sep && angle == 0 && paren == 0 && brace == 0 {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func parseStructs(input string) []Struct {
	// Match: pub struct Name { fields }
	structRe := regexp.MustCompile(`(?s)#\[soroban_sdk::contracttype[^\]]*\]\s*(?:#\[derive[^\]]*\]\s*)*pub struct (\w+)\s*\{([^}]+)\}`)
	fieldRe := regexp.MustCompile(`pub (\w+):\s*([^,]+),`)

	var structs []Struct
	matches := structRe.FindAllStringSubmatch(input, -1)

	for _, match := range matches {
		name := match[1]
		body := match[2]

		var fields []Field
		fieldMatches := fieldRe.FindAllStringSubmatch(body, -1)
		for _, fm := range fieldMatches {
			fields = append(fields, Field{
				Name: fm[1],
				Type: strings.TrimSpace(fm[2]),
			})
		}

		structs = append(structs, Struct{
			Name:   name,
			Fields: fields,
		})
	}

	return structs
}

func parseFunctions(input string) []Function {
	// Match trait block (Contract or {Name}Interface from gen_interfaces.sh)
	traitRe := regexp.MustCompile(`(?s)pub trait \w+ \{(.+?)\n\}`)
	traitMatch := traitRe.FindStringSubmatch(input)
	if traitMatch == nil {
		return nil
	}
	traitBody := traitMatch[1]

	// Match individual functions
	// fn name(env: Env, param: Type, ...) -> Result<RetType, Error>;
	funcRe := regexp.MustCompile(`fn (\w+)\s*\(\s*env:\s*soroban_sdk::Env\s*(?:,\s*([^)]*))?\)\s*->\s*([^;]+);`)

	var functions []Function
	matches := funcRe.FindAllStringSubmatch(traitBody, -1)

	for _, match := range matches {
		name := match[1]
		paramsStr := strings.TrimSpace(match[2])
		returnStr := strings.TrimSpace(match[3])

		var inputs []Field
		if paramsStr != "" {
			// Parse each parameter
			params := splitParams(paramsStr)
			for _, p := range params {
				parts := strings.SplitN(p, ":", 2)
				if len(parts) == 2 {
					inputs = append(inputs, Field{
						Name: strings.TrimSpace(parts[0]),
						Type: strings.TrimSpace(parts[1]),
					})
				}
			}
		}

		functions = append(functions, Function{
			Name:    name,
			Inputs:  inputs,
			Returns: returnStr,
		})
	}

	return functions
}

func splitParams(params string) []string {
	// Split by comma but handle nested generics like Vec<Address>
	var result []string
	var current strings.Builder
	depth := 0

	for _, ch := range params {
		switch ch {
		case '<':
			depth++
			current.WriteRune(ch)
		case '>':
			depth--
			current.WriteRune(ch)
		case ',':
			if depth == 0 {
				result = append(result, strings.TrimSpace(current.String()))
				current.Reset()
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}

	if s := strings.TrimSpace(current.String()); s != "" {
		result = append(result, s)
	}

	return result
}

func parseErrors(input string) []ErrorEnum {
	// Match: #[soroban_sdk::contracterror] pub enum Name { ... }
	enumRe := regexp.MustCompile(`(?s)#\[soroban_sdk::contracterror[^\]]*\]\s*(?:#\[derive[^\]]*\]\s*)*pub enum (\w+)\s*\{([^}]+)\}`)
	variantRe := regexp.MustCompile(`(\w+)\s*=\s*(\d+)`)

	var errors []ErrorEnum
	matches := enumRe.FindAllStringSubmatch(input, -1)

	for _, match := range matches {
		name := match[1]
		body := match[2]

		var variants []ErrorVariant
		variantMatches := variantRe.FindAllStringSubmatch(body, -1)
		for _, vm := range variantMatches {
			val := 0
			fmt.Sscanf(vm[2], "%d", &val)
			variants = append(variants, ErrorVariant{
				Name:  vm[1],
				Value: val,
			})
		}

		errors = append(errors, ErrorEnum{
			Name:     name,
			Variants: variants,
		})
	}

	return errors
}

// qualifySorobanType adds the soroban_sdk:: prefix to bare Soroban types
// so that codegen type-matching works uniformly regardless of whether the
// source uses `use soroban_sdk::Address` or the fully-qualified form.
func qualifySorobanType(t string) string {
	switch t {
	case "Address":
		return "soroban_sdk::Address"
	case "Bytes":
		return "soroban_sdk::Bytes"
	case "Symbol":
		return "soroban_sdk::Symbol"
	}
	if strings.HasPrefix(t, "BytesN<") {
		return "soroban_sdk::" + t
	}
	if strings.HasPrefix(t, "Vec<") {
		return "soroban_sdk::" + t
	}
	return t
}

func parseEvents(input string) []Event {
	// Match both source-level #[contractevent(...)] and generated #[soroban_sdk::contractevent(...)]
	eventRe := regexp.MustCompile(`(?s)#\[(?:soroban_sdk::)?contractevent\s*\(\s*topics\s*=\s*\[([^\]]+)\][^)]*\)\s*\]\s*(?:#\[derive[^\]]*\]\s*)*pub struct (\w+)\s*\{([^}]+)\}`)
	fieldRe := regexp.MustCompile(`pub (\w+):\s*([^,]+),`)
	topicRe := regexp.MustCompile(`"([^"]+)"`)

	var events []Event
	matches := eventRe.FindAllStringSubmatch(input, -1)

	for _, match := range matches {
		topicsStr := match[1]
		name := match[2]
		body := match[3]

		var topics []string
		topicMatches := topicRe.FindAllStringSubmatch(topicsStr, -1)
		for _, tm := range topicMatches {
			topics = append(topics, tm[1])
		}

		var fields []Field
		fieldMatches := fieldRe.FindAllStringSubmatch(body, -1)
		for _, fm := range fieldMatches {
			fields = append(fields, Field{
				Name: fm[1],
				Type: qualifySorobanType(strings.TrimSpace(fm[2])),
			})
		}

		events = append(events, Event{
			Name:   name,
			Topics: topics,
			Fields: fields,
		})
	}

	return events
}
