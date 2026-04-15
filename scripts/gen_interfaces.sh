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

MIN_STELLAR="25.1.0"
ACTUAL_STELLAR=$(stellar --version 2>/dev/null | head -1 | awk '{print $2}')
if [[ -z "$ACTUAL_STELLAR" ]]; then
  echo "ERROR: stellar CLI not found. Install: cargo install stellar-cli --version $MIN_STELLAR"
  exit 1
fi
if [[ "$(printf '%s\n%s' "$MIN_STELLAR" "$ACTUAL_STELLAR" | sort -V | head -1)" != "$MIN_STELLAR" ]]; then
  echo "ERROR: stellar CLI >= $MIN_STELLAR required (found $ACTUAL_STELLAR)"
  echo "Install: cargo install stellar-cli"
  exit 1
fi

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
  "rmn_remote|rmn_remote|RmnRemote|0"
  "offramp|offramp|OffRamp|0"
  "router|router|Router|0"
  "ccip_receiver_example|ccip_receiver|ExampleCcipReceiver|0"
  "token_admin_registry|token_admin_registry|TokenAdminRegistry|0"
  "pools_lock_release_pool|lock_release_pool|LockReleasePool|0"
  "pools_burn_mint_pool|burn_mint_pool|BurnMintPool|0"
  "pools_token_lock_box|token_lock_box|TokenLockBox|0"
  "pools_siloed_lock_release_pool|siloed_lock_release_pool|SiloedLockReleasePool|0"
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

# Safety-net: remove duplicate fn declarations inside the trait block and
# duplicate struct/enum/event definitions outside it.  Keeps the first
# occurrence of each name.  Handles the case where a cross-contract WASM
# dependency leaks its functions/types into the generated output.
strip_duplicate_declarations() {
  perl -e '
    use strict; use warnings;
    local $/;
    my $input = <STDIN>;

    # ── Phase 1: deduplicate fn declarations inside the trait block ──
    if ($input =~ s/(pub\s+trait\s+\w+\s*\{)(.*?)(\})/
        my ($hdr, $body, $close) = ($1, $2, $3);
        my %seen;
        my $clean = "";
        while ($body =~ m!(\s*fn\s+(\w+)\s*\(.*?;)!gs) {
            $clean .= $1 unless $seen{$2}++;
        }
        "$hdr$clean\n$close"
    /se) {}

    # ── Phase 2: deduplicate struct and enum definitions ──
    my %seen_types;
    my @lines = split /\n/, $input;
    my @out;
    my @attr_buf;
    my $skipping = 0;

    for my $line (@lines) {
        if ($skipping) {
            $skipping = 0 if $line =~ /^\}/;
            next;
        }
        if ($line =~ /^#\[/) {
            push @attr_buf, $line;
            next;
        }
        if ($line =~ /^pub\s+(?:struct|enum)\s+(\w+)/) {
            if ($seen_types{$1}++) {
                @attr_buf = ();
                $skipping = 1;
                next;
            }
            push @out, @attr_buf;
            @attr_buf = ();
            push @out, $line;
            next;
        }
        if (@attr_buf) {
            push @out, @attr_buf;
            @attr_buf = ();
        }
        push @out, $line;
    }
    push @out, @attr_buf if @attr_buf;

    my $result = join("\n", @out) . "\n";
    $result =~ s/\n{3,}/\n/g;
    print $result;
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

# Stellar bindings for ccip_receiver_example pull unrelated #[contracttype]s from the
# common_interfaces dependency (e.g. AllowList*) and omit Ccv* structs referenced by the trait.
# Normalize the checked-in interface module after generation.
patch_ccip_receiver_interfaces() {
  local f="$1"
  perl -i -0pe '
    # Drop OnRamp-style types leaked into this module via the dependency graph.
    s/\n#\[soroban_sdk::contracttype\(export = false\)\]\n#\[derive\(Debug, Clone, Eq, PartialEq, Ord, PartialOrd\)\]\npub struct AllowListEntry \{[^}]+\}//s;
    s/\n#\[soroban_sdk::contracttype\(export = false\)\]\n#\[derive\(Debug, Clone, Eq, PartialEq, Ord, PartialOrd\)\]\npub struct AllowListUpdate \{[^}]+\}//s;
    # Drop auth events re-exported from Ownable (same names exist in authorization helpers).
    s/\n#\[soroban_sdk::contractevent\(topics = \["auth_[^]]+"\], export = false\)\]\n#\[derive\(Debug, Clone, Eq, PartialEq, Ord, PartialOrd\)\]\npub struct [^{]+\{[^}]+\}//sg;
    # stellar-cli omits contract-owned struct defs; insert before first remaining contracttype after the trait.
    if ($_ !~ /pub struct CcvChainConfig/s) {
      s/(\n\}\n)(#\[soroban_sdk::contracttype\(export = false\)\]\n#\[derive\(Debug, Clone, Eq, PartialEq, Ord, PartialOrd\)\]\npub struct TokenAmount)/$1\n#\[soroban_sdk::contracttype(export = false)\]\n#\[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)\]\npub struct CcvConfigUpdate {\n    pub source_chain_selector: u64,\n    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_threshold: u32,\n}\n#\[soroban_sdk::contracttype(export = false)\]\n#\[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)\]\npub struct CcvChainConfig {\n    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_threshold: u32,\n}\n#\[soroban_sdk::contracttype(export = false)\]\n#\[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)\]\npub struct RemoteChainConfig {\n    pub extra_args: soroban_sdk::Bytes,\n    pub allowed_finality_config: u32,\n}\n#\[soroban_sdk::contracttype(export = false)\]\n#\[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)\]\npub struct CcvsAndFinalityConfig {\n    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_threshold: u32,\n    pub allowed_finality_config: u32,\n}\n$2/s;
    }
  ' "$f"
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
    | strip_duplicate_declarations \
    | apply_renames "$pascal_name" \
    | use_common_message_types "${use_common_msg:-0}" \
    > "$out_path"

  if [[ "$output_module" == "ccip_receiver" ]]; then
    patch_ccip_receiver_interfaces "$out_path"
  fi
done

echo "Done. Interfaces written to $INTERFACES_DIR"
