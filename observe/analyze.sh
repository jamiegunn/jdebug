#!/usr/bin/env bash
#
# analyze.sh — first-pass analysis of captured evidence: thread dumps, heap
# dumps, snapshot bundles, memory reports, health captures. Reads everything
# it finds and says what stands out and which real analyzer to open next.
# It is a quick triage pass, NOT a replacement for Eclipse MAT / VisualVM.
#
# Usage:
#   ./analyze.sh [file-or-directory]      # default: the kit's dumps/ dir
#
# Understands:
#   thread dumps    (files containing "Full thread dump")
#   heap dumps      (*.hprof — validated + pointed at MAT; binary, so no deep read)
#   snapshots       (snapshot-*/ and jdebug-snapshot-*/ directories, per section)
#   memory reports  (memory-report.txt / memory.txt)
#   actuator health (health.json)

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"
set +e   # greps that find nothing are data here, not failures

ANALYZED=0; FLAGS=0
say()  { printf '    %s\n' "$*"; }
flag() { printf '    ⚠ %s\n' "$*"; FLAGS=$((FLAGS+1)); }
hd()   { printf '\n■ %s\n' "$*"; ANALYZED=$((ANALYZED+1)); }

analyze_threads() {
    local f="$1" before=$FLAGS total run blk wai tim
    hd "thread dump: $f"
    total=$(grep -c '^"' "$f")
    run=$(grep -c 'Thread.State: RUNNABLE' "$f")
    blk=$(grep -c 'Thread.State: BLOCKED' "$f")
    wai=$(grep -c 'Thread.State: WAITING' "$f")
    tim=$(grep -c 'Thread.State: TIMED_WAITING' "$f")
    say "$total threads — $run RUNNABLE · $blk BLOCKED · $wai WAITING · $tim TIMED_WAITING"
    grep -q 'Java-level deadlock' "$f" \
        && flag "DEADLOCK detected — open the 'Found one Java-level deadlock' section of the file"
    if [[ "$blk" -gt 0 ]]; then
        flag "$blk thread(s) BLOCKED — lock contention. Most-contended locks:"
        grep -o 'waiting to lock <[^>]*> ([^)]*)' "$f" | sort | uniq -c | sort -rn | head -3 | sed 's/^ */      /'
    fi
    # many RUNNABLE threads on the same top frame = a busy loop / hot spot
    local hot hotn
    hot=$(awk '/Thread.State: RUNNABLE/{getline; if ($1=="at") print $2}' "$f" | sort | uniq -c | sort -rn | head -1)
    hotn=$(awk '{print $1}' <<<"$hot")
    if [[ -n "$hot" && "${hotn:-0}" -ge 3 ]]; then
        flag "hot frame: ${hotn}× $(awk '{print $2}' <<<"$hot") — that many RUNNABLE threads in one spot suggests a busy loop"
    fi
    [[ "$FLAGS" -eq "$before" ]] && say "nothing alarming — mostly waiting threads is normal for a pool-based app"
    say "deeper: open the file in VisualVM (free, runs locally — visualvm.github.io)"
}

analyze_hprof() {
    local f="$1"
    hd "heap dump: $f"
    if ! head -c 12 "$f" 2>/dev/null | grep -q 'JAVA PROFILE'; then
        # an EMPTY file means the capture COMMAND produced nothing — that's almost
        # never an actuator problem (a secured/absent actuator returns a non-empty
        # login/error page). It means the in-pod exec itself failed.
        if [ ! -s "$f" ]; then
            flag "EMPTY heap capture (0 bytes) — the capture produced no output, so there is no heap here"
            say "an empty capture means the COMMAND failed, not the heap. Usual causes:"
            say "  · the target pod is GONE or was RENAMED — pod names change on every restart"
            say "      → re-pick the current pod, then recapture:  jdebug status   (menu: g → p)"
            say "  · the exec was denied (RBAC), or the connection dropped mid-capture"
            say "  · a failed snapshot can leave a 0-byte file behind — delete it once you've recaptured"
            say "  (a secured/absent actuator returns a LOGIN or ERROR page, which is NOT empty)"
            return
        fi
        flag "NOT a valid hprof (bad magic) — this file is not a heap dump, so Eclipse MAT can't open it"
        local cls; cls="$(classify_capture "$f")"
        [ -n "$cls" ] && say "$cls"
        say "this is a CAPTURE-ROUTE problem, not a heap to analyze. Recover it:"
        say "  · secured / disabled actuator → set auth (k in the target editor), or use a no-HTTP route:"
        say "      jdebug heap --via jattach --confirm"
        say "  · app too wedged to serve HTTP → jdebug heap --via jdk --confirm"
        say "  · wrong actuator URL / base path → fix it in the target editor (g/a)"
        return
    fi
    say "valid hprof, $(du -h "$f" | cut -f1 | tr -d ' ')"
    # the Go TUI binary carries a fast class-histogram reader — the first-pass
    # "what's eating the heap?" without opening a desktop tool
    local tui="$SCRIPTS_ROOT/tui/jdebug-tui"
    if [[ -x "$tui" ]]; then
        "$tui" -analyze-heap "$f" 2>/dev/null | sed 's/^/    /' \
            || say "(couldn't parse the histogram — open it in Eclipse MAT instead)"
    else
        say "open: MAT → File → Open Heap Dump → run 'Leak Suspects'  (build the TUI for an inline histogram: make tui)"
        say "leak hunting: take a second dump after more load, then MAT → 'compare to another heap dump'"
    fi
}

