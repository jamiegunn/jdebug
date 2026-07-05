#!/usr/bin/env bash
#
# context.sh — runtime context / app wiring: what this app IS, what exposes it,
# what configuration it runs with, and what dependencies it talks to — gathered
# from the pod spec, Services/Endpoints, and env/config references so a junior
# doesn't have to hop between five kubectl commands. Read-only.
#
# SECRET VALUES ARE NEVER PRINTED. Only names, keys, and references are shown;
# any value whose key looks sensitive (PASS/TOKEN/SECRET/KEY/…) is redacted, and
# values sourced from a Secret are shown as "<from Secret NAME/key>".
#
# Usage:
#   ./context.sh [-n ns] [-l selector] [pod]

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

POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
DEP="$(owning_deployment "$POD" 2>/dev/null || true)"

fetch() { # fetch <kind> [args] → JSON on stdout, "" + explain on failure
    local kind="$1"; shift
    if ! kubectl -n "$NAMESPACE" get "$kind" "$@" -o json 2>"$ERRF"; then
        explain_kubectl_error "$(head -n1 "$ERRF")" "listing $kind" >&2
        echo '{"items":[]}'
    fi
}

POD_JSON="$(kubectl -n "$NAMESPACE" get pod "$POD" -o json 2>"$ERRF" || echo '{}')"
SVC_JSON="$(fetch svc)"
EP_JSON="$(fetch endpoints)"
DEP_JSON="{}"
[[ -n "$DEP" ]] && DEP_JSON="$(kubectl -n "$NAMESPACE" get deployment "$DEP" -o json 2>/dev/null || echo '{}')"

# ConfigMaps the pod references (envFrom / volumes / valueFrom) — fetched so we
# can spot dependency config (e.g. a mounted redis.conf) without printing secrets.
CM_NAMES="$(POD_JSON="$POD_JSON" python3 -c '
import json, os
p = json.loads(os.environ.get("POD_JSON") or "{}")
names = set()
for c in p.get("spec", {}).get("containers", []):
    for ef in c.get("envFrom", []) or []:
        if ef.get("configMapRef"): names.add(ef["configMapRef"]["name"])
    for e in c.get("env", []) or []:
        ref = (e.get("valueFrom") or {}).get("configMapKeyRef")
        if ref: names.add(ref["name"])
for v in p.get("spec", {}).get("volumes", []) or []:
    if v.get("configMap"): names.add(v["configMap"]["name"])
print("\n".join(sorted(n for n in names if n)))' 2>/dev/null || true)"

CM_JSON='{}'
if [[ -n "$CM_NAMES" ]]; then
    # bounded: fetch each referenced ConfigMap (skip any we can't read) and wrap
    # them into an items[] list for the analyzer
    _cms=""
    while IFS= read -r cm; do
        [[ -z "$cm" ]] && continue
        one="$(kubectl -n "$NAMESPACE" get configmap "$cm" -o json 2>/dev/null || true)"
        [[ -n "$one" ]] && _cms+="$one,"
    done <<< "$CM_NAMES"
    CM_JSON="$(printf '{"items":[%s]}' "${_cms%,}")"
fi

export POD_JSON SVC_JSON EP_JSON DEP_JSON CM_JSON POD DEP NAMESPACE
export CONTAINER="$APP_CONTAINER"
python3 <<'PY'
import json, os, re

def load(k):
    try: return json.loads(os.environ.get(k, "") or "{}")
    except Exception: return {}

pod  = load("POD_JSON")
svcs = load("SVC_JSON").get("items", [])
eps  = load("EP_JSON").get("items", [])
dep  = load("DEP_JSON")
cms  = load("CM_JSON").get("items", [])
POD  = os.environ.get("POD", "")
NS   = os.environ.get("NAMESPACE", "")
CONT = os.environ.get("CONTAINER", "")

