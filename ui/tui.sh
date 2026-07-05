#!/usr/bin/env bash
#
# tui.sh — the jdebug interactive menu. It opens by asking WHERE the JVM is:
#   1 remote      operator machine → kubectl exec into a pod (drives the jdebug CLI)
#   2 in-pod      a shell inside the pod, no kubectl        (drives jdebug-local on localhost)
#   3 bare metal  a JVM on this host, no Kubernetes         (drives jdebug-local on localhost)
# Set JDEBUG_MODE=1|2|3 to skip the prompt. Launch via `./jdebug` or the kit CLI.

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"
set +e   # interactive loop — never die on a failed action

DBG="$SCRIPTS_ROOT/jdebug"              # mode 1 backend (kubectl)
LOCAL="$SCRIPTS_ROOT/jdebug-local"      # mode 2/3 backend (localhost, no kubectl)
export NAMESPACE SELECTOR APP_CONTAINER # mode 1: 't' retargets; children inherit
POD_PIN="${SAVED_POD:-}"                # mode 1: '' = auto; 't' can pin one pod (remembered)
SAVED_POD_GONE=""                       # set when a remembered pin no longer exists

# save_target — remember the current target between sessions (loaded by
# lib/common.sh as the layer under flags and env).
save_target() {
    mkdir -p "$JDEBUG_CONFIG_DIR" 2>/dev/null || return 0
    {
        echo "# written by jdebug's target editor — delete this file to forget"
        printf 'SAVED_NAMESPACE=%q\n' "$NAMESPACE"
        printf 'SAVED_SELECTOR=%q\n'  "$SELECTOR"
        printf 'SAVED_CONTAINER=%q\n' "$APP_CONTAINER"
        printf 'SAVED_ACTUATOR=%q\n'  "$ACTUATOR_BASE"
        printf 'SAVED_POD=%q\n'       "$POD_PIN"
    } > "$JDEBUG_TARGET_FILE" 2>/dev/null || true
}
: "${ACTUATOR_BASE:=http://localhost:8080/actuator}"; export ACTUATOR_BASE
: "${JATTACH_BIN:=/tmp/jattach}";                     export JATTACH_BIN
MODE="${JDEBUG_MODE:-}"

# Everything a command prints is also written here, so nothing is ever lost
# to a redraw — the path is shown at every pause and on quit.
SESSION_LOG="$JDEBUG_DUMPS/session-$(date +%Y%m%d-%H%M%S).log"

# Ctrl-C stops the running command (e.g. a streaming `logs`) and returns to
# the menu instead of killing the whole TUI.
trap 'printf "\n"' INT

# The screen is cleared ONCE at startup. After that everything scrolls, so
# results stay visible above the next menu and in the terminal's scrollback.
CLEAR_NEXT=1
maybe_clear() {
    if [[ -n "$CLEAR_NEXT" ]]; then clear 2>/dev/null || printf '\n\n'; CLEAR_NEXT=""
    else printf '\n'; fi
}

# Cached cluster reachability for the header (probed at most every 20s so
# menu redraws stay snappy; a target change forces a re-probe).
CLUSTER_OK="" CLUSTER_TS=-999
cluster_probe() {
    (( SECONDS - CLUSTER_TS < 20 )) && return
    CLUSTER_TS=$SECONDS
    if kubectl get --raw=/version --request-timeout=3s >/dev/null 2>&1; then CLUSTER_OK=1; else CLUSTER_OK=""; fi
}

bye() {
    [[ -f "$SESSION_LOG" ]] && printf '\n%stranscript of everything from this session: %s%s\n' "$DIM" "$SESSION_LOG" "$OFF"
    exit 0
}

# --- readiness gate -----------------------------------------------------------
# The action menu stays hidden until the target is actually usable, so a
# capture can never be fired at nothing or at the wrong thing.
#   remote: cluster answering + a specific pod pinned + the container really
#           existing in that pod
#   local:  at least one working route to the JVM (actuator answering, or
#           jattach staged)
TARGET_OK="" TARGET_WHY="" TARGET_TS=-999
target_probe() {
    (( SECONDS - TARGET_TS < 20 )) && return
    TARGET_TS=$SECONDS
    TARGET_OK=""; TARGET_WHY=""
    cluster_probe
    if [[ -n "$CLUSTER_OK" ]]; then
        TARGET_WHY+="   ${GN}✓${OFF} cluster reachable"$'\n'
    else
        TARGET_WHY+="   ${RD}✗${OFF} cluster — not reachable (press ${GN}c${OFF} for the full why + fix, or g to switch context)"$'\n'
    fi
    if [[ -z "$POD_PIN" ]]; then
        TARGET_WHY+="   ${RD}✗${OFF} pod — none selected yet (press ${GN}g${OFF}, then ${GN}p${OFF}, and pick the exact pod)"$'\n'
        TARGET_WHY+="   ${DIM}·${OFF} container — checked once a pod is selected"$'\n'
    elif [[ -n "$CLUSTER_OK" ]]; then
        local conts
        conts="$(kubectl -n "$NAMESPACE" get pod "$POD_PIN" -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' 2>/dev/null)"
        if [[ -z "$conts" ]]; then
            TARGET_WHY+="   ${RD}✗${OFF} pod — $POD_PIN no longer exists (press ${GN}t${OFF}, then ${GN}p${OFF}, to re-pick)"$'\n'
        else
            TARGET_WHY+="   ${GN}✓${OFF} pod $POD_PIN"$'\n'
            if printf '%s\n' "$conts" | grep -qx "$APP_CONTAINER"; then
                TARGET_WHY+="   ${GN}✓${OFF} container $APP_CONTAINER"$'\n'
            else
                TARGET_WHY+="   ${RD}✗${OFF} container — '$APP_CONTAINER' is not in that pod (it has: $(printf '%s' "$conts" | tr '\n' ' ')) — press ${GN}t${OFF}, then ${GN}o${OFF}"$'\n'
            fi
        fi
    else
        TARGET_WHY+="   ${DIM}·${OFF} pod + container — checked once the cluster answers"$'\n'
    fi
    [[ "$TARGET_WHY" != *✗* ]] && TARGET_OK=1
}

LOCAL_OK="" LOCAL_WHY="" LOCAL_TS=-999
local_probe() {
    (( SECONDS - LOCAL_TS < 20 )) && return
    LOCAL_TS=$SECONDS
    LOCAL_OK=""; LOCAL_WHY=""
    local act="" jat=""
    sh "$LOCAL" health >/dev/null 2>&1 && act=1
    [[ -x "$JATTACH_BIN" ]] && jat=1
    if [[ -n "$act" ]]; then LOCAL_WHY+="   ${GN}✓${OFF} actuator answering at $ACTUATOR_BASE"$'\n'
    else LOCAL_WHY+="   ${RD}✗${OFF} actuator — nothing answering at $ACTUATOR_BASE (press ${GN}s${OFF} to fix the URL/port)"$'\n'; fi
    if [[ -n "$jat" ]]; then LOCAL_WHY+="   ${GN}✓${OFF} jattach staged at $JATTACH_BIN"$'\n'
    else LOCAL_WHY+="   ${RD}✗${OFF} jattach — not staged (press ${GN}i${OFF} to download it, ~80 KB)"$'\n'; fi
    if [[ -n "$act" || -n "$jat" ]]; then
        LOCAL_OK=1
        [[ -z "$act" ]] && LOCAL_WHY+="   ${DIM}(jattach alone is enough: threads/heap/jcmd work; actuator-only views won't)${OFF}"$'\n'
        [[ -z "$jat" ]] && LOCAL_WHY+="   ${DIM}(actuator alone is enough to start; jattach adds jcmd/JFR on top)${OFF}"$'\n'
    fi
}

