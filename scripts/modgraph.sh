#!/usr/bin/env bash

# Generates go.md

set -e

# Module mode: the repo-root go.work would otherwise switch go commands to
# workspace mode and change the committed go.md graph.
export GOWORK=off

echo "# smartcontractkit Go modules
## Main module
\`\`\`mermaid
flowchart LR
"
go mod graph | modgraph -prefix github.com/smartcontractkit/
echo "\`\`\`"

echo "## All modules
\`\`\`mermaid
flowchart LR
"
gomods graph | modgraph -prefix github.com/smartcontractkit/
echo "\`\`\`"