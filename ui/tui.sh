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
POD_PIN=""                              # mode 1: '' = auto (first match); 't' can pin one pod
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

# --- colors (respect NO_COLOR / non-tty) -----------------------------------
if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    B=$'\033[1m'; DIM=$'\033[2m'; CY=$'\033[36m'; GN=$'\033[32m'; YL=$'\033[33m'; RD=$'\033[31m'; OFF=$'\033[0m'
else B=""; DIM=""; CY=""; GN=""; YL=""; RD=""; OFF=""; fi

box() { printf '%s╔══════════════════════════════════════════════════════════════╗%s\n' "$B" "$OFF"
        printf '%s║  %-60s║%s\n' "$B" "$1" "$OFF"
        printf '%s╚══════════════════════════════════════════════════════════════╝%s\n' "$B" "$OFF"; }
hr() { printf '%s────────────────────────────────────────────────────────────────%s\n' "$DIM" "$OFF"; }
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
    maybe_clear
    box "jdebug - where is the JVM you want to debug?"
    printf '\n'
    printf '   %s1%s  %sRemote%s      operator machine → %skubectl exec%s into a pod  %s(needs kubectl + a context)%s\n' "$GN" "$OFF" "$B" "$OFF" "$CY" "$OFF" "$DIM" "$OFF"
    printf '   %s2%s  %sIn-pod%s      a shell INSIDE the pod, no kubectl        %s(JRE-only image is fine)%s\n' "$GN" "$OFF" "$B" "$OFF" "$DIM" "$OFF"
    printf '   %s3%s  %sBare metal%s  a JVM on THIS host, no Kubernetes at all\n' "$GN" "$OFF" "$B" "$OFF"
    printf '\n  %sNot sure? If you normally type kubectl to reach the app, pick 1.%s\n' "$B" "$OFF"
    printf '  %sModes 2 & 3 talk to localhost actuator + a local jattach + /proc (via jdebug-local).%s\n' "$DIM" "$OFF"
    printf '  %sNote: this menu needs bash. A stock JRE/busybox pod has none — for those, run the%s\n' "$YL" "$OFF"
    printf '  %ssingle-file  jdebug-local  CLI in the pod instead:  sh /tmp/jdebug-local help%s\n' "$YL" "$OFF"
    printf '\n  %s> %s' "$B" "$OFF"; local m; read -rn1 m || bye; printf '\n'
    case "$m" in 1|2|3) MODE="$m" ;; q|Q) bye ;; *) MODE=1 ;; esac
}
mode_label() { case "$MODE" in 1) echo "remote · kubectl → pod";; 2) echo "in-pod · localhost";; 3) echo "bare metal · localhost";; esac; }