# --- colors (respect NO_COLOR / non-tty) -----------------------------------
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    B=$'\033[1m'; DIM=$'\033[2m'; CY=$'\033[36m'; GN=$'\033[32m'; YL=$'\033[33m'; RD=$'\033[31m'; OFF=$'\033[0m'
else B=""; DIM=""; CY=""; GN=""; YL=""; RD=""; OFF=""; fi

box() { printf '%s╔══════════════════════════════════════════════════════════════╗%s\n' "$B" "$OFF"
        printf '%s║  %-60s║%s\n' "$B" "$1" "$OFF"
        printf '%s╚══════════════════════════════════════════════════════════════╝%s\n' "$B" "$OFF"; }
hr() { printf '%s────────────────────────────────────────────────────────────────%s\n' "$DIM" "$OFF"; }

# --- main-menu palette (readability-tuned GitHub-dark; truecolor → 16-color) --
# Brightened from the spec's literal hexes: on real terminals #8b949e/#6e7681/
# #484f58 read as mud. The whole grey ramp is lifted ~2 steps and the key
# elements are bold, keeping the hierarchy (body > muted > dim > faint) but
# making every tier comfortably legible. Rules stay dark — they're chrome.
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    MB=$'\033[1m'   # bold weight for keys / names / labels
    if [[ "${COLORTERM:-}" == *truecolor* || "${COLORTERM:-}" == *24bit* ]]; then
        tc() { printf '\033[38;2;%s;%s;%sm' "$1" "$2" "$3"; }
        C_TITLE=$MB$(tc 240 246 252)  # #f0f6fc bold — title, hero label
        C_BODY=$MB$(tc 230 237 243)   # #e6edf3 bold — command names
        C_MUTED=$(tc 182 194 207)     # #b6c2cf — descriptions, status values
        C_DIMT=$MB$(tc 158 167 177)   # #9ea7b1 bold — section labels, nav keys
        C_FAINT=$(tc 139 148 158)     # #8b949e — separators, sub-labels
        C_KEY=$MB$(tc 121 192 255)    # #79c0ff bold — mnemonic keys
        C_ACC=$(tc 31 111 235)        # #1f6feb accent bar
        C_RULE=$(tc 48 54 61)         # #30363d hairlines
        C_SAFE=$(tc 63 185 80)        # #3fb950 risk-safe
        C_CAUT=$(tc 227 179 65)       # #e3b341 risk-caution
        C_DISR=$MB$(tc 255 123 114)   # #ff7b72 bold — risk-disruptive
        C_OK=$MB$(tc 63 185 80)       # #3fb950 bold — live dot, prompt caret
        unset -f tc
    else
        C_TITLE=$'\033[1;97m'; C_BODY=$'\033[1;37m'; C_MUTED=$'\033[0;37m'
        C_DIMT=$'\033[1;90m'; C_FAINT=$'\033[0;90m'; C_KEY=$'\033[1;94m'
        C_ACC=$'\033[0;34m'; C_RULE=$'\033[0;90m'
        C_SAFE=$'\033[0;92m'; C_CAUT=$'\033[0;93m'; C_DISR=$'\033[1;91m'; C_OK=$'\033[1;92m'
    fi
    C_R=$'\033[0m'; C_REV=$'\033[7m'
else
    C_TITLE="" C_BODY="" C_MUTED="" C_DIMT="" C_FAINT="" C_KEY="" C_ACC=""
    C_RULE="" C_SAFE="" C_CAUT="" C_DISR="" C_OK="" C_R="" C_REV=""
fi

# --- main-menu render helpers -------------------------------------------------
TW=80
panel_width() {
    TW=$( { command -v tput >/dev/null 2>&1 && tput cols; } 2>/dev/null || echo 80 )
    [[ "$TW" =~ ^[0-9]+$ ]] || TW=80
    (( TW < 78 )) && TW=78
    (( TW > 120 )) && TW=120   # fill wide terminals; the description column flexes
}
mrep() { local s; s="$(printf '%*s' "$1" '')"; printf '%s' "${s// /─}"; }
mrule() { printf ' %s%s%s\n' "$C_RULE" "$(mrep $((TW-2)))" "$C_R"; }

