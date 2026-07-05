#!/usr/bin/env bash
#
# what-changed.sh — the "a deploy just happened, is that why it broke?" workflow.
# Even when jdebug can't diff everything, NAMING the question and pulling the
# usual suspects into one place — image, rollout timing, restart reason, events
# since the last restart, HPA-vs-Deployment replica intent — helps a junior ask
# the right question instead of staring at logs. Read-only.
#
# Usage:
#   ./what-changed.sh [-n ns] [-l selector] [pod]

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl
require_cmd python3
parse_common_args "$@"
check_cluster || exit 1

POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
DEP="$(owning_deployment "$POD" 2>/dev/null || true)"

ERRF="$(mktemp)"; trap 'rm -f "$ERRF"' EXIT
POD_JSON="$(kubectl -n "$NAMESPACE" get pod "$POD" -o json 2>"$ERRF" || echo '{}')"
DEP_JSON='{}'
[[ -n "$DEP" ]] && DEP_JSON="$(kubectl -n "$NAMESPACE" get deployment "$DEP" -o json 2>/dev/null || echo '{}')"
HPA_JSON="$(kubectl -n "$NAMESPACE" get hpa -o json 2>/dev/null || echo '{}')"

export POD_JSON DEP_JSON HPA_JSON POD DEP NAMESPACE CONTAINER="$APP_CONTAINER"
python3 <<'PY'
import json, os

def load(k):
    try: return json.loads(os.environ.get(k, "") or "{}")
    except Exception: return {}

pod = load("POD_JSON"); dep = load("DEP_JSON"); hpas = load("HPA_JSON").get("items", [])
POD, NS, CONT, DEP = (os.environ[k] for k in ("POD", "NAMESPACE", "CONTAINER", "DEP"))

meta, spec, st = pod.get("metadata", {}), pod.get("spec", {}), pod.get("status", {})
cont = next((c for c in spec.get("containers", []) if c.get("name") == CONT),
            (spec.get("containers") or [{}])[0])
cstat = next((c for c in st.get("containerStatuses", []) if c.get("name") == CONT),
             (st.get("containerStatuses") or [{}])[0])

print(f"== what changed? — {POD} ==")
print("   the usual suspects when a deploy or restart precedes trouble (read-only)")
print()

# image + imageID (the digest is what actually ran)
print("■ image")
print(f"    spec image : {cont.get('image','?')}")
imgid = cstat.get("imageID", "")
print(f"    running    : {imgid or '(imageID not reported yet)'}")
print("    · if 'running' pins a different digest than you expect, the tag was re-pushed")

# rollout / creation / restart timing
print("\n■ timing")
print(f"    pod created: {meta.get('creationTimestamp','?')}")
running = (cstat.get("state", {}) or {}).get("running", {})
if running.get("startedAt"):
    print(f"    started at : {running['startedAt']}")
dep_meta = dep.get("metadata", {})
if dep_meta:
    rev = dep_meta.get("annotations", {}).get("deployment.kubernetes.io/revision", "?")
    print(f"    Deployment {dep_meta.get('name','?')} revision: {rev}")
    for c in dep.get("status", {}).get("conditions", []) or []:
        if c.get("type") == "Progressing":
            print(f"    rollout    : {c.get('reason','?')} — {c.get('message','')[:80]}")

# restart reason (the strongest 'something changed' signal)
print("\n■ restarts")
rc = cstat.get("restartCount", 0)
last = (cstat.get("lastState", {}) or {}).get("terminated")
print(f"    restart count: {rc}")
if last:
    print(f"    last exit    : {last.get('reason','?')} (code {last.get('exitCode','?')}) at {last.get('finishedAt','?')}")
    print("    · a fresh restart right after a deploy points the finger at the new revision")
else:
    print("    no prior termination recorded (no crash since start)")

# HPA vs Deployment replica intent (a common post-deploy surprise)
print("\n■ scale intent")
desired = dep.get("spec", {}).get("replicas") if dep.get("metadata") else None
if desired is not None:
    print(f"    Deployment replicas: {desired}")
hpa = next((h for h in hpas), None)
if hpa:
    hs, hsp = hpa.get("status", {}), hpa.get("spec", {})
    print(f"    HPA: {hs.get('currentReplicas','?')} now (min {hsp.get('minReplicas',1)} / max {hsp.get('maxReplicas','?')})")
    if desired is not None:
        print("    · a Deployment replicas: value AND an HPA fight — each deploy resets the count")

print()
print("Next:")
print("  jdebug logs --previous   — the previous container's last words (crash reason)")
print("  jdebug timeline          — order these against events + your captures")
print("  jdebug topology          — is an OLD ReplicaSet still serving pods (rollout stuck)?")
PY
