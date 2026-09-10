#!/usr/bin/env bash
# Generate Rust interface traits for each contract in the CCIP Stellar workspace.
#
# For each contract, runs `stellar contract bindings rust` and post-processes the
# output:
#   1. strip_wasm_block        - remove the WASM const block (interfaces don't need it)
#   2. normalize_attributes    - collapse CLI-version differences: stellar-cli 25.x
#                                emits `contracttype(export = false)` while 28.x emits
#                                plain `contracttype`; both are normalized to the
#                                `export = false` form so local and CI (pinned 25.1.0)
#                                output agree. export = false is required: without it
#                                every type/event in this crate embeds spec entries into
#                                the wasm of every contract linking common-interfaces,
#                                inflating contract wasm past network upload limits.
#   3. strip_duplicate_auth_events - drop auth_OwnerTransferred (conflicts with the
#                                contract-specific OwnershipTransferredEvent)
#   4. prune_interface         - dedup trait fns and type/event definitions, drop
#                                events whose topic prefix is not the contract's own,
#                                and drop types not reachable from the trait or the
#                                kept events. This removes the spec entries leaked
#                                into the wasm from linked common-* crates (e.g.
#                                common-interfaces via common-helpers).
#   5. apply_renames           - Args -> {Contract}Args, Client -> {Contract}Client,
#                                Contract -> {Contract}Interface
#   6. use_common_message_types - onramp/fee_quoter: replace StellarToAnyMessage and
#                                TokenAmount with re-exports from common_message
#
# Usage:
#   ./scripts/gen_interfaces.sh              # Generate interfaces (builds first)
#   ./scripts/gen_interfaces.sh --no-build   # Skip build, use existing wasm files
#
# Requires: stellar CLI (>= 25.1.0; CI pins 25.1.0, 28.x is verified compatible),
#           contracts built (stellar contract build)

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

# Contract config: "wasm_basename|output_module|PascalCaseName|use_common_message|event_prefixes"
# use_common_message=1 when the trait uses StellarToAnyMessage (avoids type conflicts)
CONTRACTS=(
  "fee_quoter|fee_quoter|FeeQuoter|1|auth_,fq_"
  "ccvs_committee_verifier|committee_verifier|CommitteeVerifier|0|auth_,ccv_"
  "ccvs_versioned_verifier_resolver|versioned_verifier_resolver|VersionedVerifierResolver|0|auth_,vvr_"
  "onramp|onramp|OnRamp|1|auth_,onramp_"
  "rmn_proxy|rmn_proxy|RmnProxy|0|auth_,rmn_proxy_"
  "rmn_remote|rmn_remote|RmnRemote|0|auth_,rmn_"
  "offramp|offramp|OffRamp|0|auth_,offramp_"
  "router|router|Router|0|auth_,router_"
  "ccip_ramp_registry|ramp_registry|RampRegistry|0|auth_,ramp_"
  "ccip_receiver_example|ccip_receiver|ExampleCcipReceiver|0|auth_,example_"
  "token_admin_registry|token_admin_registry|TokenAdminRegistry|0|auth_,tar_"
  "pools_lock_release_pool|lock_release_pool|LockReleasePool|0|auth_,pool_"
  "pools_burn_mint_pool|burn_mint_pool|BurnMintPool|0|auth_,pool_"
  "pools_token_lock_box|token_lock_box|TokenLockBox|0|auth_,lockbox_"
  "pools_siloed_lock_release_pool|siloed_lock_release_pool|SiloedLockReleasePool|0|auth_,pool_"
  "mcms|mcms|Mcms|0|auth_,mcms_"
  "timelock|timelock|Timelock|0|tl_"
  "forwarder|forwarder|cre|0|auth_,forwarder_"
  "data_feeds_cache|data_feeds_cache|DataFeedsCache|0|"
  "data_feeds_proxy|data_feeds_proxy|DataFeedsProxy|0|"
)

# Remove the WASM const block from generated output (interfaces don't need it)
strip_wasm_block() {
  sed -e '/^pub const WASM/,/^);$/d'
}

