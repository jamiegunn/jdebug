#!/usr/bin/env bash
# jdebug — shared helpers. PORTABLE: no assumptions about any particular app,
# namespace, or kubeconfig. Targets whatever `kubectl`/$KUBECONFIG is active.
# Override the target with -n/--namespace, -l/--selector, --container, or the
# JDEBUG_NAMESPACE / JDEBUG_SELECTOR / JDEBUG_CONTAINER environment variables.

set -euo pipefail

# Remembered target — the menu's target editor saves its selections here so
# they survive between sessions. Precedence: flags > environment > saved >
# built-in. Change values in the menu (or delete the file) to forget.
JDEBUG_CONFIG_DIR="${JDEBUG_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/jdebug}"
JDEBUG_TARGET_FILE="$JDEBUG_CONFIG_DIR/target"
if [[ -f "$JDEBUG_TARGET_FILE" ]]; then
    # The file is written by the menu's target editor with printf %q, and
    # sourcing EXECUTES it — so gate it first with a WHITELIST: every line must
    # be a comment, blank, or a plain SAVED_* assignment whose value uses only
    # benign characters (or is the empty ''). A prefix-only check is bypassable
    # ("SAVED_NAMESPACE=x; evil" and "SAVED_NAMESPACE=x evil" both start with a
    # valid assignment but execute a command), so the WHOLE line is matched.
    # Anything fancier means tampered/corrupted; ignore it (fall back to
    # defaults, with a warning) rather than execute it.
    if grep -qvE "^(#.*|[[:space:]]*|SAVED_[A-Z_]+=(''|[A-Za-z0-9_.,:/=@%+-]*))$" "$JDEBUG_TARGET_FILE" 2>/dev/null; then
        printf 'warning: ignoring %s — unexpected content (not a saved-target file); using defaults\n' \
            "$JDEBUG_TARGET_FILE" >&2
    else
        # shellcheck source=/dev/null
        source "$JDEBUG_TARGET_FILE" 2>/dev/null || true
    fi
fi

# ${VAR+x} tests set-ness (not emptiness): an exported-but-empty SELECTOR must
# keep meaning "any pod", not fall through to the saved value.
[[ -n "${NAMESPACE+x}"     ]] || NAMESPACE="${JDEBUG_NAMESPACE:-${SAVED_NAMESPACE:-default}}"
[[ -n "${SELECTOR+x}"      ]] || SELECTOR="${JDEBUG_SELECTOR:-${SAVED_SELECTOR:-}}"      # empty = any pod
[[ -n "${APP_CONTAINER+x}" ]] || APP_CONTAINER="${JDEBUG_CONTAINER:-${SAVED_CONTAINER:-app}}"
if [[ -z "${ACTUATOR_BASE+x}" && -n "${SAVED_ACTUATOR:-}" ]]; then
    ACTUATOR_BASE="$SAVED_ACTUATOR"; export ACTUATOR_BASE
fi
# actuator auth is a REFERENCE to pod env vars ("bearer:VAR"/"basic:U:P"),
# never a secret value. env > saved.
[[ -n "${ACTUATOR_AUTH+x}" ]] || ACTUATOR_AUTH="${JDEBUG_ACTUATOR_AUTH:-${SAVED_ACTUATOR_AUTH:-}}"
export ACTUATOR_AUTH
: "${JDK_DEBUG_IMAGE:=${JDEBUG_JDK_IMAGE:-eclipse-temurin:21-jdk-alpine}}"

# Cache for the downloaded jattach binary — a standard per-user location so the
# kit works the same whether it's run from a repo checkout or installed on PATH.
: "${JDEBUG_CACHE_DIR:=${XDG_CACHE_HOME:-$HOME/.cache}/jdebug}"

# Where operator-side captures (dumps, snapshots) land — under the kit itself,
# NOT the caller's CWD, so they're always in one findable place and covered by
# the kit's .gitignore. Override per run with $OUT_DIR, or move the root with
# $JDEBUG_DUMPS.
JDEBUG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${JDEBUG_DUMPS:=$JDEBUG_ROOT/dumps}"

# NOTE: no automatic KUBECONFIG rewriting. jdebug uses the ambient kubectl
# context. Point it at a cluster the normal way (KUBECONFIG=... or kubectl config
# use-context), exactly like kubectl itself.

