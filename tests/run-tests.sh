#!/usr/bin/env bash
#
# run-tests.sh — self-contained test suite for the jdebug kit. No test
# framework needed:  ./tests/run-tests.sh
#
# Cluster and pod HTTP are faked via tests/mocks/{kubectl,curl} on PATH,
# driven by MOCK_* env vars (see the mocks' headers). Each case runs a real
# entry point and asserts on exit code + output, so the user-facing text —
# error explanations, hints, safety warnings — is what's under test.
#
# SCOPE — this is the MOCK layer. It proves contracts and user-facing text, not
# real-cluster/real-JVM behavior. Two further suites prove the rest (run them
# when you have the environment; CI runs all three):
#   tests/live/run-live-tests.sh        — against a REAL HotSpot JVM on this host
#                                         (authentic jcmd/hprof, the vendored
#                                         jattach actually speaking the protocol)
#   tests/integration/run-kind-tests.sh — against a REAL cluster (kubectl exec/cp
#                                         over an API server, a genuine ephemeral
#                                         `kubectl debug` attach)

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KIT="$(dirname "$HERE")"
MOCKS="$HERE/mocks"
TMP="$(mktemp -d -t jdebug-tests.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT
chmod +x "$MOCKS"/*

PASS=0; FAIL=0; FAILED=()
OUT=""; RC=0

# All tests run with mocks first on PATH, colors off, and dumps + the
# remembered-target config in a sandbox (never the user's real ~/.config).
ENV=(env "PATH=$MOCKS:$PATH" NO_COLOR=1 "JDEBUG_DUMPS=$TMP/dumps" "JDEBUG_CONFIG_DIR=$TMP/config" JDEBUG_QUIET=1)

run_case()  { OUT="$("${ENV[@]}" "$@" 2>&1)"; RC=$?; }                       # capture out+err+rc

ok()  { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  FAIL %s\n       %s\n' "$1" "$2"; }
assert_rc()  { [[ $RC -eq $2 ]] && ok "$1" || bad "$1" "expected exit $2, got $RC | $(printf '%s' "$OUT" | head -2 | tr '\n' ' ')"; }
assert_has() { [[ "$OUT" == *"$2"* ]] && ok "$1" || bad "$1" "output missing: '$2'"; }
assert_not() { [[ "$OUT" != *"$2"* ]] && ok "$1" || bad "$1" "output should NOT contain: '$2'"; }
section()    { printf '\n== %s ==\n' "$1"; }

cd "$KIT"

# --- syntax: every script parses ---------------------------------------------
section "syntax"
for f in jdebug install.sh lib/common.sh capture/*.sh observe/*.sh tests/run-tests.sh; do
    if bash -n "$f" 2>/dev/null; then ok "bash -n $f"; else run_case bash -n "$f"; bad "bash -n $f" "$OUT"; fi
done
if sh -n jdebug-local 2>/dev/null; then ok "sh -n jdebug-local (POSIX)"; else ok_skip=1; run_case sh -n jdebug-local; bad "sh -n jdebug-local" "$OUT"; fi

# --- jdebug CLI basics --------------------------------------------------------
section "jdebug CLI"
run_case ./jdebug --version
assert_rc  "--version exits 0" 0
assert_has "--version prints name+version" "jdebug 1.0.0"

run_case ./jdebug --help
assert_has "--help shows commands" "threads"
assert_has "--help documents dumps location" "dumps/"

run_case ./jdebug frobnicate
assert_rc  "unknown command exits 64" 64
assert_has "unknown command shows usage" "unknown command: frobnicate"

# --- cluster preflight: errors must be translated, never raw ------------------
section "cluster preflight (check_cluster)"
MOCK_KUBECTL=x509 run_case ./jdebug status
assert_rc  "stale cert: exits 3" 3
assert_has "stale cert: plain-language why" "TLS certificate isn't trusted"
assert_has "stale cert: names the usual suspects" "Rancher Desktop"
assert_has "stale cert: gives the fix" "kubectl config use-context"
assert_not "stale cert: raw x509 wall suppressed" "Unhandled Error"

MOCK_KUBECTL=refused run_case ./jdebug memory
assert_rc  "cluster off: exits 3" 3
assert_has "cluster off: says nothing answered" "nothing answered"

MOCK_KUBECTL=noctx run_case ./jdebug threads
assert_rc  "no context: exits 3" 3
assert_has "no context: explains" "no context selected"

# expired token (EKS/GKE SSO) is NOT "unreachable" — it needs re-auth, and
# "switch context" is the WRONG fix. The most common junior failure mode.
MOCK_KUBECTL=unauthorized run_case ./jdebug status
assert_rc  "expired creds: exits 3" 3
assert_has "expired creds: says rejected, not unreachable" "REJECTED your credentials"
assert_has "expired creds: names the fix" "re-authenticate"
assert_has "expired creds: warns off the wrong fix" "switching contexts will NOT fix"
assert_not "expired creds: no 'can't reach' misdiagnosis" "can't reach the Kubernetes cluster"

run_case ./jdebug dumps
assert_rc  "dumps needs no cluster (no preflight)" 0

# --- jdebug doctor: the pre-incident checkup -----------------------------------
section "jdebug doctor"
MOCK_EXEC_OUT='{"status":"UP"}' run_case ./jdebug doctor
assert_rc  "healthy setup exits 0" 0
assert_has "checks kubectl" "kubectl on PATH"
assert_has "checks the cluster answers" "answers"
assert_has "checks pods match" "pod(s) match"
assert_has "checks the container exists" "container 'app' exists"
assert_has "checks the CAPTURE endpoint, not just /health" "tier 1 (actuator) ready"

# /health up but /threaddump 404 — stock Spring Boot's default. Doctor must NOT
# certify tier 1; it must name the exposure property (the #1 real-world miss).
MOCK_ACTUATOR=absent MOCK_EXEC_OUT='{"status":"UP"}' run_case ./jdebug doctor
assert_not "unexposed endpoint: no tier-1 blessing" "tier 1 (actuator) ready"
assert_has "unexposed endpoint: names the fix" "management.endpoints.web.exposure.include"

# wrong container name (the 'app' default on a real cluster) must be its own
# finding — not disguised as an actuator failure.
JDEBUG_CONTAINER=web run_case ./jdebug doctor
assert_rc  "wrong container: doctor fails loudly" 1
assert_has "wrong container: names the real containers" "no container named 'web'"
assert_has "wrong container: gives the fix" "--container"

MOCK_KUBECTL=x509 run_case ./jdebug doctor
assert_rc  "unreachable cluster exits 1" 1
assert_has "unreachable cluster flagged" "cluster unreachable"

MOCK_PODS=none run_case ./jdebug doctor
assert_rc  "no matching pods exits 1" 1
assert_has "no pods: says how to fix" "set -n/-l"

# --- jdebug status/health/top teach how to read them --------------------------
section "triage guidance"
run_case ./jdebug status
assert_rc  "status exits 0" 0
assert_has "status shows pods" "pod-a"
assert_has "status explains CrashLoopBackOff" "CrashLoopBackOff"
assert_has "status routes OOM to the wizard" "OOMKilled"
assert_has "status ends with a verdict" "Bottom line:"
assert_has "status names the next move" "Next:"

MOCK_EXEC_OUT='{"status":"UP"}' run_case ./jdebug health
assert_has "health explains UP/DOWN reading" "chase that system first"
assert_has "health UP: bottom line" "the app says it is healthy"

# secured actuator: pod_fetch applies auth from $ACTUATOR_AUTH, referencing
# pod env vars (the emitted snippet expands them IN THE POD, secret never local)
section "secured actuator (auth is a pod-env reference)"
run_case bash -c 'source lib/common.sh; ACTUATOR_AUTH=bearer:MGMT_TOKEN pod_fetch http://x/actuator/health'
assert_has "bearer: adds the Authorization header" "Authorization: Bearer"
assert_has "bearer: references the pod env var, unexpanded" 'Bearer $MGMT_TOKEN'
run_case bash -c 'source lib/common.sh; ACTUATOR_AUTH=basic:U:P pod_fetch http://x/actuator/health'
assert_has "basic: uses curl -u from pod env vars" 'curl -fsS -u "$U:$P"'
run_case bash -c 'source lib/common.sh; pod_fetch http://x/actuator/health'
assert_not "no auth: no Authorization header when unset" "Authorization"
# the secured-actuator failure guidance is present in the capture script
run_case grep -q "set auth in the target editor" capture/actuator.sh
assert_rc "actuator failure points at auth setup" 0

# 401-vs-absent: a FAILED actuator fetch probes the HTTP status and names the
# precise fix (secured → auth, absent → wrong path) instead of a catch-all
MOCK_ACTUATOR=secured run_case env JDEBUG_DUMPS="$TMP/adump" ./capture/actuator.sh threads -n default pod-a
assert_rc  "secured actuator: threads fails" 1
assert_has "secured actuator: names 401 + auth fix" "secured (HTTP 401)"
assert_has "secured actuator: offers the no-HTTP route" "via jattach"
MOCK_ACTUATOR=absent run_case env JDEBUG_DUMPS="$TMP/adump" ./capture/actuator.sh heap --confirm -n default pod-a
assert_rc  "absent actuator: heap fails" 1
assert_has "absent actuator: names 404 + URL fix" "not found (HTTP 404)"
# a 200 that isn't a heap dump (secured endpoint's login page) is classified,
# not passed off as a real capture headed for Eclipse MAT
MOCK_ACTUATOR=badpage run_case env JDEBUG_DUMPS="$TMP/adump" ./capture/actuator.sh heap --confirm -n default pod-a
assert_rc  "badpage: heap capture rejected" 1
assert_has "badpage: classified as an HTML login page" "HTML login page"
assert_has "badpage: names the recovery route" "via jattach"
# pod gone: a capture whose exec fails at kubectl (NotFound) must blame the POD
# (re-pick it), NOT the actuator — and must leave no misleading 0-byte file
MOCK_POD_GONE=1 run_case env JDEBUG_DUMPS="$TMP/gdump" ./capture/actuator.sh heap --confirm -n default pod-a
assert_rc  "pod-gone heap: fails" 1
assert_has "pod-gone heap: says it couldn't reach the pod" "couldn't reach the pod"
assert_has "pod-gone heap: names the real cause (renamed pod)" "REPLACED under a new name"
assert_not "pod-gone heap: does NOT blame the actuator" "secured (HTTP"
run_case bash -c 'ls '"$TMP"'/gdump/pods/pod-a/*/heap-actuator.hprof 2>/dev/null | wc -l | tr -d " "'
assert_has "pod-gone heap: leaves no 0-byte file behind" "0"
rm -rf "$TMP/gdump"

