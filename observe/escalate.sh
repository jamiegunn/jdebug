#!/usr/bin/env bash
#
# escalate.sh — the one-command handoff for asking a senior SRE or developer for
# help. Knowing WHAT context to include is half the hard part for a junior, so
# this assembles it from what jdebug already has: the current target, the live
# pod state (as findings with a confidence level), the commands already run
# (from the session log), the captures on disk and their paths, the checks that
# are blocked and why, a suggested next action, and a sensitive-evidence warning.
# Read-only — it changes nothing; it just writes a brief you can paste into chat.
#
# Usage:
#   ./escalate.sh [-n ns] [-l selector] [pod]

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

require_cmd kubectl
parse_common_args "$@"
check_cluster || exit 1

POD="$(resolve_one_pod "${REMAINING_ARGS[0]:-}")"
CTX="$(kubectl config current-context 2>/dev/null || echo '<unknown>')"

ERRF="$(mktemp)"; trap 'rm -f "$ERRF"' EXIT
POD_JSON="$(kubectl -n "$NAMESPACE" get pod "$POD" -o json 2>"$ERRF" || true)"
POD_ERR="$(head -n1 "$ERRF" 2>/dev/null || true)"
TOP_OUT="$(kubectl -n "$NAMESPACE" top pod "$POD" --no-headers 2>"$ERRF" || true)"
TOP_ERR="$(head -n1 "$ERRF" 2>/dev/null || true)"
HPA_JSON="$(kubectl -n "$NAMESPACE" get hpa -o json 2>/dev/null || echo '{}')"

# newest session transcript, if the TUI/CLI recorded one
SESSION_LOG="$(ls -t "$JDEBUG_DUMPS"/session-*.log 2>/dev/null | head -n1 || true)"

# captures for this pod
CAP_DIR="$JDEBUG_DUMPS/pods/$POD"

export POD_JSON TOP_OUT HPA_JSON POD POD_ERR TOP_ERR NAMESPACE CTX \
       CONTAINER="$APP_CONTAINER" SELECTOR="${SELECTOR:-}" \
       SESSION_LOG CAP_DIR JDEBUG_DUMPS
if ! command -v python3 >/dev/null 2>&1; then
    # python3 absent: still produce a minimal, useful brief
    echo "== escalation summary =="
    echo "target: context=$CTX ns=$NAMESPACE pod=$POD container=$APP_CONTAINER"
    echo "(install python3 for the full findings/commands/captures brief)"
    exit 0
fi
python3 - <<'PY'
import json, os, re, glob

def load(k):
    try: return json.loads(os.environ.get(k, "") or "{}")
    except Exception: return {}

pod = load("POD_JSON")
NS, POD, CONT = os.environ["NAMESPACE"], os.environ["POD"], os.environ["CONTAINER"]
CTX, SEL = os.environ["CTX"], os.environ.get("SELECTOR", "")
top = os.environ.get("TOP_OUT", "").split()
pod_err, top_err = os.environ.get("POD_ERR", ""), os.environ.get("TOP_ERR", "")

st = pod.get("status", {})
spec = pod.get("spec", {})
phase = st.get("phase", "?")
waiting = reason = None
restarts = 0
for cs in st.get("containerStatuses", []) or []:
    if cs.get("name") == CONT or len(st.get("containerStatuses", [])) == 1:
        restarts = cs.get("restartCount", 0)
        if cs.get("state", {}).get("waiting"):
            waiting = cs["state"]["waiting"].get("reason")
        if cs.get("lastState", {}).get("terminated"):
            reason = cs["lastState"]["terminated"].get("reason")

# memory % of limit (best-effort, from kubectl top + the pod's limit)
def to_bytes(s):
    m = re.match(r'([0-9.]+)([KMGT]i?)?', s or "")
    if not m: return 0
    v = float(m.group(1)); u = m.group(2) or ""
    return v * {"Ki":1024,"Mi":1024**2,"Gi":1024**3,"Ti":1024**4,"K":1e3,"M":1e6,"G":1e9,"T":1e12}.get(u, 1)
mem_pct = None
if len(top) >= 3:
    lim = ""
    for c in spec.get("containers", []):
        if c.get("name") == CONT:
            lim = c.get("resources", {}).get("limits", {}).get("memory", "")
    use_b, lim_b = to_bytes(top[2]), to_bytes(lim)
    if use_b and lim_b:
        mem_pct = int(use_b * 100 / lim_b)

# HPA
hpa = None
for h in load("HPA_JSON").get("items", []):
    hs, hsp = h.get("status", {}), h.get("spec", {})
    failing = any(c.get("type") == "ScalingActive" and c.get("status") == "False"
                  for c in hs.get("conditions", []) or [])
    hpa = (hs.get("currentReplicas"), hsp.get("maxReplicas"), failing)