err()  { printf 'error: %s\n' "$*" >&2; }
info() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }

require_cmd() {
    for cmd in "$@"; do
        command -v "$cmd" >/dev/null 2>&1 || { err "missing required command: $cmd"; exit 127; }
    done
}

# usage — print the calling script's header comment block (line 2 to the first
# blank line) as its --help text. Every tool keeps its docs in the header.
usage() {
    sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

# announce_target — print the resolved target to stderr so every command makes
# clear which pod it will hit. Once per process tree (the guard is exported, so a
# tool that shells out to another jdebug tool doesn't repeat it). Silence with
# JDEBUG_QUIET=1; respects NO_COLOR.
announce_target() {
    [[ -n "${JDEBUG_TARGET_ANNOUNCED:-}" || -n "${JDEBUG_QUIET:-}" ]] && return 0
    export JDEBUG_TARGET_ANNOUNCED=1
    local d="" o=""; [[ -t 2 && -z "${NO_COLOR:-}" ]] && { d=$'\033[2m'; o=$'\033[0m'; }
    printf '%sjdebug → namespace=%s  selector=%s  container=%s%s%s\n' \
        "$d" "$NAMESPACE" "${SELECTOR:-<any pod>}" "$APP_CONTAINER" \
        "${KUBECONFIG:+  kubeconfig=$KUBECONFIG}" "$o" >&2
}

# parse_common_args <args...> — consumes -n/--namespace, -l/--selector,
# --container, and -h/--help. Sets NAMESPACE/SELECTOR/APP_CONTAINER; leaves the
# rest in REMAINING_ARGS. Announces the resolved target once it has parsed them.
parse_common_args() {
    REMAINING_ARGS=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -n|--namespace) NAMESPACE="$2"; shift 2 ;;
            -l|--selector)  SELECTOR="$2";  shift 2 ;;
            --container)    APP_CONTAINER="$2"; shift 2 ;;
            --actuator-base) ACTUATOR_BASE="$2"; export ACTUATOR_BASE; shift 2 ;;
            -h|--help)      usage; exit 0 ;;
            --) shift; REMAINING_ARGS+=("$@"); break ;;
            *)  REMAINING_ARGS+=("$1"); shift ;;
        esac
    done
    announce_target
}

# show_cmd <words...> — echo the exact command a tool is about to run, so every
# capture doubles as a copy-pasteable cookbook.
show_cmd() { printf '  $ %s\n' "$*" >&2; }

# _pod_auth <client:curl|wget> — emit auth flags for a secured actuator, from
# $ACTUATOR_AUTH. The spec references env var NAMES that live INSIDE the pod
# ("bearer:VAR" or "basic:USERVAR:PASSVAR"), so the secret is expanded by the
# pod's shell and never leaves the pod or touches jdebug's config. The emitted
# flags contain a literal $VAR (escaped here) for the pod to expand.
_pod_auth() {
    local client="$1" spec="${ACTUATOR_AUTH:-}"
    case "$spec" in
        bearer:?*)
            local v="${spec#bearer:}"
            if [ "$client" = curl ]; then printf -- '-H "Authorization: Bearer $%s" ' "$v"
            else printf -- '--header="Authorization: Bearer $%s" ' "$v"; fi ;;
        basic:?*:?*)
            local rest="${spec#basic:}" u p; u="${rest%%:*}"; p="${rest#*:}"
            if [ "$client" = curl ]; then printf -- '-u "$%s:$%s" ' "$u" "$p"
            else printf -- '--user="$%s" --password="$%s" ' "$u" "$p"; fi ;;
    esac
}

# pod_fetch <url> [accept] — emit an sh snippet that GETs <url> from INSIDE the
# pod with whatever HTTP client it has: curl, else busybox wget (stock
# JRE-alpine ships wget, not curl). Applies $ACTUATOR_AUTH when set (secured
# actuator). Run via `kubectl exec -- sh -c "$(pod_fetch ...)"`.
pod_fetch() {
    local url="$1" accept="${2:-}"
    local ac aw; ac="$(_pod_auth curl)"; aw="$(_pod_auth wget)"
    local nohttp="echo 'error: neither curl nor wget exists in this container — the actuator tier cannot run here (jattach needs no HTTP: --via jattach)' >&2; exit 127"
    if [[ -n "$accept" ]]; then
        echo "if command -v curl >/dev/null 2>&1; then curl -fsS ${ac}-H 'Accept: $accept' '$url'; elif command -v wget >/dev/null 2>&1; then wget -qO- ${aw}--header='Accept: $accept' '$url' 2>/dev/null || wget -qO- '$url'; else $nohttp; fi"
    else
        echo "if command -v curl >/dev/null 2>&1; then curl -fsS ${ac}'$url'; elif command -v wget >/dev/null 2>&1; then wget -qO- ${aw}'$url'; else $nohttp; fi"
    fi
}