# msection <LABEL> [sublabel] — small-caps label + trailing hairline rule
msection() {
    local label="$1" sub="${2:-}" used fill
    used=$(( 1 + ${#label} + ${#sub} + (${#sub}>0 ? 2 : 0) + 1 ))
    fill=$(( TW - used - 1 )); (( fill < 3 )) && fill=3
    printf ' %s%s%s' "$C_DIMT" "$label" "$C_R"
    [[ -n "$sub" ]] && printf '  %s%s%s' "$C_FAINT" "$sub" "$C_R"
    printf ' %s%s%s\n' "$C_RULE" "$(mrep "$fill")" "$C_R"
}

# mrow <key> <name> <description> <safe|caution|disruptive> [inline-risk-text]
# 4 columns: key(accent) name(12,body) desc(flex,muted) risk-dot(right edge)
mrow() {
    local key="$1" name="$2" desc="$3" risk="$4" rtext="${5:-}"
    local dotc pad rlen
    case "$risk" in
        safe)       dotc="$C_SAFE" ;;
        caution)    dotc="$C_CAUT" ;;
        disruptive) dotc="$C_DISR" ;;
    esac
    rlen=$(( 1 + ${#rtext} + (${#rtext}>0 ? 1 : 0) ))
    pad=$(( TW - 3 - 1 - 3 - 12 - ${#desc} - rlen - 1 )); (( pad < 1 )) && pad=1
    printf '   %s%s%s   %s%-12s%s%s%s%s%*s%s●%s%s%s\n' \
        "$C_KEY" "$key" "$C_R" "$C_BODY" "$name" "$C_R" "$C_MUTED" "$desc" "$C_R" \
        "$pad" '' "$dotc" "${rtext:+ }${rtext}" "$C_R"
}

mprompt() { printf '\n %s❯%s %s %s' "$C_OK" "$C_R" "$C_REV" "$C_R"; }
# Single keypress everywhere: navigation acts instantly; only confirmations
# (destructive actions, quitting) demand a deliberate y.
pause() {
    if [[ -f "$SESSION_LOG" ]]; then
        printf '\n%sany key for the menu — this output stays in your scrollback and is saved to%s\n' "$DIM" "$OFF"
        printf '%s%s%s ' "$DIM" "$SESSION_LOG" "$OFF"
    else
        printf '\n%sany key for the menu…%s ' "$DIM" "$OFF"
    fi
    read -rsn1 _ || bye; printf '\n'
}
confirm() { printf '%s%s%s [y/N] ' "$YL" "$1" "$OFF"; local a; read -rn1 a || return 1; printf '\n'; [[ "$a" == y || "$a" == Y ]]; }
run() {
    printf '\n%s$ %s%s\n\n' "$CY" "$*" "$OFF"
    mkdir -p "$(dirname "$SESSION_LOG")" 2>/dev/null
    printf '\n$ %s\n' "$*" >> "$SESSION_LOG" 2>/dev/null
    "$@" 2>&1 | tee -a "$SESSION_LOG"
    local rc=${PIPESTATUS[0]}
    if [[ $rc -eq 0 ]]; then printf '\n%s✓ done%s\n' "$GN" "$OFF"
    else printf '\n%s✗ that didn'\''t work (exit %s) — the messages above say why and what to try next%s\n' "$RD" "$rc" "$OFF"; fi
    return $rc
}

choose_mode() {
    local m
    while true; do
        maybe_clear
        box "jdebug - where is the JVM you want to debug?"
        printf '\n'
        printf '   %s1%s  %sRemote%s      operator machine → %skubectl exec%s into a pod  %s(needs kubectl + a context)%s\n' "$GN" "$OFF" "$B" "$OFF" "$CY" "$OFF" "$DIM" "$OFF"
        printf '   %s2%s  %sIn-pod%s      a shell INSIDE the pod, no kubectl        %s(JRE-only image is fine)%s\n' "$GN" "$OFF" "$B" "$OFF" "$DIM" "$OFF"
        printf '   %s3%s  %sBare metal%s  a JVM on THIS host, no Kubernetes at all\n' "$GN" "$OFF" "$B" "$OFF"
        printf '   %su%s  %sself-test%s   run the kit'\''s own test suite %s(~10s, touches nothing of yours)%s\n' "$GN" "$OFF" "$B" "$OFF" "$DIM" "$OFF"
        printf '\n  %sNot sure? If you normally type kubectl to reach the app, pick 1.%s\n' "$B" "$OFF"
        printf '  %sModes 2 & 3 talk to localhost actuator + a local jattach + /proc (via jdebug-local).%s\n' "$DIM" "$OFF"
        printf '  %sNote: this menu needs bash. A stock JRE/busybox pod has none — for those, run the%s\n' "$YL" "$OFF"
        printf '  %ssingle-file  jdebug-local  CLI in the pod instead:  sh /tmp/jdebug-local help%s\n' "$YL" "$OFF"
        printf '\n  %s> %s' "$B" "$OFF"; read -rn1 m || bye; printf '\n'
        case "$m" in
            1|2|3) MODE="$m"; return ;;
            u|U)
                if [[ -f "$SCRIPTS_ROOT/tests/run-tests.sh" ]]; then
                    run bash "$SCRIPTS_ROOT/tests/run-tests.sh"
                else
                    printf '  %stests not found at %s/tests — run from a full checkout%s\n' "$YL" "$SCRIPTS_ROOT" "$OFF"
                fi
                pause ;;
            q|Q) bye ;;
            *) MODE=1; return ;;
        esac
    done
}
mode_label() { case "$MODE" in 1) echo "remote · kubectl → pod";; 2) echo "in-pod · localhost";; 3) echo "bare metal · localhost";; esac; }

