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

	return contract, nil
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
				Type: strings.TrimSpace(fm[2]),
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