analyze_health() {
    local f="$1"
    hd "actuator health: $f"
    if grep -q '"status":"DOWN"' "$f"; then
        flag "health DOWN — failing component(s): $(grep -o '"[A-Za-z0-9]*":{"status":"DOWN"' "$f" | cut -d'"' -f2 | tr '\n' ' ')"
        say "chase the failing dependency first — the JVM is often just the victim"
    elif grep -q '"status":"UP"' "$f"; then
        say "UP — every component healthy at capture time"
    else
        say "unrecognized health format — open the file"
    fi
}

analyze_memreport() {
    local f="$1" pct unacc
    hd "memory report: $f"
    pct=$(sed -n 's/.*RSS \/ limit *: *\([0-9][0-9.]*\)%.*/\1/p' "$f" | head -1)
    if [[ -n "$pct" ]]; then
        if [[ "${pct%.*}" -ge 90 ]]; then flag "RSS at ${pct}% of the container limit — OOM-kill risk"
        else say "RSS at ${pct}% of the container limit"; fi
    fi
    unacc=$(grep -m1 'Unaccounted' "$f" | awk -F: '{print $2}' | awk '{print $1}')
    [[ -n "$unacc" ]] && say "unaccounted memory (native + overhead): ${unacc} MiB — growing across snapshots ⇒ suspect a native leak"
    grep -q 'metrics unreachable\|actuator not reachable\|CAPTURE FAILED' "$f" \
        && flag "the report itself failed to read JVM metrics — re-capture before trusting it"
}

analyze_gcheap() {
    hd "GC heap info: $1"
    grep -E 'used|garbage-first|ZHeap|PSYoungGen|def new' "$1" | head -3 | sed 's/^ */    /'
    say "(pauses climbing while 'used' stays near the total ⇒ allocation pressure or a leak)"
}

analyze_pod() {
    local f="$1" rst warn
    hd "pod describe: $f"
    rst=$(grep -m1 'Restart Count' "$f" | sed 's/.*: *//' | tr -d ' ')
    if [[ -n "$rst" && "$rst" != 0 ]]; then flag "restart count: $rst — the Events section at the bottom usually says why"
    else say "restart count: ${rst:-unknown}"; fi
    warn=$(grep -c ' Warning ' "$f")
    [[ "$warn" -gt 0 ]] && say "$warn Warning event line(s) in the file — worth reading"
}

# analyze_findings — surface the ⚠ lines a why/security report already
# computed, so the analyze summary carries the kubernetes-layer verdicts too.
analyze_findings() {
    local f="$1" label="$2" n
    hd "$label: $f"
    n=$(grep -c '⚠' "$f" 2>/dev/null || echo 0)
    if [[ "$n" -gt 0 ]]; then
        grep '⚠' "$f" | head -4 | sed 's/^ *⚠ */    ⚠ /' | cut -c1-108
        [[ "$n" -gt 4 ]] && say "…and $((n-4)) more finding(s) — open the file"
        FLAGS=$((FLAGS+1))
    else
        say "no findings flagged — this layer looks clean"
    fi
}

# analyze_session <dir> — one capture session (a single capture or a full
# snapshot bundle, both now dumps/pods/<pod>/<ts>/). Dispatches every file in
# it; related evidence groups under one header.
analyze_session() {
    local d="$1" kind="capture session" f
    [[ -f "$d/.snapshot" ]] && kind="snapshot bundle"
    printf '\n━━ %s: %s\n' "$kind" "$d"
    while IFS= read -r f; do
        analyze_file "$f" || true
    done < <(find "$d" -maxdepth 1 -type f ! -name '.*' ! -name 'session-*.log' ! -name 'remote-artifacts.tsv' 2>/dev/null | sort)
}

