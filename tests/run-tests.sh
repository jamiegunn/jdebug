#!/usr/bin/env bash
#
# run-tests.sh — self-contained test suite for the jdebug kit. No test
# framework needed:  ./tests/run-tests.sh
#
# Cluster and pod HTTP are faked via tests/mocks/{kubectl,curl} on PATH,
# driven by MOCK_* env vars (see the mocks' headers). Each case runs a real
# entry point and asserts on exit code + output, so the user-facing text —
# error explanations, hints, safety warnings — is what's under test.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KIT="$(dirname "$HERE")"
MOCKS="$HERE/mocks"
TMP="$(mktemp -d -t jdebug-tests.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT
chmod +x "$MOCKS"/*

PASS=0; FAIL=0; FAILED=()
OUT=""; RC=0

# All tests run with mocks first on PATH, colors off, and dumps in a sandbox.
ENV=(env "PATH=$MOCKS:$PATH" NO_COLOR=1 "JDEBUG_DUMPS=$TMP/dumps" JDEBUG_QUIET=1)

run_case()  { OUT="$("${ENV[@]}" "$@" 2>&1)"; RC=$?; }                       # capture out+err+rc
run_input() { local in="$1"; shift; OUT="$(printf '%b' "$in" | "${ENV[@]}" "$@" 2>&1)"; RC=$?; }

ok()  { PASS=$((PASS+1)); printf '  ok   %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  FAIL %s\n       %s\n' "$1" "$2"; }
assert_rc()  { [[ $RC -eq $2 ]] && ok "$1" || bad "$1" "expected exit $2, got $RC | $(printf '%s' "$OUT" | head -2 | tr '\n' ' ')"; }
assert_has() { [[ "$OUT" == *"$2"* ]] && ok "$1" || bad "$1" "output missing: '$2'"; }
assert_not() { [[ "$OUT" != *"$2"* ]] && ok "$1" || bad "$1" "output should NOT contain: '$2'"; }
section()    { printf '\n== %s ==\n' "$1"; }

cd "$KIT"

# --- syntax: every script parses ---------------------------------------------
section "syntax"
for f in jdebug install.sh lib/common.sh capture/*.sh observe/*.sh ui/tui.sh tests/run-tests.sh; do
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

run_case ./jdebug dumps
assert_rc  "dumps needs no cluster (no preflight)" 0

# --- jdebug status/health/top teach how to read them --------------------------
section "triage guidance"
run_case ./jdebug status
assert_rc  "status exits 0" 0
assert_has "status shows pods" "pod-a"
assert_has "status explains CrashLoopBackOff" "CrashLoopBackOff"
assert_has "status routes OOM to the wizard" "OOMKilled"

MOCK_EXEC_OUT='{"status":"UP"}' run_case ./jdebug health
assert_has "health explains UP/DOWN reading" "chase that system first"

run_case ./jdebug top
assert_has "top explains what near-limit means" "OOM risk"

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

mkdir -p "$TMP/dumps/threads" "$TMP/dumps/snapshot-20260704T000000Z"
echo t > "$TMP/dumps/threads/pod-a-thread.txt"
echo s > "$TMP/dumps/snapshot-20260704T000000Z/health.json"
run_case ./jdebug dumps
assert_has "lists thread capture" "threads/pod-a-thread.txt"
assert_has "lists snapshot dir as one entry" "snapshot-20260704T000000Z"
assert_has "explains fastthread for threads" "fastthread.io"
assert_has "explains MAT for heap" "Leak Suspects"
assert_has "PII warning present" "real user data"
rm -rf "$TMP/dumps"

# --- destructive-action gates ---------------------------------------------------
section "confirm gates (heap pauses the JVM)"
run_case ./capture/actuator.sh heap
assert_rc  "actuator heap w/o --confirm exits 64" 64
assert_has "actuator heap explains the pause" "pause the JVM"

run_case ./capture/jdk-heap.sh
assert_rc  "jdk heap w/o --confirm exits 64" 64

run_case ./observe/snapshot.sh --heap
assert_rc  "snapshot --heap w/o --confirm exits 64" 64

run_case ./capture/jattach.sh heap
assert_rc  "jattach heap w/o --confirm exits 64" 64

run_case ./capture/jattach.sh jcmd
assert_rc  "jcmd w/o command exits 64" 64
assert_has "jcmd error shows an example" "GC.heap_info"

run_case ./jdebug threads --via bogus
assert_rc  "unknown --via exits 64" 64
assert_has "unknown --via lists valid tiers" "actuator|jattach|jdk"

# --- observe tools ---------------------------------------------------------------
section "observe tools"
run_case ./observe/tail-logs.sh
assert_rc  "logs w/o selector exits 64" 64
assert_has "logs w/o selector says how to fix" "pass -l <selector>"

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

OUT_DIR="$LOCAL_OUT" run_case sh ./jdebug-local dumps
assert_has "dumps lists the heap file" "heap-"

OUT_DIR="$TMP/local-empty" run_case sh -c 'mkdir -p "$OUT_DIR"; sh ./jdebug-local dumps'
assert_has "dumps empty: threads redirect tip" "threads >"

run_case sh ./jdebug-local jcmd "GC.heap_info"
assert_rc  "jcmd w/o jattach exits 3" 3
assert_has "jcmd missing-jattach covers in-pod" "jdebug install-jattach"
assert_has "jcmd missing-jattach covers bare metal" "bare metal"

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

# --- TUI ---------------------------------------------------------------------------
section "TUI"
run_input 'q\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_rc  "remote menu: q quits cleanly" 0
assert_has "remote menu: wizard promoted" "GUIDED DIAGNOSIS"
assert_has "remote menu: heap risk labeled" "pauses the app"
assert_has "remote menu: help key present" "h help/glossary"
assert_has "remote header: reachability shown" "cluster reachable"
assert_has "remote header: empty selector hint" "press t to narrow"

MOCK_KUBECTL=x509 run_input 'q\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "remote header: unreachable flagged" "can't connect"

# THE regression test: a FAILED command must pause with its error still visible.
MOCK_KUBECTL=x509 run_input '1\n\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "failed action: error shown" "TLS certificate isn't trusted"
assert_has "failed action: marked failed" "that didn't work"
assert_has "failed action: pauses (error not wiped)" "Press Enter for the menu"

run_input '1\n\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "action output is tee'd to session log path" "$TMP/dumps/session-"
grep -rq 'jdebug status' "$TMP"/dumps/session-*.log 2>/dev/null \
    && ok "session log records the command" || bad "session log records the command" "no session log with 'jdebug status'"

run_input 'h\n\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "help: glossary defines pod" "one running copy of the app"
assert_has "help: heap dump risk in glossary" "Pauses the app"
assert_has "help: first-10-minutes workflow" "A GOOD FIRST 10 MINUTES"
assert_has "help: safety rules" "answering n is always safe"

run_input 'zz\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_rc  "unknown key: no crash, menu redraws" 0

run_input '\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_rc  "bare Enter does NOT quit (q still needed)" 0

run_input 'q\n' env JDEBUG_MODE=2 ./ui/tui.sh
assert_has "local menu: wizard available" "GUIDED DIAGNOSIS"
assert_has "local menu: stage jattach present" "stage jattach"

run_input 'w\nb\nq\n' env JDEBUG_MODE=2 ./ui/tui.sh
assert_has "local wizard: mode-aware target" "this machine (localhost)"

run_input '7\n\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "jcmd quick-pick offered" "GC.heap_info"
assert_has "jcmd quick-pick includes JFR" "JFR.start"

MOCK_PODS=multi run_input 't\n\n\n\n\n\n0\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "target screen: context picker" "Which cluster?"
assert_has "target screen: current context marked" "mock-ctx  (current)"
assert_has "target screen: pod picker on multi" "pods match. Which one?"

run_input '1\n\nq\n' env JDEBUG_MODE=1 ./ui/tui.sh
assert_has "quit shows transcript path" "transcript of everything from this session"

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
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
if [[ $FAIL -gt 0 ]]; then
    printf 'failed:\n'; printf '  - %s\n' "${FAILED[@]}"
    exit 1
fi
