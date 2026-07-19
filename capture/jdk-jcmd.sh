#!/usr/bin/env bash
#
# jdk-jcmd.sh — run an arbitrary jcmd against the target JVM via an EPHEMERAL
# JDK container. This is the tier-3 path for jcmd (VM.native_memory, GC.heap_info,
# VM.flags, Thread.print, …) when you can't stage jattach into the pod — most
# importantly on DISTROLESS / scratch images, which ship no shell and no tar, so
# `kubectl cp` (and therefore the jattach tier) simply can't install anything.
#
# It injects a JDK image with `kubectl debug --target=<app>` (shared PID
# namespace), hand-shakes the HotSpot attach protocol across the container
# boundary — identical to jdk-threads.sh / jdk-heap.sh — and runs jcmd. Nothing
# is written into the app container; the debug container is torn down after.
#
# Requires: the EphemeralContainers feature (GA in k8s 1.25+) and RBAC for
# pods/ephemeralcontainers. Needs a pullable JDK image ($JDK_DEBUG_IMAGE).
#
# Usage:
#   ./jdk-jcmd.sh "<jcmd command>" [-n ns] [-l selector] [--container name] [pod]
#   ./jdk-jcmd.sh "VM.native_memory summary" -n payments payments-abc

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl

# the first non-flag arg is the jcmd command; the rest are common target flags
JCMD=""
FILTERED_ARGS=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        -n|--namespace|-l|--selector|--container) FILTERED_ARGS+=("$1" "$2"); shift 2 ;;
        -*) FILTERED_ARGS+=("$1"); shift ;;
        *) if [[ -z "$JCMD" ]]; then JCMD="$1"; else FILTERED_ARGS+=("$1"); fi; shift ;;
    esac
done
if [[ -z "$JCMD" ]]; then
    err 'jdk-jcmd needs a command string, e.g.  jdk-jcmd.sh "VM.native_memory summary"'
    exit 64
fi

parse_common_args ${FILTERED_ARGS[@]+"${FILTERED_ARGS[@]}"}
POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"

DEBUG_CONTAINER="jcmd-$(date +%s)"   # RFC 1123: lowercase alnum + '-'

# Runs inside the debug container: find the JVM PID (PID 1 is /pause under
# shareProcessNamespace), hand-shake the HotSpot attach protocol across the
# container boundary (the trigger file must land in the JVM's own /tmp via
# /proc/<pid>/root/tmp — needs root; the JVM's socket is then symlinked into our
# /tmp; the attach listener accepts a root peer on JDK 10+), then jcmd to stdout.
# JCMD_TOKEN is substituted below so the operator's command is quoted exactly once.
REMOTE_CMD='
JPID=""
for p in /proc/[0-9]*/comm; do
    [ "$(cat "$p" 2>/dev/null)" = "java" ] && { JPID="${p#/proc/}"; JPID="${JPID%/comm}"; break; }
done
if [ -z "$JPID" ]; then
    for m in /proc/[0-9]*/maps; do
        grep -q libjvm "$m" 2>/dev/null && { JPID="${m#/proc/}"; JPID="${JPID%/maps}"; break; }
    done
fi
[ -n "$JPID" ] || { echo "ERROR: no JVM visible in the shared PID namespace (no java comm, nothing maps libjvm)" >&2; exit 1; }
SOCK="/proc/$JPID/root/tmp/.java_pid$JPID"
if [ ! -S "$SOCK" ]; then
    touch "/proc/$JPID/root/tmp/.attach_pid$JPID" \
        || { echo "ERROR: cannot reach the JVM /tmp via /proc/$JPID/root (need root in the debug container)" >&2; exit 1; }
    kill -QUIT "$JPID"
    n=0; while [ $n -lt 50 ] && [ ! -S "$SOCK" ]; do sleep 0.2; n=$((n+1)); done
fi
[ -S "$SOCK" ] || { echo "ERROR: JVM never opened the attach socket ($SOCK)" >&2; exit 1; }
ln -sf "$SOCK" "/tmp/.java_pid$JPID"
exec jcmd "$JPID" JCMD_TOKEN
'
REMOTE_CMD="${REMOTE_CMD//JCMD_TOKEN/$JCMD}"

info "jcmd '$JCMD' via ephemeral JDK container (pod=$POD target=$APP_CONTAINER image=$JDK_DEBUG_IMAGE)"
show_cmd kubectl -n "$NAMESPACE" debug "$POD" --image="$JDK_DEBUG_IMAGE" \
    --target="$APP_CONTAINER" --container="$DEBUG_CONTAINER" --profile=general \
    -q -- sh -c "'<find java pid; attach; jcmd \$JPID $JCMD>'"
kubectl -n "$NAMESPACE" debug "$POD" \
    --image="$JDK_DEBUG_IMAGE" \
    --target="$APP_CONTAINER" \
    --container="$DEBUG_CONTAINER" \
    --profile=general -q \
    -- sh -c "$REMOTE_CMD" >/dev/null

# stream the debug container's output (works during and after it terminates)
OUT=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if OUT="$(kubectl -n "$NAMESPACE" logs "$POD" -c "$DEBUG_CONTAINER" -f 2>/dev/null)"; then
        break
    fi
    sleep 1
done
if [[ -z "$OUT" ]] || grep -q "^ERROR:" <<<"$OUT"; then
    err "jcmd via ephemeral container failed:"
    sed 's/^/    /' <<<"$OUT" >&2
    exit 1
fi
printf '%s\n' "$OUT"