spec = pod.get("spec", {})
conts = spec.get("containers", []) or []
c = next((x for x in conts if x.get("name") == CONT), conts[0] if conts else {})
labels = pod.get("metadata", {}).get("labels", {}) or {}

SECRETY = re.compile(r'(PASS|PWD|SECRET|TOKEN|KEY|CRED|AUTH|PRIVATE)', re.I)
def redact_val(name, val):
    if SECRETY.search(name or ""):
        return "<redacted>"
    v = str(val)
    return v if len(v) <= 80 else v[:77] + "…"

def cmd(s): print(f"    · gathered with: {s}")

def head(t): print(f"\n■ {t}")

print(f"== runtime context — {POD} ==")
print(f"   what this app is, what exposes it, its config, and its dependencies (read-only; secrets redacted)")

# 1) OWNER & ROLLOUT ----------------------------------------------------------
head("owner & rollout")
cmd(f"kubectl -n {NS} get pod {POD} -o json ; get deployment -o json")
if dep.get("metadata"):
    dm, ds, dsp = dep["metadata"], dep.get("status", {}), dep.get("spec", {})
    rev = dm.get("annotations", {}).get("deployment.kubernetes.io/revision", "?")
    print(f"    Deployment {dm.get('name','?')} (revision {rev}) · "
          f"{ds.get('readyReplicas',0)}/{dsp.get('replicas','?')} ready")
else:
    print("    no owning Deployment (standalone pod, or a StatefulSet/DaemonSet/Job — see jdebug topology)")
for cc in conts:
    img = cc.get("image", "?")
    print(f"    container {cc.get('name','?')}  image {img}")
    if cc.get("command"): print(f"      command: {' '.join(cc['command'])}")
    if cc.get("args"):    print(f"      args:    {' '.join(str(a) for a in cc['args'])}")

# 2) SERVICES & PORTS ---------------------------------------------------------
head("services & ports")
cmd(f"kubectl -n {NS} get svc -o json ; get endpoints -o json")
def selects(sel):
    return sel and all(labels.get(k) == v for k, v in sel.items())
routed = [s for s in svcs if selects((s.get("spec", {}) or {}).get("selector") or {})]
if not routed:
    print("    no Service selects this pod — nothing exposes it via a Service (port-forward/headless only)")
for s in routed:
    sm, ss = s.get("metadata", {}), s.get("spec", {})
    print(f"    Service {sm.get('name','?')}  type {ss.get('type','ClusterIP')}  clusterIP {ss.get('clusterIP','?')}")
    for p in ss.get("ports", []) or []:
        nm = f" ({p['name']})" if p.get("name") else ""
        print(f"      port {p.get('port','?')}{nm} → targetPort {p.get('targetPort','?')}  {p.get('protocol','TCP')}")
    # is THIS pod in the Service endpoints (i.e. actually receiving traffic)?
    inep = notready = False
    for ep in eps:
        if ep.get("metadata", {}).get("name") != sm.get("name"): continue
        for sub in ep.get("subsets", []) or []:
            for a in sub.get("addresses", []) or []:
                if (a.get("targetRef") or {}).get("name") == POD: inep = True
            for a in sub.get("notReadyAddresses", []) or []:
                if (a.get("targetRef") or {}).get("name") == POD: notready = True
    if inep:      print("      endpoints: this pod IS in rotation (Ready, receiving traffic)")
    elif notready:print("      endpoints: this pod is NOT ready — pulled from rotation (probe failing?)")
    else:         print("      endpoints: this pod is not listed — check the Service selector vs the pod labels")