# --- headers (2 lines + rule; one glanceable status line) --------------------
# header_line1 <right-label>
header_line1() {
    local title=" jvm debug kit" right="$1" pad
    pad=$(( TW - ${#title} - ${#right} - 1 )); (( pad < 1 )) && pad=1
    printf '%s%s%s%*s%s%s%s\n' "$C_TITLE" "$title" "$C_R" "$pad" '' "$C_DIMT" "$right" "$C_R"
}
header_remote() {
    maybe_clear; panel_width
    local ctx; ctx="$(kubectl config current-context 2>/dev/null)"
    cluster_probe
    header_line1 "$(mode_label) "
    # status line: ● ctx · ns / container [· pod] · :port/path · hints
    local dot="${C_OK}●${C_R}" act="${ACTUATOR_BASE#http://localhost}"
    [[ "$act" == "$ACTUATOR_BASE" ]] && act="$ACTUATOR_BASE"
    if [[ -z "$CLUSTER_OK" ]]; then dot="${C_DISR}●${C_R}"; fi
    # keep the status line one line: long pod names truncate to their unique tail
    local podshow="$POD_PIN"
    [[ ${#podshow} -gt 18 ]] && podshow="…${podshow: -15}"
    printf ' %s %s%s%s' "$dot" "$C_MUTED" "${ctx:-<no context>}" "$C_R"
    [[ -z "$CLUSTER_OK" ]] && printf ' %sunreachable — [c] explains why%s' "$C_DISR" "$C_R"
    printf '  %s·%s  %s%s / %s%s%s' "$C_FAINT" "$C_R" "$C_MUTED" "$NAMESPACE" "$APP_CONTAINER" "${podshow:+ · $podshow}" "$C_R"
    printf '  %s·%s  %s%s%s' "$C_FAINT" "$C_R" "$C_MUTED" "$act" "$C_R"
    printf '  %s·%s  %s[g] retarget  [M] mode%s\n' "$C_FAINT" "$C_R" "$C_FAINT" "$C_R"
    [[ -n "$SAVED_POD_GONE" ]] && printf '   %syour previous pin %s no longer exists — back to auto ([g] to re-pick)%s\n' "$C_CAUT" "$SAVED_POD_GONE" "$C_R"
    mrule
}
header_local() {
    maybe_clear; panel_width
    header_line1 "$(mode_label) "
    local jat="jattach missing" jatc="$C_DISR" act="${ACTUATOR_BASE#http://localhost}"
    [[ "$act" == "$ACTUATOR_BASE" ]] && act="$ACTUATOR_BASE"
    [[ -x "$JATTACH_BIN" ]] && { jat="jattach ok"; jatc="$C_MUTED"; }
    printf ' %s●%s %s%s%s  %s·%s  %s%s%s  %s·%s  %s[s] settings  [M] mode%s\n' \
        "$C_OK" "$C_R" "$C_MUTED" "$act" "$C_R" "$C_FAINT" "$C_R" "$jatc" "$jat" "$C_R" "$C_FAINT" "$C_R" "$C_FAINT" "$C_R"
    mrule
}

# --- utilities --------------------------------------------------------------
# Default (Enter) = auto: actuator → jattach → jdk. Explicit choices force one tier.
ask_via() {
    printf '  %sThere are three ways to capture — auto tries them safest-first and tells you which worked:%s\n' "$DIM" "$OFF"
    printf '  %s    actuator  ask the app itself over HTTP (safest, needs Spring Boot actuator)%s\n' "$DIM" "$OFF"
    printf '  %s    jattach   tiny helper binary placed in the pod (works without actuator)%s\n' "$DIM" "$OFF"
    printf '  %s    jdk       temporary JDK debug container (last resort, needs cluster permission)%s\n' "$DIM" "$OFF"
    printf '  [Enter] auto (recommended) / [o] actuator / [j] jattach / [d] jdk: '
    local v; read -rn1 v; printf '\n'; case "$v" in j|J) VIA_FLAG="--via jattach" ;; d|D) VIA_FLAG="--via jdk" ;; o|O) VIA_FLAG="--via actuator" ;; *) VIA_FLAG="" ;; esac; }

# choose_from <title> <current> <free:0|1> <options> — numbered dropdown.
# <options> is a newline-separated string (passed as an argument, NOT stdin —
# stdin must stay free for the selection keypress). ≤9 options select on a
# single keypress; longer lists take a typed number. 't' types a free value
# (when allowed); Enter/anything else keeps the current.
# Result in $CHOICE (empty = keep current).
choose_from() {
    local title="$1" current="$2" free="${3:-1}" opts=() line
    CHOICE=""
    while IFS= read -r line; do [[ -n "$line" ]] && opts+=("$line"); done <<< "${4:-}"
    if [[ ${#opts[@]} -eq 0 ]]; then
        # nothing enumerable (RBAC may forbid listing) — free text still works
        if [[ "$free" == 1 ]]; then
            printf '  %s(nothing to list — no permission to enumerate? just type the value)%s\n' "$DIM" "$OFF"
            printf '  %s [%s]: ' "$title" "$current"; IFS= read -r CHOICE
        else
            printf '  %s(nothing to list)%s\n' "$DIM" "$OFF"
        fi
        return 0
    fi
    printf '\n  %s%s%s\n' "$B" "$title" "$OFF"
    local i=1 o
    for o in "${opts[@]}"; do
        if [[ "$o" == "$current" ]]; then printf '   %s%d%s  %s  %s(current)%s\n' "$GN" "$i" "$OFF" "$o" "$DIM" "$OFF"
        else printf '   %s%d%s  %s\n' "$GN" "$i" "$OFF" "$o"; fi
        i=$((i+1))
    done
    [[ "$free" == 1 ]] && printf '   %st%s  type a value\n' "$GN" "$OFF"
    printf '  %s(any other key keeps: %s)%s > ' "$DIM" "$current" "$OFF"
    local k
    if [[ ${#opts[@]} -le 9 ]]; then read -rn1 k; printf '\n'; else read -r k; fi
    if [[ "$k" == t && "$free" == 1 ]]; then printf '  value: '; IFS= read -r CHOICE
    elif [[ "$k" =~ ^[0-9]+$ ]] && (( k >= 1 && k <= ${#opts[@]} )); then CHOICE="${opts[$((k-1))]}"
    fi
    return 0
}

# ask_jcmd — nobody fresh out of college knows jcmd commands by heart; offer
# the five useful ones and still accept anything typed.
ask_jcmd() {
    JCMD_PICK=""
    printf '  %sThe useful jcmd commands:%s\n' "$DIM" "$OFF"
    printf '   %s1%s  GC.heap_info               how full is the heap, which collector    %ssafe%s\n' "$GN" "$OFF" "$GN" "$OFF"
    printf '   %s2%s  VM.native_memory summary   off-heap breakdown %s(needs NMT enabled)%s   %ssafe%s\n' "$GN" "$OFF" "$DIM" "$OFF" "$GN" "$OFF"
    printf '   %s3%s  Thread.print -l            thread dump via the attach socket        %ssafe%s\n' "$GN" "$OFF" "$GN" "$OFF"
    printf '   %s4%s  VM.flags                   the flags the JVM actually started with  %ssafe%s\n' "$GN" "$OFF" "$GN" "$OFF"
    printf '   %s5%s  JFR.start duration=60s filename=/tmp/rec.jfr   60s profiling recording\n' "$GN" "$OFF"
    printf '  pick 1-5 · t to type any jcmd · anything else cancels: '
    local v; read -rn1 v; printf '\n'
    case "$v" in
        1) JCMD_PICK="GC.heap_info" ;;
        2) JCMD_PICK="VM.native_memory summary" ;;
        3) JCMD_PICK="Thread.print -l" ;;
        4) JCMD_PICK="VM.flags" ;;
        5) JCMD_PICK="JFR.start duration=60s filename=/tmp/rec.jfr" ;;
        t|T) printf '  jcmd command: '; IFS= read -r JCMD_PICK ;;
        *) JCMD_PICK="" ;;
    esac
}

# show_help — the glossary + workflow screen ('h'). Assumes zero prior K8s/JVM
# knowledge: every word the menus use is explained here in plain language.
show_help() {
    box "jdebug help — the words, the workflow, the safety rules"
    cat <<EOF

  ${B}THE WORDS${OFF}
    pod          one running copy of the app (a container, roughly). Replicas = several pods.
    namespace    a folder for pods; your app lives in one (header shows which you target)
    selector     a label filter like app=payments that picks YOUR app's pods out of the namespace
    container    pods can hold several containers; we talk to the app's one (usually "app")
    actuator     Spring Boot's built-in admin endpoints over HTTP — health, metrics, dumps.
                 Safest way in; everything tries it first.
    thread dump  a snapshot of what every thread is doing — THE tool for slow/hung/high-CPU.
                 Safe, instant, no impact.
    heap dump    every object in memory written to a file — THE tool for leaks/OOM.
                 ${RD}Pauses the app while it writes${OFF} — that's why it always asks first.
    jattach      an ~80 KB helper binary we place in the pod to talk to the JVM directly
                 when actuator can't. jcmd = the JVM's admin commands, sent through it.
    heap vs RSS  heap = memory the JVM manages; RSS = everything the container really uses.
                 The gap (buffers, metaspace, threads) is what 'memory' (option 4) explains.

  ${B}A GOOD FIRST 10 MINUTES${OFF}
    1. ${GN}s${OFF} status — is anything restarting or stuck? read the hints under the output
    2. ${GN}h${OFF} health — is a dependency (db/queue) DOWN? chase that system first
    3. ${GN}w${OFF} wizard — tell it the symptom; it runs the right captures and says what's next
    4. ${GN}d${OFF} — see what you captured · ${GN}a${OFF} — analyze it all in one pass

  ${B}KEYS NOT SHOWN ON THE MENU${OFF}
    ${GN}i${OFF} stage jattach in the pod · ${GN}p${OFF} push the in-pod tool (jdebug-local)
    ${GN}g${OFF} target editor · ${GN}M${OFF} switch mode · ${GN}d${OFF} browse captures

  ${B}THE SAFETY RULES${OFF}
    · everything is read-only except: ${RD}heap dumps pause the app${OFF} (H asks for a
      second H before it fires), log-level adds log volume
    · anything risky asks you first — cancelling is always safe
    · every capture is saved under dumps/ and every command's output goes to the
      session log — you can't lose evidence by pressing the wrong key
    · heap dumps can contain real user data: treat them like production data

EOF
}
# retarget — the TARGET editor ('t'). Each field is one keypress; fields the
# cluster can enumerate open a live dropdown (contexts, namespaces, selectors
# built from the pods' actual labels, containers from the pod spec, pods);
# free text stays available everywhere. Enter/b returns to the menu.
retarget() {
    local k v cur
    while true; do
        printf '\n  %sTARGET%s — press a letter to change a field · %sEnter%s/%sb%s back to the menu\n' "$B" "$OFF" "$GN" "$OFF" "$GN" "$OFF"
        printf '   %sc%s  context     %s%s%s\n' "$GN" "$OFF" "$GN" "$(kubectl config current-context 2>/dev/null || echo '<none>')" "$OFF"
        printf '   %sn%s  namespace   %s%s%s\n' "$GN" "$OFF" "$GN" "$NAMESPACE" "$OFF"
        printf '   %ss%s  selector    %s%s%s\n' "$GN" "$OFF" "$GN" "${SELECTOR:-<any pod>}" "$OFF"
        printf '   %sp%s  pod         %s%s%s\n' "$GN" "$OFF" "$GN" "${POD_PIN:-<auto: first match>}" "$OFF"
        printf '   %so%s  container   %s%s%s\n' "$GN" "$OFF" "$GN" "$APP_CONTAINER" "$OFF"
        # (selections are saved on exit and remembered next session)
        printf '   %sa%s  actuator    %s%s%s\n' "$GN" "$OFF" "$GN" "$ACTUATOR_BASE" "$OFF"
        printf '  > '
        read -rn1 k || break; printf '\n'
        case "$k" in
            c|C)
                cur="$(kubectl config current-context 2>/dev/null)"
                choose_from "Which cluster? (kube contexts on this machine)" "$cur" 0 \
                    "$(kubectl config get-contexts -o name 2>/dev/null)"
                if [[ -n "$CHOICE" && "$CHOICE" != "$cur" ]]; then
                    printf '  %sthis runs `kubectl config use-context %s` — it becomes your kubectl default in every terminal%s\n' "$YL" "$CHOICE" "$OFF"
                    if confirm "switch to $CHOICE?"; then
                        kubectl config use-context "$CHOICE" >/dev/null 2>&1 && printf '  switched to %s%s%s\n' "$GN" "$CHOICE" "$OFF"
                        CLUSTER_TS=-999; POD_PIN=""
                    fi
                fi ;;
            n|N)
                choose_from "Namespace" "$NAMESPACE" 1 \
                    "$(kubectl get namespaces -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)"
                [[ -n "$CHOICE" ]] && { NAMESPACE="$CHOICE"; POD_PIN=""; CLUSTER_TS=-999; } ;;
            s|S)
                # dropdown built from the `app` labels actually on pods here,
                # plus an explicit any-pod option ('t' still types any label)
                choose_from "Selector — apps found in $NAMESPACE" "${SELECTOR:-<any pod>}" 1 \
                    "$(printf '<any pod>\n'; kubectl -n "$NAMESPACE" get pods -o jsonpath='{range .items[*]}{.metadata.labels.app}{"\n"}{end}' 2>/dev/null | grep . | sort -u | sed 's/^/app=/')"
                if [[ "$CHOICE" == "<any pod>" ]]; then SELECTOR=""; POD_PIN=""
                elif [[ -n "$CHOICE" ]]; then SELECTOR="$CHOICE"; POD_PIN=""; fi ;;
            o|O)
                # containers come from the pinned pod's spec (else the first match)
                local basepod conts=""
                basepod="${POD_PIN:-$(resolve_pods 2>/dev/null | head -n1 || true)}"
                [[ -n "$basepod" ]] && conts="$(kubectl -n "$NAMESPACE" get pod "$basepod" -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' 2>/dev/null)"
                choose_from "Container${basepod:+ (in $basepod)}" "$APP_CONTAINER" 1 "$conts"
                [[ -n "$CHOICE" ]] && APP_CONTAINER="$CHOICE" ;;
            p|P) pick_pod ;;
            a|A) printf '  actuator base [%s]: ' "$ACTUATOR_BASE"; IFS= read -r v; [[ -n "$v" ]] && ACTUATOR_BASE="$v" ;;
            b|B|"") break ;;
            *) : ;;
        esac
        export NAMESPACE SELECTOR APP_CONTAINER ACTUATOR_BASE
    done
    export NAMESPACE SELECTOR APP_CONTAINER ACTUATOR_BASE
    CLUSTER_TS=-999; TARGET_TS=-999   # target changed — re-probe everything
    save_target       # remember these selections for the next session
}