rm -rf "$TMP/adump"

# classify_capture: sniff a would-be dump and name what it actually is
run_case bash -c 'source lib/common.sh; f=$(mktemp); printf "<!DOCTYPE html><html>login password" >"$f"; classify_capture "$f"; rm -f "$f"'
assert_has "classify: HTML login page" "HTML login page"
run_case bash -c 'source lib/common.sh; f=$(mktemp); printf "{\"status\":500,\"error\":\"x\"}" >"$f"; classify_capture "$f"; rm -f "$f"'
assert_has "classify: JSON actuator error" "JSON error response"
run_case bash -c 'source lib/common.sh; f=$(mktemp); : >"$f"; classify_capture "$f"; rm -f "$f"'
assert_has "classify: empty/truncated file" "empty file"

run_case ./jdebug top
assert_has "top explains what near-limit means" "OOM risk"
assert_has "top names the next move" "Next:"

# --- pod-layer analysis: why & security ---------------------------------------
section "pod deep-dive (why) & security posture"
run_case ./jdebug why pod-a
assert_rc  "why exits 0" 0
assert_has "why: requests vs limits explained" "requests = the scheduler's promise"
assert_has "why: missing readiness probe warned" "traffic arrives the MOMENT"
assert_has "why: exit 137 decoded" "KERNEL killed it"
assert_has "why: HPA blindness explained" "ScalingActive=False"
assert_has "why: replicas-vs-HPA fight detected" "fights it back"
assert_has "why: HPA percent-of-request explained" "of the REQUEST"
assert_has "why: ends with a verdict" "Bottom line:"

MOCK_TOP=absent run_case ./jdebug why pod-a
assert_has "why: metrics-server absence explained, not blank" "metrics-server isn't installed"

MOCK_RBAC=forbidden run_case ./jdebug why pod-a
assert_rc  "why under RBAC denial exits 1" 1
assert_has "why: RBAC denial explained, never silent" "your RBAC doesn't allow"

run_case ./jdebug security pod-a
assert_rc  "security exits 0" 0
assert_has "security: root exposure explained" "prevents root"
assert_has "security: privilege escalation flagged" "allowPrivilegeEscalation"
assert_has "security: SA token risk explained" "kubernetes API"
assert_has "security: open network flagged" "no NetworkPolicy"
assert_has "security: verdict counts findings" "hardened"

MOCK_EXEC_OUT='0' run_case ./jdebug security pod-a
assert_has "security: live uid check beats the spec" "VERIFIED LIVE"

MOCK_RBAC=forbidden run_case ./jdebug security pod-a
assert_has "security: RBAC denial explained" "your RBAC doesn't allow"

# --- multi-pod transparency ----------------------------------------------------
section "pod resolution"
MOCK_PODS=multi MOCK_EXEC_OUT='{"status":"UP"}' run_case ./jdebug health
assert_has "multi-pod: announces the choice" "3 pods match — using pod-a"
assert_has "multi-pod: lists alternatives" "pod-c"

MOCK_PODS=none run_case ./jdebug health
assert_rc  "no pods: exits 2" 2
assert_has "no pods: says how to fix" "pass -n/-l"

# --- jdebug dumps --------------------------------------------------------------
section "jdebug dumps"
run_case ./jdebug dumps
assert_has "empty: says none yet" "none yet"
assert_has "empty: suggests a safe first capture" "jdebug threads"

# organized layout: dumps/pods/<pod>/<ts>/<file>
mkdir -p "$TMP/dumps/pods/pod-a/20260704T010000Z" "$TMP/dumps/pods/pod-a/20260704T020000Z"
echo t > "$TMP/dumps/pods/pod-a/20260704T010000Z/threads-jattach.txt"
: > "$TMP/dumps/pods/pod-a/20260704T020000Z/.snapshot"
echo s > "$TMP/dumps/pods/pod-a/20260704T020000Z/health.json"
run_case ./jdebug dumps
assert_has "dumps: groups by pod" "pod-a/"
assert_has "dumps: lists a single capture file" "threads-jattach.txt"
assert_has "dumps: marks a snapshot bundle" "snapshot bundle"
assert_has "dumps: explains VisualVM (local) for threads" "VisualVM"
assert_has "dumps: explains MAT for heap" "Leak Suspects"
assert_has "dumps: PII warning present" "real user data"
rm -rf "$TMP/dumps"

# --- jdebug analyze: first-pass triage of captures -------------------------------
section "jdebug analyze"
AD="$TMP/dumps"
SESS="$AD/pods/pod-a/20260704T010000Z"; BUNDLE="$AD/pods/pod-a/20260704T020000Z"
mkdir -p "$SESS" "$BUNDLE"; : > "$BUNDLE/.snapshot"
cat > "$SESS/threads-jattach.txt" <<'EOF'
Full thread dump OpenJDK 64-Bit Server VM
"main" #1 prio=5
   java.lang.Thread.State: RUNNABLE
	at com.example.Hot.spin(Hot.java:10)
