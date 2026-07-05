#!/usr/bin/env bash
#
# tail-logs.sh — stream logs from all replicas matching the selector, with
# pod-name prefix. Uses `stern` if available, otherwise `kubectl logs -f -l`.
# With --previous: the PREVIOUS container's last lines from one pod — the
# last words before a crash, which is where a CrashLoopBackOff explains
# itself. No follow, no stern.
#
# Usage:
#   ./tail-logs.sh [-n namespace] [-l selector] [--container name]
#   ./tail-logs.sh --previous [pod]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl

parse_common_args "$@"

# --previous: one pod's dead-container logs, not a live stream
PREVIOUS=0
PODARG=""
for a in ${REMAINING_ARGS[@]+"${REMAINING_ARGS[@]}"}; do
    case "$a" in
        --previous) PREVIOUS=1 ;;
        *)          PODARG="$a" ;;
    esac
done
if [[ "$PREVIOUS" == 1 ]]; then
    POD="$(resolve_one_pod "$PODARG")"
    show_cmd kubectl -n "$NAMESPACE" logs "$POD" -c "$APP_CONTAINER" --previous --tail=100
    if ! kubectl -n "$NAMESPACE" logs "$POD" -c "$APP_CONTAINER" --previous --tail=100; then
        err "no previous container for $POD — it hasn't restarted in place (or the pod was rescheduled; check: jdebug status)"
        exit 3
    fi
    echo
    echo "how to read this: these are the container's last lines before it died."
    echo "An exception or 'OutOfMemoryError' here IS the crash reason; exit 137 = killed on memory."
    exit 0
fi

# kubectl/stern can't stream a whole namespace via an empty selector — require one.
if [[ -z "$SELECTOR" ]]; then
    err "logs needs a selector: pass -l <selector> (or set JDEBUG_SELECTOR)"
    exit 64
fi

if command -v stern >/dev/null 2>&1; then
    info "using stern"
    exec stern -n "$NAMESPACE" --selector "$SELECTOR" --container "$APP_CONTAINER" --tail 50
fi

info "using kubectl (install 'stern' for prettier output)"
exec kubectl -n "$NAMESPACE" logs -f \
    --selector "$SELECTOR" \
    --container "$APP_CONTAINER" \
    --max-log-requests 10 \
    --prefix \
    --tail 50