# pod_post_json <url> <json> — same idea for a JSON POST (busybox wget speaks
# --post-data). The JSON must not contain single quotes.
pod_post_json() {
    echo "if command -v curl >/dev/null 2>&1; then curl -fsS -X POST -H 'Content-Type: application/json' -d '$2' '$1'; elif command -v wget >/dev/null 2>&1; then wget -qO- --header='Content-Type: application/json' --post-data='$2' '$1'; else echo 'error: neither curl nor wget exists in this container' >&2; exit 127; fi"
}

# pod_http_status <url> — emit an sh snippet that prints ONLY the HTTP status
# code for <url> (best-effort), so a FAILED actuator fetch can be classified as
# secured (401/403) vs absent (404) rather than a generic "it didn't work".
# Prints 000 when no HTTP client can determine it. Applies $ACTUATOR_AUTH like
# pod_fetch so a correctly-authed-but-missing endpoint still reads as 404.
pod_http_status() {
    local url="$1" ac; ac="$(_pod_auth curl)"
    echo "if command -v curl >/dev/null 2>&1; then curl -s -o /dev/null -w '%{http_code}' ${ac}'$url' 2>/dev/null || echo 000; elif command -v wget >/dev/null 2>&1; then wget -S -O /dev/null '$url' 2>&1 | awk '/HTTP\/[0-9]/{c=\$2} END{print (c==\"\"?\"000\":c)}'; else echo 000; fi"
}

# classify_capture <file> — sniff the first bytes of a would-be dump and name
# what it actually looks like, so a junior isn't sent to Eclipse MAT with an
# error page. Echoes a one-line classification, or nothing when the content
# looks like real binary/unknown data (no confident guess). Used at capture
# time (a 200 that isn't a dump) and by analyze on kept-around bad files.
classify_capture() {
    local f="$1" head
    if [ ! -s "$f" ]; then
        echo "empty file (0 bytes) — the download returned nothing"
        return
    fi
    # strip NULs so `case` globs work on binary-ish input; sniff the head only
    head="$(head -c 512 "$f" 2>/dev/null | tr -d '\000')"
    case "$head" in
        *"<!DOCTYPE html"*|*"<!doctype html"*|*"<html"*|*"<HTML"*)
            case "$head" in
                *login*|*Login*|*password*|*Password*|*j_spring_security*|*"sign in"*|*"Sign In"*)
                    echo "looks like an HTML login page — the endpoint is secured (you were redirected to a login)" ;;
                *)  echo "looks like an HTML error page — not a heap dump" ;;
            esac ;;
        "{"*|*'"status"'*|*'"error"'*|*'"timestamp"'*|*'"message"'*)
            echo "looks like a JSON error response (a Spring/actuator error) — not a heap dump" ;;
        "HTTP/"*|*"401 Unauthorized"*|*"403 Forbidden"*|*"404 "*)
            echo "looks like a raw HTTP error response — not a heap dump" ;;
        *)  echo "" ;;
    esac
}

# resolve_pods — pod names matching selector in namespace (empty selector = all).
resolve_pods() {
    if [[ -n "$SELECTOR" ]]; then
        kubectl -n "$NAMESPACE" get pods -l "$SELECTOR" \
            -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
    else
        kubectl -n "$NAMESPACE" get pods \
            -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
    fi
}