"worker-1" #12
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.Db.get(Db.java:5)
	- waiting to lock <0x12345> (a java.lang.Object)
"worker-2" #13
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.Db.get(Db.java:5)
	- waiting to lock <0x12345> (a java.lang.Object)
"idle-1" #14
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(Native Method)

Found one Java-level deadlock:
EOF
printf 'JAVA PROFILE 1.0.2\0heapbytes' > "$SESS/good.hprof"
printf 'HTTP 404 not found' > "$SESS/bad.hprof"
printf '{"status":"DOWN","components":{"db":{"status":"DOWN"},"redis":{"status":"UP"}}}' \
    > "$BUNDLE/health.json"

run_case ./jdebug analyze "$AD/pods"   # explicit tree → walk every session
assert_rc  "analyze exits 0" 0
assert_has "threads: state histogram" "4 threads — 1 RUNNABLE · 2 BLOCKED · 1 WAITING"
assert_has "threads: deadlock flagged" "DEADLOCK detected"
assert_has "threads: contention + the lock" "waiting to lock <0x12345>"
assert_has "threads: names a LOCAL deep tool" "VisualVM"
assert_not "no cloud analyzers recommended" "fastthread"
assert_has "health: DOWN component named" "failing component(s): db"
assert_has "hprof: valid one sanity-checked" "valid hprof"
assert_has "hprof: invalid one flagged" "NOT a valid hprof"
assert_has "hprof: invalid classified, not sent to MAT" "raw HTTP error response"
assert_has "hprof: invalid gives exact recovery route" "via jattach --confirm"
assert_has "summary counts findings" "finding(s) flagged above"
assert_has "analyze names the next move" "Next: chase the ⚠ findings"
# the shallow heap pass advertises the opt-in retained-size (dominator) pass
assert_has "hprof: advertises the deep retained pass" "jdebug analyze --deep"

# idle NIO selector threads are RUNNABLE-but-parked — must NOT be called a busy loop
IDLE="$AD/pods/pod-idle/20260704T030000Z"; mkdir -p "$IDLE"
cat > "$IDLE/threads.txt" <<'EOF'
Full thread dump OpenJDK 64-Bit Server VM
"reactor-http-epoll-1" #20
   java.lang.Thread.State: RUNNABLE
	at java.base@21.0.11/sun.nio.ch.EPoll.wait(Native Method)
"reactor-http-epoll-2" #21
   java.lang.Thread.State: RUNNABLE
	at java.base@21.0.11/sun.nio.ch.EPoll.wait(Native Method)
"reactor-http-epoll-3" #22
   java.lang.Thread.State: RUNNABLE
	at java.base@21.0.11/sun.nio.ch.EPoll.wait(Native Method)
EOF
run_case ./jdebug analyze "$IDLE/threads.txt"
assert_rc  "analyze idle threads exits 0" 0
assert_not "idle selectors are NOT called a busy loop"   "busy loop"
assert_not "idle selectors are NOT flagged as a hot frame" "hot frame"
assert_has "idle selectors explained as parked I/O"       "parked in native I/O"

# --deep is a FLAG, not a path — it must be filtered from the target, not opened
run_case ./jdebug analyze --deep "$AD/pods/pod-a/20260704T010000Z"
assert_rc  "analyze --deep: flag parsed (exit 0)" 0
assert_not "analyze --deep: not mistaken for a file" "no such file or directory: --deep"

# an EMPTY (0-byte) heap capture means the CAPTURE failed (pod gone/RBAC), NOT
# an actuator route problem — the guidance must not mislead toward auth/URL
EMPTYH="$(mktemp -d)"; : > "$EMPTYH/heap-actuator.hprof"
run_case ./jdebug analyze "$EMPTYH/heap-actuator.hprof"
assert_has "empty hprof: flagged as empty capture" "EMPTY heap capture"
assert_has "empty hprof: blames the capture, points at the pod" "GONE or was RENAMED"
assert_has "empty hprof: says re-pick and recapture" "re-pick the current pod"
assert_not "empty hprof: does NOT mislead toward actuator auth" "set auth (k in the target editor)"
rm -rf "$EMPTYH"

# analyze with NO args: lead with the newest session, never mistake a session
# transcript (session-*.log) or remote-artifacts.tsv for a capture
AD2="$(mktemp -d)"
mkdir -p "$AD2/pods/pod-a/20260101T000000Z" "$AD2/pods/pod-a/20260102T000000Z"
printf 'JAVA PROFILE 1.0.2\0heap' > "$AD2/pods/pod-a/20260102T000000Z/heap-actuator.hprof"   # newest
printf 'Full thread dump\n"main"\n'   > "$AD2/pods/pod-a/20260101T000000Z/threads.txt"        # older
printf '\n$ jdebug threads\nFull thread dump (transcript)\n' > "$AD2/session-20260101-000000.log"
: > "$AD2/remote-artifacts.tsv"
run_case env JDEBUG_DUMPS="$AD2" ./jdebug analyze
assert_rc  "analyze default exits 0" 0
assert_has "analyze default: leads with the NEWEST session" "20260102T000000Z"
assert_has "analyze default: points at the older sessions" "showing the newest of"
assert_not "analyze default: ignores session-*.log transcripts" "session-20260101"
assert_not "analyze default: does not replay older sessions" "20260101T000000Z/threads"
run_case env JDEBUG_DUMPS="$AD2" ./jdebug analyze "$AD2/pods/pod-a/20260101T000000Z"
assert_has "analyze <dir>: analyzes exactly the requested session" "20260101T000000Z"
rm -rf "$AD2"

run_case ./jdebug analyze "$SESS/threads-jattach.txt"
assert_has "single-file analysis works" "DEADLOCK detected"
rm -rf "$AD"

run_case ./jdebug analyze
assert_has "empty: says capture first" "nothing to analyze"

# --- workload topology --------------------------------------------------------
section "topology (deployment → replicasets → pods)"
run_case ./jdebug topology pod-a
assert_rc  "topology exits 0" 0
assert_has "topology names the deployment + revision" "Deployment app  (revision 3)"
assert_has "topology marks the current ReplicaSet" "← current"
assert_has "topology flags an old RS still running pods" "OLD revision still running pods"
assert_has "topology detects the replicas-vs-HPA fight" "they FIGHT"
assert_has "topology lists the routing Service" "Services routing here: app"
assert_has "topology ends with a verdict" "Bottom line:"

# workload = topology + why collapsed into one view ([W] in both frontends)
run_case ./jdebug workload pod-a
assert_rc  "workload exits 0" 0
assert_has "workload includes the topology tree" "Deployment app  (revision 3)"
assert_has "workload includes the why deep-dive" "requests = the scheduler's promise"
assert_has "workload includes probes from why" "traffic arrives the MOMENT"

# --- runtime context / app wiring ---------------------------------------------
section "context (app wiring: services, env, probes, deps)"
MOCK_CONTEXT=1 run_case ./jdebug context pod-a
assert_rc  "context exits 0" 0
assert_has "context: owner + image" "image reg/payments:1.4.2"
assert_has "context: services & ports" "port 80 (http) → targetPort 8080"
assert_has "context: endpoints membership" "IS in rotation"
assert_has "context: probes with thresholds" "readiness: HTTP /actuator/health/readiness"
assert_has "context: JVM env surfaced" "JAVA_TOOL_OPTIONS"
assert_has "context: Spring profiles" "Spring profiles: prod,cluster"
assert_has "context: secretKeyRef shown as reference" "← Secret app-secrets/redis-pw"
assert_has "context: memory-backed volume flagged" "MEMORY-backed"
assert_has "context: PVC named" "PVC payments-pvc"
assert_has "context: Valkey/Redis client detected" "SPRING_DATA_REDIS_HOST"
assert_has "context: cluster-announce surfaced" "cluster-announce-ip"
assert_has "context: requirepass redacted (never printed)" "requirepass <redacted>"
assert_not "context: never prints the redis secret value" "supersecret"
assert_has "context: cluster-announce warning" "verify they resolve from CLIENTS"
# a secretKeyRef VALUE must never leak, and neither should a Secret env value
assert_not "context: no raw secret env values" "redis-pw ="

