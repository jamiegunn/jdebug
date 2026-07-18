#!/usr/bin/env bash
#
# why.sh — the kubernetes-layer deep-dive: requests/limits, probes, exit
# codes, container memory beyond the JVM, and autoscaling — every finding
# explained for someone who has never read a pod spec. Read-only.
#
# Usage:
#   ./why.sh [-n namespace] [-l selector] [pod]

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

echo "== pod deep-dive: $POD =="
echo

# --- everything below degrades loudly, never silently -------------------------
if ! POD_JSON="$(kubectl -n "$NAMESPACE" get pod "$POD" -o json 2>"$ERRF")"; then
    explain_kubectl_error "$(head -n1 "$ERRF")" "reading pod $POD"
    exit 1
fi

TOP_OUT=""; TOP_ERR=""
TOP_OUT="$(kubectl -n "$NAMESPACE" top pod "$POD" --no-headers 2>"$ERRF")" || TOP_ERR="$(head -n1 "$ERRF")"

HPA_JSON=""; HPA_ERR=""
HPA_JSON="$(kubectl -n "$NAMESPACE" get hpa -o json 2>"$ERRF")" || HPA_ERR="$(head -n1 "$ERRF")"

# the Deployment that owns this pod (pod → ReplicaSet → strip the hash) —
# needed to spot the classic "manifest pins replicas while an HPA scales" fight
DEPLOY_JSON=""; DEPLOY_ERR=""
RS="$(printf '%s' "$POD_JSON" | python3 -c 'import json,sys
for o in json.load(sys.stdin).get("metadata",{}).get("ownerReferences",[]) or []:
    if o.get("kind")=="ReplicaSet": print(o["name"])' 2>/dev/null || true)"
if [[ -n "$RS" ]]; then
    DEPLOY="${RS%-*}"
    DEPLOY_JSON="$(kubectl -n "$NAMESPACE" get deployment "$DEPLOY" -o json 2>"$ERRF")" || DEPLOY_ERR="$(head -n1 "$ERRF")"
fi

# cgroup memory breakdown from inside the pod: catches the non-JVM leaks
# (tmpfs volumes! file cache!) that no JVM tool will ever show
MEMSTAT=""; MEMSTAT_ERR=""
MEMSTAT="$(kubectl -n "$NAMESPACE" exec "$POD" -c "$APP_CONTAINER" -- sh -c \
    'cat /sys/fs/cgroup/memory.stat 2>/dev/null || cat /sys/fs/cgroup/memory/memory.stat 2>/dev/null' 2>"$ERRF")" \
    || MEMSTAT_ERR="$(head -n1 "$ERRF")"

export POD_JSON TOP_OUT TOP_ERR HPA_JSON HPA_ERR DEPLOY_JSON DEPLOY_ERR MEMSTAT MEMSTAT_ERR APP_CONTAINER POD
python3 <<'PY'
import json, os, re

pod = json.loads(os.environ["POD_JSON"])
container = os.environ.get("APP_CONTAINER", "")
podname = os.environ.get("POD", "")
warns = []

def sect(title, sub=""):
    print(f"■ {title}" + (f"  — {sub}" if sub else ""))

def line(s): print(f"  {s}")
def warn(s):
    warns.append(s)
    print(f"  ⚠ {s}")
def good(s): print(f"  ✓ {s}")

# --- resources ---------------------------------------------------------------
sect("resources", "requests = the scheduler's promise · limits = the hard ceiling")
spec = pod.get("spec", {})
qos = pod.get("status", {}).get("qosClass", "?")
no_mem_limit = False
for c in spec.get("containers", []):
    res = c.get("resources", {}) or {}
    req, lim = res.get("requests", {}) or {}, res.get("limits", {}) or {}
    line(f"{c['name']}: requests cpu {req.get('cpu','—')} / mem {req.get('memory','—')}"
         f" · limits cpu {lim.get('cpu','—')} / mem {lim.get('memory','—')}")
    if c["name"] != container and container:
        continue
    if not lim.get("memory"):
        no_mem_limit = True
        warn("no MEMORY limit → this container can eat the node until the kernel's OOM killer "
             "picks a victim (often a neighbour) — set one")
    if not req.get("memory"):
        warn("no memory REQUEST → the scheduler packs it onto nodes blind; first candidate "
             "for eviction under pressure")
    if req.get("memory") and lim.get("memory") and req["memory"] != lim["memory"]:
        line("  (memory request < limit → 'Burstable': fine, but evictable before "
             "'Guaranteed' pods when the node runs hot)")
    if not lim.get("cpu"):
        line("  (no cpu limit → it may burst freely; that's usually fine — cpu is throttled, "
             "never killed)")
print(f"  QoS class: {qos}" + ("  (Guaranteed = last to be evicted)" if qos == "Guaranteed" else ""))
print()

