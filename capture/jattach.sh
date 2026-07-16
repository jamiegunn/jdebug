#!/usr/bin/env bash
#
# jattach.sh — capture JVM thread/heap dumps using jattach
# (https://github.com/jattach/jattach), a single statically-linked binary
# (~80 KB) that speaks the JVM Hotspot attach protocol.
#
# Why this path:
#   - Smaller than `kubectl debug` with a JDK ephemeral container
#   - Doesn't need an ephemeral container at all (some clusters disable that)
#   - Gives access to jcmd-style operations (Thread.print -l, GC.heap_info,
#     JFR.start, VM.native_memory, ...) that actuator doesn't expose
#
# What this path is NOT:
#   - Not the preferred default. Prefer the actuator endpoints first
#     (see scripts that hit /actuator/threaddump and /actuator/heapdump).
#   - jattach is a binary you install INTO the pod. The runtime image
#     ships JRE-only, so this script handles the install:
#       1. --binary <path>              kubectl cp a local copy you provide
#       2. $JATTACH_BINARY env          same as --binary
#       3. vendor/jattach/ in this repo — the PINNED, checksum-verified
#          binaries (x64 + arm64), kubectl cp'd in. NOTHING is downloaded
#          at runtime; see vendor/jattach/PROVENANCE.md
#
# Usage:
#   ./jattach.sh threads [pod]
#   ./jattach.sh heap --confirm [pod]
#   ./jattach.sh jcmd "GC.heap_info" [pod]
#   ./jattach.sh jcmd "VM.native_memory summary" [pod]
#   ./jattach.sh install [pod]                    # just install, do nothing
#
# Common flags from lib/common.sh: -n <ns>, -l <selector>, --container <name>

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl

: "${JATTACH_VERSION:=v2.2}"
: "${JATTACH_REMOTE_PATH:=/tmp/jattach}"
: "${JATTACH_CACHE_DIR:=$JDEBUG_CACHE_DIR}"
# jattach is VENDORED in this repo (pinned $JATTACH_VERSION) and installed from
# here — NO runtime download. See vendor/jattach/PROVENANCE.md. Override the
# vendored copy with --binary / $JATTACH_BINARY.
: "${JATTACH_VENDOR_DIR:=$SCRIPTS_ROOT/vendor/jattach}"

ACTION=""
CONFIRMED=0
JCMD_ARG=""
LOCAL_BINARY="${JATTACH_BINARY:-}"
FILTERED_ARGS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        threads|heap|jcmd|install) ACTION="$1"; shift ;;
        --binary)                  LOCAL_BINARY="$2"; shift 2 ;;
        --confirm)                 CONFIRMED=1; shift ;;
        -h|--help)
            usage
            exit 0 ;;
        --) shift; FILTERED_ARGS+=("$@"); break ;;
        *)  FILTERED_ARGS+=("$1"); shift ;;
    esac
done

if [[ "$ACTION" == "jcmd" ]]; then
    JCMD_ARG="${FILTERED_ARGS[0]:-}"
    if [[ -z "$JCMD_ARG" ]]; then
        err "jcmd action requires a command string (e.g. 'GC.heap_info')"
        exit 64
    fi
    FILTERED_ARGS=("${FILTERED_ARGS[@]:1}")
fi

# ${arr[@]+...} guard: bash 3.2 (stock macOS) treats "${arr[@]}" on an empty
# array as unbound under `set -u`; fixed only in bash 4.4.
parse_common_args ${FILTERED_ARGS[@]+"${FILTERED_ARGS[@]}"}
# heap pauses the JVM — never let it hit a guessed replica (resolve_one_pod
# refuses on an ambiguous multi-pod match when this is set)
[[ "$ACTION" == "heap" ]] && export JDEBUG_DESTRUCTIVE=1
POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"

if [[ -z "$ACTION" ]]; then
    err "usage: jattach.sh {threads|heap|jcmd|install} [args] [pod]"
    exit 64
fi
if [[ "$ACTION" == "heap" && $CONFIRMED -ne 1 ]]; then
    err "heap dump pauses the JVM. Re-run with --confirm to proceed."
    exit 64
fi