# --- escalation summary: the paste-ready handoff ------------------------------
section "escalate (handoff summary from session state)"
ESC="$TMP/esc"; rm -rf "$ESC"; mkdir -p "$ESC/pods/pod-a/20260705T120000Z"
printf '\n$ jdebug status\n\nout\n$ jdebug why pod-a\n' > "$ESC/session-20260705-120000.log"
printf 'JAVA PROFILE 1.0.2\0x' > "$ESC/pods/pod-a/20260705T120000Z/heap-actuator.hprof"
run_case env JDEBUG_DUMPS="$ESC" ./jdebug escalate -n default pod-a
assert_rc  "escalate exits 0" 0
assert_has "escalate: names the target" "pod pod-a · container app"
assert_has "escalate: findings carry confidence" "[likely]"
assert_has "escalate: OOM finding with memory chain" "last restart was OOMKilled"
assert_has "escalate: lists commands from the session log" "\$ jdebug why pod-a"
assert_has "escalate: lists captures with paths" "heap-actuator.hprof"
assert_has "escalate: suggests a next action" "SUGGESTED NEXT"
assert_has "escalate: warns about sensitive evidence" "SENSITIVE EVIDENCE"
# with no captures/log, it still produces a valid brief and no false sensitive warning
run_case env JDEBUG_DUMPS="$TMP/esc-empty" ./jdebug escalate -n default pod-a
assert_rc  "escalate: empty state still works" 0
assert_has "escalate: notes nothing sensitive yet" "nothing sensitive to warn about"
rm -rf "$ESC"

# --- remote artifacts: record + cleanup (never removes pre-existing) -----------
section "cleanup (remote artifacts jdebug staged in the pod)"
ART="$TMP/art"; rm -rf "$ART"
run_case env JDEBUG_DUMPS="$ART" bash -c 'source lib/common.sh; NAMESPACE=default POD=pod-a APP_CONTAINER=app record_artifact 1 /tmp/jattach jattach; NAMESPACE=default POD=pod-a APP_CONTAINER=app record_artifact 0 /tmp/keepme preexisting'
run_case env JDEBUG_DUMPS="$ART" ./jdebug cleanup
assert_rc  "cleanup lists exits 0" 0
assert_has "cleanup: lists the staged file" "/tmp/jattach"
assert_has "cleanup: marks the staged one" "staged by jdebug"
assert_has "cleanup: keeps pre-existing" "will NOT be removed"
assert_has "cleanup: names local dumps as safe" "local dumps/"
run_case env JDEBUG_DUMPS="$ART" ./jdebug cleanup --confirm
assert_rc  "cleanup --confirm exits 0" 0
assert_has "cleanup: removes the staged file" "removed /tmp/jattach"
assert_has "cleanup: pre-existing survives" "keepme"
run_case grep -c "jattach" "$ART/remote-artifacts.tsv"
assert_has "cleanup: staged entry gone from manifest" "0"
rm -rf "$ART"

# --- incident timeline: events + captures in time order -----------------------
section "timeline (events merged with captures, chronological)"
TL="$TMP/tl"; rm -rf "$TL"; mkdir -p "$TL/pods/pod-a/20260705T140900Z"
: > "$TL/pods/pod-a/20260705T140900Z/heap-actuator.hprof"
run_case env JDEBUG_DUMPS="$TL" ./jdebug timeline -n default pod-a
assert_rc  "timeline exits 0" 0
assert_has "timeline: events oldest-first" "14:00:00Z"
assert_has "timeline: warning event marked" "BackOff (x7)"
assert_has "timeline: your captures interleaved" "YOU captured"
assert_has "timeline: capture sorts after the events" "14:09:00Z"
assert_has "timeline: has a legend" "a capture you took"
rm -rf "$TL"

# --- what changed: the deploy-just-happened workflow --------------------------
section "what-changed (image / rollout / restart / scale)"
run_case ./jdebug what-changed -n default pod-a
assert_rc  "what-changed exits 0" 0
assert_has "what-changed: spec image" "spec image : reg/payments:1.4.2"
assert_has "what-changed: running image digest" "sha256:abc123"
assert_has "what-changed: restart reason" "last exit    : OOMKilled"
assert_has "what-changed: HPA vs Deployment replicas" "each deploy resets the count"
assert_has "what-changed: points at logs --previous" "the previous container's last words"

# --- lifecycle: state-changing actions, gated hard -------------------------------
section "lifecycle (re-roll / kill)"
run_case ./jdebug restart pod-a
assert_rc  "restart w/o --confirm exits 64" 64
assert_has "restart explains what happens" "rolling restart"
assert_has "restart explains the downtime risk" "NO downtime"
run_case ./jdebug restart pod-a --confirm
assert_has "restart --confirm re-rolls" "successfully rolled out"
assert_has "restart names the next move" "Next:"

run_case ./jdebug kill pod-a
assert_rc  "kill w/o --confirm exits 64" 64
assert_has "kill explains graceful termination (SIGTERM)" "SIGTERM"
assert_has "kill notes the managed respawn" "REPLACEMENT starts automatically"
run_case ./jdebug kill pod-a --confirm
assert_has "kill deletes the pod" "deleted"

MOCK_RBAC=forbidden run_case ./jdebug restart pod-a --confirm
assert_rc  "restart under RBAC denial exits 1" 1
assert_has "restart RBAC denial explained, not masked" "your RBAC doesn't allow"

# DESTRUCTIVE verbs must never guess a replica: several matching pods and no
# explicit name → refuse (same contract as heap), and kubectl delete must NOT
# run. MOCK_LOG records every kubectl call so we can assert the absence.
KILL_LOG="$TMP/kill-calls.log"; : > "$KILL_LOG"
MOCK_PODS=multi MOCK_LOG="$KILL_LOG" run_case ./jdebug kill --confirm
assert_rc  "kill --confirm with ambiguous match REFUSES (exit 2)" 2
assert_has "kill refusal says why" "refusing to guess which replica"
assert_has "kill refusal names the harm" "DELETES a pod"
assert_has "kill refusal lists the candidates" "pod-b"
if grep -q "delete pod" "$KILL_LOG"; then
    bad "kill refusal really deleted nothing" "kubectl delete WAS invoked: $(grep 'delete pod' "$KILL_LOG" | head -1)"
else
    ok  "kill refusal really deleted nothing (no kubectl delete in call log)"
fi
MOCK_PODS=multi run_case ./jdebug restart --confirm
assert_rc  "restart --confirm with ambiguous match REFUSES (exit 2)" 2
assert_has "restart refusal says why" "refusing to guess"
# an explicit pod name still works with multiple matches
MOCK_PODS=multi run_case ./jdebug kill pod-b --confirm
assert_has "kill with explicit pod proceeds" "deleted"

# --- destructive-action gates ---------------------------------------------------
section "confirm gates (heap pauses the JVM)"
run_case ./capture/actuator.sh heap
assert_rc  "actuator heap w/o --confirm exits 64" 64
assert_has "actuator heap explains the pause" "pause the JVM"

run_case ./capture/jdk-heap.sh
assert_rc  "jdk heap w/o --confirm exits 64" 64

# jcmd via an ephemeral JDK container — the distroless path (no in-pod binary).
run_case ./capture/jdk-jcmd.sh
assert_rc  "jdk-jcmd w/o a command exits 64" 64
assert_has "jdk-jcmd names the missing command" "needs a command string"
run_case ./jdebug jcmd "VM.version" --via jdk pod-a
assert_has "jcmd --via jdk routes to an ephemeral JDK container" "via ephemeral JDK container"

