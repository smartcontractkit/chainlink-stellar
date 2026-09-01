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

func writeGoFile(outDir, name, src string) {
	path := filepath.Join(outDir, name)
	formatted, err := format.Source([]byte(src))
	if err != nil {
		if writeErr := os.WriteFile(path, []byte(src), 0644); writeErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to write unformatted %s (after gofmt failure): %v\n", name, writeErr)
		}
		fmt.Fprintf(os.Stderr, "Failed to gofmt %s: %v\nUnformatted source was written to %s for inspection.\n", name, err, path)
		os.Exit(1)
	}
	if err := os.WriteFile(path, formatted, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write %s: %v\n", name, err)
		os.Exit(1)
	}
	fmt.Printf("Generated %s\n", path)
}

func parseFnSet(flagName, csv string, valid map[string]bool, contractName string) map[string]bool {
	if csv == "" {
		return nil
	}
	set := make(map[string]bool)
	for _, fn := range strings.Split(csv, ",") {
		set[strings.TrimSpace(fn)] = true
	}
	for fn := range set {
		if !valid[fn] {
			fmt.Fprintf(os.Stderr, "%s function %q not valid for contract %s\n", flagName, fn, contractName)
			os.Exit(1)
		}
	}
	return set
}

func main() {
	name := flag.String("name", "", "Contract name (e.g., OnRamp)")
	pkg := flag.String("pkg", "", "Go package name for generated code")
	out := flag.String("out", "", "Output directory for generated files")
	events := flag.String("events", "", "Optional path to Rust events source file (e.g., contracts/onramp/src/events.rs)")
	readonly := flag.String("readonly", "", "Optional comma-separated list of read-only contract functions (generated as simulations, not transactions). When provided it is authoritative and replaces the name-based heuristic; every listed function must exist in the contract.")
	includeVoid := flag.String("include-void", "", "Optional comma-separated list of void contract functions (no return type) to generate methods for. Void functions not listed are omitted from the client, preserving historical behavior; every listed function must exist in the contract and be void.")
	flag.Parse()

	if *name == "" || *pkg == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "Usage: stellar contract bindings rust --wasm <contract.wasm> | go run ./generator -name <Name> -pkg <package> -out <dir> [-events <events.rs>] [-readonly fn1,fn2] [-include-void fn1,fn2]")
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

	allFns := make(map[string]bool, len(contract.Functions))
	voidFns := make(map[string]bool)
	for _, fn := range contract.Functions {
		allFns[fn.Name] = true
		if fn.Returns == "" {
			voidFns[fn.Name] = true
		}
	}
	readOnlyFns := parseFnSet("readonly", *readonly, allFns, *name)
	includeVoidFns := parseFnSet("include-void", *includeVoid, voidFns, *name)

	functions := contract.Functions[:0]
	for _, fn := range contract.Functions {
		if fn.Returns == "" && !includeVoidFns[fn.Name] {
			continue
		}
		if readOnlyFns != nil {
			fn.ReadOnly = readOnlyFns[fn.Name]
		} else {
			n := strings.ToLower(fn.Name)
			fn.ReadOnly = strings.HasPrefix(n, "get_") || strings.HasPrefix(n, "is_") ||
				n == "owner" || n == "balance"
		}
		functions = append(functions, fn)
	}
	contract.Functions = functions

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

	writeGoFile(*out, "types.go", GenerateTypes(*pkg, contract))
	writeGoFile(*out, "client.go", GenerateClient(*pkg, contract))

	fmt.Printf("Successfully generated Go bindings for %s\n", *name)
}