# ---------------------------------------------------------------------------
# Install jattach into the pod if it's not already there.
# ---------------------------------------------------------------------------
install_jattach() {
    if kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
            test -x "$JATTACH_REMOTE_PATH" 2>/dev/null; then
        info "jattach already present at $JATTACH_REMOTE_PATH (in $POD)"
        record_artifact 0 "$JATTACH_REMOTE_PATH" "jattach (already in the pod)"
        return
    fi

    # 1. Explicit binary the caller handed us
    if [[ -n "$LOCAL_BINARY" ]]; then
        if [[ ! -f "$LOCAL_BINARY" ]]; then
            err "--binary path not found: $LOCAL_BINARY"
            exit 1
        fi
        info "installing jattach from local file: $LOCAL_BINARY"
        kubectl -n "$NAMESPACE" cp "$LOCAL_BINARY" "$POD:$JATTACH_REMOTE_PATH" -c "$APP_CONTAINER"
        kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- chmod +x "$JATTACH_REMOTE_PATH"
        record_artifact 1 "$JATTACH_REMOTE_PATH" "jattach"
        return
    fi

    # 2. Auto-detect the pod arch and install the jattach binary VENDORED in
    #    this repo (pinned $JATTACH_VERSION). No runtime download.
    local arch tarball_arch
    arch="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- uname -m)"
    case "$arch" in
        x86_64|amd64)  tarball_arch="x64"   ;;
        aarch64|arm64) tarball_arch="arm64" ;;
        *) err "unsupported pod arch: $arch (provide --binary instead)"; exit 1 ;;
    esac

    local cache_file="$JATTACH_VENDOR_DIR/jattach-linux-${tarball_arch}"
    if [[ ! -f "$cache_file" ]]; then
        err "no vendored jattach for arch '$arch' at: $cache_file"
        err "  expected a repo-local binary (pinned $JATTACH_VERSION, see vendor/jattach/)."
        err "  → provide one with --binary <path> or \$JATTACH_BINARY."
        exit 1
    fi
    info "using vendored jattach ($JATTACH_VERSION, linux-${tarball_arch}): $cache_file"

    # Integrity gate: verify the vendored binary against SHA256SUMS BEFORE it
    # ships into a production pod. A corrupted or tampered local file must
    # never run next to the JVM. --binary/$JATTACH_BINARY (an explicit operator
    # choice) bypasses this; the vendored default does not.
    local sums="$JATTACH_VENDOR_DIR/SHA256SUMS" want got
    if [[ ! -f "$sums" ]]; then
        err "missing $sums — refusing to install an unverified binary into the pod."
        err "  → restore it from git, or pass an explicit --binary <path>."
        exit 1
    fi
    want="$(awk -v f="jattach-linux-${tarball_arch}" '$2==f {print $1}' "$sums")"
    if [[ -z "$want" ]]; then
        err "no entry for jattach-linux-${tarball_arch} in $sums — refusing to install unverified."
        err "  → restore SHA256SUMS from git, or pass an explicit --binary <path>."
        exit 1
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        got="$(sha256sum "$cache_file" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then       # stock macOS
        got="$(shasum -a 256 "$cache_file" | awk '{print $1}')"
    else
        err "neither sha256sum nor shasum on this host — can't verify the vendored binary."
        err "  → install coreutils/perl, or pass an explicit --binary <path> to skip verification."
        exit 1
    fi
    if [[ "$got" != "$want" ]]; then
        err "vendored jattach FAILED its checksum — refusing to install it into the pod."
        err "  expected  $want  (SHA256SUMS)"
        err "  got       $got  ($cache_file)"
        err "  → the file was modified or corrupted; restore vendor/jattach/ from git,"
        err "    or pass an explicit --binary <path>."
        exit 1
    fi
    info "checksum verified (sha256 ${got:0:12}…)"

    info "kubectl cp $cache_file $POD:$JATTACH_REMOTE_PATH"
    kubectl -n "$NAMESPACE" cp "$cache_file" "$POD:$JATTACH_REMOTE_PATH" -c "$APP_CONTAINER"
    kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- chmod +x "$JATTACH_REMOTE_PATH"
    record_artifact 1 "$JATTACH_REMOTE_PATH" "jattach"

    # Sanity check: jattach with no args prints "Usage: jattach <pid>..." to
    # stderr and exits non-zero. We capture both streams and only fail if
    # we see nothing at all (i.e. exec/libc broke).
    local probe
    probe="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- "$JATTACH_REMOTE_PATH" 2>&1 || true)"
    if [[ -z "$probe" ]]; then
        err "jattach binary produced no output inside the pod (libc/arch mismatch?)."
        err "Provide a binary that matches the pod with --binary."
        exit 1
    fi
    info "jattach installed and working ($(echo "$probe" | head -1 | head -c 60)...)"
}

# Find the actual JVM PID inside the pod. With shareProcessNamespace=true,
# pod PID 1 is the pause sandbox container — the JVM is somewhere else.
# First pass: comm == "java". Second pass: any process that maps libjvm —
# catches custom launchers (jwebserver, jshell, jlink images) whose comm
# is not "java".
find_jvm_pid() {
    local pid
    pid="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- sh -c '
        for p in $(ls /proc 2>/dev/null | grep -E "^[0-9]+$"); do
            if [ "$(cat /proc/$p/comm 2>/dev/null)" = "java" ]; then
                echo "$p"; exit 0
            fi
        done
        for p in $(ls /proc 2>/dev/null | grep -E "^[0-9]+$"); do
            if grep -q libjvm "/proc/$p/maps" 2>/dev/null; then
                echo "$p"; exit 0
            fi
        done
        exit 1
    ' 2>/dev/null || true)"
    if [[ -z "$pid" ]]; then
        err "no JVM found inside pod $POD container $APP_CONTAINER (no 'java' process, nothing maps libjvm)"
        exit 1
    fi
    echo "$pid"
}

install_jattach

if [[ "$ACTION" == "install" ]]; then
    info "done."
    exit 0