# distroless (no tar) staging must detect it and offer the ephemeral jcmd path.
MOCK_EXEC=noshell run_case ./capture/jattach.sh install pod-a
assert_rc  "distroless staging refused" 1
assert_has "distroless staging: detects the missing tar" "no 'tar' in"
assert_has "distroless staging: offers the ephemeral jcmd path" "jcmd \"VM.native_memory summary\" --via jdk"

run_case ./observe/snapshot.sh --heap
assert_rc  "snapshot --heap w/o --confirm exits 64" 64

run_case ./capture/jattach.sh heap
assert_rc  "jattach heap w/o --confirm exits 64" 64

run_case ./capture/jattach.sh jcmd
assert_rc  "jcmd w/o command exits 64" 64
assert_has "jcmd error shows an example" "GC.heap_info"

# --- jattach integrity gate (shared by in-pod, bare-metal, and SSH staging) ----
# The bare-metal/SSH path must NOT download an unverified binary — it installs
# the vendored, checksum-verified one, same gate as the in-pod path. Build a
# fake vendor dir so verify/tamper/unsupported are deterministic and offline.
JV="$TMP/vendor-jattach"; mkdir -p "$JV"
_ta=x64; case "$(uname -m)" in aarch64|arm64) _ta=arm64 ;; esac
printf 'FAKE-JATTACH\n' > "$JV/jattach-linux-$_ta"
( cd "$JV" && { sha256sum "jattach-linux-$_ta" 2>/dev/null || shasum -a 256 "jattach-linux-$_ta"; } > SHA256SUMS )

JATTACH_VENDOR_DIR="$JV" JATTACH_BIN="$TMP/staged-jattach" run_case bash ./capture/stage-jattach.sh local
assert_rc  "stage-jattach local: verified binary installs" 0
assert_has "stage-jattach local: says it's checksum-verified" "checksum-verified"

# KS-7: doctor must surface the PINNED jattach version so it can be CVE-tracked,
# not leave it invisible. (Clean fake vendor, before the tamper below.)
JATTACH_VENDOR_DIR="$JV" run_case ./jdebug doctor
assert_has "doctor surfaces the pinned jattach version" "jattach v2.2 vendored"
assert_has "doctor points at provenance for CVE tracking" "PROVENANCE.md"

# tamper the vendored binary → the gate must refuse (no silent install)
printf 'EVIL\n' >> "$JV/jattach-linux-$_ta"
JATTACH_VENDOR_DIR="$JV" JATTACH_BIN="$TMP/staged-jattach2" run_case bash ./capture/stage-jattach.sh local
assert_rc  "stage-jattach local: tampered binary is refused" 1
assert_has "stage-jattach local: names the checksum failure" "FAILED its checksum"

# an OS with no vendored binary must refuse and point at the actuator tier —
# never fall back to an unverified download
run_case env JATTACH_VENDOR_DIR="$JV" bash -c '
  source lib/common.sh
  jattach_verified_path Darwin arm64'
assert_rc  "no vendored binary for the platform → refuse" 1
assert_has "unsupported platform points at the actuator tier" "actuator tier"

# read-only / restricted target: an unwritable staging dir must be caught up
# front and steered to the actuator tier, not fail opaquely mid-copy. (Parent
# dir doesn't exist → the write probe fails deterministically, even as root.)
JATTACH_VENDOR_DIR="$JV" JATTACH_BIN="$TMP/no-such-dir/jattach" run_case bash ./capture/stage-jattach.sh local
assert_rc  "stage-jattach: unwritable staging dir refused" 1
assert_has "stage-jattach: names the writability limit" "not writable"

# heap data-governance gate (shared by every heap tier via jdebug's central path)
run_case env JDEBUG_REQUIRE_DATA_ACK=1 bash -c 'source lib/common.sh; heap_data_gate'
assert_rc  "heap_data_gate: governed w/o ack refuses (65)" 65
assert_has "heap_data_gate: warns about secrets/PII" "secrets"
run_case env JDEBUG_REQUIRE_DATA_ACK=1 JDEBUG_DATA_ACK=1 bash -c 'source lib/common.sh; heap_data_gate'
assert_rc  "heap_data_gate: acknowledged proceeds" 0

run_case ./jdebug threads --via bogus
assert_rc  "unknown --via exits 64" 64
assert_has "unknown --via lists valid tiers" "actuator|jattach|jdk"

# --- observe tools ---------------------------------------------------------------
section "observe tools"
run_case ./observe/tail-logs.sh
assert_rc  "logs w/o selector exits 64" 64
assert_has "logs w/o selector says how to fix" "pass -l <selector>"

run_case ./jdebug logs --previous pod-a
assert_rc  "logs --previous exits 0" 0
assert_has "logs --previous: dead container's last lines" "OutOfMemoryError"
assert_has "logs --previous: reading guide" "last lines before it died"
run_case ./jdebug logs --previous
assert_has "logs --previous resolves the pod itself" "OutOfMemoryError"

run_case ./observe/set-log-level.sh onlyonearg
assert_rc  "log-level w/o level exits 64" 64

run_case ./observe/set-log-level.sh com.example FATAL
assert_rc  "invalid level exits 64" 64
assert_has "invalid level lists valid ones" "TRACE|DEBUG|INFO"

MOCK_EXEC_OUT='{"configuredLevel":"DEBUG"}' run_case ./observe/set-log-level.sh com.example debug
assert_rc  "lowercase level accepted" 0
assert_has "lowercase level uppercased" "com.example=DEBUG"
assert_has "reminds change is not persistent" "NOT persistent"

MOCK_EXEC_OUT='{"configuredLevel":"TRACE"}' run_case ./observe/set-log-level.sh ROOT trace
assert_has "ROOT TRACE warns about noise" "VERY noisy"
assert_has "ROOT TRACE says how to revert" "log-level ROOT INFO"

# --- jdebug-local (POSIX, in-pod tool) -------------------------------------------
section "jdebug-local"
run_case sh ./jdebug-local help
assert_rc  "help exits 0" 0
assert_has "help lists dumps command" "dumps"

run_case sh ./jdebug-local frob
assert_rc  "unknown command exits 64" 64

run_case sh ./jdebug-local health
assert_has "health returns fixture body" '"status":"UP"'

run_case sh ./jdebug-local threads
assert_has "threads emits jstack format" "Full thread dump mock JVM"

run_case sh ./jdebug-local metrics
assert_has "metrics lists jvm names" "jvm.gc.pause"
assert_has "metrics lists process names" "process.cpu.usage"

run_case sh ./jdebug-local memory
assert_has "memory parses heap metric (117.7 MiB)" "117.7"
assert_has "memory shows live threads" "42"

run_case sh ./jdebug-local heap
assert_rc  "heap w/o --confirm exits 2" 2
assert_has "heap explains the pause" "PAUSES the JVM"

LOCAL_OUT="$TMP/local-out"; mkdir -p "$LOCAL_OUT"
OUT_DIR="$LOCAL_OUT" run_case sh ./jdebug-local heap --confirm
assert_rc  "heap --confirm succeeds" 0
assert_has "heap says where it wrote" "wrote $LOCAL_OUT/heap-"
assert_has "heap bare-metal hint (already local)" "saved on this machine"
assert_has "heap analyzer hint" "Leak Suspects"
assert_has "heap prints the data-governance notice" "may contain secrets"

