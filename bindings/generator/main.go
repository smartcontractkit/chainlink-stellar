// Package main provides a CLI tool to generate Go bindings from Stellar contract Rust bindings.
//
// Usage:
//
//	stellar contract bindings rust --wasm <contract.wasm> | go run ./generator -name OnRamp -pkg onramp -out ./onramp
package main

import (
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// formatOrDie runs src through gofmt (go/format.Source) so committed
// generated files are always gofmt-canonical and `git status` stays clean
// after a regen. If formatting fails (a bug in the generator producing
// invalid Go), the unformatted source is still written to path — so the
// broken output can be inspected/diffed — but the tool exits non-zero with
// the format error instead of silently emitting output that looks fine but
// isn't gofmt-clean.
func formatOrDie(label, path string, src []byte) []byte {
	formatted, err := format.Source(src)
	if err != nil {
		if writeErr := os.WriteFile(path, src, 0644); writeErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to write unformatted %s (after gofmt failure): %v\n", label, writeErr)
		}
		fmt.Fprintf(os.Stderr, "Failed to gofmt %s: %v\nUnformatted source was written to %s for inspection.\n", label, err, path)
		os.Exit(1)
	}
	return formatted
}

func main() {
	name := flag.String("name", "", "Contract name (e.g., OnRamp)")
	pkg := flag.String("pkg", "", "Go package name for generated code")
	out := flag.String("out", "", "Output directory for generated files")
	events := flag.String("events", "", "Optional path to Rust events source file (e.g., contracts/onramp/src/events.rs)")
	readonly := flag.String("readonly", "", "Optional comma-separated list of read-only contract functions (generated as simulations, not transactions). When provided it is authoritative and replaces the name-based heuristic; every listed function must exist in the contract.")
	flag.Parse()

	if *name == "" || *pkg == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "Usage: stellar contract bindings rust --wasm <contract.wasm> | go run ./generator -name <Name> -pkg <package> -out <dir> [-events <events.rs>] [-readonly fn1,fn2]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Read Rust bindings from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read input: %v\n", err)
		os.Exit(1)
	}

	// Parse the Rust bindings
	contract, err := ParseRustBindings(string(input))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse Rust bindings: %v\n", err)
		os.Exit(1)
	}
	contract.Name = *name

	// Optionally build the explicit read-only function set. A nil map means "not
	// provided" and GenerateClient falls back to the legacy name heuristic.
	var readOnlyFns map[string]bool
	if *readonly != "" {
		readOnlyFns = make(map[string]bool)
		for _, fn := range strings.Split(*readonly, ",") {
			readOnlyFns[strings.TrimSpace(fn)] = true
		}
		// Guard against typos and contract drift: every listed function must exist.
		known := make(map[string]bool, len(contract.Functions))
		for _, fn := range contract.Functions {
			known[fn.Name] = true
		}
		for fn := range readOnlyFns {
			if !known[fn] {
				fmt.Fprintf(os.Stderr, "readonly function %q not found in contract %s\n", fn, *name)
				os.Exit(1)
			}
		}
	}

	// Optionally parse events from Rust source file
	if *events != "" {
		eventsSource, err := os.ReadFile(*events)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read events file: %v\n", err)
			os.Exit(1)
		}
		parsedEvents := parseEvents(string(eventsSource))
		contract.Events = append(contract.Events, parsedEvents...)
		fmt.Printf("Parsed %d events from %s\n", len(parsedEvents), *events)
	}

	// Create output directory
	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output directory: %v\n", err)
		os.Exit(1)
	}

	// Generate types file
	typesCode := GenerateTypes(*pkg, contract)
	typesPath := filepath.Join(*out, "types.go")
	formattedTypes := formatOrDie("types.go", typesPath, []byte(typesCode))
	if err := os.WriteFile(typesPath, formattedTypes, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write types.go: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s\n", typesPath)

	// Generate client file
	clientCode := GenerateClient(*pkg, contract, readOnlyFns)
	clientPath := filepath.Join(*out, "client.go")
	formattedClient := formatOrDie("client.go", clientPath, []byte(clientCode))
	if err := os.WriteFile(clientPath, formattedClient, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write client.go: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s\n", clientPath)

	fmt.Printf("Successfully generated Go bindings for %s\n", *name)
}
