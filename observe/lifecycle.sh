#!/usr/bin/env bash
#
# lifecycle.sh — the state-CHANGING actions, gated hard. Unlike every other
# jdebug command (which only observes), these restart or delete things, so
# each one explains exactly what will happen, what the risk is, and refuses
# to run without --confirm. Failures are explained, never silent.
#
# Usage:
#   ./lifecycle.sh restart [-n ns] [-l selector] [pod]   --confirm
#   ./lifecycle.sh kill    [-n ns] [-l selector] [pod]   --confirm

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl
ACTION="${1:-}"; shift || true
parse_common_args "$@"

CONFIRM=0
for a in ${REMAINING_ARGS[@]+"${REMAINING_ARGS[@]}"}; do
    [[ "$a" == "--confirm" ]] && CONFIRM=1
done
# drop --confirm so it isn't mistaken for a pod name
CLEAN=(); for a in ${REMAINING_ARGS[@]+"${REMAINING_ARGS[@]}"}; do [[ "$a" == "--confirm" ]] || CLEAN+=("$a"); done
REMAINING_ARGS=("${CLEAN[@]+"${CLEAN[@]}"}")

check_cluster || exit 1
ERRF="$(mktemp)"; trap 'rm -f "$ERRF"' EXIT

case "$ACTION" in
    restart)
        # Destructive: with several matching pods and no explicit name, REFUSE
        # to guess (same contract as heap). Guessing which deployment to
        # re-roll mid-incident is how the wrong workload gets cycled.
        JDEBUG_DESTRUCTIVE=1 JDEBUG_DESTRUCTIVE_WHY="RE-ROLLS the owning deployment"
        export JDEBUG_DESTRUCTIVE JDEBUG_DESTRUCTIVE_WHY
        POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
        unset JDEBUG_DESTRUCTIVE JDEBUG_DESTRUCTIVE_WHY
        require_cmd python3
        # is the pod even readable? separate an RBAC/not-found failure (explain
        # it) from a pod that's readable but simply not Deployment-owned.
        if ! kubectl -n "$NAMESPACE" get pod "$POD" -o name >/dev/null 2>"$ERRF"; then
            explain_kubectl_error "$(head -n1 "$ERRF")" "reading pod $POD"
            exit 1
        fi
        DEP="$(owning_deployment "$POD" || true)"
        if [[ -z "$DEP" ]]; then
            err "can't re-roll: $POD isn't owned by a Deployment (standalone pod, or a"
            err "  StatefulSet/DaemonSet/Job — those restart differently)."
            err "  → to cycle just this pod, use:  jdebug kill $POD --confirm"
            exit 3
        fi
        echo "About to RE-ROLL deployment '$DEP' (a rolling restart)."
        echo
        echo "What happens:"
        echo "  • kubernetes starts fresh pods and retires the old ones a few at a time"
        echo "    (honouring the deployment's maxSurge/maxUnavailable), so with >1 replica"
        echo "    and a working readiness probe there is normally NO downtime."
        echo "  • every pod is replaced — in-flight requests on a retiring pod are cut off,"
        echo "    and any in-memory state (caches, sessions not in Redis) is lost."
        echo "  • it does NOT change your image or config — it just cycles the pods. Common"
        echo "    reasons: pick up a rotated Secret/ConfigMap, clear a wedged process, or"
        echo "    recover from a bad in-memory state without a redeploy."
        echo
        echo "Risk: LOW-to-MEDIUM. Safe with healthy replicas + probes; disruptive if you"
        echo "  have 1 replica (that IS a brief outage) or readiness probes that lie."
        if [[ "$CONFIRM" -ne 1 ]]; then
            err "not confirmed — re-run with --confirm to proceed."
            exit 64
        fi
        show_cmd kubectl -n "$NAMESPACE" rollout restart "deployment/$DEP"
        if ! kubectl -n "$NAMESPACE" rollout restart "deployment/$DEP" 2>"$ERRF"; then
            explain_kubectl_error "$(head -n1 "$ERRF")" "restarting deployment/$DEP"
            exit 1
        fi
        echo
        echo "watching the rollout (Ctrl-C to stop watching — the restart continues):"
        show_cmd kubectl -n "$NAMESPACE" rollout status "deployment/$DEP" --timeout=120s
        kubectl -n "$NAMESPACE" rollout status "deployment/$DEP" --timeout=120s 2>"$ERRF" || {
            explain_kubectl_error "$(head -n1 "$ERRF")" "watching the rollout"
            echo "  (the restart may still be in progress — check: jdebug status)"
        }
        echo
        echo "Bottom line: rolling restart of '$DEP' requested."
        echo "Next: jdebug status — confirm the new pods are Running and restarts aren't climbing."
        ;;

    kill)
        # Destructive: with several matching pods and no explicit name, REFUSE
        # to guess (same contract as heap). Deleting a guessed replica can take
        # out the healthy pod — or destroy the sick one's evidence.
        JDEBUG_DESTRUCTIVE=1 JDEBUG_DESTRUCTIVE_WHY="DELETES a pod"
        export JDEBUG_DESTRUCTIVE JDEBUG_DESTRUCTIVE_WHY
        POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
        unset JDEBUG_DESTRUCTIVE JDEBUG_DESTRUCTIVE_WHY
        # is it managed? then a replacement comes back automatically
        MANAGED=""
        if command -v python3 >/dev/null 2>&1; then
            MANAGED="$(kubectl -n "$NAMESPACE" get pod "$POD" -o json 2>/dev/null | python3 -c 'import json,sys