# governed environment: without acknowledgment the dump is refused BEFORE it runs;
# with acknowledgment it proceeds.
JDEBUG_REQUIRE_DATA_ACK=1 OUT_DIR="$LOCAL_OUT" run_case sh ./jdebug-local heap --confirm
assert_rc  "heap governed w/o ack: refused (65)" 65
assert_has "heap governed w/o ack: names the ack var" "JDEBUG_DATA_ACK=1"
JDEBUG_REQUIRE_DATA_ACK=1 JDEBUG_DATA_ACK=1 OUT_DIR="$LOCAL_OUT" run_case sh ./jdebug-local heap --confirm
assert_rc  "heap governed w/ ack: proceeds" 0

# when jdebug drove us over SSH ($JDEBUG_SSH_BACK), the capture hint hands back
# the exact scp to pull the dump to the operator's machine.
OUT_DIR="$LOCAL_OUT" JDEBUG_SSH_BACK="ops@vm1" run_case sh ./jdebug-local heap --confirm
assert_has "heap over-ssh hint names the host" "saved on ops@vm1"
assert_has "heap over-ssh hint gives the scp" "scp ops@vm1:$LOCAL_OUT/heap-"

OUT_DIR="$LOCAL_OUT" run_case sh ./jdebug-local dumps
assert_has "dumps lists the heap file" "heap-"

OUT_DIR="$TMP/local-empty" run_case sh -c 'mkdir -p "$OUT_DIR"; sh ./jdebug-local dumps'
assert_has "dumps empty: threads redirect tip" "threads >"

# no route at all (force-skip native jcmd AND no jattach) → the guidance that
# names how to get a route. JDEBUG_FORCE_JATTACH makes this deterministic even on
# a host that has a JDK's jcmd.
JDEBUG_FORCE_JATTACH=1 JATTACH_BIN=/no/such/jattach run_case sh ./jdebug-local jcmd "GC.heap_info"
assert_rc  "jcmd w/ no route exits 3" 3
assert_has "no-route guidance mentions native jcmd (JDK)" "install a JDK"
assert_has "no-route guidance covers in-pod" "jdebug install-jattach"
assert_has "no-route guidance covers bare metal" "bare metal"

# KS-6: with a JDK's jcmd on PATH, jdebug uses it natively — no jattach staged,
# and jcmd is what runs (proven by JDEBUG_FORCE_JATTACH flipping the route).
if command -v jcmd >/dev/null 2>&1; then
    JATTACH_BIN=/no/such/jattach run_case sh ./jdebug-local jcmd "VM.version"
    # native jcmd against our own (non-JVM) shell fails, but the failure is a
    # jcmd/uid failure — NOT the "no route / stage jattach" guidance.
    if printf '%s' "$OUT" | grep -q "install a JDK"; then
        bad "native jcmd is preferred when present" "fell through to stage-jattach guidance despite jcmd on PATH"
    else ok "native jcmd is preferred when present (no staging needed)"; fi
fi

# cross-UID attach: the attach route must run as the JVM's user. Force the jattach
# path so this is deterministic regardless of a local JDK.
printf '#!/bin/sh\necho "cannot open socket" >&2\nexit 1\n' > "$TMP/fakejattach"; chmod +x "$TMP/fakejattach"
JDEBUG_FORCE_JATTACH=1 JATTACH_BIN="$TMP/fakejattach" JVM_PID=$$ JDEBUG_JVM_UID_OVERRIDE=1000 run_case sh ./jdebug-local jcmd "GC.heap_info"
assert_rc  "attach uid-mismatch: non-zero exit" 1
assert_has "attach uid-mismatch: names the same-user requirement" "SAME user as the JVM"
assert_has "attach uid-mismatch: shows both uids" "uid 1000"
# same uid (no override) must NOT fabricate a uid gap
JDEBUG_FORCE_JATTACH=1 JATTACH_BIN="$TMP/fakejattach" JVM_PID=$$ run_case sh ./jdebug-local jcmd "GC.heap_info"
assert_has "attach same-uid failure: falls back to the generic cause list" "common causes"

# jvms lists every JVM on the host (pid<TAB>cmd) so the menu can pick one when
# several run. Plant a fake `java` process to exercise the listing path.
run_case sh ./jdebug-local help
assert_has "usage lists the jvms command" "jvms"
cp /bin/sleep "$TMP/java" 2>/dev/null
"$TMP/java" 5 & FAKEJVM=$!   # $! is now the java process itself, not a subshell
sleep 0.3
run_case sh ./jdebug-local jvms
assert_rc  "jvms finds the running JVM" 0
assert_has "jvms lists the fake JVM's pid" "$FAKEJVM"
kill "$FAKEJVM" 2>/dev/null; wait "$FAKEJVM" 2>/dev/null; rm -f "$TMP/java"

MOCK_HTTP=fail run_case sh ./jdebug-local health
assert_rc  "actuator down: health fails" 1
assert_has "actuator down: explains + env fix" 'set $ACTUATOR_BASE'

# --- lib/common.sh units ----------------------------------------------------------
section "lib/common.sh"
run_case bash -c 'source lib/common.sh; parse_common_args -n prod -l app=x --container web extra1 extra2; echo "$NAMESPACE/$SELECTOR/$APP_CONTAINER/${REMAINING_ARGS[*]}"'
assert_has "parse_common_args sets all + remains" "prod/app=x/web/extra1 extra2"

run_case bash -c 'source lib/common.sh; pod_fetch http://x/y text/plain'
assert_has "pod_fetch prefers curl" "command -v curl"
assert_has "pod_fetch falls back to wget" "wget -qO-"
assert_has "pod_fetch sends Accept header" "Accept: text/plain"

run_case bash -c 'source lib/common.sh; pod_post_json http://x/y "{\"a\":1}"'
assert_has "pod_post_json wget path uses --post-data" "--post-data"

MOCK_PODS=multi run_case bash -c 'source lib/common.sh; resolve_one_pod'
assert_has "resolve_one_pod picks first" "pod-a"
assert_has "resolve_one_pod flags the sick-pod trap" "restarting one"

# --- remembered target: the saved-target file drives the CLI --------------------------
# (the file is written by the Go TUI's target editor; here we write it the same
# way — printf %q assignments — and assert the CLI layer honours + gates it)
section "remembered target"
mkdir -p "$TMP/config"
{
    printf '# written by jdebug\x27s target editor — delete this file to forget\n'
    printf 'SAVED_NAMESPACE=%q\n'  payments
    printf 'SAVED_SELECTOR=%q\n'   ""
    printf 'SAVED_CONTAINER=%q\n'  app
    printf 'SAVED_ACTUATOR=%q\n'   http://localhost:8080/actuator
    printf 'SAVED_ACTUATOR_AUTH=%q\n' ""
    printf 'SAVED_POD=%q\n'        pod-b
} > "$TMP/config/target"

run_case ./jdebug status
assert_has "CLI layer uses the remembered namespace" "kubectl -n payments"

JDEBUG_NAMESPACE=zzz run_case ./jdebug status
assert_has "environment still outranks the remembered value" "kubectl -n zzz"

# a tampered target file must be IGNORED (never executed) with a warning
printf 'SAVED_NAMESPACE=$(touch %s/pwned)\n' "$TMP" > "$TMP/config/target"
run_case ./jdebug status
assert_has "tampered target file is ignored with a warning" "ignoring"
assert_has "tampered target file falls back to defaults" "kubectl -n default"
[[ -f "$TMP/pwned" ]] && bad "tampered target file must never execute" "command substitution ran" \
    || ok "tampered target file never executes"

# bypass attempts that START with a valid assignment must also be caught:
# "VAR=x; cmd" (command after separator) and "VAR=x cmd" (assignment-prefix
# execution) both defeated the old prefix-only gate.
printf 'SAVED_NAMESPACE=prod; touch %s/pwned2\n' "$TMP" > "$TMP/config/target"
run_case ./jdebug status
assert_has "tamper via ';' is ignored with a warning" "ignoring"
[[ -f "$TMP/pwned2" ]] && bad "tamper via ';' must never execute" "separator command ran" \
    || ok "tamper via ';' never executes"