# --- live usage ---------------------------------------------------------------
sect("live usage")
top_out, top_err = os.environ.get("TOP_OUT", "").strip(), os.environ.get("TOP_ERR", "")
if top_out:
    f = top_out.split()
    if len(f) >= 3:
        line(f"cpu {f[1]} · memory {f[2]}  (from metrics-server; compare with the limits above)")
elif top_err:
    if "Metrics API" in top_err or "metrics" in top_err.lower():
        warn("metrics-server isn't installed/healthy — live CPU/memory numbers don't exist in "
             "this cluster. Limits above still come from the spec and are still enforced; an "
             "HPA with cpu/memory targets is BLIND without it")
    elif "orbidden" in top_err:
        warn(f"your RBAC can't read pod metrics: {top_err}")
    else:
        warn(f"couldn't read live usage: {top_err}")
print()

# --- container memory beyond the JVM -------------------------------------------
memstat, mem_err = os.environ.get("MEMSTAT", ""), os.environ.get("MEMSTAT_ERR", "")
sect("what's actually in the container's memory", "the cgroup's own accounting")
if memstat:
    kv = {}
    for ln in memstat.splitlines():
        p = ln.split()
        if len(p) == 2 and p[1].isdigit():
            kv[p[0]] = int(p[1])
    anon = kv.get("anon", kv.get("rss", 0))
    filec = kv.get("file", kv.get("cache", 0))
    shmem = kv.get("shmem", 0)
    mib = lambda b: f"{b/1048576:.0f}Mi"
    line(f"anon (real app memory) {mib(anon)} · file cache {mib(filec)} · shmem/tmpfs {mib(shmem)}")
    if shmem > 64 * 1048576:
        warn("shmem/tmpfs is significant — files written to an emptyDir/tmpfs volume COUNT "
             "AGAINST the memory limit. A 'memory leak' that is actually a growing tmpfs file "
             "is a classic — check volume mounts")
    if filec > anon:
        line("  (file cache larger than app memory is normal — the kernel reclaims it before "
         "OOM-killing; don't chase it as a leak)")
else:
    if "waiting to start" in mem_err:
        warn("container is between crashes — cgroup stats unreadable right now")
    elif "orbidden" in mem_err:
        warn(f"your RBAC can't exec into the pod to read cgroup stats: {mem_err}")
    elif mem_err:
        warn(f"couldn't read the cgroup breakdown (no shell in the image?): {mem_err} — "
             "T in the menu attaches a debug container that CAN look around")
    else:
        line("(no cgroup data returned — minimal image without /sys access?)")
print()

# --- probes --------------------------------------------------------------------
sect("probes", "liveness failures RESTART the container · readiness failures pull it out of the Service")
for c in spec.get("containers", []):
    if container and c["name"] != container:
        continue
    def probeline(kind, p):
        if not p:
            if kind == "liveness":
                line(f"{kind}: none — k8s never restarts it on hang; a deadlocked app just sits there")
            else:
                warn(f"{kind}: none — traffic arrives the MOMENT the process starts, ready or not; "
                     "startup errors become user-facing 502s")
            return
        how = "?"
        if p.get("httpGet"): how = f"http :{p['httpGet'].get('port','?')}{p['httpGet'].get('path','')}"
        elif p.get("tcpSocket"): how = f"tcp :{p['tcpSocket'].get('port','?')}"
        elif p.get("exec"): how = "exec " + " ".join(p["exec"].get("command", [])[:3])
        line(f"{kind}: {how} · every {p.get('periodSeconds',10)}s · fails after {p.get('failureThreshold',3)}×")
    probeline("liveness", c.get("livenessProbe"))
    probeline("readiness", c.get("readinessProbe"))
print()

# --- restarts & exit codes -------------------------------------------------------
sect("restarts & exit codes")
EXIT_MEANING = {
    0: "clean exit — for a server that should run forever, 'clean' is still wrong: something told it to stop",
    1: "the app itself errored on startup/run — its logs (--previous) name the exception",
    2: "shell/entrypoint misuse — check the container command/args",
    126: "entrypoint found but not executable — image build problem",
    127: "entrypoint not found — image build or command typo",
    134: "SIGABRT — the runtime aborted itself (JVM: OutOfMemoryError with -XX:+CrashOnOutOfMemoryError, native crash)",
    137: "SIGKILL — 128+9. If 'reason: OOMKilled', the KERNEL killed it for exceeding the memory limit; "
         "otherwise something force-killed it (or the liveness probe gave up and k8s did)",
    139: "SIGSEGV — a native crash (JNI, corrupted native lib)",
    143: "SIGTERM — 128+15, a polite shutdown request: a deploy, eviction, or scale-down asked it to stop",
}
oom_seen = False
for cs in pod.get("status", {}).get("containerStatuses", []) or []:
    if container and cs.get("name") != container:
        continue
    rc = cs.get("restartCount", 0)
    line(f"{cs.get('name')}: {rc} restart(s)")
    term = (cs.get("lastState") or {}).get("terminated")
    if term:
        code, reason = term.get("exitCode"), term.get("reason", "")
        if reason == "OOMKilled":
            oom_seen = True
        meaning = EXIT_MEANING.get(code, "uncommon code — 128+N usually means killed by signal N")
        w = warn if rc > 3 or reason == "OOMKilled" else line
        w(f"last exit {code} ({reason or 'no reason recorded'}) → {meaning}")
    waiting = (cs.get("state") or {}).get("waiting")
    if waiting and waiting.get("reason"):
        warn(f"currently {waiting['reason']} — kubernetes is backing off between restart attempts; "
             "'jdebug logs --previous' has the crash itself")