# pick_pod — when several pods match, let the user pin one instead of silently
# taking the first. Status + restart counts are shown because the restarting
# pod is usually the one worth debugging.
pick_pod() {
    POD_PIN=""
    SAVED_POD_GONE=""
    local pods
    pods="$(kubectl -n "$NAMESPACE" get pods ${SELECTOR:+-l "$SELECTOR"} \
        -o jsonpath='{range .items[*]}{.metadata.name}{"  "}{.status.phase}{"  restarts="}{.status.containerStatuses[0].restartCount}{"\n"}{end}' 2>/dev/null)"
    if [[ -z "$pods" ]]; then
        printf '  %sno pods match this target right now — captures will say so too. Check namespace/selector.%s\n' "$YL" "$OFF"
        return
    fi
    local n; n="$(printf '%s\n' "$pods" | grep -c .)"
    if [[ "$n" == 1 ]]; then
        printf '  one pod matches — it will be used automatically: %s\n' "$(printf '%s\n' "$pods" | awk '{print $1}')"
        return
    fi
    printf '\n  %s%s pods match. Which one? (a high restart count usually marks the sick one)%s\n' "$B" "$n" "$OFF"
    local i=1 line
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        printf '   %s%d%s  %s\n' "$GN" "$i" "$OFF" "$line"
        i=$((i+1))
    done <<< "$pods"
    printf '   %s0%s  auto — just use the first match each time\n' "$GN" "$OFF"
    printf '  > '; local c
    if [[ "$n" -le 9 ]]; then read -rn1 c; printf '\n'; else read -r c; fi
    if [[ "$c" =~ ^[0-9]+$ ]] && (( c >= 1 && c < i )); then
        POD_PIN="$(printf '%s\n' "$pods" | sed -n "${c}p" | awk '{print $1}')"
        printf '  pinned: every capture now targets %s%s%s (press g to change)\n' "$GN" "$POD_PIN" "$OFF"
    else
        printf '  auto — the first matching pod is used (you will be told which)\n'
    fi
}
local_settings() {
    printf '  actuator base URL [%s]: ' "$ACTUATOR_BASE"; local v; read -r v; [[ -n "$v" ]] && ACTUATOR_BASE="$v"
    printf '  jattach binary    [%s]: ' "$JATTACH_BIN";   read -r v; [[ -n "$v" ]] && JATTACH_BIN="$v"
    printf '  JVM pid           [%s]: ' "${JVM_PID:-auto}"; read -r v; [[ -n "$v" ]] && export JVM_PID="$v"
    export ACTUATOR_BASE JATTACH_BIN
    LOCAL_TS=-999   # route may have changed — re-probe
    save_target
}