# --- findings with confidence (likely/possible/unknown) ----------------------
findings = []
if waiting == "CrashLoopBackOff":
    findings.append(("likely", "CrashLoopBackOff — the container won't stay up"))
elif phase and phase != "Running":
    findings.append(("possible", f"pod phase is {phase} (not Running)"))
if reason == "OOMKilled":
    ev = " — mem %d%% of limit" % mem_pct if mem_pct and mem_pct >= 75 else ""
    findings.append(("likely", f"last restart was OOMKilled{ev}"))
elif restarts > 3:
    findings.append(("possible", f"{restarts} restarts (no OOM reason recorded)"))
if hpa and hpa[2]:
    findings.append(("unknown", "autoscaler can't scale (metrics blind?) — verify metrics-server"))
elif hpa and hpa[0] is not None and hpa[1] and hpa[0] >= hpa[1]:
    findings.append(("possible", f"HPA at max replicas ({hpa[0]}/{hpa[1]})"))
if mem_pct is not None and mem_pct >= 90:
    findings.append(("likely", f"memory {mem_pct}% of limit — at OOM risk"))
elif mem_pct is not None and mem_pct >= 75:
    findings.append(("possible", f"memory {mem_pct}% of limit"))
if not findings:
    findings.append(("unknown", "no obvious failure signal from the pod state — see the commands/captures"))

# --- blocked checks ----------------------------------------------------------
blocked = []
if re.search(r'(?i)forbidden', pod_err or ""):
    blocked.append("RBAC — a read was denied (Forbidden); needs get/list on pods+events+pods/log")
if re.search(r'(?i)metrics', top_err or "") or mem_pct is None:
    blocked.append("metrics-server — live CPU/mem % unavailable (kubectl top has no data)")

# --- commands run (from the newest session transcript) -----------------------
cmds = []
slog = os.environ.get("SESSION_LOG", "")
if slog and os.path.exists(slog):
    seen = set()
    with open(slog, errors="replace") as f:
        for line in f:
            m = re.match(r'\$\s+(.*)', line.strip())
            if m and m.group(1) not in seen:
                seen.add(m.group(1)); cmds.append(m.group(1))

# --- captures on disk --------------------------------------------------------
caps, sensitive = [], False
cap_dir = os.environ.get("CAP_DIR", "")
if cap_dir and os.path.isdir(cap_dir):
    for p in sorted(glob.glob(os.path.join(cap_dir, "**", "*"), recursive=True)):
        if os.path.isfile(p):
            caps.append(p)
            if p.endswith(".hprof") or "log" in os.path.basename(p).lower():
                sensitive = True

# --- suggested next action (from the top finding) ----------------------------
nxt = "run jdebug why for the pod-layer deep-dive, then jdebug wizard to capture evidence"
top_conf = findings[0][0]
top_txt = findings[0][1]
if "OOMKilled" in top_txt or "memory" in top_txt:
    nxt = "treat as memory: jdebug memory (no pause), then jdebug wizard flow 1; raise the limit or fix the leak"
elif "CrashLoopBackOff" in top_txt:
    nxt = "jdebug logs --previous on the crashed container + jdebug why; the crash reason is usually the last log lines"
elif "autoscaler" in top_txt:
    nxt = "jdebug workload + check metrics-server; the HPA can't read metrics"

# --- render ------------------------------------------------------------------
def line(s=""): print(s)
line("== escalation summary — paste this when asking for help ==")
line()
line(f"TARGET   context {CTX} · ns {NS} · pod {POD} · container {CONT}" + (f" · selector {SEL}" if SEL else ""))
line(f"         phase {phase} · restarts {restarts}" + (f" · last exit {reason}" if reason else ""))
line()
line("FINDINGS (confidence)")
for conf, txt in findings:
    line(f"  [{conf}] {txt}")
line()
if blocked:
    line("BLOCKED CHECKS")
    for b in blocked:
        line(f"  ✗ {b}")
    line()
line("COMMANDS ALREADY RUN")
if cmds:
    for c in cmds[-12:]:
        line(f"  $ {c}")
else:
    line("  (none recorded in a session transcript yet)")
line()
line("CAPTURES ON DISK")
if caps:
    for c in caps[-12:]:
        line(f"  {c}")
    line(f"  ({len(caps)} file(s) under {cap_dir})")
else:
    line(f"  (none under {cap_dir})")
line()
line(f"SUGGESTED NEXT  {nxt}")
line()
if sensitive:
    line("⚠ SENSITIVE EVIDENCE: heap dumps and logs can contain real user data, tokens, or PII.")
    line("  Share them over a secure channel, and treat them like production data.")
else:
    line("note: no heap dumps/logs captured yet — nothing sensitive to warn about.")
PY
