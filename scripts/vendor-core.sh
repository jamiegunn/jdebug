#!/usr/bin/env bash
#
# vendor-core.sh — build the v2 capture engine (core/) for every supported
# platform and vendor the binaries into tools/core/ with hash proof, so the
# engine ships runnable to users who have no Go toolchain. Symmetric with
# scripts/vendor-tui.sh; run it whenever core/ sources change.
#
# Writes:
#   tools/core/jdebug-core-<os>-<arch>   static binaries (darwin/linux × arm64/amd64)
#   tools/core/SHA256SUMS                sha256 of each binary (verified by jdebug
#                                        before it will exec a vendored binary)
#   tools/core/BUILDINFO                 go version + source tree hash, binding the
#                                        binaries to the exact sources they came from
#
# Determinism: -trimpath, -buildvcs=false and stripped ldflags make the same
# source + same Go version produce byte-identical binaries.

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

PLATFORMS=("darwin arm64" "linux arm64" "linux amd64")
OUT="tools/core"

# source hash: every input that determines the binaries (module + all .go under core/)
source_hash() {
    { cat core/go.mod core/go.sum 2>/dev/null; find core -name '*.go' -not -name '*_test.go' | sort | xargs cat; } \
        | { command -v sha256sum >/dev/null 2>&1 && sha256sum || shasum -a 256; } | awk '{print $1}'
}

SRC_SHA="$(source_hash)"

if [[ "${1:-}" != "--force" && -f "$OUT/BUILDINFO" ]]; then
    prev="$(awk -F': ' '/^source_sha256/ {print $2}' "$OUT/BUILDINFO")"
    if [[ "$prev" == "$SRC_SHA" ]] && (cd "$OUT" 2>/dev/null && { command -v sha256sum >/dev/null 2>&1 && sha256sum -c SHA256SUMS || shasum -a 256 -c SHA256SUMS; } >/dev/null 2>&1); then
        echo "vendor-core: up to date (source $SRC_SHA)"
        exit 0
    fi
fi

command -v go >/dev/null 2>&1 || {
    echo "vendor-core: Go toolchain required to (re)build the vendored core engine — install Go" >&2
    exit 1
}

echo "vendor-core: building jdebug-core for ${#PLATFORMS[@]} platforms (source $SRC_SHA)"
mkdir -p "$OUT"

# tests first — never vendor a binary whose tests are red
(cd core && go vet ./... && go test ./...)

for p in "${PLATFORMS[@]}"; do
    read -r GOOS GOARCH <<<"$p"
    bin="$OUT/jdebug-core-$GOOS-$GOARCH"
    echo "  → $bin"
    (cd core && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -trimpath -buildvcs=false -ldflags="-s -w" -o "../$bin" ./cmd/jdebug-core)
done

(cd "$OUT" && { command -v sha256sum >/dev/null 2>&1 && sha256sum jdebug-core-* || shasum -a 256 jdebug-core-*; } > SHA256SUMS)

{
    echo "# vendored jdebug-core — written by scripts/vendor-core.sh"
    echo "go_version: $(go version | awk '{print $3}')"
    echo "source_sha256: $SRC_SHA"
    echo "built_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$OUT/BUILDINFO"

echo "vendor-core: done — $(wc -l < "$OUT/SHA256SUMS" | tr -d ' ') binaries hashed in $OUT/SHA256SUMS"
