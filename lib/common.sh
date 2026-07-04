#!/usr/bin/env bash
# jdebug — shared helpers. PORTABLE: no assumptions about any particular app,
# namespace, or kubeconfig. Targets whatever `kubectl`/$KUBECONFIG is active.
# Override the target with -n/--namespace, -l/--selector, --container, or the
# JDEBUG_NAMESPACE / JDEBUG_SELECTOR / JDEBUG_CONTAINER environment variables.

set -euo pipefail

: "${NAMESPACE:=${JDEBUG_NAMESPACE:-default}}"
: "${SELECTOR:=${JDEBUG_SELECTOR:-}}"          # empty = any pod in the namespace
: "${APP_CONTAINER:=${JDEBUG_CONTAINER:-app}}" # common Spring Boot container name
: "${JDK_DEBUG_IMAGE:=${JDEBUG_JDK_IMAGE:-eclipse-temurin:21-jdk-alpine}}"

# Cache for the downloaded jattach binary — a standard per-user location so the
# kit works the same whether it's run from a repo checkout or installed on PATH.
: "${JDEBUG_CACHE_DIR:=${XDG_CACHE_HOME:-$HOME/.cache}/jdebug}"

# Where operator-side captures (dumps, snapshots) land — under the kit itself,
# NOT the caller's CWD, so they're always in one findable place and covered by
# the kit's .gitignore. Override per run with $OUT_DIR, or move the root with
# $JDEBUG_DUMPS.
JDEBUG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
: "${JDEBUG_DUMPS:=$JDEBUG_ROOT/dumps}"

# NOTE: no automatic KUBECONFIG rewriting. jdebug uses the ambient kubectl
# context. Point it at a cluster the normal way (KUBECONFIG=... or kubectl config
# use-context), exactly like kubectl itself.

err()  { printf 'error: %s\n' "$*" >&2; }
info() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2; }

require_cmd() {
    for cmd in "$@"; do
        command -v "$cmd" >/dev/null 2>&1 || { err "missing required command: $cmd"; exit 127; }
    done
}

# usage — print the calling script's header comment block (line 2 to the first
# blank line) as its --help text. Every tool keeps its docs in the header.
usage() {
    sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
}

# announce_target — print the resolved target to stderr so every command makes
# clear which pod it will hit. Once per process tree (the guard is exported, so a
# tool that shells out to another jdebug tool doesn't repeat it). Silence with
# JDEBUG_QUIET=1; respects NO_COLOR.
announce_target() {
    [[ -n "${JDEBUG_TARGET_ANNOUNCED:-}" || -n "${JDEBUG_QUIET:-}" ]] && return 0
    export JDEBUG_TARGET_ANNOUNCED=1
    local d="" o=""; [[ -t 2 && -z "${NO_COLOR:-}" ]] && { d=$'\033[2m'; o=$'\033[0m'; }
    printf '%sjdebug → namespace=%s  selector=%s  container=%s%s%s\n' \
        "$d" "$NAMESPACE" "${SELECTOR:-<any pod>}" "$APP_CONTAINER" \
        "${KUBECONFIG:+  kubeconfig=$KUBECONFIG}" "$o" >&2
}

# parse_common_args <args...> — consumes -n/--namespace, -l/--selector,
# --container, and -h/--help. Sets NAMESPACE/SELECTOR/APP_CONTAINER; leaves the
# rest in REMAINING_ARGS. Announces the resolved target once it has parsed them.
parse_common_args() {
    REMAINING_ARGS=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -n|--namespace) NAMESPACE="$2"; shift 2 ;;
            -l|--selector)  SELECTOR="$2";  shift 2 ;;
            --container)    APP_CONTAINER="$2"; shift 2 ;;
            --actuator-base) ACTUATOR_BASE="$2"; export ACTUATOR_BASE; shift 2 ;;
            -h|--help)      usage; exit 0 ;;
            --) shift; REMAINING_ARGS+=("$@"); break ;;
            *)  REMAINING_ARGS+=("$1"); shift ;;
        esac
    done
    announce_target
}

# show_cmd <words...> — echo the exact command a tool is about to run, so every
# capture doubles as a copy-pasteable cookbook.
show_cmd() { printf '  $ %s\n' "$*" >&2; }