# 3) PROBES -------------------------------------------------------------------
head("probes")
cmd(f"kubectl -n {NS} get pod {POD} -o json  (.spec.containers[].*Probe)")
def probe_line(kind, pr):
    if not pr:
        print(f"    {kind}: none set"); return
    where = ""
    if pr.get("httpGet"):   g = pr["httpGet"]; where = f"HTTP {g.get('path','/')} :{g.get('port','?')}"
    elif pr.get("tcpSocket"): where = f"TCP :{pr['tcpSocket'].get('port','?')}"
    elif pr.get("exec"):    where = "exec " + " ".join(pr["exec"].get("command", []))
    print(f"    {kind}: {where}  delay {pr.get('initialDelaySeconds',0)}s · "
          f"period {pr.get('periodSeconds',10)}s · timeout {pr.get('timeoutSeconds',1)}s · "
          f"failures {pr.get('failureThreshold',3)}")
probe_line("readiness", c.get("readinessProbe"))
probe_line("liveness",  c.get("livenessProbe"))
probe_line("startup",   c.get("startupProbe"))

# 4) ENVIRONMENT --------------------------------------------------------------
head("environment")
cmd(f"kubectl -n {NS} get pod {POD} -o json  (.spec.containers[].env / envFrom)")
JVM = ("JAVA_TOOL_OPTIONS", "JAVA_OPTS", "JDK_JAVA_OPTIONS", "JAVA_OPTIONS", "_JAVA_OPTIONS")
jvm_seen, prof, tz, proxies = [], None, None, []
for e in c.get("env", []) or []:
    n = e.get("name", "")
    if n in JVM: jvm_seen.append((n, e.get("value", "")))
    if n in ("SPRING_PROFILES_ACTIVE", "SPRING_PROFILES_DEFAULT"): prof = e.get("value")
    if n == "TZ": tz = e.get("value")
    if "PROXY" in n.upper(): proxies.append(n)
if jvm_seen:
    for n, v in jvm_seen: print(f"    JVM  {n} = {v or '(empty)'}")
else:
    print("    JVM  no JAVA_TOOL_OPTIONS / JAVA_OPTS set (flags may be baked into the image — jdebug jcmd 'VM.flags')")
if prof: print(f"    Spring profiles: {prof}")
if tz:   print(f"    timezone (TZ): {tz}")
if proxies: print(f"    proxy vars set: {', '.join(proxies)}")
efrom = c.get("envFrom", []) or []
if efrom:
    for ef in efrom:
        if ef.get("configMapRef"): print(f"    envFrom ConfigMap {ef['configMapRef']['name']}  (all its keys become env)")
        if ef.get("secretRef"):    print(f"    envFrom Secret {ef['secretRef']['name']}  (values redacted — from a Secret)")

# 5) SECRET & CONFIG REFERENCES ----------------------------------------------
head("secret & config references")
cmd(f"kubectl -n {NS} get pod {POD} -o json  (env[].valueFrom / volumes)")
found = False
for e in c.get("env", []) or []:
    vf = e.get("valueFrom") or {}
    if vf.get("secretKeyRef"):
        r = vf["secretKeyRef"]; found = True
        print(f"    {e.get('name','?')}  ← Secret {r.get('name','?')}/{r.get('key','?')}  (value not shown)")
    elif vf.get("configMapKeyRef"):
        r = vf["configMapKeyRef"]; found = True
        print(f"    {e.get('name','?')}  ← ConfigMap {r.get('name','?')}/{r.get('key','?')}")
for v in spec.get("volumes", []) or []:
    if v.get("secret"):    found = True; print(f"    volume {v['name']}  ← Secret {v['secret'].get('secretName','?')} (mounted)")
    if v.get("configMap"): found = True; print(f"    volume {v['name']}  ← ConfigMap {v['configMap'].get('name','?')} (mounted)")
if not found:
    print("    no Secret/ConfigMap references on this container")

# 6) VOLUMES & STORAGE --------------------------------------------------------
head("volumes & storage")
cmd(f"kubectl -n {NS} get pod {POD} -o json  (.spec.volumes / .containers[].volumeMounts)")
mounts = {m.get("name"): m for m in (c.get("volumeMounts", []) or [])}
vols = spec.get("volumes", []) or []
if not vols:
    print("    no volumes mounted")
