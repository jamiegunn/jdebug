#!/usr/bin/env bash
#
# actuator.sh — thread + heap dumps via Spring Boot Actuator.
#
# PREFERRED capture path (tier 1): JRE-only, nothing installed into the pod,
# works whenever the app can still serve HTTP. When it can't, fall back to
# capture/jattach.sh (tier 2) or capture/jdk-threads.sh / capture/jdk-heap.sh
# (tier 3, ephemeral JDK container).
#
# Usage:
#   ./actuator.sh threads [--json] [-n ns] [-l selector] [--container name] [pod]
#   ./actuator.sh heap --confirm [-n ns] [-l selector] [--container name] [pod]
#
# threads  text/plain by default (jstack-style — opens in VisualVM
#          unchanged); --json for Spring's structured format.
# heap     downloads an hprof from /actuator/heapdump. DESTRUCTIVE IN
#          PRODUCTION: the JVM freezes for the duration of the dump
#          (seconds on small heaps, minutes on multi-GB). Requires --confirm.
#
# The actuator base URL inside the pod defaults to
# http://localhost:8080/actuator — override with $ACTUATOR_BASE.
#
# Output (under the kit's dumps/ dir — override with $OUT_DIR):
#         dumps/pods/<pod>/<ts>/threads-actuator.{txt,json}
#         dumps/pods/<pod>/<ts>/heap-actuator.hprof

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl

: "${ACTUATOR_BASE:=http://localhost:8080/actuator}"

# In-pod HTTP goes through pod_fetch (lib/common.sh): curl, else busybox wget.

# explain_capture_fail <errfile> <url> <no-http-fallback-cmd> — a failed capture
# has two very different causes: the pod/exec itself failed (gone, renamed, RBAC,
# unreachable) OR the pod is fine but the actuator didn't serve. Kubernetes puts
# the first kind on stderr; route those to explain_kubectl_error (which says
# "re-pick the pod") instead of blaming the actuator.
explain_capture_fail() {
    local errfile="$1" url="$2" jhint="$3" eline
    eline="$(head -n1 "$errfile" 2>/dev/null)"
    case "$eline" in
        # wrong-container and no-shell first: both CONTAIN "not found"-ish text
        # that must not be mistaken for "pod gone" or blamed on the actuator.
        *"not valid for pod"*|*"container name must be specified"*|*'exec: "sh"'*|*'"sh": executable file not found'*|*Unauthorized*|*"must be logged in"*)
            err "the capture couldn't reach the pod (it didn't fail at the actuator):"
            explain_kubectl_error "$eline" "the in-pod capture" ;;
        *NotFound*|*"not found"*|*[Ff]orbidden*|*refused*|*"no such"*|*Unable*|*"context deadline"*|*"unable to upgrade"*)
            err "the capture couldn't reach the pod (it didn't fail at the actuator):"
            explain_kubectl_error "$eline" "the in-pod capture" ;;
        *)
            err "actuator fetch failed."
            explain_actuator_fail "$url" "$jhint" ;;
    esac
}

# explain_actuator_fail <url> <no-http-fallback-cmd> — a failed actuator fetch
# is ambiguous (secured? absent? app wedged?). Probe the HTTP status and give
# the ONE precise next action instead of a catch-all. Prints to stderr.
explain_actuator_fail() {
    local url="$1" jhint="$2" code
    code="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
              sh -c "$(pod_http_status "$url")" 2>/dev/null | tr -dc '0-9')"
    case "$code" in
        401|403)
            err "  secured (HTTP $code): the actuator needs credentials."
            err "    → CLI: export ACTUATOR_AUTH=bearer:ENV_VAR  (or basic:USER_VAR:PASS_VAR),"
            err "      naming the pod's OWN credential env vars — never a literal secret."
            err "    → menu: set the same value in the target editor (k); verify with T, then: env | grep -i actuator"
            err "    → or skip HTTP entirely: $jhint" ;;
        404)
            err "  not found (HTTP 404): nothing is served at this path."
            err "    → MOST COMMON CAUSE: stock Spring Boot only exposes /health over HTTP."
            err "      The app must opt in to the capture endpoints, e.g. in application properties:"
            err "        management.endpoints.web.exposure.include=health,threaddump,heapdump,metrics,loggers"
            err "    → also possible: actuator disabled, a different base path, or management.server.port."
            err "      Fix the URL via --actuator-base / \$ACTUATOR_BASE (menu: target editor g/a)."
            err "    → or skip HTTP entirely: $jhint" ;;
        ""|000)
            err "  no HTTP reply: the app isn't serving (wedged, still starting, or actuator off)."
            err "    → skip HTTP entirely: $jhint" ;;
        *)
            err "  the actuator returned HTTP $code."
            err "    → try a no-HTTP route: $jhint" ;;
    esac
}

ACTION=""
CONFIRMED=0
AS_JSON=0
FILTERED_ARGS=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        threads|heap) ACTION="$1"; shift ;;
        --confirm)    CONFIRMED=1; shift ;;
        --json)       AS_JSON=1; shift ;;
        -h|--help)    usage; exit 0 ;;
        --) shift; FILTERED_ARGS+=("$@"); break ;;
        *)  FILTERED_ARGS+=("$1"); shift ;;
    esac
