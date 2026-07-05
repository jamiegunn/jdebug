#!/usr/bin/env bash
#
# topology.sh — the workload tree the pod belongs to: Deployment → its
# ReplicaSets (current + old revisions) → pods, plus the HPA and the Services
# that route to it. Explains what's CURRENT vs stale and why there may be more
# than one ReplicaSet — the shape that tells you a rollout is mid-flight or
# stuck. Read-only.
#
# Usage:
#   ./topology.sh [-n ns] [-l selector] [pod]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl
require_cmd python3
parse_common_args "$@"
check_cluster || exit 1

ERRF="$(mktemp)"; trap 'rm -f "$ERRF"' EXIT

# Which deployment? The pod's owner when we have one; else the selector's.
DEP=""
POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}" 2>/dev/null || true)"
[[ -n "$POD" ]] && DEP="$(owning_deployment "$POD" 2>/dev/null || true)"

fetch() { # fetch <kind> [extra args] → JSON on stdout, "" + explain on failure
    local kind="$1"; shift
    if ! kubectl -n "$NAMESPACE" get "$kind" "$@" -o json 2>"$ERRF"; then
        explain_kubectl_error "$(head -n1 "$ERRF")" "listing $kind" >&2
        echo '{"items":[]}'
    fi
}

if [[ -n "$DEP" ]]; then
    DEPLOY_JSON="$(kubectl -n "$NAMESPACE" get deployment "$DEP" -o json 2>"$ERRF" || echo '{}')"
else
    if [[ -n "$SELECTOR" ]]; then DEPLOY_JSON="$(fetch deployment -l "$SELECTOR")"; else DEPLOY_JSON="$(fetch deployment)"; fi
fi
RS_JSON="$(fetch rs)"
POD_JSON="$(fetch pods)"
HPA_JSON="$(fetch hpa)"
SVC_JSON="$(fetch svc)"

export DEPLOY_JSON RS_JSON POD_JSON HPA_JSON SVC_JSON DEP NAMESPACE
python3 <<'PY'
import json, os

def load(k):
    try: return json.loads(os.environ.get(k, "") or "{}")
    except Exception: return {}

dep_raw = load("DEPLOY_JSON")
deps = dep_raw.get("items", [dep_raw]) if dep_raw.get("kind") != "Deployment" else [dep_raw]
deps = [d for d in deps if d and d.get("kind") == "Deployment" or d.get("metadata")]
rsets = load("RS_JSON").get("items", [])
pods  = load("POD_JSON").get("items", [])
hpas  = load("HPA_JSON").get("items", [])
svcs  = load("SVC_JSON").get("items", [])
notes = []

def owned_by(obj, kind, name):
    for o in obj.get("metadata", {}).get("ownerReferences", []) or []:
        if o.get("kind") == kind and o.get("name") == name:
            return True
    return False

def match(selector, labels):
    ml = (selector or {}).get("matchLabels", {}) or {}
    return ml and all(labels.get(k) == v for k, v in ml.items())

print("== workload topology ==")
if not deps:
    print("  no Deployment found for this target — the pods may be standalone, or owned")
    print("  by a StatefulSet/DaemonSet/Job (not shown here). 'jdebug status' lists the pods.")

for d in deps:
    dm = d.get("metadata", {})
    ds = d.get("status", {})
    dspec = d.get("spec", {})
    name = dm.get("name", "?")
    rev = dm.get("annotations", {}).get("deployment.kubernetes.io/revision", "?")
    desired = dspec.get("replicas", "?")
    print(f"\n■ Deployment {name}  (revision {rev})")
    print(f"    replicas: {ds.get('readyReplicas',0)}/{desired} ready · "
          f"{ds.get('updatedReplicas',0)} updated · {ds.get('availableReplicas',0)} available")
    strat = dspec.get("strategy", {}).get("type", "RollingUpdate")
    print(f"    strategy: {strat}")
    if desired not in ("?", None) and ds.get("updatedReplicas", 0) not in (desired, "?"):
        notes.append(f"Deployment {name} is mid-rollout (updated {ds.get('updatedReplicas',0)} of {desired}) "
                     "— or stuck: if this doesn't finish, a new pod is failing its readiness probe")

    # ReplicaSets owned by this deployment
    mine = [rs for rs in rsets if owned_by(rs, "Deployment", name)]
    mine.sort(key=lambda rs: int(rs.get("metadata", {}).get("annotations", {})
                                 .get("deployment.kubernetes.io/revision", "0") or 0), reverse=True)
    print("    ReplicaSets (a new one per rollout; old ones kept for rollback):")
    live_old = 0
    for rs in mine:
        rm = rs.get("metadata", {})
        rrev = rm.get("annotations", {}).get("deployment.kubernetes.io/revision", "?")
        rdesired = rs.get("spec", {}).get("replicas", 0)
        rready = rs.get("status", {}).get("readyReplicas", 0)
        tag = "  ← current" if str(rrev) == str(rev) else ""
        if str(rrev) != str(rev) and rdesired and rdesired > 0:
            live_old += 1
            tag = "  ⚠ OLD revision still running pods"
        print(f"      rev {rrev}: {rready}/{rdesired} ready  {rm.get('name','?')}{tag}")
    if live_old:
        notes.append(f"{live_old} OLD ReplicaSet(s) under {name} still run pods — a rollout is in progress "
                     "or stuck partway. Expected briefly during a deploy; persistent = the new pods can't go ready")

    # HPA targeting this deployment
    for h in hpas:
        tref = h.get("spec", {}).get("scaleTargetRef", {})
        if tref.get("kind") == "Deployment" and tref.get("name") == name:
            hs, hsp = h.get("status", {}), h.get("spec", {})
            print(f"    HPA {h['metadata']['name']}: {hs.get('currentReplicas','?')} now "
                  f"(min {hsp.get('minReplicas',1)} / max {hsp.get('maxReplicas','?')})")
            import re
            la = dm.get("annotations", {}).get("kubectl.kubernetes.io/last-applied-configuration", "")
            if re.search(r'"replicas"\s*:', la):
                notes.append(f"{name}'s manifest pins 'replicas:' while an HPA manages it — they FIGHT "
                             "(every apply resets the count). Remove 'replicas:' from the manifest")

    # Services routing to these pods
    hits = []
    dl = dspec.get("template", {}).get("metadata", {}).get("labels", {}) or {}
    for s in svcs:
        sel = s.get("spec", {}).get("selector") or {}
        if sel and all(dl.get(k) == v for k, v in sel.items()):
            hits.append(s["metadata"]["name"])
    if hits:
        print(f"    Services routing here: {', '.join(hits)}  (a readiness-failing pod is pulled from these)")
    else:
        print("    Services routing here: none found — nothing exposes these pods via a Service")

print("\nBottom line:")
if notes:
    for n in notes[:4]:
        print(f"  ⚠ {n[:112]}")
    print("Next:")
    print("  mid/stuck rollout → jdebug status + jdebug logs --previous on a not-ready new pod;")
    print("  the ⚠ items name their own fix")
else:
    print("  a clean single-revision deployment — one current ReplicaSet, old ones scaled to zero.")
    print("Next: nothing to do at the workload layer; the app's behaviour is inside the pods (w)")
PY
