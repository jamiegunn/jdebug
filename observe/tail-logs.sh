#!/usr/bin/env bash
#
# tail-logs.sh — stream logs from all replicas matching the selector, with
# pod-name prefix. Uses `stern` if available, otherwise `kubectl logs -f -l`.
#
# Usage:
#   ./tail-logs.sh [-n namespace] [-l selector] [--container name]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl

parse_common_args "$@"

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