done

# ${arr[@]+...} guard: bash 3.2 (stock macOS) treats "${arr[@]}" on an empty
# array as unbound under `set -u`; fixed only in bash 4.4.
parse_common_args ${FILTERED_ARGS[@]+"${FILTERED_ARGS[@]}"}

if [[ -z "$ACTION" ]]; then
    err "usage: actuator.sh {threads [--json] | heap --confirm} [-n ns] [-l selector] [pod]"
    exit 64
fi
if [[ "$ACTION" == "heap" && $CONFIRMED -ne 1 ]]; then
    err "heap dumps pause the JVM (destructive in production). Re-run with --confirm."
    exit 64
fi

# heap pauses the JVM — never let it hit a guessed replica
[[ "$ACTION" == "heap" ]] && export JDEBUG_DESTRUCTIVE=1
POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
TS="$(date -u +%Y%m%dT%H%M%SZ)"

case "$ACTION" in
    threads)
        OUT_DIR="${OUT_DIR:-$(session_dir "$POD" "$TS")}"
        ensure_dir "$OUT_DIR"
        if [[ $AS_JSON -eq 1 ]]; then
            LOCAL_PATH="$OUT_DIR/threads-actuator.json"
            ACCEPT="application/json"
        else
            LOCAL_PATH="$OUT_DIR/threads-actuator.txt"
            ACCEPT="text/plain"
        fi
        info "thread dump via actuator (pod=$POD)"
        show_cmd "kubectl -n $NAMESPACE exec $POD -c $APP_CONTAINER -- sh -c '<curl-or-wget> -H Accept:$ACCEPT $ACTUATOR_BASE/threaddump' > $LOCAL_PATH"
        CERR="$(mktemp)"
        if ! kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
                sh -c "$(pod_fetch "$ACTUATOR_BASE/threaddump" "$ACCEPT")" > "$LOCAL_PATH" 2>"$CERR"; then
            rm -f "$LOCAL_PATH"
            explain_capture_fail "$CERR" "$ACTUATOR_BASE/threaddump" "jdebug threads --via jattach -n $NAMESPACE $POD"
            rm -f "$CERR"
            exit 1
        fi
        rm -f "$CERR"
        if [[ $AS_JSON -eq 1 ]]; then MARKER='"threads"'; else MARKER="Full thread dump"; fi
        if ! grep -q "$MARKER" "$LOCAL_PATH" 2>/dev/null; then
            err "capture looks wrong (no '$MARKER' marker) — leaving it for inspection: $LOCAL_PATH"
            cls="$(classify_capture "$LOCAL_PATH")"
            [ -n "$cls" ] && err "  $cls — set auth (k), check the URL, or use: jdebug threads --via jattach -n $NAMESPACE $POD"
            exit 1
        fi
        info "wrote $LOCAL_PATH ($(wc -l <"$LOCAL_PATH" | tr -d ' ') lines)"
        info "analyze: open it in VisualVM (free, runs locally — visualvm.github.io) and look for deadlocks & blocked pools"
        ;;
    heap)
        OUT_DIR="${OUT_DIR:-$(session_dir "$POD" "$TS")}"
        ensure_dir "$OUT_DIR"
        LOCAL_PATH="$OUT_DIR/heap-actuator.hprof"
        info "heap dump via actuator (pod=$POD) — this PAUSES the JVM"
        show_cmd "kubectl -n $NAMESPACE exec $POD -c $APP_CONTAINER -- sh -c '<curl-or-wget> $ACTUATOR_BASE/heapdump' > $LOCAL_PATH"
        CERR="$(mktemp)"
        if ! kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- \
                sh -c "$(pod_fetch "$ACTUATOR_BASE/heapdump")" > "$LOCAL_PATH" 2>"$CERR"; then
            rm -f "$LOCAL_PATH"
            explain_capture_fail "$CERR" "$ACTUATOR_BASE/heapdump" "jdebug heap --via jattach --confirm -n $NAMESPACE $POD"
            rm -f "$CERR"
            exit 1
        fi
        rm -f "$CERR"
        # hprof files start with the magic "JAVA PROFILE 1.0.x". A 200 that isn't
        # one is almost always a secured endpoint's login/error page — classify
        # it so the user fixes the ROUTE, not chases a corrupt "heap" in MAT.
        if ! head -c 12 "$LOCAL_PATH" 2>/dev/null | grep -q "JAVA PROFILE"; then
            err "the actuator answered but did not return a heap dump — leaving the file for inspection: $LOCAL_PATH"
            cls="$(classify_capture "$LOCAL_PATH")"
            [ -n "$cls" ] && err "  $cls"
            err "  recover the capture: set auth in the target editor (k), fix the actuator URL, or skip HTTP:"
            err "    jdebug heap --via jattach --confirm -n $NAMESPACE $POD"
            exit 1
        fi
        info "wrote $LOCAL_PATH ($(du -h "$LOCAL_PATH" | cut -f1 | tr -d ' '))"
        info "analyze: Eclipse MAT (Leak Suspects) or VisualVM — both run on a JRE"
        ;;
esac