# pod_fetch <url> [accept] — emit an sh snippet that GETs <url> from INSIDE the
# pod with whatever HTTP client it has: curl, else busybox wget (stock
# JRE-alpine ships wget, not curl). Run via `kubectl exec -- sh -c "$(pod_fetch ...)"`.
pod_fetch() {
    local url="$1" accept="${2:-}"
    if [[ -n "$accept" ]]; then
        echo "if command -v curl >/dev/null 2>&1; then curl -fsS -H 'Accept: $accept' '$url'; else wget -qO- --header='Accept: $accept' '$url' 2>/dev/null || wget -qO- '$url'; fi"
    else
        echo "if command -v curl >/dev/null 2>&1; then curl -fsS '$url'; else wget -qO- '$url'; fi"
    fi
}

# pod_post_json <url> <json> — same idea for a JSON POST (busybox wget speaks
# --post-data). The JSON must not contain single quotes.
pod_post_json() {
    echo "if command -v curl >/dev/null 2>&1; then curl -fsS -X POST -H 'Content-Type: application/json' -d '$2' '$1'; else wget -qO- --header='Content-Type: application/json' --post-data='$2' '$1'; fi"
}

# resolve_pods — pod names matching selector in namespace (empty selector = all).
resolve_pods() {
    if [[ -n "$SELECTOR" ]]; then
        kubectl -n "$NAMESPACE" get pods -l "$SELECTOR" \
            -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
    else
        kubectl -n "$NAMESPACE" get pods \
            -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
    fi
}

# resolve_one_pod [explicit-name] — a single pod (explicit, or first match).
# When several pods match and none was named, say so: capturing from a healthy
# replica while a sick one sits next to it is a classic wrong-diagnosis trap.
resolve_one_pod() {
    local explicit="${1:-}"
    if [[ -n "$explicit" ]]; then echo "$explicit"; return; fi
    local pods; pods="$(resolve_pods)"
    if [[ -z "$pods" ]]; then
        err "no pod matched namespace=$NAMESPACE selector='${SELECTOR:-<any>}' — pass -n/-l"
        exit 2
    fi
    local pod n; pod="$(printf '%s\n' "$pods" | head -n1)"
    n="$(printf '%s\n' "$pods" | grep -c .)"
    if [[ "$n" -gt 1 ]]; then
        info "$n pods match — using $pod. If you meant another (e.g. the restarting one), add its name:"
        printf '%s\n' "$pods" | sed 's/^/           /' >&2
    fi
    echo "$pod"
}

# ensure_dir <dir> — mkdir -p with friendly error.
ensure_dir() {
    mkdir -p "$1" || { err "cannot create directory: $1"; exit 1; }
}

# check_cluster — is the kube context actually answering? If not, translate the
# usual kubectl failure modes into plain language and a likely fix, instead of
# letting every later kubectl call spew TLS stack traces and memcache spam.
# (/version is readable by anyone, so this works with any RBAC.)
check_cluster() {
    local out ctx
    out="$(kubectl get --raw=/version --request-timeout=4s 2>&1 >/dev/null)" && return 0
    ctx="$(kubectl config current-context 2>/dev/null || true)"
    err "can't reach the Kubernetes cluster  (context: ${ctx:-<none set>})"
    case "$out" in
        *x509*|*certificate*)
            err "  why: the cluster's TLS certificate isn't trusted. This almost always means the"
            err "       cluster was recreated/restarted and your saved kubeconfig credentials went"
            err "       stale — very common with Rancher Desktop, k3s, minikube, and kind."
            err "  fix: restart the local cluster app (it rewrites the kubeconfig), or switch to a"
            err "       working context:  kubectl config use-context <name>"
            err "       (in the jdebug menu, press t — it lists your contexts and switches for you)" ;;
        *"connection refused"*|*"i/o timeout"*|*"no such host"*|*"Unable to connect"*|*"context deadline"*)
            err "  why: nothing answered at the cluster's address — it's off, asleep, or unreachable."
            err "  fix: start the cluster (Rancher/Docker Desktop, VPN for remote clusters), or"
            err "       switch to a context that is up (menu: t · shell: kubectl config use-context)" ;;
        *"current-context"*|*"no configuration"*|*"Missing or incomplete"*)
            err "  why: kubectl has no context selected, so it doesn't know which cluster to talk to."
            err "  fix: pick one:  kubectl config use-context <name>   (list: kubectl config get-contexts)"
            err "       or point KUBECONFIG at the right file." ;;
        *)
            err "  kubectl's own explanation (first lines):"
            printf '%s\n' "$out" | grep -v '^E[0-9]' | head -3 | sed 's/^/    /' >&2 ;;
    esac
    return 1
}
