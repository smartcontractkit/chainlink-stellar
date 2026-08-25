#!/usr/bin/env bash
# Generate Go bindings for all contracts from their compiled wasm specs.
#
# Each contract's spec (functions, types, events including data_format) is
# read straight from the wasm via `stellar contract info interface --output
# json-formatted`, so the generated Go always matches the deployed artifact.
# The one exception is token_pool: it is a lib-only base crate with no wasm,
# so it still generates from its committed Rust interface file.
#
# Usage:
#   ./scripts/gen_bindings.sh                # Build wasms, then generate
#   ./scripts/gen_bindings.sh --no-build     # Use existing wasms in target/
#   ./scripts/gen_bindings.sh --no-interfaces  # Alias of --no-build (compat)
#
# Requires: go, stellar CLI, rust toolchain (unless --no-build)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
INTERFACES_DIR="$REPO_ROOT/contracts/common/interfaces/src"
BINDINGS_DIR="$REPO_ROOT/bindings"
CONTRACTS_DIR="$BINDINGS_DIR/contracts"
WASM_DIR="$REPO_ROOT/target/wasm32v1-none/release"

# Contract config: "wasm_basename|PascalCaseName|go_package|events_file|readonly_fns|include_void_fns"
# wasm_basename: the wasm in target/wasm32v1-none/release the spec is read
# from; the literal value "interface:<module>" falls back to the committed
# Rust interface file (only token_pool, which has no wasm)
# events_file: optional path (relative to REPO_ROOT) to a Rust events source
# file for -events; only needed for events that exist in no wasm
# readonly_fns: optional comma-separated -readonly list; when set, listed fns simulate and
# all others submit transactions; when empty the name heuristic (get_*/is_*/owner/balance) applies
# include_void_fns: optional comma-separated -include-void list; listed void fns (no return
# type) get generated methods, unlisted ones are omitted
CONTRACTS=(
  "ccvs_committee_verifier|CommitteeVerifier|committee_verifier|"
  "fee_quoter|FeeQuoter|fee_quoter|"
  "ccvs_versioned_verifier_resolver|VersionedVerifierResolver|versioned_verifier_resolver|"
  "onramp|OnRamp|onramp|"
  "rmn_proxy|RmnProxy|rmn_proxy|"
  "rmn_remote|RmnRemote|rmn_remote|"
  "offramp|OffRamp|offramp|"
  "router|Router|router|"
  "ccip_ramp_registry|RampRegistry|ramp_registry|"
  # ccip_receiver's wasm spec is missing types its public functions use
  # (CcvChainConfig etc. are #[contracttype(export = false)], which excludes
  # them from the spec — a contract-side ABI bug worth fixing there). Until
  # then it generates from its committed interface file.
  "interface:ccip_receiver|ExampleCcipReceiver|ccip_receiver|"
  "token_admin_registry|TokenAdminRegistry|token_admin_registry|"
  "pools_lock_release_pool|LockReleasePool|lock_release_pool|contracts/pools/lock-release-pool/src/events.rs"
  "pools_burn_mint_pool|BurnMintPool|burn_mint_pool|contracts/pools/burn-mint-pool/src/events.rs"
  "interface:token_pool|TokenPool|token_pool|"
  "pools_token_lock_box|TokenLockBox|token_lock_box|"
  "pools_siloed_lock_release_pool|SiloedLockReleasePool|siloed_lock_release_pool|"
  "mcms|Mcms|mcms|"
  "timelock|Timelock|timelock|"
  "forwarder|Forwarder|cre|"
  "data_feeds_cache|DataFeedsCache|data_feeds_cache||latest_round,get_round,round_range,find_round,decimals,description,get_feed_permissions,has_permission,is_feed_admin,is_frozen,is_configured,version,type_and_version,get_owner|upgrade,recover_tokens,accept_ownership,renounce_ownership,transfer_ownership"
  "data_feeds_proxy|DataFeedsProxy|data_feeds_proxy||latest_round,get_round,decimals,description,version,type_and_version,get_owner|upgrade,set_cache,recover_tokens,accept_ownership,renounce_ownership,transfer_ownership"
)

run_build=true
for arg in "$@"; do
  case "$arg" in
    --no-build|--no-interfaces) run_build=false ;;
    -h|--help)
      echo "Usage: $0 [--no-build]"
      echo "  --no-build  Skip 'stellar contract build', use existing wasms in target/"
      exit 0
      ;;
  esac
done

cd "$REPO_ROOT"

if [[ "$run_build" == true ]]; then
  echo "Building contracts..."
  stellar contract build
  (cd "$REPO_ROOT/contracts/data-feeds" && CARGO_TARGET_DIR="$REPO_ROOT/target" stellar contract build)
  echo ""
fi

mkdir -p "$CONTRACTS_DIR"

for entry in "${CONTRACTS[@]}"; do
  IFS='|' read -r wasm_basename pascal_name pkg events_file readonly_fns include_void_fns <<< "$entry"
  out_dir="$CONTRACTS_DIR/$pkg"

  events_flag=""
  if [[ -n "$events_file" ]]; then
    events_flag="-events $REPO_ROOT/$events_file"
  fi

  readonly_flag=""
  if [[ -n "${readonly_fns:-}" ]]; then
    readonly_flag="-readonly $readonly_fns"
  fi

  void_flag=""
  if [[ -n "${include_void_fns:-}" ]]; then
    void_flag="-include-void $include_void_fns"
  fi

  echo "Generating Go bindings for $pascal_name..."
  if [[ "$wasm_basename" == interface:* ]]; then
    iface_path="$INTERFACES_DIR/${wasm_basename#interface:}.rs"
    if [[ ! -f "$iface_path" ]]; then
      echo "ERROR: $iface_path not found for $pkg" >&2
      exit 1
    fi
    (cd "$BINDINGS_DIR" && go run ./generator -name "$pascal_name" -pkg "$pkg" -out "$out_dir" $events_flag $readonly_flag $void_flag) < "$iface_path"
  else
    wasm_path="$WASM_DIR/${wasm_basename}.wasm"
    if [[ ! -f "$wasm_path" ]]; then
      echo "ERROR: $wasm_path not found for $pkg (run without --no-build?)" >&2
      exit 1
    fi
    stellar contract info interface --wasm "$wasm_path" --output json-formatted 2>/dev/null       | (cd "$BINDINGS_DIR" && go run ./generator -spec-json -name "$pascal_name" -pkg "$pkg" -out "$out_dir" $events_flag $readonly_flag $void_flag)
  fi
done

echo ""
echo "Done. Bindings written to $CONTRACTS_DIR"
