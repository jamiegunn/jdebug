#!/usr/bin/env bash
#
# timeline.sh — the incident chronology: the pod's own Kubernetes events merged
# with the evidence YOU captured, in time order. Chronology is often the missing
# context for a junior — "it was fine, then the deploy at 14:02, then OOMs at
# 14:05, then I grabbed a heap dump at 14:09" tells the story a pile of separate
# outputs doesn't. Read-only.
#
# Usage:
#   ./timeline.sh [-n ns] [-l selector] [pod]

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

ERRF="$(mktemp)"; trap 'rm -f "$ERRF"' EXIT
EVENTS_JSON="$(kubectl -n "$NAMESPACE" get events --field-selector "involvedObject.name=$POD" -o json 2>"$ERRF" || echo '{}')"
[[ -s "$ERRF" ]] && explain_kubectl_error "$(head -n1 "$ERRF")" "reading events" >&2

CAP_DIR="$JDEBUG_DUMPS/pods/$POD"

export EVENTS_JSON CAP_DIR POD NAMESPACE
python3 <<'PY'
import json, os, glob, re
from datetime import datetime, timezone

def load(k):
    try: return json.loads(os.environ.get(k, "") or "{}")
    except Exception: return {}

POD, NS = os.environ["POD"], os.environ["NAMESPACE"]
cap_dir = os.environ.get("CAP_DIR", "")

def parse_iso(s):
    if not s: return None
    s = s.replace("Z", "+00:00")
    try: return datetime.fromisoformat(s)
    except Exception: return None

def parse_capts(name):
    # capture dir names are compact ISO: 20260705T120000Z
    m = re.match(r'(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z', name)
    if not m: return None
    y, mo, d, h, mi, s = map(int, m.groups())
    try: return datetime(y, mo, d, h, mi, s, tzinfo=timezone.utc)
    except Exception: return None

entries = []  # (datetime_or_None, kind, text)

# 1) Kubernetes events for this pod
for e in load("EVENTS_JSON").get("items", []):
    ts = e.get("lastTimestamp") or e.get("eventTime") or e.get("firstTimestamp")
    dt = parse_iso(ts)
    reason = e.get("reason", "?")
    etype = e.get("type", "Normal")
    msg = (e.get("message", "") or "").strip().replace("\n", " ")
    count = e.get("count", 1) or 1
    mark = "⚠" if etype == "Warning" else "·"
    rpt = f" (x{count})" if count and count > 1 else ""
    entries.append((dt, "event", f"{mark} {reason}{rpt}: {msg[:90]}"))

# 2) captures you took (each timestamp dir is one capture session)
if cap_dir and os.path.isdir(cap_dir):
    for d in sorted(glob.glob(os.path.join(cap_dir, "*"))):
        if not os.path.isdir(d): continue
        dt = parse_capts(os.path.basename(d))
        files = [os.path.basename(f) for f in glob.glob(os.path.join(d, "*")) if os.path.isfile(f)]
        snap = os.path.exists(os.path.join(d, ".snapshot"))
        label = "snapshot bundle" if snap else ", ".join(files) if files else "(empty)"
        entries.append((dt, "capture", f"⬇ YOU captured: {label[:90]}"))

print(f"== incident timeline — {POD} ==")
print("   the pod's Kubernetes events + the evidence you captured, oldest → newest")
print()

# oldest first; undated entries (rare) sink to the end but are still shown
dated = sorted([e for e in entries if e[0] is not None], key=lambda x: x[0])
undated = [e for e in entries if e[0] is None]
if not dated and not undated:
    print("   nothing yet — no events for this pod and no captures on disk.")
    print("   run a check (s/w) or a capture (t/x) and the timeline fills in.")
else:
    for dt, kind, text in dated:
        stamp = dt.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%SZ")
        print(f"   {stamp}  {text}")
    for _, kind, text in undated:
        print(f"   (no timestamp)        {text}")

print()
print("legend: ⚠ warning event · · normal event · ⬇ a capture you took")
print("Next: jdebug logs --previous (crash reason) · jdebug why (pod layer) · jdebug escalate (handoff)")
PY