# resolve_one_pod [explicit-name] — a single pod (explicit, or first match).
# When several pods match and none was named, say so: capturing from a healthy
# replica while a sick one sits next to it is a classic wrong-diagnosis trap.
resolve_one_pod() {
    local explicit="${1:-}"
    if [[ -n "$explicit" ]]; then echo "$explicit"; return; fi
    local pods; pods="$(resolve_pods)"
    if [[ -z "$pods" ]]; then
        err "no pod matched namespace=$NAMESPACE selector='${SELECTOR:-<any>}' — pass -n/-l"
        exit 2
    fi
    local pod n; pod="$(printf '%s\n' "$pods" | head -n1)"
    n="$(printf '%s\n' "$pods" | grep -c .)"
    if [[ "$n" -gt 1 ]]; then
        if [[ -n "${JDEBUG_DESTRUCTIVE:-}" ]]; then
            # a destructive operation must never hit a guessed replica —
            # capturing from a healthy pod while the sick one sits next to it
            # is the classic wrong-diagnosis trap, and here it also HURTS.
            # JDEBUG_DESTRUCTIVE_WHY lets each verb say what the harm is
            # (heap: pauses the JVM; kill: deletes a pod; restart: re-rolls).
            err "$n pods match and this operation ${JDEBUG_DESTRUCTIVE_WHY:-PAUSES the JVM} — refusing to guess which replica."
            err "  name the pod explicitly (e.g. the restarting one). Matching pods:"
            printf '%s\n' "$pods" | sed 's/^/    /' >&2
            exit 2
        fi
        info "$n pods match — using $pod. If you meant another (e.g. the restarting one), add its name:"
        printf '%s\n' "$pods" | sed 's/^/           /' >&2
    fi
    echo "$pod"
}

# ensure_dir <dir> — mkdir -p with friendly error.
ensure_dir() {
    mkdir -p "$1" || { err "cannot create directory: $1"; exit 1; }
    # captures (heap dumps!) can hold real production data — owner-only
    chmod go-rwx "$1" 2>/dev/null || true
}

# owning_deployment <pod> — the Deployment that ultimately owns a pod
# (pod → ReplicaSet → strip the -<hash> suffix). Empty if the pod is
# standalone or owned by something else (StatefulSet/DaemonSet/Job). Needs
# python3 for the JSON walk. Prints nothing + returns 1 when it can't tell.
owning_deployment() {
    local pod="$1" js rs
    js="$(kubectl -n "$NAMESPACE" get pod "$pod" -o json 2>/dev/null)" || return 1
    rs="$(printf '%s' "$js" | python3 -c 'import json,sys
for o in json.load(sys.stdin).get("metadata",{}).get("ownerReferences",[]) or []:
    if o.get("kind")=="ReplicaSet": print(o["name"]); break' 2>/dev/null)" || return 1
    [ -n "$rs" ] || return 1
    # the ReplicaSet name is <deployment>-<pod-template-hash>; confirm the
    # Deployment actually exists before returning it
    local dep="${rs%-*}"
    kubectl -n "$NAMESPACE" get deployment "$dep" >/dev/null 2>&1 && { printf '%s' "$dep"; return 0; }
    return 1
}

# session_dir <pod> <ts> — the organized directory a capture writes into:
# dumps/pods/<pod>/<ts>/ , so evidence groups by pod → session and the TUI
# browser can navigate it (pod → date → file). A single capture drops one
# file here; a snapshot drops many. Callers still honour an explicit $OUT_DIR
# (snapshot sets its own; in-pod captures write to /tmp).
session_dir() {
    printf '%s/pods/%s/%s' "$JDEBUG_DUMPS" "$1" "$2"
}

# artifacts_manifest — the file that records what jdebug staged INSIDE a pod
# (jattach, jdebug-local, …), so the TUI can show a remote-artifacts indicator
# and offer cleanup on quit. One tab-separated row per artifact.
artifacts_manifest() { printf '%s/remote-artifacts.tsv' "$JDEBUG_DUMPS"; }

# record_artifact <owned:0|1> <path> [note] — note a file jdebug touched inside
# the current pod. owned=1 = jdebug STAGED it this run (offer to remove it);
# owned=0 = it was already there (never remove). De-duped by pod+path.
record_artifact() {
    local owned="$1" path="$2" note="${3:-}" mf key
    mf="$(artifacts_manifest)"
    mkdir -p "$JDEBUG_DUMPS" 2>/dev/null || return 0
    key="$(printf '\t%s\t%s\t%s\t' "${POD:-}" "${APP_CONTAINER:-}" "$path")"
    if [ -f "$mf" ] && grep -qF "$key" "$mf" 2>/dev/null; then return 0; fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$owned" "${NAMESPACE:-}" "${POD:-}" "${APP_CONTAINER:-}" "$path" "$note" >> "$mf"
}

