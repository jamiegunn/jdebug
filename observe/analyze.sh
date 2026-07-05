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
    if head -c 12 "$f" 2>/dev/null | grep -q 'JAVA PROFILE'; then
        say "valid hprof, $(du -h "$f" | cut -f1 | tr -d ' ') — binary format, so the real analysis happens in Eclipse MAT"
        say "open: MAT → File → Open Heap Dump → run 'Leak Suspects'"
        say "leak hunting: take a second dump after more load, then MAT → 'compare to another heap dump'"
    else
        flag "NOT a valid hprof (bad magic) — likely an error page was captured instead; retry with --via jattach"
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

analyze_snapshot() {
    local d="$1" m h
    printf '\n━━ snapshot bundle: %s\n' "$d"
    [[ -f "$d/health.json" ]] && analyze_health "$d/health.json"
    for m in "$d/memory-report.txt" "$d/memory.txt"; do [[ -f "$m" ]] && analyze_memreport "$m"; done
    [[ -f "$d/threads.txt" ]] && grep -q 'Full thread dump' "$d/threads.txt" 2>/dev/null && analyze_threads "$d/threads.txt"
    [[ -f "$d/gc-heap-info.txt" ]] && analyze_gcheap "$d/gc-heap-info.txt"
    [[ -f "$d/pod.txt" ]] && analyze_pod "$d/pod.txt"
    for h in "$d"/*.hprof; do [[ -f "$h" ]] && analyze_hprof "$h"; done
}

analyze_file() {
    local f="$1"
    if head -c 12 "$f" 2>/dev/null | grep -q 'JAVA PROFILE'; then analyze_hprof "$f"; return 0; fi
    case "$f" in *.hprof) analyze_hprof "$f"; return 0 ;; esac
    if grep -q 'Full thread dump' "$f" 2>/dev/null; then analyze_threads "$f"; return 0; fi
    case "$(basename "$f")" in
        health.json)              analyze_health "$f" ;;
        memory-report.txt|memory.txt) analyze_memreport "$f" ;;
        *) return 1 ;;
    esac
    return 0
}

TARGET="${1:-$JDEBUG_DUMPS}"
echo "jdebug analyze — first-pass triage of captured evidence (the deep tools, Eclipse MAT and VisualVM, are free local installs)"

# The default dumps dir not existing just means nothing was captured yet.
if [[ ! -e "$TARGET" && "$TARGET" == "$JDEBUG_DUMPS" ]]; then
    echo
    echo "nothing to analyze in $TARGET — capture something first (threads, snapshot), then re-run."
    exit 0
fi

if [[ -f "$TARGET" ]]; then
    analyze_file "$TARGET" || { err "don't know how to analyze: $TARGET"; exit 64; }
elif [[ -d "$TARGET" ]]; then
    for d in "$TARGET"/snapshot-* "$TARGET"/jdebug-snapshot-*; do
        [[ -d "$d" ]] && analyze_snapshot "$d"
    done
    while IFS= read -r f; do
        analyze_file "$f" || true
    done < <(find "$TARGET" \( -type d \( -name 'snapshot-*' -o -name 'jdebug-snapshot-*' \) -prune \) \
                -o -type f \( -name '*.txt' -o -name '*.hprof' -o -name '*.json' \) -print 2>/dev/null | sort)
else
    err "no such file or directory: $TARGET"
    exit 2
fi

echo
if [[ "$ANALYZED" -eq 0 ]]; then
    echo "nothing to analyze in $TARGET — capture something first (threads, snapshot), then re-run."
elif [[ "$FLAGS" -gt 0 ]]; then
    echo "⚠ $ANALYZED capture(s) read, $FLAGS finding(s) flagged above."
else
    echo "✓ $ANALYZED capture(s) read — nothing alarming flagged. (The deeper tools may still find more.)"
fi