printf 'SAVED_NAMESPACE=prod touch %s/pwned3\n' "$TMP" > "$TMP/config/target"
run_case ./jdebug status
assert_has "tamper via assignment-prefix is ignored" "ignoring"
[[ -f "$TMP/pwned3" ]] && bad "tamper via assignment-prefix must never execute" "prefix command ran" \
    || ok "tamper via assignment-prefix never executes"

rm -f "$TMP/config/target"

# --- misdiagnosis regressions: the errors a junior actually hits --------------------
section "misdiagnosis regressions"

# wrong container name (default 'app' on a real cluster) must be named as such —
# not disguised as "actuator fetch failed / secured? wrong port?" — in BOTH engines.
JDEBUG_V1=1 MOCK_EXEC=wrongcontainer run_case ./jdebug threads --via actuator
assert_rc  "wrong container (v1): capture fails" 1
assert_has "wrong container (v1): names the real cause" "no container named"
assert_has "wrong container (v1): gives the fix" "--container"
assert_not "wrong container (v1): not blamed on the actuator" "actuator fetch failed"
if [[ -x core/jdebug-core ]]; then
    MOCK_EXEC=wrongcontainer run_case ./jdebug threads --via actuator
    assert_rc  "wrong container (core): capture fails" 1
    assert_has "wrong container (core): names the real cause" "no container named"
    assert_has "wrong container (core): gives the fix" "--container"
fi

# distroless image (no sh) must not read as "pod gone — re-pick it" — in BOTH engines.
JDEBUG_V1=1 MOCK_EXEC=noshell run_case ./jdebug threads --via actuator
assert_rc  "no shell (v1): capture fails" 1
assert_has "no shell (v1): names the real cause" "NO SHELL"
assert_has "no shell (v1): points at the tier that works" "--via jdk"
assert_not "no shell (v1): no pod-replaced misdiagnosis" "REPLACED under a new name"
if [[ -x core/jdebug-core ]]; then
    MOCK_EXEC=noshell run_case ./jdebug threads --via actuator
    assert_rc  "no shell (core): capture fails" 1
    assert_has "no shell (core): names the real cause" "NO SHELL"
    assert_has "no shell (core): points at the tier that works" "--via jdk"
fi

# events RBAC restricted: `status` must still print its decision guidance
MOCK_EVENTS=forbidden run_case ./jdebug status
assert_rc  "status survives events RBAC denial" 0
assert_has "status explains the missing events" "events not readable here"
assert_has "status still prints its reading guide" "how to read this"
assert_has "status still prints the bottom line" "Bottom line:"

# the v1 bash cascade must actually cascade (it was dead under inherited set -e):
# tier 1 secured → announce jattach fallback → announce jdk fallback → honest failure
JDEBUG_V1=1 MOCK_ACTUATOR=secured run_case ./jdebug threads
assert_rc  "v1 cascade: still fails overall (mock has no real JVM)" 1
assert_has "v1 cascade: announces the jattach fallback" "auto-falling back to jattach"
assert_has "v1 cascade: announces the jdk fallback" "falling back to an ephemeral JDK container"
assert_has "v1 cascade: honest summary" "all capture tiers failed"

# the core-binary checksum gate must FAIL CLOSED: a present-but-tampered vendored
# binary returns 2 (loud), a missing one returns 1 (quiet fallback).
GATE="$TMP/coregate"; mkdir -p "$GATE/tools/core"
_os="$(uname -s | tr '[:upper:]' '[:lower:]')"; _arch="$(uname -m)"
case "$_arch" in x86_64) _arch=amd64 ;; aarch64) _arch=arm64 ;; esac
printf '#!/bin/sh\nexit 0\n' > "$GATE/tools/core/jdebug-core-$_os-$_arch"
chmod +x "$GATE/tools/core/jdebug-core-$_os-$_arch"
printf '%s  jdebug-core-%s-%s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$_os" "$_arch" > "$GATE/tools/core/SHA256SUMS"
run_case bash -c "source lib/common.sh; resolve_core_binary '$GATE'"
assert_rc  "tampered vendored core: resolver returns 2" 2
assert_has "tampered vendored core: loud refusal" "FAILED its checksum"
rm -f "$GATE/tools/core/SHA256SUMS"
run_case bash -c "source lib/common.sh; resolve_core_binary '$GATE'"
assert_rc  "unverifiable vendored core (no SHA256SUMS): returns 2" 2
rm -rf "$GATE/tools/core"; mkdir -p "$GATE/tools/core"
run_case bash -c "source lib/common.sh; rc=0; resolve_core_binary '$GATE' || rc=\$?; echo \"rc=\$rc\""
assert_has "absent core: quiet fallback (rc=1, no error text)" "rc=1"
assert_not "absent core: stays quiet" "FAILED"

# --- end-to-end capture: a SUCCESSFUL capture writes real, findable bytes -----------
# (the review's root-cause finding: 293 assertions and not one proved a capture
# actually wrote a file — which is how wrong-path and dead-validator bugs shipped)
section "end-to-end capture"
E2E_DUMP='Full thread dump OpenJDK 64-Bit Server VM (21.0.2+13 mixed mode):
"main" #1 prio=5 os_prio=0 tid=0x1 nid=0x1 runnable
   java.lang.Thread.State: RUNNABLE'

e2e_check() { # <label-prefix> — $OUT/$RC already hold the run
    assert_rc  "$1: exits 0" 0
    assert_has "$1: reports the written file" "wrote "
    local p
    p="$(printf '%s\n' "$OUT" | sed -n 's/.*wrote \([^ ]*\) .*/\1/p' | head -1)"
    if [[ -n "$p" && -f "$p" ]] && grep -q "Full thread dump" "$p"; then
        ok "$1: the PRINTED path exists and holds the dump"
    else
        bad "$1: the PRINTED path exists and holds the dump" "path='$p'"
    fi
}

E2E_LOG="$TMP/e2e-calls.log"; : > "$E2E_LOG"
MOCK_LOG="$E2E_LOG" MOCK_EXEC_OUT="$E2E_DUMP" run_case ./jdebug threads
e2e_check "capture (default engine)"
if grep -q " exec " "$E2E_LOG"; then
    ok "capture (default engine): kubectl exec really ran (call log)"
else
    bad "capture (default engine): kubectl exec really ran (call log)" "no exec in $E2E_LOG"
fi
if [[ -x core/jdebug-core ]]; then
    E2E_PATH="$(printf '%s\n' "$OUT" | sed -n 's/.*wrote \([^ ]*\) .*/\1/p' | head -1)"
    if [[ -n "$E2E_PATH" && -f "$(dirname "$E2E_PATH")/manifest.json" ]] \
        && grep -q '"sha256"' "$(dirname "$E2E_PATH")/manifest.json"; then
        ok "capture (core): manifest with sha256 sits beside the artifact"
    else
        bad "capture (core): manifest with sha256 sits beside the artifact" "none beside '$E2E_PATH'"
    fi
fi

JDEBUG_V1=1 MOCK_EXEC_OUT="$E2E_DUMP" run_case ./jdebug threads
e2e_check "capture (v1 bash engine)"

# --- Go core engine (runs when a Go toolchain is present) ----------------------------
# The core's unit tests INCLUDE the adversarial-review regression suite
# (core/trial_regression_test.go) — they must run wherever this suite runs,
# or CI can go green while the deadlock detector is broken.
if command -v go >/dev/null 2>&1 && [[ -f core/go.mod ]]; then
    section "Go core engine"
    if (cd core && go vet ./... >/dev/null 2>&1); then ok "core: go vet"; else bad "core: go vet" "see go vet ./core/..."; fi
    if (cd core && go test ./... >"$TMP/gocore.out" 2>&1); then ok "core: go test (pipeline, validators, parsers, regressions)"
    else bad "core: go test" "$(grep -nE '^(--- FAIL|FAIL|panic:)|_test\.go:[0-9]+' "$TMP/gocore.out" | head -12)"; fi
    if fmtout="$(gofmt -l core tui 2>/dev/null)" && [[ -z "$fmtout" ]]; then ok "gofmt: all Go sources formatted"
    else bad "gofmt: all Go sources formatted" "needs gofmt -w: $fmtout"; fi