# heap_data_gate — a heap dump is a full copy of LIVE process memory: it can
# contain credentials, tokens, session data, and customer PII. Always print that
# notice. In a governed environment the org sets JDEBUG_REQUIRE_DATA_ACK=1 (via a
# wrapper, CI, or admission policy); the operator must then acknowledge with
# JDEBUG_DATA_ACK=1, or the dump is refused (exit 65). Off by default, so casual
# use is unchanged — this is the opt-in hook regulated environments asked for.
heap_data_gate() {
    err "⚠ a heap dump is a full copy of live memory — it may contain secrets, tokens, and PII."
    err "  store and delete it per your data-retention/PII policy."
    if [ -n "${JDEBUG_REQUIRE_DATA_ACK:-}" ] && [ -z "${JDEBUG_DATA_ACK:-}" ]; then
        err "  this environment requires sign-off: set JDEBUG_DATA_ACK=1 to acknowledge and proceed."
        exit 65
    fi
}

# jattach_verified_path <uname-s> <uname-m> — echo the path to the vendored
# jattach matching that OS/arch, AFTER verifying it against
# vendor/jattach/SHA256SUMS. Prints nothing and returns non-zero (reason on
# stderr) when no vendored binary covers the platform or the checksum fails.
#
# This is the SINGLE integrity gate for staging jattach anywhere — into a pod
# (capture/jattach.sh), onto this host, or onto an SSH target
# (capture/stage-jattach.sh). Nothing is downloaded at runtime; a tampered or
# corrupt vendored binary is refused before it can run next to a JVM. An
# operator who sets $JATTACH_BINARY makes an explicit choice and bypasses this.
jattach_verified_path() {
    local os="$1" arch="$2" dir plat ta file sums want got
    dir="${JATTACH_VENDOR_DIR:-${SCRIPTS_ROOT:-.}/vendor/jattach}"
    case "$os" in
        Linux) plat="linux" ;;
        *) err "no vendored jattach for OS '$os' (only Linux is vendored)."
           err "  → on macOS you usually don't need it: with a JDK installed, jdebug uses the native jcmd."
           err "  → or use the actuator tier (needs no jattach), or set \$JATTACH_BINARY to your own copy."
           return 1 ;;
    esac
    case "$arch" in
        x86_64|amd64)  ta="x64"   ;;
        aarch64|arm64) ta="arm64" ;;
        *) err "unsupported arch '$arch' for the vendored jattach — set \$JATTACH_BINARY to your own copy."
           return 1 ;;
    esac
    file="$dir/jattach-${plat}-${ta}"
    [ -f "$file" ] || { err "no vendored jattach at $file — restore vendor/jattach/ from git, or set \$JATTACH_BINARY."; return 1; }
    sums="$dir/SHA256SUMS"
    [ -f "$sums" ] || { err "missing $sums — refusing to stage an unverified binary."; return 1; }
    want="$(awk -v f="jattach-${plat}-${ta}" '$2==f {print $1}' "$sums")"
    [ -n "$want" ] || { err "no SHA256SUMS entry for jattach-${plat}-${ta} — refusing to stage unverified."; return 1; }
    if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$file" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$file" | awk '{print $1}')"
    else err "no sha256sum/shasum on this host to verify the binary — install coreutils, or set \$JATTACH_BINARY."; return 1; fi
    if [ "$got" != "$want" ]; then
        err "vendored jattach FAILED its checksum — refusing to stage it."
        err "  expected  $want  (SHA256SUMS)"
        err "  got       $got  ($file)"
        err "  → the file was modified or corrupted; restore vendor/jattach/ from git."
        return 1
    fi
    printf '%s\n' "$file"
}