# --- jattach staging (local modes) -------------------------------------------
# jdebug-local auto-falls back to jattach when the actuator is unreachable, but
# only if the binary already sits at $JATTACH_BIN — being a one-file in-pod
# tool, it never downloads anything itself. THIS process, though, runs where
# there usually IS egress (especially bare metal), so the menu can fetch it.
stage_jattach_local() {
    LOCAL_TS=-999   # route is about to change — re-probe on the next menu
    if [[ -x "$JATTACH_BIN" ]]; then printf '  jattach already staged at %s\n' "$JATTACH_BIN"; return 0; fi
    local ver="${JATTACH_VERSION:-v2.2}" asset
    case "$(uname -s)-$(uname -m)" in
        Linux-x86_64|Linux-amd64)  asset="jattach-linux-x64.tgz" ;;
        Linux-aarch64|Linux-arm64) asset="jattach-linux-arm64.tgz" ;;
        Darwin-*)                  asset="jattach-macos.zip" ;;   # universal binary; bsdtar extracts zip
        *) err "no prebuilt jattach for $(uname -s)/$(uname -m) — place one at $JATTACH_BIN yourself"; return 1 ;;
    esac
    local cache; cache="$JDEBUG_CACHE_DIR/jattach-$(uname -s)-$(uname -m)-$ver"
    if [[ ! -f "$cache" ]]; then
        ensure_dir "$JDEBUG_CACHE_DIR"
        local url="https://github.com/jattach/jattach/releases/download/$ver/$asset"
        local tmp; tmp="$(mktemp -d -t jattach.XXXXXX)"
        info "downloading $url"
        # tar -xf auto-detects gzip (GNU + bsd) and, on macOS, zip (bsdtar).
        { if command -v curl >/dev/null 2>&1; then curl -fsSL -o "$tmp/$asset" "$url"; else wget -qO "$tmp/$asset" "$url"; fi; } \
            && tar -xf "$tmp/$asset" -C "$tmp" && mv "$tmp/jattach" "$cache" && chmod +x "$cache"
        if [[ ! -f "$cache" ]]; then
            err "download/unpack failed — fetch $url yourself and place the binary at $JATTACH_BIN"
            rm -rf "$tmp"; return 1
        fi
        rm -rf "$tmp"
        info "cached at $cache"
    fi
    if cp "$cache" "$JATTACH_BIN" && chmod +x "$JATTACH_BIN"; then
        info "staged jattach at $JATTACH_BIN"
    else
        err "cannot write $JATTACH_BIN — point at the cache instead: settings (s) → jattach binary → $cache"
        return 1
    fi
}
# Pre-flight for local captures: when the actuator is down AND jattach (the
# automatic fallback) is missing, the capture is doomed — say so and offer the
# download BEFORE running it, instead of failing with "stage it first".
jattach_fallback_check() {
    [[ -x "$JATTACH_BIN" ]] && return 0
    sh "$LOCAL" health >/dev/null 2>&1 && return 0
    printf '  %sactuator not answering at %s and jattach is not staged — this capture will fail.%s\n' "$YL" "$ACTUATOR_BASE" "$OFF"
    confirm "download jattach now (~80 KB, github.com/jattach) so the fallback works?" && stage_jattach_local
}

# --- guided diagnosis wizard (works in every mode) ---------------------------
# Each symptom maps to a diagnostic recipe: explain the plan, run the right
# capture sequence against the current target, then name the next step.
wiz_say() { printf '  %s%s%s\n' "$CY" "$*" "$OFF"; }
wiz_hd()  { printf '\n  %s— %s —%s\n\n' "$B" "$*" "$OFF"; }
# wrun <verb> [args] — run a capture verb against the current mode's backend.
# The remote CLI and jdebug-local share verbs (health/memory/threads/heap/
# jcmd/snapshot); top and status need kubectl, so local modes skip them.
wrun() {
    if [[ "$MODE" == 1 ]]; then run "$DBG" "$@" ${POD_PIN:+"$POD_PIN"}
    else case "$1" in
            top|status) wiz_say "(skipping '$1' — it needs kubectl, so it only works in remote mode)" ;;
            *) run sh "$LOCAL" "$@" ;;
         esac
    fi
}
wiz_oom() {
    wiz_hd "OOMKilled / memory restarts"
    wiz_say "First question: is the memory going into the Java heap, or somewhere else?"
    wiz_say "The memory report reconciles what the kernel sees vs what the JVM admits to:"
    wrun memory
    wiz_say "How to read it:  heap ≈ limit        → heap pressure or a leak → heap dump + MAT"
    wiz_say "                 heap low, RSS high  → off-heap: metaspace / buffers / native (NMT)"
    confirm "capture a HEAP DUMP now? (⚠ pauses the app for seconds-to-minutes)" && wrun heap --confirm
    confirm "capture native-memory detail (NMT, via jattach — safe)?" && wrun jcmd "VM.native_memory summary"
    wiz_say "Next → open the .hprof in Eclipse MAT and run 'Leak Suspects'."
    wiz_say "       press d in the menu to see every capture and where it lives."
}
wiz_slow() {
    wiz_hd "Slow / hung / high latency"
    wiz_say "A thread dump shows what every thread is doing — look for threads BLOCKED"
    wiz_say "waiting on a pool (db connections) or a deadlock. Safe, no pause:"
    wrun threads
    wiz_say "And the app's own health checks — a DOWN dependency (db/queue/cache) explains stalls:"
    wrun health
    wiz_say "Next → press a (analyze) — it flags deadlocks, blocked pools, and hot loops —"
    wiz_say "       then open the .txt in VisualVM (free, local). d lists your captures."
}
wiz_cpu() {
    wiz_hd "High CPU / autoscaler scaling up"
    wiz_say "Two thread dumps a few seconds apart — a stack that is RUNNABLE in both"
    wiz_say "is your hot loop. Both are safe and instant:"
    wrun threads
    wrun threads
    wrun top
    wiz_say "And the JVM's own CPU number (0.0–1.0 of what it's allowed to use):"
    wrun metrics process.cpu.usage
    wiz_say "Next → diff the two dumps; the persistently-RUNNABLE stack is eating your CPU."
    wiz_say "       Deeper: a 60s flight recording — jcmd \"JFR.start duration=60s filename=/tmp/r.jfr\""
}
wiz_leak() {
    wiz_hd "Memory creeping up (suspected leak)"
    wiz_say "A leak = objects that survive and accumulate. First, the number to watch"
    wiz_say "(write down VALUE — re-run this option later; steady growth = leak):"
    wrun metrics jvm.memory.used
    wiz_say "The proof is two heap dumps: a baseline now, a second after more load,"
    wiz_say "then diff them in Eclipse MAT."
    confirm "take the BASELINE heap dump now? (⚠ pauses the app)" && wrun heap --confirm
    wiz_say "Next → let the app run/take traffic, come back, re-run this option for dump #2,"
    wiz_say "       then MAT → open both → 'compare to another heap dump' (dominator trees)."
}
wiz_gc() {
    wiz_hd "GC pauses climbing"
    wiz_say "Checking how full the heap is and how the collector is coping:"
    wrun jcmd "GC.heap_info"
    wrun memory
    wiz_say "The GC's own scorecard — COUNT = collections so far, TOTAL_TIME = seconds spent paused:"
    wrun metrics jvm.gc.pause
    wiz_say "Trend it: note TOTAL_TIME, wait a minute under load, run this option again —"
    wiz_say "if TOTAL_TIME grows fast while the heap stays near-full, GC is thrashing."
    wiz_say "Next → that pattern = allocation pressure or a leak → heap dump (option 4) → Eclipse MAT."
}
wiz_all() {
    wiz_hd "Not sure — capture everything"
    wiz_say "One bundle with threads + health + memory + JVM internals, so you (or a"
    wiz_say "colleague) can analyze offline without touching production again."
    if confirm "include a HEAP DUMP in the bundle? (⚠ pauses the app)"; then wrun snapshot --heap --confirm; else wrun snapshot; fi
    wiz_say "Next → press a (analyze) for a first pass over the whole bundle; then"
    wiz_say "       threads.txt → VisualVM · heap.hprof → Eclipse MAT (both free, local)."
}
wizard() {
    while true; do
        maybe_clear
        box "Guided diagnosis - what are you seeing?"
        printf '\n'
        printf '   %s1%s  Pod %sOOMKilled%s / restarts on memory\n'   "$GN" "$OFF" "$B" "$OFF"
        printf '   %s2%s  %sSlow%s / hung / high latency\n'           "$GN" "$OFF" "$B" "$OFF"
        printf '   %s3%s  %sHigh CPU%s / autoscaler adding pods\n'    "$GN" "$OFF" "$B" "$OFF"
        printf '   %s4%s  Memory %screeping up%s over time (leak)\n'  "$GN" "$OFF" "$B" "$OFF"
        printf '   %s5%s  %sGC pauses%s climbing\n'                   "$GN" "$OFF" "$B" "$OFF"
        printf '   %s6%s  Not sure — %scapture everything%s\n'        "$GN" "$OFF" "$B" "$OFF"
        printf '   %sb%s  back\n'                                     "$GN" "$OFF"
        local tgt; if [[ "$MODE" == 1 ]]; then tgt="$NAMESPACE / ${SELECTOR:-<any pod>}"; else tgt="this machine (localhost)"; fi
        printf '\n  %starget: %s · anything that could hurt the app asks you first%s\n' "$DIM" "$tgt" "$OFF"
        printf '\n  %s> %s' "$B" "$OFF"; local s; read -rn1 s || return; printf '\n'
        case "$s" in
            1) wiz_oom ;; 2) wiz_slow ;; 3) wiz_cpu ;; 4) wiz_leak ;; 5) wiz_gc ;; 6) wiz_all ;;
            b|B|"") return ;;
            *) continue ;;
        esac
        pause
    done
}