# Normalize attribute forms across stellar-cli versions to the canonical
# `export = false` form. CLI 25.x emits it natively; CLI 28.x omits it. The
# interfaces crate is only ever linked into other contracts, never deployed
# itself, and export = false keeps its types/events out of dependent contracts'
# wasm specs (otherwise every contract embedding common-interfaces balloons past
# the network's wasm-upload resource limits).
normalize_attributes() {
  perl -0 -pe '
    s/#\[\s*soroban_sdk::contracttype\s*(?:\(\s*export\s*=\s*false\s*\))?\s*\]/#[soroban_sdk::contracttype(export = false)]/gs;
    s/#\[\s*soroban_sdk::contracterror\s*(?:\(\s*export\s*=\s*false\s*\))?\s*\]/#[soroban_sdk::contracterror(export = false)]/gs;
    s{#\[\s*soroban_sdk::contractevent\s*\(([^)]*)\)\s*\]}{"#[soroban_sdk::contractevent(topics = [" . join(", ", map { "\"$_\"" } ($1 =~ /"([^"]*)"/g)) . "], export = false)]"}ges;
  '
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

# Prune a generated interface module to the contract's own surface. The wasm spec
# of every contract that (transitively) links common-interfaces embeds the spec
# entries of the whole dependency graph, so the raw bindings contain other
# contracts' types and events. Keeps:
#   - the trait (with duplicate fn declarations removed),
#   - events whose first topic matches one of the given prefixes (all if none given),
#   - contracttype/contracterror definitions reachable (transitively) from the
#     trait fn signatures or the kept events.
# Duplicate type/event names keep their first occurrence.
prune_interface() {
  local prefixes="$1"
  perl -e '
    use strict; use warnings;
    my @prefixes = grep { length } split /,/, ($ARGV[0] // "");
    local $/;
    my $input = <STDIN>;
    my @lines = split /\n/, $input;

    # Split into top-level items: an attribute run followed by a struct/enum/trait.
    my @items;
    my $i = 0;
    while ($i <= $#lines) {
        my $start = $i;
        $i++ while ($i <= $#lines && $lines[$i] =~ /^#\[/);
        my ($kind, $name) = ("other", undef);
        if ($i <= $#lines && $lines[$i] =~ /^pub\s+(?:struct|enum)\s+(\w+)\s*\{/) {
            $name = $1;
            my $attrs = join("\n", @lines[$start .. $i - 1]);
            $kind = "event" if $attrs =~ /contractevent/;
            $kind = "type"  if $attrs =~ /contracttype|contracterror/;
            $i++;
            $i++ while ($i <= $#lines && $lines[$i] !~ /^\}/);
            $i++ if $i <= $#lines;
        } elsif ($i <= $#lines && $lines[$i] =~ /^pub\s+trait\s+\w+/) {
            $kind = "trait";
            $i++;
            $i++ while ($i <= $#lines && $lines[$i] !~ /^\}/);
            $i++ if $i <= $#lines;
        } else {
            $i = $start + 1;
        }
        push @items, { kind => $kind, name => $name,
                       text => join("\n", @lines[$start .. $i - 1]) };
    }

    # Deduplicate fn declarations inside the trait (keeps first occurrence).
    for my $it (@items) {
        next unless $it->{kind} eq "trait";
        if ($it->{text} =~ /^(.*\{)(.*?)(\})$/s) {
            my ($hdr, $body, $close) = ($1, $2, $3);
            my %seen;
            my $clean = "";
            while ($body =~ m!(\s*fn\s+(\w+)\s*\(.*?;)!gs) {
                $clean .= $1 unless $seen{$2}++;
            }
            $it->{text} = "$hdr$clean\n$close";
        }
    }

    # Filter events by topic prefix. A prefix matches when the topic starts with
    # it and the following character is uppercase (namespace segments are
    # lowercase snake_case, event names PascalCase).
    if (@prefixes) {
        @items = grep {
            $_->{kind} ne "event" || do {
                my ($topic) = $_->{text} =~ /topics\s*=\s*\[\s*"([^"]+)"/;
                !$topic || grep {
                    index($topic, $_) == 0 &&
                    (length($topic) == length($_) ||
                     substr($topic, length($_), 1) =~ /[A-Z]/)
                } @prefixes;
            }
        } @items;
    }

    # Deduplicate types and events by name (keeps first occurrence).
    my %seen_named;
    @items = grep {
        ($_->{kind} ne "type" && $_->{kind} ne "event") || !$seen_named{$_->{name}}++;
    } @items;

    # Drop types not reachable from the trait or the kept events.
    my %type_text;
    for my $it (@items) {
        $type_text{$it->{name}} //= $it->{text} if $it->{kind} eq "type";
    }
    my %keep;
    my @work;
    my $collect = sub {
        my $text = shift;
        while ($text =~ /\b([A-Z][A-Za-z0-9_]*)\b/g) {
            push @work, $1 if exists $type_text{$1} && !$keep{$1};
        }
    };
    for my $it (@items) {
        $collect->($it->{text}) if $it->{kind} eq "trait" || $it->{kind} eq "event";
    }
    while (@work) {
        my $n = pop @work;
        next if $keep{$n}++;
        $collect->($type_text{$n});
    }
    @items = grep { $_->{kind} ne "type" || $keep{$_->{name}} } @items;

    my $result = join("\n", map { $_->{text} } @items) . "\n";
    $result =~ s/\n{3,}/\n/g;
    print $result;
  ' "$prefixes"
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
    my $removed = 0;
    # Remove the structs (with preceding attribute runs)
    $removed++ if s/(?:#\[[^\n]*\]\n)+pub struct TokenAmount \{[^}]*\}\n?//;
    $removed++ if s/(?:#\[[^\n]*\]\n)+pub struct StellarToAnyMessage \{[^}]*\}\n?//;
    if ($removed) {
      my @used;
      push @used, "StellarToAnyMessage" if /\bStellarToAnyMessage\b/;
      push @used, "TokenAmount" if /\bTokenAmount\b/;
      $_ = "use common_message::{" . join(", ", @used) . "};\n\n" . $_ if @used;
    }
  '
}

# Older stellar-cli versions omit the contract-owned Ccv*/RemoteChainConfig struct
# defs referenced by the ccip_receiver trait. Insert them if missing (safety net;
# current CLI versions emit them and this is a no-op).
patch_ccip_receiver_interfaces() {
  local f="$1"
  perl -i -0pe '
    if ($_ !~ /pub struct CcvChainConfig/s) {
      my $ccv = "#[soroban_sdk::contracttype(export = false)]\n#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]\npub struct CcvConfigUpdate {\n    pub source_chain_selector: u64,\n    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_threshold: u32,\n}\n#[soroban_sdk::contracttype(export = false)]\n#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]\npub struct CcvChainConfig {\n    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_threshold: u32,\n}\n#[soroban_sdk::contracttype(export = false)]\n#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]\npub struct RemoteChainConfig {\n    pub extra_args: soroban_sdk::Bytes,\n    pub allowed_finality_config: u32,\n}\n#[soroban_sdk::contracttype(export = false)]\n#[derive(Debug, Clone, Eq, PartialEq, Ord, PartialOrd)]\npub struct CcvsAndFinalityConfig {\n    pub required_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_ccvs: soroban_sdk::Vec<soroban_sdk::Address>,\n    pub optional_threshold: u32,\n    pub allowed_finality_config: u32,\n}\n";
      s/(\n\}\n)((?:#\[[^\n]*\]\n)+pub struct TokenAmount)/$1 . $ccv . $2/se;
    }
  ' "$f"
}

# RMN Remote uses the workspace `CCIPError`; replace the emitted copy with it so
# typed cross-contract callers share one error type.
patch_rmn_remote_interfaces() {
  local f="$1"
  perl -i -0pe '
    unless (/^pub use common_error::CCIPError;/m) {
      $_ = "//! RMN Remote interface (generated from rmn_remote.wasm; uses common_error::CCIPError).\n\npub use common_error::CCIPError;\n\n$_";
    }
    s/\n(?:#\[[^\n]*\]\n)+pub enum CCIPError \{[^}]*\}//s;
  ' "$f"
}

# Ramp registry uses the workspace `CCIPError`; replace the emitted copy with it.
patch_ramp_registry_interfaces() {
  local f="$1"
  perl -i -0pe '
    unless (/^pub use common_error::CCIPError;/m) {
      $_ = "//! Ramp registry interface (generated from ccip_ramp_registry.wasm; uses common_error::CCIPError).\n\npub use common_error::CCIPError;\n\n$_";
    }
    s/\n(?:#\[[^\n]*\]\n)+pub enum CCIPError \{[^}]*\}//s;
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
  (cd "$REPO_ROOT/contracts/data-feeds" && CARGO_TARGET_DIR="$REPO_ROOT/target" stellar contract build)
fi

for entry in "${CONTRACTS[@]}"; do
  IFS='|' read -r wasm_basename output_module pascal_name use_common_msg event_prefixes <<< "$entry"
  wasm_path="$WASM_DIR/${wasm_basename}.wasm"
  out_path="$INTERFACES_DIR/${output_module}.rs"

  if [[ ! -f "$wasm_path" ]]; then
    echo "ERROR: $wasm_path not found for $output_module" >&2
    exit 1
  fi

  echo "Generating interface for $output_module..."
  stellar contract bindings rust --wasm "$wasm_path" 2>/dev/null \
    | strip_wasm_block \
    | normalize_attributes \
    | strip_duplicate_auth_events \
    | prune_interface "${event_prefixes:-}" \
    | apply_renames "$pascal_name" \
    | use_common_message_types "${use_common_msg:-0}" \
    > "$out_path"

  if [[ "$output_module" == "ccip_receiver" ]]; then
    patch_ccip_receiver_interfaces "$out_path"
  fi
  if [[ "$output_module" == "ramp_registry" ]]; then
    patch_ramp_registry_interfaces "$out_path"
  fi
  if [[ "$output_module" == "rmn_remote" ]]; then
    patch_rmn_remote_interfaces "$out_path"
  fi
done

echo "Done. Interfaces written to $INTERFACES_DIR"