# resolve_tui_binary <kit-root> — the Go TUI carries the interactive frontend AND
# the heap reader (histogram / retained-size / two-dump diff). EVERY entry point
# that needs it (this CLI's menu/wizard, observe/analyze.sh) resolves through here
# so they all get the SAME binary. Resolution:
#   1. <root>/tui/jdebug-tui        a local `make tui` build (dev loop; the
#                                   developer's own fresh output, not hash-gated)
#   2. <root>/vendor/tui/…-<os>-<arch>  the binary VENDORED into the repo,
#                                   verified against SHA256SUMS before use — a
#                                   tampered/corrupt binary must not run
# Neither present → explain, return 1. The whole CLI works without the TUI.
resolve_tui_binary() {
    local root="${1:-${JDEBUG_KIT:-}}"
    if [[ -x "$root/tui/jdebug-tui" ]]; then
        printf '%s\n' "$root/tui/jdebug-tui"; return 0
    fi
    local os arch f sums want got
    case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) os="$(uname -s | tr '[:upper:]' '[:lower:]')" ;; esac
    case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch="$(uname -m)" ;; esac
    f="$root/vendor/tui/jdebug-tui-$os-$arch"
    sums="$root/vendor/tui/SHA256SUMS"
    if [[ ! -x "$f" ]]; then
        err "the Go TUI (interactive menu + heap reader) is not available for $os/$arch."
        err "  build it:  make tui        (needs Go; ~5s — then jdebug uses tui/jdebug-tui)"
        err "  or commit once with the hooks installed (make hooks) — the pre-commit hook"
        err "  vendors verified binaries into vendor/tui/ for darwin+linux, arm64+amd64."
        err "  every CLI command works without the TUI: jdebug --help"
        return 1
    fi
    if [[ ! -f "$sums" ]]; then
        err "vendored TUI has no SHA256SUMS ($sums) — refusing to run an unverified binary."
        err "  → re-commit with the hooks installed (make hooks), or build fresh: make tui"
        return 1
    fi
    want="$(awk -v f="$(basename "$f")" '$2==f {print $1}' "$sums")"
    if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$f" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$f" | awk '{print $1}')"
    else got=""; fi
    if [[ -z "$want" || -z "$got" || "$want" != "$got" ]]; then
        err "vendored TUI FAILED its checksum — refusing to run it."
        err "  expected  ${want:-<no entry in SHA256SUMS>}"
        err "  got       ${got:-<could not hash>}  ($f)"
        err "  → restore vendor/tui/ from git, or build fresh: make tui"
        return 1
    fi
    # provenance (non-fatal): the binary is checksum-verified above; also flag when
    # the sources beside it drifted from what was vendored, so a clone never runs a
    # binary whose relationship to the visible source is silently unknown. Recipe is
    # locale-pinned to match scripts/vendor-tui.sh and the pre-push/CI gate.
    local bi="$root/vendor/tui/BUILDINFO" rec now gofiles
    if [[ -f "$bi" ]]; then
        rec="$(awk -F': ' '/^source_sha256/{print $2}' "$bi")"
        gofiles="$(ls "$root"/tui/*.go 2>/dev/null | LC_ALL=C sort)"
        # shellcheck disable=SC2086  # $gofiles is a newline list of .go paths (no spaces) — deliberate split
        now="$(cat "$root/tui/go.mod" "$root/tui/go.sum" $gofiles 2>/dev/null | { command -v sha256sum >/dev/null 2>&1 && sha256sum || shasum -a 256; } | awk '{print $1}')"
        [[ -n "$rec" && -n "$now" && "$rec" != "$now" ]] && \
            err "note: vendored TUI does not match tui/ sources (provenance drift; re-vendor: make vendor-tui)"
    fi
    printf '%s\n' "$f"
}

