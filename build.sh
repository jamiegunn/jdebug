#!/usr/bin/env bash
#
# build.sh — rebuild jdebug's Go parts for THIS machine, in one command.
#
# The bash kit (jdebug + observe/*.sh + capture/*.sh) needs NO build — it runs
# as-is. Only two Go binaries are compiled, and jdebug prefers your freshly-built
# copies (tui/jdebug-tui, core/jdebug-core) over the committed vendored ones:
#   • tui/jdebug-tui    the interactive menu/wizard + the heap-dump analyzer
#   • core/jdebug-core  the v2 capture engine (threads/heap/jcmd + analyze)
#
# Usage:
#   ./build.sh              build both binaries for this machine, then vet them
#   ./build.sh --test       …and run the full test suite (bash kit + Go units)
#   ./build.sh --vendor     …and re-vendor the multi-platform, hash-verified
#                           binaries under vendor/tui + tools/core (what the
#                           pre-commit hook does — run before committing Go changes)
#   ./build.sh --all        build + test + vendor
#   ./build.sh --clean      remove the local dev builds (back to vendored)
#   ./build.sh --tui | --core   build only that one
#   -h | --help
#
# Needs the Go toolchain (https://go.dev/dl or `brew install go`). Without Go,
# jdebug still works off the committed vendored binaries — you just can't rebuild.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

# --- options -----------------------------------------------------------------
DO_TUI=1; DO_CORE=1; DO_TEST=0; DO_VENDOR=0; DO_CLEAN=0
only=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --test)   DO_TEST=1 ;;
        --vendor) DO_VENDOR=1 ;;
        --all)    DO_TEST=1; DO_VENDOR=1 ;;
        --clean)  DO_CLEAN=1 ;;
        --tui)    only=tui ;;
        --core)   only=core ;;
        -h|--help) sed -n '2,/^set /p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "build.sh: unknown option '$1' (try --help)" >&2; exit 64 ;;
    esac
    shift
done
[[ "$only" == tui  ]] && DO_CORE=0
[[ "$only" == core ]] && DO_TUI=0

# --- pretty output (respects NO_COLOR) ---------------------------------------
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; D=$'\033[2m'; X=$'\033[0m'
else B=""; G=""; Y=""; R=""; D=""; X=""; fi
step() { printf '%s▸ %s%s\n' "$B" "$*" "$X"; }
ok()   { printf '  %s✓%s %s\n' "$G" "$X" "$*"; }
warn() { printf '  %s!%s %s\n' "$Y" "$X" "$*"; }
die()  { printf '%s✗ %s%s\n' "$R" "$*" "$X" >&2; exit 1; }

# --- clean short-circuit -----------------------------------------------------
if [[ "$DO_CLEAN" == 1 ]]; then
    step "clean"
    rm -f tui/jdebug-tui core/jdebug-core
    ok "removed tui/jdebug-tui and core/jdebug-core — jdebug falls back to the vendored binaries"
    exit 0
fi

# --- toolchain ---------------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
    printf '%s✗ Go isn'\''t installed (or not on PATH), so the Go binaries can'\''t be rebuilt.%s\n' "$R" "$X" >&2
    echo   "  install it: https://go.dev/dl  (macOS: brew install go), then re-run ./build.sh" >&2
    echo   "  meanwhile jdebug still runs off the committed vendored binaries — try: ./jdebug --help" >&2
    exit 1
fi
step "toolchain"
ok "$(go version)"

built=()

# --- build TUI ---------------------------------------------------------------
if [[ "$DO_TUI" == 1 ]]; then
    step "build TUI  (tui/jdebug-tui)"
    ( cd tui && go build -o jdebug-tui . ) || die "TUI build failed"
    ( cd tui && go vet ./... ) >/dev/null 2>&1 && ok "built + vet clean" || warn "built, but 'go vet' (in tui/) had notes"
    built+=("tui/jdebug-tui")
fi

# --- build core engine -------------------------------------------------------
if [[ "$DO_CORE" == 1 ]]; then
    step "build core (core/jdebug-core)"
    ( cd core && go build -o jdebug-core ./cmd/jdebug-core ) || die "core build failed"
    ( cd core && go vet ./... ) >/dev/null 2>&1 && ok "built + vet clean" || warn "built, but 'go vet' (in core/) had notes"
    built+=("core/jdebug-core")
fi

# --- optional: full test suite ----------------------------------------------
if [[ "$DO_TEST" == 1 ]]; then
    step "test  (bash kit + Go units)"
    ( cd tui  && go test ./... >/dev/null ) && ok "tui  go test" || die "tui go test failed"
    ( cd core && go test ./... >/dev/null ) && ok "core go test" || die "core go test failed"
    # the shell suite prints its own per-case lines + a final tally
    ./tests/run-tests.sh || die "shell suite failed"
fi

# --- optional: re-vendor multi-platform, hash-verified binaries --------------
if [[ "$DO_VENDOR" == 1 ]]; then
    step "vendor (darwin+linux · arm64+amd64, hash-verified)"
    ./scripts/vendor-tui.sh --force
    [[ -x ./scripts/vendor-core.sh ]] && ./scripts/vendor-core.sh || warn "scripts/vendor-core.sh not runnable — skipped"
    ok "vendored binaries + SHA256SUMS refreshed (commit these with your Go changes)"
fi

# --- summary -----------------------------------------------------------------
step "done"
if [[ ${#built[@]} -gt 0 ]]; then
    for b in "${built[@]}"; do ok "$b"; done
    printf '%s  jdebug now uses these local builds. Run it:%s  ./jdebug --help   ·   ./jdebug (menu)\n' "$D" "$X"
fi
[[ "$DO_TEST"   == 0 ]] && printf '%s  tip: ./build.sh --test    runs the full suite (453 shell assertions + Go units)%s\n' "$D" "$X"
[[ "$DO_VENDOR" == 0 ]] && printf '%s  tip: ./build.sh --vendor  refreshes the committed cross-platform binaries%s\n' "$D" "$X"
exit 0
