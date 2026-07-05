#!/usr/bin/env bash
#
# security.sh — the pod's security posture in plain language: who it runs
# as (verified live when possible), what privileges it holds, what can reach
# it. Read-only. Every check explains why it matters; every failure explains
# itself instead of vanishing.
#
# Usage:
#   ./security.sh [-n namespace] [-l selector] [pod]

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl
require_cmd python3
parse_common_args "$@"
check_cluster || exit 1

POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
ERRF="$(mktemp)"; trap 'rm -f "$ERRF"' EXIT

echo "== security posture: $POD =="
echo

if ! POD_JSON="$(kubectl -n "$NAMESPACE" get pod "$POD" -o json 2>"$ERRF")"; then
    explain_kubectl_error "$(head -n1 "$ERRF")" "reading pod $POD"
    exit 1
fi

# ground truth beats spec: what uid is the process ACTUALLY running as?
LIVE_UID=""; LIVE_UID_ERR=""
LIVE_UID="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- sh -c 'id -u' 2>"$ERRF" | tr -d '[:space:]')" \
    || LIVE_UID_ERR="$(head -n1 "$ERRF")"

NETPOL_JSON=""; NETPOL_ERR=""
NETPOL_JSON="$(kubectl -n "$NAMESPACE" get networkpolicy -o json 2>"$ERRF")" || NETPOL_ERR="$(head -n1 "$ERRF")"

export POD_JSON LIVE_UID LIVE_UID_ERR NETPOL_JSON NETPOL_ERR APP_CONTAINER
python3 <<'PY'
import json, os

pod = json.loads(os.environ["POD_JSON"])
container = os.environ.get("APP_CONTAINER", "")
spec = pod.get("spec", {})
psc = spec.get("securityContext", {}) or {}
warns, goods = [], []

def sect(t): print(f"■ {t}")
def line(s): print(f"  {s}")
def warn(s):
    warns.append(s)
    print(f"  ⚠ {s}")
def good(s):
    goods.append(s)
    print(f"  ✓ {s}")

def csc_of(name):
    for c in spec.get("containers", []):
        if c["name"] == name or not name:
            return c.get("securityContext", {}) or {}
    return {}

csc = csc_of(container)
eff = lambda k: csc.get(k, psc.get(k))

# --- identity -----------------------------------------------------------------
sect("identity — who is the process?")
live_uid, live_err = os.environ.get("LIVE_UID", ""), os.environ.get("LIVE_UID_ERR", "")
run_as = eff("runAsUser")
non_root = eff("runAsNonRoot")
if live_uid.isdigit():
    if live_uid == "0":
        warn("VERIFIED LIVE: the process runs as uid 0 (root). Any code-exec bug in the app is "
             "root inside the container — one kernel bug away from the node")
    else:
        good(f"verified live: runs as uid {live_uid} (non-root)")
else:
    if "orbidden" in live_err:
        line(f"couldn't verify the live uid (RBAC forbids exec): {live_err}")
    elif live_err:
        line(f"couldn't verify the live uid (no shell in image?): {live_err}")
    # fall back to what the spec promises
    if run_as == 0:
        warn("spec sets runAsUser: 0 — root, explicitly")
    elif run_as:
        good(f"spec sets runAsUser: {run_as} (non-root)")
    elif non_root:
        good("runAsNonRoot: true — kubelet refuses to start it as root")
    else:
        warn("nothing in the spec prevents root, and the image default decides — most base "
             "images default to root. Set runAsNonRoot: true")

# --- privilege ------------------------------------------------------------------
sect("privilege — what could it do if compromised?")
if eff("privileged"):
    warn("privileged: true — the container IS the host for most purposes. Almost never needed "
         "for an app")
else:
    good("not privileged")
ape = eff("allowPrivilegeEscalation")
if ape is False:
    good("allowPrivilegeEscalation: false — setuid binaries can't raise privileges")
else:
    warn("allowPrivilegeEscalation not disabled — a setuid binary in the image can still climb "
         "to root. Set it to false")