# resolve_core_binary <kit-root> — the v2 capture engine (Go). Same discipline as
# resolve_tui_binary: a local `make core` build wins; otherwise the vendored,
# checksum-verified binary for this os/arch. Prints the path.
# Return codes — callers MUST distinguish them (a checksum gate that fails
# open into the v1 bash tiers silently would defeat its own purpose):
#   0  path printed, binary verified
#   1  no binary for this platform (quiet — v1 bash tiers are a fair fallback)
#   2  binary PRESENT but FAILED verification (loud — do NOT run it, do NOT
#      silently fall back; the operator must decide)
resolve_core_binary() {
    local root="${1:-${JDEBUG_KIT:-}}"
    if [[ -x "$root/core/jdebug-core" ]]; then
        printf '%s\n' "$root/core/jdebug-core"; return 0
    fi
    local os arch f sums want got
    case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) os="$(uname -s | tr '[:upper:]' '[:lower:]')" ;; esac
    case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch="$(uname -m)" ;; esac
    f="$root/tools/core/jdebug-core-$os-$arch"
    sums="$root/tools/core/SHA256SUMS"
    [[ -x "$f" ]] || return 1
    if [[ ! -f "$sums" ]]; then
        err "vendored core has no SHA256SUMS ($sums) — refusing to run an unverified binary."
        err "  → restore tools/core/ from git, or build fresh: make core"
        return 2
    fi
    want="$(awk -v f="$(basename "$f")" '$2==f {print $1}' "$sums")"
    if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$f" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$f" | awk '{print $1}')"
    else got=""; fi
    if [[ -z "$want" || -z "$got" || "$want" != "$got" ]]; then
        err "vendored core FAILED its checksum — refusing to run it."
        err "  expected  ${want:-<no entry in SHA256SUMS>}"
        err "  got       ${got:-<could not hash>}  ($f)"
        err "  → restore tools/core/ from git, or build fresh: make core"
        return 2
    fi
    # provenance (non-fatal): same source-drift note as the TUI resolver.
    local bi="$root/tools/core/BUILDINFO" rec now
    if [[ -f "$bi" ]]; then
        rec="$(awk -F': ' '/^source_sha256/{print $2}' "$bi")"
        now="$({ cat "$root/core/go.mod" "$root/core/go.sum" 2>/dev/null; find "$root/core" -name '*.go' -not -name '*_test.go' | LC_ALL=C sort | xargs cat; } | { command -v sha256sum >/dev/null 2>&1 && sha256sum || shasum -a 256; } | awk '{print $1}')"
        [[ -n "$rec" && -n "$now" && "$rec" != "$now" ]] && \
            err "note: vendored core does not match core/ sources (provenance drift; re-vendor: make vendor-core)" >&2
    fi
    printf '%s\n' "$f"
}

# check_cluster — is the kube context actually answering? If not, translate the
# usual kubectl failure modes into plain language and a likely fix, instead of
# letting every later kubectl call spew TLS stack traces and memcache spam.
# (/version is readable by anyone, so this works with any RBAC.)
check_cluster() {
    # kubectl-missing is NOT cluster-unreachable: without kubectl there is no
    # command to reach ANY cluster, and calling it below would surface a bare
    # "kubectl: command not found" mislabelled as the cluster's own answer.
    if ! command -v kubectl >/dev/null 2>&1; then
        err "kubectl is not installed, or not on PATH — jdebug drives kubectl to reach the cluster."
        err "  why: nothing here can talk to Kubernetes until kubectl exists on PATH."
        err "  fix: install it (macOS: brew install kubectl · docs: https://kubernetes.io/docs/tasks/tools/)"
        err "       then re-run.  (jdebug doctor checks your whole setup.)"
        return 1
    fi
    local out ctx
    out="$(kubectl get --raw=/version --request-timeout=4s 2>&1 >/dev/null)" && return 0
    ctx="$(kubectl config current-context 2>/dev/null || true)"
    # Expired credentials are NOT "unreachable": the cluster answered and
    # rejected them. This is the single most common failure on managed clusters
    # (EKS/GKE/AKS/OpenShift) — an expired SSO/OIDC/exec-plugin token — and it
    # needs a different headline, because "switch context" won't fix it.
    case "$out" in
        *Unauthorized*|*"must be logged in"*|*"provide credentials"*|*"token has expired"*)
            err "the cluster is UP but REJECTED your credentials  (context: ${ctx:-<none set>})"
            err "  why: your login token has expired (typical on EKS/GKE/AKS/OpenShift:"
            err "       SSO / OIDC / cloud-CLI tokens time out)."
            err "  fix: re-authenticate for this cluster, then re-run. Common commands:"
            err "         EKS: aws sso login  (or aws eks update-kubeconfig --name <cluster>)"
            err "         GKE: gcloud auth login   ·   AKS: az login   ·   OpenShift: oc login"
            err "       switching contexts will NOT fix expired credentials."
            return 1 ;;
    esac
    err "can't reach the Kubernetes cluster  (context: ${ctx:-<none set>})"
    case "$out" in
        *x509*|*certificate*)
            err "  why: the cluster's TLS certificate isn't trusted."
            err "       LOCAL clusters (Rancher Desktop, k3s, minikube, kind): the cluster was"
            err "       recreated/restarted and your saved kubeconfig credentials went stale —"
            err "       fix: restart the local cluster app (it rewrites the kubeconfig)."
            err "       MANAGED clusters (EKS/GKE/AKS/OpenShift): usually a corporate proxy"
            err "       intercepting TLS, a rotated cluster CA, or a stale kubeconfig —"
            err "       fix: re-fetch the kubeconfig (e.g. aws eks update-kubeconfig / gcloud"
            err "       container clusters get-credentials), or ask about the proxy's CA."
            err "       Either way you can also switch to a working context:"
            err "       kubectl config use-context <name>  (jdebug menu: press t)" ;;
        *"connection refused"*|*"i/o timeout"*|*"no such host"*|*"Unable to connect"*|*"context deadline"*)
            err "  why: nothing answered at the cluster's address — it's off, asleep, or unreachable."
            err "  fix: start the cluster (Rancher/Docker Desktop, VPN for remote clusters), or"
            err "       switch to a context that is up (menu: t · shell: kubectl config use-context)" ;;
        *"current-context"*|*"no configuration"*|*"Missing or incomplete"*)
            err "  why: kubectl has no context selected, so it doesn't know which cluster to talk to."
            err "  fix: pick one:  kubectl config use-context <name>   (list: kubectl config get-contexts)"
            err "       or point KUBECONFIG at the right file." ;;
        *)
            err "  kubectl's own explanation (first lines):"
            printf '%s\n' "$out" | grep -v '^E[0-9]' | head -3 | sed 's/^/    /' >&2 ;;
    esac
    return 1
}