# --- headers ----------------------------------------------------------------
header_remote() {
    maybe_clear
    box "JVM debug kit - remote (kubectl)"
    local ctx; ctx="$(kubectl config current-context 2>/dev/null)"
    cluster_probe
    local reach
    if [[ -n "$CLUSTER_OK" ]]; then reach="${GN}✓ cluster reachable${OFF}"
    else reach="${RD}✗ can't connect — any command will explain why + the fix (or press t to switch)${OFF}"; fi
    printf '  %smode%s      %s  %s(m to switch)%s\n' "$B" "$OFF" "$(mode_label)" "$DIM" "$OFF"
    printf '  %scontext%s   %s%s%s  %s\n' "$B" "$OFF" "$GN" "${ctx:-<none — is KUBECONFIG set?>}" "$OFF" "$reach"
    printf '  %starget%s    namespace  %s%s%s\n' "$B" "$OFF" "$GN" "$NAMESPACE" "$OFF"
    printf '            selector   %s%s%s\n' "$GN" "${SELECTOR:-<any pod — press t to narrow to your app>}" "$OFF"
    printf '            container  %s%s%s\n' "$GN" "$APP_CONTAINER" "$OFF"
    printf '            pod        %s%s%s\n' "$GN" "${POD_PIN:-<auto: first match — pick one under t>}" "$OFF"
    printf '  %sactuator%s  %s%s%s\n' "$B" "$OFF" "$GN" "$ACTUATOR_BASE" "$OFF"
    printf '            %s↳ press %st%s%s to change target/actuator · %sm%s%s to switch mode%s\n' "$DIM" "$OFF$GN" "$OFF" "$DIM" "$GN" "$OFF" "$DIM" "$OFF"
    printf '  %skubeconfig%s %s\n' "$B" "$OFF" "${KUBECONFIG:-"(default context)"}"
    printf '  %sExamples:  jdebug health · jdebug -n prod -l app=web memory · jdebug jcmd "GC.heap_info"%s\n' "$DIM" "$OFF"
    hr
}
header_local() {
    maybe_clear
    box "JVM debug kit - local (no kubectl)"
    local jat="not staged"; [[ -x "$JATTACH_BIN" ]] && jat="ok"
    printf '  %smode%s      %s  %s(m to switch)%s\n' "$B" "$OFF" "$(mode_label)" "$DIM" "$OFF"
    printf '  %sactuator%s  %s%s%s\n' "$B" "$OFF" "$GN" "$ACTUATOR_BASE" "$OFF"
    printf '  %sjattach%s   %s%s%s %s(%s)%s\n' "$B" "$OFF" "$GN" "$JATTACH_BIN" "$OFF" "$DIM" "$jat" "$OFF"
    printf '            %s↳ press %ss%s%s for settings (actuator / jattach / pid) · %sm%s%s to switch mode%s\n' "$DIM" "$OFF$GN" "$OFF" "$DIM" "$GN" "$OFF" "$DIM" "$OFF"
    printf '  %sReaches this machine'\''s JVM directly (localhost + /proc). No pod/kubectl needed.%s\n' "$DIM" "$OFF"
    hr
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
        if [[ "$free" == 1 ]]; then printf '  %s [%s]: ' "$title" "$current"; IFS= read -r CHOICE; fi
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
    1. ${GN}status${OFF} — is anything restarting or stuck? read the hints under the output
    2. ${GN}health${OFF} — is a dependency (db/queue) DOWN? chase that system first
    3. ${GN}w${OFF} wizard — tell it the symptom; it runs the right captures and says what's next
    4. ${GN}d${OFF} — see what you captured and what tool opens each file

  ${B}THE SAFETY RULES${OFF}
    · everything is read-only except: ${RD}heap dumps pause the app${OFF}, log-level adds log volume
    · anything risky asks you first — answering n is always safe
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
        printf '   %so%s  container   %s%s%s\n' "$GN" "$OFF" "$GN" "$APP_CONTAINER" "$OFF"
        printf '   %sp%s  pod         %s%s%s\n' "$GN" "$OFF" "$GN" "${POD_PIN:-<auto: first match>}" "$OFF"
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
                # dropdown built from the `app` labels actually on pods here
                choose_from "Selector — apps found in $NAMESPACE ('t' to type any label, '-' for any pod)" "${SELECTOR:-<any pod>}" 1 \
                    "$(kubectl -n "$NAMESPACE" get pods -o jsonpath='{range .items[*]}{.metadata.labels.app}{"\n"}{end}' 2>/dev/null | grep . | sort -u | sed 's/^/app=/')"
                if [[ "$CHOICE" == "-" ]]; then SELECTOR=""; POD_PIN=""
                elif [[ -n "$CHOICE" ]]; then SELECTOR="$CHOICE"; POD_PIN=""; fi ;;
            o|O)
                local firstpod conts=""
                firstpod="$(resolve_pods 2>/dev/null | head -n1 || true)"
                [[ -n "$firstpod" ]] && conts="$(kubectl -n "$NAMESPACE" get pod "$firstpod" -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' 2>/dev/null)"
                choose_from "Container${firstpod:+ (in $firstpod)}" "$APP_CONTAINER" 1 "$conts"
                [[ -n "$CHOICE" ]] && APP_CONTAINER="$CHOICE" ;;
            p|P) pick_pod ;;
            a|A) printf '  actuator base [%s]: ' "$ACTUATOR_BASE"; IFS= read -r v; [[ -n "$v" ]] && ACTUATOR_BASE="$v" ;;
            b|B|"") break ;;
            *) : ;;
        esac
        export NAMESPACE SELECTOR APP_CONTAINER ACTUATOR_BASE
    done
    export NAMESPACE SELECTOR APP_CONTAINER ACTUATOR_BASE
    CLUSTER_TS=-999   # target changed — re-probe reachability for the header
}

# pick_pod — when several pods match, let the user pin one instead of silently
# taking the first. Status + restart counts are shown because the restarting
# pod is usually the one worth debugging.
pick_pod() {
    POD_PIN=""
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
        printf '  pinned: every capture now targets %s%s%s (press t to change)\n' "$GN" "$POD_PIN" "$OFF"
    else
        printf '  auto — the first matching pod is used (you will be told which)\n'
    fi
}
local_settings() {
    printf '  actuator base URL [%s]: ' "$ACTUATOR_BASE"; local v; read -r v; [[ -n "$v" ]] && ACTUATOR_BASE="$v"
    printf '  jattach binary    [%s]: ' "$JATTACH_BIN";   read -r v; [[ -n "$v" ]] && JATTACH_BIN="$v"
    printf '  JVM pid           [%s]: ' "${JVM_PID:-auto}"; read -r v; [[ -n "$v" ]] && export JVM_PID="$v"
    export ACTUATOR_BASE JATTACH_BIN
}