# an OOM-killed JVM dies before any exec-based capture can reach it — the ONLY
# way to get that heap is the JVM writing it itself at the moment of death.
# If the flag isn't set, every future OOM loses its evidence too: say so NOW.
if oom_seen:
    env_blob = " ".join(
        (e.get("value") or "")
        for c in spec.get("containers", [])
        if not container or c.get("name") == container
        for e in (c.get("env") or [])
    )
    if "HeapDumpOnOutOfMemoryError" not in env_blob:
        warn("this container has been OOM-killed and -XX:+HeapDumpOnOutOfMemoryError is not in its env — "
             "the NEXT OOM will leave no heap dump behind either. Add it (plus "
             "-XX:HeapDumpPath=<mounted volume>) via JAVA_TOOL_OPTIONS so the crash captures itself; "
             "then 'jdebug fetch-heap' retrieves it. "
             "(Flags baked into the image aren't visible here — verify live: jdebug jcmd VM.flags)")
print()

# --- autoscaling ------------------------------------------------------------------
sect("autoscaling")
hpa_json, hpa_err = os.environ.get("HPA_JSON", ""), os.environ.get("HPA_ERR", "")
deploy_json = os.environ.get("DEPLOY_JSON", "")
shown = False
if hpa_json:
    try:
        for h in json.loads(hpa_json).get("items", []):
            shown = True
            s, sp = h.get("status", {}), h.get("spec", {})
            name = h["metadata"]["name"]
            line(f"{name}: {s.get('currentReplicas','?')} replicas now "
                 f"(min {sp.get('minReplicas',1)} / max {sp.get('maxReplicas','?')}), "
                 f"desired {s.get('desiredReplicas','?')}")
            for m_ in sp.get("metrics", []) or []:
                r = m_.get("resource", {})
                if r:
                    line(f"  scales on {r.get('name')} — target "
                         f"{r.get('target',{}).get('averageUtilization','?')}% of the REQUEST "
                         f"(that's why the request value matters so much)")
            for cond in s.get("conditions", []) or []:
                if cond.get("type") == "ScalingActive" and cond.get("status") == "False":
                    warn(f"ScalingActive=False ({cond.get('reason','?')}) — the HPA can't read its "
                         "metric, so it does NOTHING: no scale up under load, no scale down at "
                         "night. Usually = metrics-server missing")
            if deploy_json:
                try:
                    d = json.loads(deploy_json)
                    la = d.get("metadata", {}).get("annotations", {}).get(
                        "kubectl.kubernetes.io/last-applied-configuration", "")
                    if re.search(r'"replicas"\s*:', la):
                        warn("the Deployment manifest PINS 'replicas:' while this HPA manages the "
                             "same Deployment → every apply/deploy resets the count and the HPA "
                             "fights it back. Remove 'replicas:' from the manifest and let the "
                             "HPA own it")
                except Exception:
                    pass
    except Exception:
        pass
if not shown:
    if hpa_err:
        if "orbidden" in hpa_err:
            warn(f"your RBAC can't list HPAs: {hpa_err}")
        else:
            warn(f"couldn't read HPAs: {hpa_err}")
    else:
        line("no HPA in this namespace — replica count is whatever the Deployment says; "
             "nothing scales automatically")
print()

# --- verdict ------------------------------------------------------------------------
print("Bottom line:")
if warns:
    for w in warns[:4]:
        print(f"  ⚠ {w.splitlines()[0][:110]}")
else:
    print("  nothing structurally wrong at the kubernetes layer — if the app still misbehaves,")
    print("  the problem lives inside the process: wizard (w) picks the right JVM capture")
print("Next:")
if any("OOM" in w or "memory limit" in w for w in warns):
    print("  memory story → jdebug memory (JVM vs container) · wizard flow 1")
elif any("BLIND" in w or "metrics-server" in w for w in warns):
    print("  install metrics-server (or ask the cluster admin) — top, HPA and live usage all need it")
elif warns:
    print("  fix the ⚠ items top-down — each names its own next step")
else:
    print("  jdebug health · jdebug threads — move the investigation inside the app")
PY