try: refs=json.load(sys.stdin).get("metadata",{}).get("ownerReferences",[]) or []
except Exception: refs=[]
print(refs[0]["kind"] if refs else "")' 2>/dev/null || true)"
        fi
        echo "About to DELETE pod '$POD'."
        echo
        echo "What happens:"
        echo "  • kubernetes sends the app SIGTERM, waits the grace period (default 30s) for"
        echo "    a clean shutdown, then SIGKILL. In-flight requests to THIS pod are dropped;"
        echo "    a readiness-gated Service stops routing to it first."
        if [[ -n "$MANAGED" ]]; then
            echo "  • this pod is managed by a $MANAGED, so a REPLACEMENT starts automatically."
            echo "    This is the standard way to cycle one sick pod (e.g. a wedged JVM) without"
            echo "    touching its siblings — the deployment stays at its replica count."
        else
            echo "  • ⚠ this pod is NOT managed by a controller — nothing will recreate it."
            echo "    Deleting it means the app loses this instance for good. Are you sure?"
        fi
        echo "  • any in-memory state on this pod (caches, non-persisted sessions) is lost."
        echo "  • the pod's heap/thread dumps you already captured are safe — they're on YOUR"
        echo "    machine under dumps/, not on the pod."
        echo
        echo "Risk: MEDIUM. One replica among healthy siblings = fine. The only replica, or an"
        echo "  unmanaged pod = a real outage."
        if [[ "$CONFIRM" -ne 1 ]]; then
            err "not confirmed — re-run with --confirm to proceed."
            exit 64
        fi
        show_cmd kubectl -n "$NAMESPACE" delete pod "$POD"
        if ! kubectl -n "$NAMESPACE" delete pod "$POD" 2>"$ERRF"; then
            explain_kubectl_error "$(head -n1 "$ERRF")" "deleting pod $POD"
            exit 1
        fi
        echo
        echo "Bottom line: pod '$POD' deleted."
        if [[ -n "$MANAGED" ]]; then
            echo "Next: jdebug status — a replacement pod should appear (new name); re-pick it (g → p)."
        else
            echo "Next: jdebug status — confirm the rest of the app is healthy."
        fi
        ;;

    *)
        err "lifecycle.sh: unknown action '$ACTION' (expected: restart | kill)"
        exit 64
        ;;
esac