# explain_kubectl_error <first-stderr-line> [what] — turn a failed kubectl
# call into plain language + a next step. A failure must never read as
# "there was nothing" — the WHY is the diagnostic.
explain_kubectl_error() {
    local e="$1" what="${2:-that command}"
    case "$e" in
        *Unauthorized*|*"must be logged in"*)
            echo "  ✗ the cluster rejected your credentials while $what — they've expired."
            echo "      $e"
            echo "    → re-authenticate (EKS: aws sso login · GKE: gcloud auth login · AKS: az login"
            echo "      · OpenShift: oc login), then re-run. Switching contexts won't fix this." ;;
        *"not valid for pod"*|*"container name must be specified"*)
            echo "  ✗ the pod has no container named '$APP_CONTAINER' — kubernetes' exact words:"
            echo "      $e"
            echo "    → jdebug defaults to the container name 'app'. Point it at the real one:"
            echo "      --container <name>  (list them: kubectl -n $NAMESPACE get pod <pod> \\"
            echo "      -o jsonpath='{.spec.containers[*].name}')" ;;
        *'exec: "sh"'*|*'"sh": executable file not found'*)
            echo "  ✗ the container has NO SHELL (a distroless/minimal image): $e"
            echo "    → the pod exists and may be healthy — this is an image property, not a crash."
            echo "      in-pod capture tiers need sh; use the ephemeral JDK tier instead:"
            echo "      jdebug threads --via jdk   (runs in a debug container, no shell needed)" ;;
        *[Ff]orbidden*)
            echo "  ✗ your RBAC doesn't allow $what — kubernetes' exact words:"
            echo "      $e"
            echo "    → ask your cluster admin for the permission named above; the rest of jdebug still works" ;;
        *"Metrics API not available"*|*metrics.k8s.io*|*"metrics not available"*)
            echo "  ✗ metrics-server isn't installed (or isn't healthy) in this cluster,"
            echo "    so live CPU/memory numbers simply don't exist here."
            echo "    → requests/limits still come from the pod spec (shown above/below),"
            echo "      and an HPA with CPU/memory targets is BLIND without it" ;;
        *refused*|*"i/o timeout"*|*"no such host"*|*"context deadline"*)
            echo "  ✗ can't reach the cluster: $e"
            echo "    → wrong context? VPN down? 'jdebug doctor' walks through it" ;;
        *NotFound*|*"not found"*)
            echo "  ✗ it doesn't exist (anymore): $e"
            echo "    → a crash-looping pod may have been REPLACED under a new name — re-pick it (menu: g → p)" ;;
        *"is waiting to start"*)
            echo "  ✗ the container can't run commands right now: $e"
            echo "    → it's between crashes — 'jdebug logs --previous' has its last words" ;;
        "") : ;;
        *)
            echo "  ✗ $what failed: $e" ;;
    esac
}
