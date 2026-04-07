# AGENTS.md

This file applies to the `.cursor/` directory tree.

## Purpose
- `.cursor/` holds Cursor-specific workspace metadata: agents, plans, reports, rules, settings, and reusable skills.
- Keep content here durable and operational. Prefer guidance that will still help on future tasks, not one-off scratch notes.

## Ownership and coordination
- Cursor is the main implementation agent for this repo.
- Parallel agents may update `.cursor/` to improve coordination, but should avoid rewriting existing conventions without a clear reason.
- When adding new `.cursor/` artifacts, optimize for making concurrent work safer: reduce ambiguity, clarify responsibilities, and preserve useful context for future sessions.

## Directory conventions
- `agents/`: role definitions for specialized Cursor sub-agents. Keep prompts self-contained, factual, and tool-aware.
- `rules/`: durable behavior rules. Use `alwaysApply: true` only when the rule truly belongs on nearly every task.
- `skills/`: reusable workflows and references. Prefer progressive disclosure and targeted references over long embedded instructions.
- `plans/`: task or incident plans that may be revisited. Name them so humans can find them later.
- `reports/`: user-requested or materially useful outputs only. Do not create reports by default.
- `settings.json`: keep minimal and stable; avoid churn from personal preferences unless explicitly requested.

## Editing guidance
- Prefer additive, surgical changes over large rewrites of prompts or rules.
- Preserve frontmatter shape and naming patterns used by nearby files.
- Keep agent and rule instructions consistent with the root `AGENTS.md`.
- If adding a rule for collaboration, make the scope explicit so it does not accidentally over-constrain unrelated tasks.
- Reference concrete repo locations, commands, and systems when they are stable and verified.

## Quality bar
- Optimize for truth and future maintainability over verbosity.
- Avoid duplicating large sections across files unless the duplication is intentionally scoped.
- If a Cursor agent depends on external tools or MCP servers, name them explicitly and describe the fallback when they are unavailable.