analyze_file() {
    local f="$1"
    if head -c 12 "$f" 2>/dev/null | grep -q 'JAVA PROFILE'; then analyze_hprof "$f"; return 0; fi
    case "$f" in *.hprof) analyze_hprof "$f"; return 0 ;; esac
    if grep -q 'Full thread dump' "$f" 2>/dev/null; then analyze_threads "$f"; return 0; fi
    case "$(basename "$f")" in
        health.json)                       analyze_health "$f" ;;
        memory-report.txt|memory.txt)      analyze_memreport "$f" ;;
        gc-heap-info.txt)                  analyze_gcheap "$f" ;;
        pod.txt)                           analyze_pod "$f" ;;
        why.txt)                           analyze_findings "$f" "pod deep-dive" ;;
        security.txt)                      analyze_findings "$f" "security posture" ;;
        threads-*.json|*-thread-*.json)    analyze_threads "$f" ;;
        *) return 1 ;;
    esac
    return 0
}

TARGET="${1:-$JDEBUG_DUMPS}"
EXPLICIT=""; [[ $# -ge 1 ]] && EXPLICIT=1   # was a target passed, or the default?
echo "jdebug analyze — first-pass triage of captured evidence (the deep tools, Eclipse MAT and VisualVM, are free local installs)"

# The default dumps dir not existing just means nothing was captured yet.
if [[ ! -e "$TARGET" && "$TARGET" == "$JDEBUG_DUMPS" ]]; then
    echo
    echo "nothing to analyze in $TARGET — capture something first (threads, snapshot), then re-run."
    exit 0
fi

# list_sessions <root> — every capture-session dir under <root> (a dir that
# directly holds a real capture file), newest first by its timestamp name.
# session-*.log (transcripts) and remote-artifacts.tsv are NOT captures.
list_sessions() {
    find "$1" -type f ! -name '.*' ! -name 'session-*.log' ! -name 'remote-artifacts.tsv' -exec dirname {} \; 2>/dev/null | sort -u |
    while IFS= read -r d; do
        local ts; ts="$(basename "$d" | grep -oE '[0-9]{8}T[0-9]{6}Z' | tail -1)"
        printf '%s\t%s\n' "${ts:-00000000T000000Z}" "$d"
    done | sort -r
}

if [[ -f "$TARGET" ]]; then
    analyze_file "$TARGET" || { err "don't know how to analyze: $TARGET"; exit 64; }
elif [[ -d "$TARGET" ]]; then
    if find "$TARGET" -maxdepth 1 -type f ! -name '.*' ! -name 'session-*.log' ! -name 'remote-artifacts.tsv' 2>/dev/null | grep -q .; then
        # the target IS a session dir (directly holds captures) → analyze it
        analyze_session "$TARGET"
    elif [[ -z "$EXPLICIT" ]]; then
        # `jdebug analyze` with no args: analyze the NEWEST session only, so the
        # thing you just captured isn't buried under every historical dump. Point
        # at the rest rather than replaying all of them.
        sessions="$(list_sessions "$TARGET")"
        if [[ -z "$sessions" ]]; then
            echo
            echo "nothing to analyze yet — capture something (threads, heap, snapshot), then re-run."
        else
            newest="$(printf '%s\n' "$sessions" | head -1 | cut -f2-)"
            total="$(printf '%s\n' "$sessions" | grep -c .)"
            analyze_session "$newest"
            if [[ "$total" -gt 1 ]]; then
                printf '\n  ↩ showing the newest of %d capture sessions.\n' "$total"
                printf '    a specific one: jdebug analyze <dir>   ·   every one: jdebug analyze %s/pods\n' "$JDEBUG_DUMPS"
            fi
        fi
    else
        # an explicit dir → walk and analyze every session under it (newest first)
        while IFS= read -r line; do
            [[ -n "$line" ]] && analyze_session "$(printf '%s' "$line" | cut -f2-)"
        done < <(list_sessions "$TARGET")
    fi
else
    err "no such file or directory: $TARGET"
    exit 2
fi

echo
if [[ "$ANALYZED" -eq 0 ]]; then
    echo "nothing to analyze in $TARGET — capture something first (threads, snapshot), then re-run."
    echo "Next: jdebug threads is safe and instant; the menu's w picks the right capture for a symptom."
elif [[ "$FLAGS" -gt 0 ]]; then
    echo "⚠ $ANALYZED capture(s) read, $FLAGS finding(s) flagged above."
    echo "Next: chase the ⚠ findings — thread questions open in VisualVM, heap questions in Eclipse MAT."
else
    echo "✓ $ANALYZED capture(s) read — nothing alarming flagged. (The deeper tools may still find more.)"
    echo "Next: if the problem persists, capture more targeted evidence — the menu's w picks the right kind."
fi
