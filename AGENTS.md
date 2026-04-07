# AGENTS.md

This file applies to the entire repository.

## Repository overview
- This repo mixes Go services/tooling with Rust Soroban smart contracts.
- Primary areas:
  - `contracts/`: Rust contract workspace.
  - `ccv/`, `cmd/`, `pkg/`, `internal/`: Go application and library code.
  - `bindings/`: generated Go bindings and related generators.
  - `tests/integration/`, `tests/e2e/`: Go integration and end-to-end tests.
  - `tests/env/`: topology and environment config used by local devenv flows.
  - `docs/`: architecture and testing documentation.

## Agent topology
- Cursor is the primary implementation agent for this repository.
- Other agents may work on parallel threads in the same repo. Treat their changes as potentially concurrent, valid work rather than accidental drift.
- When working in parallel:
  - prefer focused, non-overlapping edits;
  - re-read the target files immediately before patching if another agent may have touched them;
  - avoid broad formatting-only rewrites or opportunistic refactors that create merge friction;
  - leave concise, durable instructions in repo docs or `.cursor/` metadata rather than ephemeral notes.
- Use `.cursor/` for Cursor-specific workflow, planning, skills, and agent metadata. Keep repo-wide expectations in this file.

## Working norms
- Optimize for accurate, minimal, repo-consistent changes.
- Fix root causes; avoid speculative cleanups and unrelated refactors.
- Prefer updating the smallest relevant surface area first, then generated artifacts only when required.
- Check for nested `AGENTS.md` files before editing deeper directories.
- Capture references when available: cite the repo docs, code, generated sources, or external systems that support the change.

## Generated and derived code
- Treat `bindings/contracts/` as generated output. Prefer changing generators or source interfaces rather than editing generated files by hand.
- `make generate-interfaces` builds contracts and refreshes Rust interfaces.
- `make generate-bindings` regenerates Go bindings from contract interfaces.
- If a change affects generated artifacts, regenerate the relevant outputs and include them with the source change.

## Common commands
- Build contracts: `stellar contract build`
- Run Rust tests: `cargo test`
- Run Rust checks: `cargo check --workspace`
- Format Rust workspace: `cargo fmt --all`
- Run Go integration tests: `go test ./tests/integration/... -v -tags=integration -count=1 -p=1 -timeout=15m`
- Run Go E2E tests: `go test -v -timeout 15m ./tests/e2e/...`
- Bring up local devenv: `make up`
- Tear down local devenv: `make down`

## Validation guidance
- Validate as narrowly as possible around the code you changed.
- For Rust contract changes, prefer targeted `cargo test -p <crate>` before broader workspace runs.
- For Go changes, run the narrowest relevant `go test` package or test selector first.
- Run regeneration commands only when source changes require them.

## Editing guidance
- Keep Go changes idiomatic and consistent with existing package boundaries.
- Keep contract changes aligned with the Cargo workspace layout under `contracts/`.
- Do not edit `target/` outputs or other build artifacts.
- Update `README.md`, `docs/`, or `.cursor/` metadata when behavior, workflows, or agent coordination expectations materially change.