# --- menus ------------------------------------------------------------------
# banner + footer shared by both modes
menu_banner() {
    printf '\n %s▎%s%s▸ w%s  %sguided diagnosis%s %s— describe the symptom, it runs the right captures%s\n\n' \
        "$C_ACC" "$C_R" "$C_KEY" "$C_R" "$C_TITLE" "$C_R" "$C_MUTED" "$C_R"
}
menu_footer() {  # $1 = nav keys string (plain), printed dim; legend right-aligned
    local nav="$1" legend_plain="●●● safe / caution / disruptive" pad
    mrule
    pad=$(( TW - 1 - 5 - ${#nav} - ${#legend_plain} - 1 )); (( pad < 2 )) && pad=2
    printf ' %smore%s  %s%s%s%*s%s●%s%s●%s%s●%s %ssafe / caution / disruptive%s\n' \
        "$C_FAINT" "$C_R" "$C_DIMT" "$nav" "$C_R" "$pad" '' \
        "$C_SAFE" "$C_R" "$C_CAUT" "$C_R" "$C_DISR" "$C_R" "$C_FAINT" "$C_R"
}

menu_remote() {
    header_remote
    target_probe
    if [[ -z "$TARGET_OK" ]]; then
        printf '\n  %s⚠ SET UP YOUR TARGET FIRST%s — the tools appear when every line below is %s✓%s:\n\n' "$YL" "$OFF" "$GN" "$OFF"
        printf '%s' "$TARGET_WHY"
        printf '\n  %sPress%s %sg%s %sto open the target editor%s %s(Enter works too)%s — it lists your clusters,\n' "$B" "$OFF" "$C_KEY" "$OFF" "$B" "$OFF" "$DIM" "$OFF"
        printf '  namespaces, pods, and containers so you pick instead of type.\n'
        printf '\n %smore%s  %s[g] target  [c] check setup  [?] help  [M] mode  [q] quit%s\n' "$C_FAINT" "$C_R" "$C_DIMT" "$C_R"
        mprompt
        return
    fi
    menu_banner
    msection "INSPECT" "read-only"
    mrow s status  "pods up? restarts, recent events"       safe
    mrow h health  "app checks — db, queue, disk"           safe
    mrow o top     "CPU + memory per pod, autoscaler"       safe
    mrow m memory  "container total vs JVM heap/non-heap"   safe
    printf '\n'
    msection "CAPTURE" "saves to dumps/ · [d] browse"
    mrow t threads  "what every thread is doing now"        safe
    mrow j jcmd     "advanced JVM — GC, JFR, native"        caution
    mrow H heap     "every object, for leak hunting"        disruptive "pauses app"
    mrow x snapshot "everything in one offline bundle"      safe
    printf '\n'
    msection "LOGS"
    mrow l logs      "live stream from every replica"       safe
    mrow v verbosity "change log level, no restart"         caution
    printf '\n'
    menu_footer "[a] analyze  [c] check setup  [?] help  [q] quit"
    mprompt
}
menu_local() {
    header_local
    local_probe
    if [[ -z "$LOCAL_OK" ]]; then
        printf '\n  %s⚠ SET UP A ROUTE TO THE JVM FIRST%s — the tools appear when at least one line is %s✓%s:\n\n' "$YL" "$OFF" "$GN" "$OFF"
        printf '%s' "$LOCAL_WHY"
        printf '\n %smore%s  %s[s] settings  [i] stage jattach  [?] help  [M] mode  [q] quit%s\n' "$C_FAINT" "$C_R" "$C_DIMT" "$C_R"
        mprompt
        return
    fi
    menu_banner
    msection "INSPECT" "read-only"
    mrow h health  "app checks — db, queue, disk"           safe
    mrow e metrics "browse JVM metrics, or one live value"  safe
    mrow m memory  "container total vs JVM heap/non-heap"   safe
    printf '\n'
    msection "CAPTURE" "saves to ${OUT_DIR:-/tmp} · [d] browse"
    mrow t threads  "what every thread is doing now"        safe
    mrow j jcmd     "advanced JVM — GC, JFR, native"        caution
    mrow H heap     "every object, for leak hunting"        disruptive "pauses app"
    mrow x snapshot "everything in one offline bundle"      safe
    printf '\n'
    menu_footer "[a] analyze  [i] stage jattach  [s] settings  [?] help  [q] quit"
    mprompt
}

# confirm_disruptive <key> <message> — spec §5: disruptive actions fire only on
# a second press of the SAME key; any other key cancels.
confirm_disruptive() {
    printf '  %s%s — press %s%s%s%s again to confirm, any other key cancels%s ' "$YL" "$2" "$OFF$C_KEY" "$1" "$OFF" "$YL" "$OFF"
    local k; read -rn1 k; printf '\n'
    [[ "$k" == "$1" ]] || { printf '  %scancelled%s\n' "$DIM" "$OFF"; return 1; }
}

# Keys are case-sensitive: H (heap) and M (mode) are deliberately shifted —
# the spec's t/threads-vs-retarget and m/memory-vs-mode collisions are
# resolved as g = target editor, M = mode switch.
dispatch_remote() {
    # Not ready → only setup/help keys work; everything else explains why.
    if [[ -z "$TARGET_OK" ]]; then
        case "$1" in
            ""|g|G) retarget; SKIP_PAUSE=1 ;;
            '?')    show_help ;;
            c|C)    run "$DBG" doctor ;;
            M)      choose_mode; SKIP_PAUSE=1 ;;
            q|Q)    confirm "quit jdebug?" && bye; return 1 ;;
            *)      printf '  %sfinish the target setup first — press g. The tools unlock when every check is ✓.%s\n' "$YL" "$OFF"; return 1 ;;
        esac
        return 0
    fi
    case "$1" in
        w|W) wizard; SKIP_PAUSE=1 ;;
        s|S) run "$DBG" status ;;
        h)   run "$DBG" health ${POD_PIN:+"$POD_PIN"} ;;
        o|O) run "$DBG" top ;;
        m)   run "$DBG" memory ${POD_PIN:+"$POD_PIN"} ;;
        t|T) ask_via; run "$DBG" threads $VIA_FLAG ${POD_PIN:+"$POD_PIN"} ;;
        j|J) ask_jcmd; [[ -n "$JCMD_PICK" ]] && run "$DBG" jcmd "$JCMD_PICK" ${POD_PIN:+"$POD_PIN"} ;;
        H)   confirm_disruptive H "heap dump pauses the app while it runs" || return 1
             ask_via; run "$DBG" heap $VIA_FLAG --confirm ${POD_PIN:+"$POD_PIN"} ;;
        x|X) if confirm "include a heap dump in the bundle? (PAUSES the JVM)"; then run "$DBG" snapshot --heap --confirm ${POD_PIN:+"$POD_PIN"}; else run "$DBG" snapshot ${POD_PIN:+"$POD_PIN"}; fi ;;
        l|L) printf '  %sstreaming — Ctrl-C to stop%s\n' "$DIM" "$OFF"; run "$DBG" logs ;;
        v|V) printf '  logger (e.g. com.example.debugdemo, ROOT): '; IFS= read -r lg
             printf '  level: 1 TRACE · 2 DEBUG · 3 INFO · 4 WARN · 5 ERROR · 6 OFF > '
             local lvk lv=""; read -rn1 lvk; printf '\n'
             case "$lvk" in 1) lv=TRACE;; 2) lv=DEBUG;; 3) lv=INFO;; 4) lv=WARN;; 5) lv=ERROR;; 6) lv=OFF;; esac
             [[ -n "$lg" && -n "$lv" ]] && run "$DBG" log-level "$lg" "$lv" ;;
        '?') show_help ;;
        c|C) run "$DBG" doctor ;;
        a|A) run "$DBG" analyze ;;
        d|D) run "$DBG" dumps ;;
        i|I) run "$DBG" install-jattach ${POD_PIN:+"$POD_PIN"} ;;   # utility; listed in [?] help
        p|P) run "$DBG" push-local ${POD_PIN:+"$POD_PIN"} ;;        # utility; listed in [?] help
        g|G) retarget; SKIP_PAUSE=1 ;;
        M)   choose_mode; SKIP_PAUSE=1 ;;
        q|Q) confirm "quit jdebug?" && bye; return 1 ;;
        *) return 1 ;;   # unknown key or bare Enter: just show the menu again
    esac
    return 0   # a FAILED action must still pause so its error stays readable
}
dispatch_local() {
    # Not ready → only setup/help keys work; everything else explains why.
    if [[ -z "$LOCAL_OK" ]]; then
        case "$1" in
            ""|s|S) local_settings; SKIP_PAUSE=1 ;;
            i|I)    run stage_jattach_local ;;
            '?')    show_help ;;
            M)      choose_mode; SKIP_PAUSE=1 ;;
            q|Q)    confirm "quit jdebug?" && bye; return 1 ;;
            *)      printf '  %sset up a route to the JVM first — press s (actuator URL) or i (stage jattach).%s\n' "$YL" "$OFF"; return 1 ;;
        esac
        return 0
    fi
    case "$1" in
        w|W) wizard; SKIP_PAUSE=1 ;;
        h)   run sh "$LOCAL" health ;;
        e|E) run sh "$LOCAL" metrics ;;
        m)   jattach_fallback_check; run sh "$LOCAL" memory ;;
        t|T) jattach_fallback_check; run sh "$LOCAL" threads ;;
        j|J) [[ -x "$JATTACH_BIN" ]] || { confirm "jcmd REQUIRES jattach and it is not staged — download now (~80 KB)?" && stage_jattach_local; }
             ask_jcmd; [[ -n "$JCMD_PICK" ]] && run sh "$LOCAL" jcmd "$JCMD_PICK" ;;
        H)   confirm_disruptive H "heap dump pauses the app while it runs" || return 1
             jattach_fallback_check; run sh "$LOCAL" heap --confirm ;;
        x|X) jattach_fallback_check
             if confirm "include a heap dump in the bundle? (PAUSES the JVM)"; then run sh "$LOCAL" snapshot --heap; else run sh "$LOCAL" snapshot; fi ;;
        '?') show_help ;;
        a|A) run "$SCRIPTS_ROOT/observe/analyze.sh" "${OUT_DIR:-/tmp}" ;;
        d|D) run sh "$LOCAL" dumps ;;
        i|I) run stage_jattach_local ;;
        s|S) local_settings; SKIP_PAUSE=1 ;;
        M)   choose_mode; SKIP_PAUSE=1 ;;
        q|Q) confirm "quit jdebug?" && bye; return 1 ;;
        *) return 1 ;;   # unknown key or bare Enter: just show the menu again
    esac
    return 0   # a FAILED action must still pause so its error stays readable
}

# --- main loop --------------------------------------------------------------
# `tui.sh wizard` (via `jdebug wizard`) jumps straight into the guided flow.
if [[ "${1:-}" == wizard ]]; then MODE=1; wizard; bye; fi
[[ -n "$MODE" ]] || choose_mode
# A remembered pod pin may have died since last session — check once, fall
# back to auto with a visible notice rather than failing every capture.
if [[ "$MODE" == 1 && -n "$POD_PIN" ]]; then
    if ! kubectl -n "$NAMESPACE" get pod "$POD_PIN" -o name >/dev/null 2>&1; then
        SAVED_POD_GONE="$POD_PIN"; POD_PIN=""
    fi
fi
while true; do
    SKIP_PAUSE=""
    if [[ "$MODE" == 1 ]]; then menu_remote; read -rn1 choice || bye; printf '\n'; dispatch_remote "$choice" || continue
    else menu_local; read -rn1 choice || bye; printf '\n'; dispatch_local "$choice" || continue; fi
    [[ -n "$SKIP_PAUSE" || -z "$choice" ]] || pause
done
