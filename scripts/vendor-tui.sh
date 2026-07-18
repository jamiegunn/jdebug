#!/usr/bin/env bash
#
# vendor-tui.sh — build the Go TUI for every supported platform and vendor the
# binaries into vendor/tui/ with hash proof. Called by the pre-commit hook
# (githooks/pre-commit) whenever tui/ sources change; run it manually any time.
#
# Writes:
#   vendor/tui/jdebug-tui-<os>-<arch>   static binaries (darwin-arm64, linux-arm64, linux-amd64)
#   vendor/tui/SHA256SUMS               sha256 of each binary (verified by jdebug
#                                       before it will exec a vendored binary)
#   vendor/tui/BUILDINFO                go version + source tree hash, binding the
#                                       binaries to the exact sources they came from
#
# Determinism: -trimpath, -buildvcs=false and stripped ldflags make the same
# source + same Go version produce byte-identical binaries, so committing them
# only changes blobs when the TUI actually changes.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

PLATFORMS=("darwin arm64" "linux arm64" "linux amd64")
OUT="vendor/tui"

sha() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@" | awk '{print $1}'
    else shasum -a 256 "$@" | awk '{print $1}'; fi
}

# source hash: every input that determines the binaries
source_hash() {
    # shellcheck disable=SC2012
    # LC_ALL=C pins a bytewise sort: without it the file order (and thus this
    # hash) depends on the runner's locale — macOS/glibc-UTF8 collate dots/case
    # differently than C, so the same sources fingerprint differently per box.
    cat tui/go.mod tui/go.sum $(ls tui/*.go | LC_ALL=C sort) | { command -v sha256sum >/dev/null 2>&1 && sha256sum || shasum -a 256; } | awk '{print $1}'
}

SRC_SHA="$(source_hash)"

# up to date already? (BUILDINFO records the source hash of the last build)
if [[ "${1:-}" != "--force" && -f "$OUT/BUILDINFO" ]]; then
    prev="$(awk -F': ' '/^source_sha256/ {print $2}' "$OUT/BUILDINFO")"
    if [[ "$prev" == "$SRC_SHA" ]] && (cd "$OUT" 2>/dev/null && { command -v sha256sum >/dev/null 2>&1 && sha256sum -c SHA256SUMS || shasum -a 256 -c SHA256SUMS; } >/dev/null 2>&1); then
        echo "vendor-tui: up to date (source $SRC_SHA)"
        exit 0
    fi
fi

command -v go >/dev/null 2>&1 || {
    echo "vendor-tui: Go toolchain required to (re)build the vendored TUI — install Go, or" >&2
    echo "            commit with --no-verify to skip (the push hook will still refuse stale binaries)" >&2
    exit 1
}

echo "vendor-tui: building jdebug-tui for ${#PLATFORMS[@]} platforms (source $SRC_SHA)"
mkdir -p "$OUT"

# tests first — never vendor a binary whose tests are red
(cd tui && go vet ./... && go test ./...)

for p in "${PLATFORMS[@]}"; do
    read -r GOOS GOARCH <<<"$p"
    bin="$OUT/jdebug-tui-$GOOS-$GOARCH"
    echo "  → $bin"
    (cd tui && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -trimpath -buildvcs=false -ldflags="-s -w" -o "../$bin" .)
done

(cd "$OUT" && { command -v sha256sum >/dev/null 2>&1 && sha256sum jdebug-tui-* || shasum -a 256 jdebug-tui-*; } > SHA256SUMS)

{
    echo "# vendored jdebug-tui — written by scripts/vendor-tui.sh (pre-commit hook)"
    echo "go_version: $(go version | awk '{print $3}')"
    echo "source_sha256: $SRC_SHA"
    echo "built_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$OUT/BUILDINFO"

echo "vendor-tui: done — $(wc -l < "$OUT/SHA256SUMS" | tr -d ' ') binaries hashed in $OUT/SHA256SUMS"