# --- jattach staging (local modes) -------------------------------------------
# jdebug-local auto-falls back to jattach when the actuator is unreachable, but
# only if the binary already sits at $JATTACH_BIN — being a one-file in-pod
# tool, it never downloads anything itself. THIS process, though, runs where
# there usually IS egress (especially bare metal), so the menu can fetch it.
stage_jattach_local() {
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
    wiz_say "Next → upload the threads .txt to https://fastthread.io — it flags deadlocks"
    wiz_say "       and identical stacks automatically. (d in the menu lists your captures)"
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
    wiz_say "Next → in the bundle: memory-report/memory.txt first, threads.txt → fastthread.io,"
    wiz_say "       heap.hprof → Eclipse MAT. (d in the menu lists your captures)"
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
menu_remote() {
    header_remote
    cat <<EOF
  ${B}${CY}▶ w${OFF}  ${B}GUIDED DIAGNOSIS${OFF} ${DIM}— describe the symptom, it runs the right captures.${OFF} ${B}Start here.${OFF}

  ${B}LOOK AROUND${OFF}  ${GN}all safe · read-only${OFF}
   ${GN}1${OFF}  status      are the pods up? restarts, recent events
   ${GN}2${OFF}  health      the app's own health checks (db, queue, disk…)
   ${GN}3${OFF}  top         CPU + memory per pod, autoscaler state
   ${GN}4${OFF}  memory      memory anatomy — container total vs JVM heap/non-heap

  ${B}CAPTURE EVIDENCE${OFF}  ${DIM}files land in dumps/ — press${OFF} ${GN}d${OFF} ${DIM}to browse them${OFF}
   ${GN}5${OFF}  threads     what is every thread doing right now?      ${GN}safe · instant${OFF}
   ${GN}6${OFF}  heap        every object in memory, for leak hunting   ${RD}⚠ pauses the app${OFF}
   ${GN}7${OFF}  jcmd        advanced JVM commands (GC, JFR, native)    ${YL}mostly safe${OFF}
   ${GN}0${OFF}  snapshot    grab EVERYTHING in one offline bundle      ${GN}safe${OFF}${DIM} · heap optional${OFF}

  ${B}LOGS${OFF}
   ${GN}8${OFF}  logs        live log stream from every replica         ${GN}safe${OFF}
   ${GN}9${OFF}  log-level   turn logging up/down without a restart     ${YL}adds log volume${OFF}

  ${B}MORE${OFF}  ${GN}h${OFF} help/glossary · ${GN}c${OFF} check setup · ${GN}d${OFF} view captures · ${GN}i${OFF} stage jattach · ${GN}p${OFF} push in-pod tool · ${GN}t${OFF} target · ${GN}m${OFF} mode · ${GN}q${OFF} quit
  ${DIM}keys act instantly — no Enter needed${OFF}
EOF
    printf '\n  %s> %s' "$B" "$OFF"
}
menu_local() {
    header_local
    cat <<EOF
  ${B}${CY}▶ w${OFF}  ${B}GUIDED DIAGNOSIS${OFF} ${DIM}— describe the symptom, it runs the right captures.${OFF} ${B}Start here.${OFF}

  ${B}LOOK AROUND${OFF}  ${GN}all safe · read-only${OFF}
   ${GN}1${OFF}  health      the app's own health checks (db, queue, disk…)
   ${GN}2${OFF}  metrics     browse JVM/process metrics, or print one value
   ${GN}3${OFF}  memory      memory anatomy — container total vs JVM heap/non-heap

  ${B}CAPTURE EVIDENCE${OFF}  ${DIM}files land in ${OUT_DIR:-/tmp} — press${OFF} ${GN}d${OFF} ${DIM}to browse them${OFF}
   ${GN}4${OFF}  threads     what is every thread doing right now?      ${GN}safe · instant${OFF}
   ${GN}5${OFF}  heap        every object in memory, for leak hunting   ${RD}⚠ pauses the app${OFF}
   ${GN}6${OFF}  jcmd        advanced JVM commands (GC, JFR, native)    ${YL}needs jattach${OFF}
   ${GN}7${OFF}  snapshot    grab EVERYTHING in one offline bundle      ${GN}safe${OFF}${DIM} · heap optional${OFF}

  ${B}MORE${OFF}  ${GN}h${OFF} help/glossary · ${GN}d${OFF} view captures · ${GN}i${OFF} stage jattach · ${GN}s${OFF} settings · ${GN}m${OFF} mode · ${GN}q${OFF} quit
  ${DIM}keys act instantly — no Enter needed${OFF}
EOF
    printf '\n  %s> %s' "$B" "$OFF"
}

dispatch_remote() {
    case "$1" in
        w|W) wizard ;;
        1)  run "$DBG" status ;;
        2)  run "$DBG" health ${POD_PIN:+"$POD_PIN"} ;;
        3)  run "$DBG" top ;;
        4)  run "$DBG" memory ${POD_PIN:+"$POD_PIN"} ;;
        5)  ask_via; run "$DBG" threads $VIA_FLAG ${POD_PIN:+"$POD_PIN"} ;;
        6)  ask_via; confirm "heap dump PAUSES the JVM (destructive in production) — proceed?" && run "$DBG" heap $VIA_FLAG --confirm ${POD_PIN:+"$POD_PIN"} ;;
        7)  ask_jcmd; [[ -n "$JCMD_PICK" ]] && run "$DBG" jcmd "$JCMD_PICK" ${POD_PIN:+"$POD_PIN"} ;;
        8)  printf '  %sstreaming — Ctrl-C to stop%s\n' "$DIM" "$OFF"; run "$DBG" logs ;;
        9)  printf '  logger (e.g. com.example.debugdemo, ROOT): '; IFS= read -r lg
            printf '  level: 1 TRACE · 2 DEBUG · 3 INFO · 4 WARN · 5 ERROR · 6 OFF > '
            local lvk lv=""; read -rn1 lvk; printf '\n'
            case "$lvk" in 1) lv=TRACE;; 2) lv=DEBUG;; 3) lv=INFO;; 4) lv=WARN;; 5) lv=ERROR;; 6) lv=OFF;; esac
            [[ -n "$lg" && -n "$lv" ]] && run "$DBG" log-level "$lg" "$lv" ;;
        0)  if confirm "include a heap dump in the bundle? (PAUSES the JVM)"; then run "$DBG" snapshot --heap --confirm ${POD_PIN:+"$POD_PIN"}; else run "$DBG" snapshot ${POD_PIN:+"$POD_PIN"}; fi ;;
        h|H) show_help ;;
        c|C) run "$DBG" doctor ;;
        d|D) run "$DBG" dumps ;;
        i|I) run "$DBG" install-jattach ${POD_PIN:+"$POD_PIN"} ;;
        p|P) run "$DBG" push-local ${POD_PIN:+"$POD_PIN"} ;;
        t|T) retarget ;;
        m|M) choose_mode ;;
        q|Q) confirm "quit jdebug?" && bye; return 1 ;;
        *) return 1 ;;   # unknown key or bare Enter: just show the menu again
    esac
    return 0   # a FAILED action must still pause so its error stays readable
}
dispatch_local() {
    case "$1" in
        w|W) wizard ;;
        1)  run sh "$LOCAL" health ;;
        2)  run sh "$LOCAL" metrics ;;
        3)  jattach_fallback_check; run sh "$LOCAL" memory ;;
        4)  jattach_fallback_check; run sh "$LOCAL" threads ;;
        5)  jattach_fallback_check
            confirm "heap dump PAUSES the JVM (destructive in production) — proceed?" && run sh "$LOCAL" heap --confirm ;;
        6)  [[ -x "$JATTACH_BIN" ]] || { confirm "jcmd REQUIRES jattach and it is not staged — download now (~80 KB)?" && stage_jattach_local; }
            ask_jcmd; [[ -n "$JCMD_PICK" ]] && run sh "$LOCAL" jcmd "$JCMD_PICK" ;;
        7)  jattach_fallback_check
            if confirm "include a heap dump in the bundle? (PAUSES the JVM)"; then run sh "$LOCAL" snapshot --heap; else run sh "$LOCAL" snapshot; fi ;;
        h|H) show_help ;;
        d|D) run sh "$LOCAL" dumps ;;
        i|I) run stage_jattach_local ;;
        s|S) local_settings ;;
        m|M) choose_mode ;;
        q|Q) confirm "quit jdebug?" && bye; return 1 ;;
        *) return 1 ;;   # unknown key or bare Enter: just show the menu again
    esac
    return 0   # a FAILED action must still pause so its error stays readable
}

# --- main loop --------------------------------------------------------------
# `tui.sh wizard` (via `jdebug wizard`) jumps straight into the guided flow.
if [[ "${1:-}" == wizard ]]; then MODE=1; wizard; bye; fi
[[ -n "$MODE" ]] || choose_mode
while true; do
    if [[ "$MODE" == 1 ]]; then menu_remote; read -rn1 choice || bye; printf '\n'; dispatch_remote "$choice" || continue
    else menu_local; read -rn1 choice || bye; printf '\n'; dispatch_local "$choice" || continue; fi
    [[ "$choice" =~ ^[tTmMsSwW]$ ]] || pause
done