caps = (csc.get("capabilities") or {})
added = caps.get("add") or []
dropped = caps.get("drop") or []
if added:
    warn(f"extra capabilities added: {', '.join(added)} — each one is kernel surface "
         "(NET_ADMIN ≈ rewrite the pod's networking, SYS_ADMIN ≈ nearly root)")
if any(d.upper() == "ALL" for d in dropped):
    good("capabilities: drop ALL — the gold standard")
elif not added:
    line("capabilities: image defaults (docker's ~14) — 'drop: [ALL]' is the hardened baseline")
if eff("readOnlyRootFilesystem"):
    good("readOnlyRootFilesystem: true — malware can't persist into the image filesystem")
else:
    warn("root filesystem is writable — an attacker can drop tools/persistence into the "
         "container. readOnlyRootFilesystem: true (+ an emptyDir for the paths that need "
         "writes) closes it")
sp = eff("seccompProfile")
if sp and sp.get("type") in ("RuntimeDefault", "Localhost"):
    good(f"seccomp: {sp.get('type')} — syscall filter on")
else:
    line("seccomp: unconfined (no profile set) — RuntimeDefault is free hardening")

# --- host access -------------------------------------------------------------------
sect("host access")
host_bits = [k for k in ("hostNetwork", "hostPID", "hostIPC") if spec.get(k)]
if host_bits:
    warn(f"{', '.join(host_bits)}: true — the container shares the NODE's "
         f"{'network' if 'hostNetwork' in host_bits else 'process/ipc space'}; isolation is "
         "mostly gone")
else:
    good("no hostNetwork / hostPID / hostIPC")

# --- service account ----------------------------------------------------------------
sect("service account — what can the pod do to the CLUSTER?")
sa = spec.get("serviceAccountName", "default")
automount = spec.get("automountServiceAccountToken")
if automount is False:
    good(f"serviceaccount '{sa}', token NOT mounted — code in the pod can't call the k8s API")
else:
    w = warn if sa == "default" else line
    w(f"serviceaccount '{sa}' with its API token auto-mounted at "
      "/var/run/secrets/… — any code-exec in the pod can call the kubernetes API with "
      "whatever RBAC that account has. If the app doesn't need the API: "
      "automountServiceAccountToken: false")

# --- network policy -------------------------------------------------------------------
sect("network reachability")
np_json, np_err = os.environ.get("NETPOL_JSON", ""), os.environ.get("NETPOL_ERR", "")
labels = pod.get("metadata", {}).get("labels", {}) or {}
def selects(sel):
    ml = (sel or {}).get("matchLabels", {}) or {}
    return all(labels.get(k) == v for k, v in ml.items())
if np_err:
    if "orbidden" in np_err:
        warn(f"your RBAC can't list NetworkPolicies: {np_err} — posture UNKNOWN, not 'fine'")
    else:
        warn(f"couldn't read NetworkPolicies: {np_err}")
else:
    try:
        pols = json.loads(np_json).get("items", []) if np_json.strip() else []
        hits = [p["metadata"]["name"] for p in pols if selects(p.get("spec", {}).get("podSelector"))]
        if hits:
            good(f"NetworkPolicy in effect: {', '.join(hits)} — traffic to/from this pod is "
                 "restricted (kubectl describe networkpolicy shows the exact rules)")
        elif pols:
            warn(f"{len(pols)} NetworkPolicy object(s) exist in the namespace but NONE selects "
                 "this pod — for it, everything is still wide open")
        else:
            warn("no NetworkPolicy in this namespace — ANY pod in the cluster can talk to this "
                 "one, and it can talk to anything (including the cloud metadata endpoint)")
    except Exception:
        warn("couldn't parse the NetworkPolicy list")
print()

# --- verdict ----------------------------------------------------------------------------
print(f"Bottom line: {len(goods)} ✓ hardened · {len(warns)} ⚠ findings")
if warns:
    print("  worst first:")
    for w in warns[:3]:
        print(f"  ⚠ {w.splitlines()[0][:110]}")
    print("Next:")
    print("  each ⚠ above names its fix — they're one-line securityContext/manifest changes,")
    print("  cheap to add and reviewable by your platform team")
else:
    print("  this pod is unusually well locked down — nice")
PY
