#!/usr/bin/env bash
# Generate Rust interface traits for each contract in the CCIP Stellar workspace.
#
# For each contract, runs `stellar contract bindings rust` and post-processes the
# output to apply the required renames:
#   - Args       -> {ContractName}Args
#   - Client     -> {ContractName}Client
#   - Contract   -> {ContractName}Interface
#
# Usage:
#   ./scripts/gen_interfaces.sh              # Generate interfaces (builds first)
#   ./scripts/gen_interfaces.sh --no-build   # Skip build, use existing wasm files
#
# Requires: stellar CLI, contracts built (stellar contract build)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
WASM_DIR="$REPO_ROOT/target/wasm32v1-none/release"
INTERFACES_DIR="$REPO_ROOT/contracts/common/interfaces/src"

# Contract config: "wasm_basename|output_module|PascalCaseName|use_common_message"
# use_common_message=1 when the trait uses StellarToAnyMessage (avoids type conflicts)
CONTRACTS=(
  "fee_quoter|fee_quoter|FeeQuoter|1"
  "ccvs_committee_verifier|committee_verifier|CommitteeVerifier|0"
  "ccvs_versioned_verifier_resolver|versioned_verifier_resolver|VersionedVerifierResolver|0"
  "onramp|onramp|OnRamp|1"
  "rmn_proxy|rmn_proxy|RmnProxy|0"
)

# Remove the WASM const block from generated output (interfaces don't need it)
strip_wasm_block() {
  sed -e '/^pub const WASM/,/^);$/d'
}

# Remove duplicate auth event blocks that conflict with contract-specific events.
# The bindings generator emits both auth_OwnerTransferred and contract-specific
# OwnershipTransferredEvent, causing "defined multiple times" errors.
strip_duplicate_auth_events() {
  awk '
    /^#\[soroban_sdk::contractevent.*auth_OwnerTransferred/ { skip=1; depth=0; depth_was_positive=0; next }
    skip {
      for (i=1; i<=length($0); i++) {
        c = substr($0,i,1)
        if (c=="{") depth++
        if (c=="}") depth--
      }
      if (depth <= 0 && depth_was_positive) skip=0
      if (depth > 0) depth_was_positive=1
      next
    }
    { depth_was_positive=0; print }
  '
}

# Apply renames for a contract: Args->XArgs, Client->XClient, Contract->XInterface
apply_renames() {
  local name="$1"
  sed \
    -e "s/name = \"Args\"/name = \"${name}Args\"/g" \
    -e "s/name = \"Client\"/name = \"${name}Client\"/g" \
    -e "s/pub trait Contract/pub trait ${name}Interface/g"
}

# Replace generated StellarToAnyMessage and TokenAmount with re-exports from common_message.
# Only run when use_common_message=1; the workspace uses common_message as the canonical
# source for these types when the trait uses them (onramp, fee_quoter).
use_common_message_types() {
  local enabled="$1"
  if [[ "$enabled" != "1" ]]; then
    cat
    return
  fi
  perl -0 -pe '
    my $use = "use common_message::{StellarToAnyMessage, TokenAmount};\n\n";
    my $removed = 0;
    # Remove TokenAmount struct (with preceding attributes)
    $removed++ if s/#\[soroban_sdk::contracttype\(export = false\)\]\n#\[derive\([^]]+\)\]\npub struct TokenAmount \{[^}]*\}//ms;
    # Remove StellarToAnyMessage struct (with preceding attributes)
    $removed++ if s/#\[soroban_sdk::contracttype\(export = false\)\]\n#\[derive\([^]]+\)\]\npub struct StellarToAnyMessage \{[^}]*\}//ms;
    $_ = $use . $_ if $removed;
  '
}

do_build=true
for arg in "$@"; do
  case "$arg" in
    --no-build) do_build=false ;;
    -h|--help)
      echo "Usage: $0 [--no-build]"
      echo "  --no-build  Skip 'stellar contract build', use existing wasm files"
      exit 0
      ;;
  esac
done

cd "$REPO_ROOT"

if [[ "$do_build" == true ]]; then
  echo "Building contracts..."
  stellar contract build
fi

for entry in "${CONTRACTS[@]}"; do
  IFS='|' read -r wasm_basename output_module pascal_name use_common_msg <<< "$entry"
  wasm_path="$WASM_DIR/${wasm_basename}.wasm"
  out_path="$INTERFACES_DIR/${output_module}.rs"

  if [[ ! -f "$wasm_path" ]]; then
    echo "Skipping $output_module: $wasm_path not found"
    continue
  fi

  echo "Generating interface for $output_module..."
  stellar contract bindings rust --wasm "$wasm_path" 2>/dev/null \
    | strip_wasm_block \
    | strip_duplicate_auth_events \
    | apply_renames "$pascal_name" \
    | use_common_message_types "${use_common_msg:-0}" \
    > "$out_path"
done

echo "Done. Interfaces written to $INTERFACES_DIR"