fi

JVM_PID="$(find_jvm_pid)"
info "JVM PID inside pod: $JVM_PID"

# ---------------------------------------------------------------------------
# Run the requested action.
#
# jattach has several actions; we want the one it calls `jcmd`. This is
# NOT the standalone JDK `jcmd` tool — it's a jattach action that proxies
# a jcmd-syntax command string into the JVM through the attach socket and
# writes the response back to jattach's own stdout. So
# `kubectl exec ... > local-file` captures cleanly, no `kubectl logs`
# scraping needed. Contrast with `jattach <pid> threaddump`, which makes
# the JVM print to its own stdout (the container log stream).
# ---------------------------------------------------------------------------
TS="$(date -u +%Y%m%dT%H%M%SZ)"
case "$ACTION" in
    threads)
        OUT_DIR="${OUT_DIR:-$(session_dir "$POD" "$TS")}"
        ensure_dir "$OUT_DIR"
        LOCAL_PATH="$OUT_DIR/threads-jattach.txt"
        info "running jattach jcmd 'Thread.print -l' on PID $JVM_PID"
        kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
            "$JATTACH_REMOTE_PATH" "$JVM_PID" jcmd "Thread.print -l" > "$LOCAL_PATH"
        if ! grep -q "Full thread dump" "$LOCAL_PATH" 2>/dev/null; then
            err "capture looks wrong (no 'Full thread dump' marker) — leaving it for inspection: $LOCAL_PATH"
            cls="$(classify_capture "$LOCAL_PATH")"
            [ -n "$cls" ] && err "  $cls"
            err "  the attach may have hit the wrong process, or the JVM refused the command."
            exit 1
        fi
        info "wrote $LOCAL_PATH ($(wc -l <"$LOCAL_PATH") lines)"
        info "analyze: open it in VisualVM (free, runs locally — visualvm.github.io) and look for deadlocks & blocked pools"
        ;;
    heap)
        OUT_DIR="${OUT_DIR:-$(session_dir "$POD" "$TS")}"
        ensure_dir "$OUT_DIR"
        REMOTE_PATH="/tmp/heap-jattach-$TS.hprof"
        LOCAL_PATH="$OUT_DIR/heap-jattach.hprof"
        info "running jattach dumpheap (PAUSES JVM)"
        kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
            "$JATTACH_REMOTE_PATH" "$JVM_PID" dumpheap "$REMOTE_PATH"
        REMOTE_SIZE="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
            sh -c "wc -c < '$REMOTE_PATH'" 2>/dev/null | tr -dc '0-9')"
        info "copying $REMOTE_PATH -> $LOCAL_PATH"
        kubectl -n "$NAMESPACE" cp "$POD:$REMOTE_PATH" "$LOCAL_PATH" -c "$APP_CONTAINER"
        # Validate BEFORE deleting the in-pod copy: kubectl cp rides on tar and
        # can truncate silently — a partial hprof handed to MAT reads as a
        # smaller heap and drives a WRONG diagnosis. Magic catches non-dumps;
        # the size compare catches truncation (magic alone can't).
        if ! head -c 12 "$LOCAL_PATH" 2>/dev/null | grep -q "JAVA PROFILE"; then
            err "the copied file is not a valid hprof (bad magic) — leaving it for inspection: $LOCAL_PATH"
            cls="$(classify_capture "$LOCAL_PATH")"
            [ -n "$cls" ] && err "  $cls"
            err "  the in-pod copy is still at $POD:$REMOTE_PATH — retry:"
            err "    kubectl -n $NAMESPACE cp $POD:$REMOTE_PATH $LOCAL_PATH -c $APP_CONTAINER"
            exit 1
        fi
        LOCAL_SIZE="$(wc -c < "$LOCAL_PATH" | tr -d ' ')"
        if [[ -n "$REMOTE_SIZE" && "$REMOTE_SIZE" != "$LOCAL_SIZE" ]]; then
            err "size mismatch after kubectl cp — the dump was TRUNCATED in transit:"
            err "  in pod: $REMOTE_SIZE bytes · copied: $LOCAL_SIZE bytes"
            err "  the in-pod copy is still at $POD:$REMOTE_PATH — retry the copy:"
            err "    kubectl -n $NAMESPACE cp $POD:$REMOTE_PATH $LOCAL_PATH -c $APP_CONTAINER"
            err "  (then clean up: kubectl -n $NAMESPACE exec $POD -c $APP_CONTAINER -- rm -f $REMOTE_PATH)"
            exit 1
        fi
        kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- rm -f "$REMOTE_PATH" || true
        info "wrote $LOCAL_PATH ($(du -h "$LOCAL_PATH" | cut -f1), verified: hprof magic + size match)"
        info "analyze: Eclipse MAT → File → Open Heap Dump → 'Leak Suspects' (or VisualVM)"
        ;;
    jcmd)
        info "running jcmd '$JCMD_ARG' on PID $JVM_PID"
        kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
            "$JATTACH_REMOTE_PATH" "$JVM_PID" jcmd "$JCMD_ARG"
        ;;
esac