fi

# --- Go TUI frontend (runs when a Go toolchain is present) ---------------------------
if command -v go >/dev/null 2>&1 && [[ -f tui/go.mod ]]; then
    section "Go TUI frontend"
    if (cd tui && go build -o jdebug-tui . 2>"$TMP/gobuild.err"); then ok "go build"
    else bad "go build" "$(head -3 "$TMP/gobuild.err")"; fi
    if (cd tui && go vet ./... >/dev/null 2>&1); then ok "go vet"; else bad "go vet" "see go vet ./tui/..."; fi
    # capture COMBINED output — `go test` prints failing assertions to stdout, so
    # discarding it (as before) left CI failures with a blank reason. Surface the
    # FAIL/panic lines and the *_test.go:NN locations on failure.
    if (cd tui && go test ./... >"$TMP/gotest.out" 2>&1); then ok "go test (update-logic + parity)"
    else bad "go test" "$(grep -nE '^(--- FAIL|=== RUN|FAIL|ok |panic:)|_test\.go:[0-9]+' "$TMP/gotest.out" | grep -vE '=== RUN|^[0-9]+:ok ' | head -12)"; fi
    if [[ -x tui/jdebug-tui ]]; then
        run_case ./tui/jdebug-tui -version
        assert_has "tui: --version" "jdebug-tui"
        # heap histogram reader: rejects non-hprof input, doesn't crash
        run_case ./tui/jdebug-tui -analyze-heap /dev/null
        assert_rc  "heap analyzer rejects a non-hprof (exit 1)" 1
        assert_has "heap analyzer explains the rejection" "not an hprof"
        run_case ./tui/jdebug-tui -render menu
        assert_has "tui: menu sections" "QUICK CHECKS"
        assert_has "tui: start-here section" "START HERE"
        assert_has "tui: advanced tools demoted" "ADVANCED"
        assert_has "tui: workload deep-dive on key W (why collapsed in)" "W   workload"
        assert_has "tui: security on shifted S" "S   security"
        assert_has "tui: terminal on shifted T" "T   terminal"
        assert_has "tui: heap inline risk" "pauses app"
        assert_has "tui: risk legend" "safe / caution / disruptive"
        assert_has "tui: hero banner" "guided diagnosis"
        run_case ./tui/jdebug-tui -render gate
        assert_has "tui: gate panel parity" "SET UP YOUR TARGET FIRST"
        run_case ./tui/jdebug-tui -render help
        assert_has "tui: glossary parity" "one running copy of the app"
        run_case ./tui/jdebug-tui -render chooser
        assert_has "tui: chooser self-test entry" "self-test"
        run_case ./tui/jdebug-tui -render cleanup
        assert_has "tui: remote-artifacts screen lists staged files" "/tmp/jattach"
        assert_has "tui: remote-artifacts screen keeps pre-existing" "pre-existing"
        assert_has "tui: remote-artifacts screen protects local dumps" "local dumps/"
        run_case ./tui/jdebug-tui -render auth
        assert_has "tui: actuator-auth screen names both formats" "bearer:MANAGEMENT_TOKEN"
        assert_has "tui: actuator-auth screen stores a reference" "REFERENCE, not the secret"
        assert_has "tui: actuator-auth screen offers a jattach fallback" "without HTTP"
        run_case ./tui/jdebug-tui -render dashboard
        assert_has "tui: dashboard work-area tabs" "LOGS"
        assert_has "tui: dashboard events tab (warn count)" "EVENTS"
        assert_has "tui: dashboard workload pane" "WORKLOAD"
        assert_has "tui: dashboard captures pane" "CAPTURES"
        assert_has "tui: dashboard trends tab" "TRENDS"
        # the TRENDS tab (full-width metrics): heap headline + restart markers
        run_case ./tui/jdebug-tui -render trends
        assert_has "tui: trends tab shows JVM heap" "heap"
        assert_has "tui: trends tab shows GC metric" "gc"
        assert_has "tui: restart marker" "▲"
        # captures focus browser (Go 'd' opens this; bash 'd' keeps the text listing)
        run_case ./tui/jdebug-tui -render capsfocus
        assert_has "tui: captures browser filter tabs" "[all]"
        assert_has "tui: captures browser recent (all-pods) tab" "recent"
        assert_has "tui: captures browser keyboard hints" "select"
        assert_has "tui: captures browser marks invalid heaps" "not a heap dump"
        assert_has "tui: captures browser tags the capture route" "actuator"
        run_case ./tui/jdebug-tui -render output
        assert_has "tui: in-app output pane" "scroll"
        run_case ./tui/jdebug-tui -render runpane
        assert_has "tui: a held command shows in the WORK tab" "WORK"
        assert_has "tui: strip verdict + way back" "esc back to logs"
        run_case ./tui/jdebug-tui -render wizard
        assert_has "tui: crash-loop flow offered" "CrashLoopBackOff"
        assert_has "tui: deploy/what-changed flow offered" "deploy just happened"
        run_case ./tui/jdebug-tui -render detail
        assert_has "tui: transparency cards render" "what each command does"
        assert_has "tui: cards name the data source" "kubectl pod status"
        assert_has "tui: cards flag disruptive risk" "PAUSES the JVM"
        run_case ./tui/jdebug-tui -render dashboard
        assert_has "tui: limits are labeled" "of 512Mi limit"
        assert_has "tui: heap names its route" "via actuator"
        # full interactive round-trip on a real pty at 200x50: dashboard with
        # live panes → commands stream into the bottom pane → wizard keeps
        # the ExecProcess drop-out → quit
        if command -v python3 >/dev/null 2>&1; then
            if pty_out="$(python3 tests/pty-drive.py "$KIT" "$TMP/ptydrive" 2>&1)"; then
                printf '%s\n' "$pty_out"; PASS=$((PASS+13))
            else
                printf '%s\n' "$pty_out"; bad "pty: interactive round-trip" "see lines above"
            fi
        fi
    fi
else
    printf '\n== Go TUI frontend ==\n  (skipped — no Go toolchain; TUI code untested in this run: install Go)\n'
fi

# --- install.sh ----------------------------------------------------------------------
section "install.sh"
run_case ./install.sh --prefix "$TMP/bin"
assert_rc  "install exits 0" 0
[[ -L "$TMP/bin/jdebug" ]] && ok "symlink created" || bad "symlink created" "no symlink at $TMP/bin/jdebug"
run_case "$TMP/bin/jdebug" --version
assert_has "symlinked CLI resolves kit and runs" "jdebug 1.0.0"
run_case ./install.sh --prefix "$TMP/bin" --uninstall
[[ ! -e "$TMP/bin/jdebug" ]] && ok "uninstall removes symlink" || bad "uninstall removes symlink" "still there"

# --- summary --------------------------------------------------------------------------
printf '\n%d passed, %d failed  (MOCK layer — contracts + text, not real-cluster behavior)\n' "$PASS" "$FAIL"
printf 'for real-JVM/real-cluster proof, also run:\n'
printf '  tests/live/run-live-tests.sh        (a real HotSpot JVM on this host)\n'
printf '  tests/integration/run-kind-tests.sh (a real cluster: exec/cp + ephemeral debug attach)\n'
if [[ $FAIL -gt 0 ]]; then
    printf 'failed:\n'; printf '  - %s\n' "${FAILED[@]}"
    exit 1
fi
