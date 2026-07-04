#!/usr/bin/env bash
#
# set-log-level.sh — change a Logback level at runtime on every replica
# via Spring Boot's /actuator/loggers endpoint. Iterates all matching pods
# because `loggers` config is per-JVM, not cluster-wide.
#
# Usage:
#   ./set-log-level.sh <logger-name> <LEVEL> [-n namespace] [-l selector]
# Example:
#   ./set-log-level.sh com.example.debugdemo DEBUG
#   ./set-log-level.sh ROOT INFO

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl

: "${ACTUATOR_BASE:=http://localhost:8080/actuator}"

parse_common_args "$@"

if [[ ${#REMAINING_ARGS[@]} -lt 2 ]]; then
    err "usage: set-log-level.sh <logger> <LEVEL> [-n ns] [-l selector]"
    exit 64
fi

LOGGER="${REMAINING_ARGS[0]}"
LEVEL="${REMAINING_ARGS[1]}"

case "$LEVEL" in
    TRACE|DEBUG|INFO|WARN|ERROR|OFF) ;;
    *) err "invalid LEVEL: $LEVEL (TRACE|DEBUG|INFO|WARN|ERROR|OFF)"; exit 64 ;;
esac

PODS="$(resolve_pods)"
if [[ -z "$PODS" ]]; then
    err "no pods matched selector=$SELECTOR namespace=$NAMESPACE"
    exit 2
fi

while IFS= read -r pod; do
    [[ -z "$pod" ]] && continue
    info "setting $LOGGER=$LEVEL on $pod"
    # POST via in-pod curl-or-wget so we don't need port-forward.
    kubectl -n "$NAMESPACE" exec "$pod" -c "$APP_CONTAINER" -- \
        sh -c "$(pod_post_json "$ACTUATOR_BASE/loggers/$LOGGER" "{\"configuredLevel\":\"$LEVEL\"}")" \
        || { err "POST failed on $pod"; continue; }
    EFFECTIVE="$(kubectl -n "$NAMESPACE" exec "$pod" -c "$APP_CONTAINER" -- \
        sh -c "$(pod_fetch "$ACTUATOR_BASE/loggers/$LOGGER")")"
    info "  -> $EFFECTIVE"
done <<< "$PODS"