for v in vols:
    n = v.get("name", "?")
    mp = mounts.get(n, {})
    ro = " read-only" if mp.get("readOnly") else ""
    at = f" at {mp.get('mountPath')}" if mp.get("mountPath") else " (not mounted in this container)"
    kind = "unknown"
    warn = ""
    if v.get("persistentVolumeClaim"): kind = f"PVC {v['persistentVolumeClaim'].get('claimName','?')}"
    elif v.get("emptyDir") is not None:
        med = (v["emptyDir"] or {}).get("medium", "")
        kind = "emptyDir"
        if med == "Memory": kind = "emptyDir (tmpfs, MEMORY-backed)"; warn = "  ⚠ counts against the pod's memory limit — a big write here can OOM the pod"
    elif v.get("configMap"): kind = f"ConfigMap {v['configMap'].get('name','?')}"
    elif v.get("secret"):    kind = f"Secret {v['secret'].get('secretName','?')}"
    elif v.get("hostPath"):  kind = "hostPath"
    print(f"    {n}: {kind}{at}{ro}{warn}")

# 7) DEPENDENCIES — Valkey / Redis-compatible --------------------------------
head("dependencies · Valkey / Redis")
cmd(f"scan of env + referenced ConfigMaps for redis/valkey config (values redacted)")
# client-side settings from the app's env
REDIS_HINT = re.compile(r'(REDIS|VALKEY|LETTUCE|JEDIS)', re.I)
client = [(e.get("name",""), e.get("value","")) for e in (c.get("env", []) or [])
          if REDIS_HINT.search(e.get("name","")) and e.get("value") is not None]
# server-side config from a mounted redis.conf-style ConfigMap
CFG_KEYS = ("cluster-enabled", "cluster-announce-ip", "cluster-announce-hostname",
            "cluster-announce-port", "cluster-announce-tls-port", "cluster-announce-bus-port",
            "bind", "protected-mode", "port", "tls-port", "requirepass", "masterauth",
            "replica-announce-ip", "replica-announce-port", "appendonly", "maxmemory",
            "maxmemory-policy", "timeout", "tcp-keepalive", "client-output-buffer-limit",
            "cluster-node-timeout", "cluster-require-full-coverage", "cluster-migration-barrier")
server, seen = [], set()
for cm in cms:
    for fname, body in (cm.get("data", {}) or {}).items():
        low = fname.lower()
        if not (low.endswith(".conf") or "redis" in low or "valkey" in low): continue
        for line in str(body).splitlines():
            line = line.strip()
            if not line or line.startswith("#"): continue
            k = line.split()[0].lower()
            if k in CFG_KEYS:
                v = line[len(k):].strip()
                if k in ("requirepass", "masterauth"): v = "<redacted>"
                cmname = cm.get("metadata", {}).get("name", "?")
                if (cmname, k) in seen: continue
                seen.add((cmname, k))
                server.append((cmname, k, v))
if not client and not server:
    print("    no Valkey/Redis client config or server config detected in env or mounted ConfigMaps")
    print("    (if this pod IS a Valkey/Redis server, check its config for cluster-announce-* — wrong")
    print("     announce settings are the classic 'works in the pod, clients fail from elsewhere' bug)")
for n, v in client:
    print(f"    client env  {n} = {redact_val(n, v)}")
announce = False
for cmname, k, v in server:
    if k.startswith(("cluster-announce", "replica-announce")): announce = True
    print(f"    server cfg  [{cmname}] {k} {v}")
if announce:
    print("    ⚠ cluster-announce/replica-announce settings present — verify they resolve from CLIENTS,")
    print("      not just inside the pod; a wrong announce address makes clients fail after MOVED/ASK")

print("\nBottom line:")
print("  read-only wiring snapshot. Each section shows the command it used; secret VALUES are never printed.")
print("  deeper: jdebug topology (rollout tree) · jdebug why (pod-layer deep-dive) · jdebug jcmd 'VM.flags' (live JVM flags)")
PY
