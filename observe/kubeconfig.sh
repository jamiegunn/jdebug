#!/usr/bin/env bash
#
# kubeconfig.sh — diagnose why jdebug can't reach the cluster, then OFFER to fix
# it by importing a working kubeconfig. Two things a stale connection needs and
# that plain `check_cluster` only describes: a fresh kubeconfig, and it applied
# somewhere. This does both.
#
# Reasoning first: it runs the same check_cluster diagnosis, so you see the WHY
# (expired token / bad cert / no context) before it changes anything.
#
# Import source (your choice):
#   • a FILE you point it at (downloaded, or from a colleague)
#   • a provider RE-FETCH (aws eks update-kubeconfig / gcloud … get-credentials /
#     az aks get-credentials), detected from your current context
#
# Scope (your choice — importing for everything can clobber existing contexts):
#   • session — jdebug-ONLY: kept in ~/.config/jdebug; your ~/.kube/config and
#               every other kubectl tool are untouched. Safe default. Reversible.
#   • global  — merged into ~/.kube/config for ALL tools (a timestamped backup is
#               written first, and existing contexts are preserved by merging).
#
# It always TESTS that the new kubeconfig actually connects before committing it.
#
# Usage:
#   jdebug kubeconfig                      diagnose, then walk through the fix
#   jdebug kubeconfig --status             what jdebug is using right now
#   jdebug kubeconfig --file <path> [--scope session|global] [--yes]
#   jdebug kubeconfig --refetch [--scope session|global] [--yes]
#   jdebug kubeconfig --forget             stop using the jdebug-only kubeconfig

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

MODE=""          # "" (interactive) | file | refetch | status | forget
FILE=""
SCOPE=""         # session | global (empty = ask)
ASSUME_YES=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --file) MODE="file"; FILE="${2:-}"; shift 2 ;;
        --file=*) MODE="file"; FILE="${1#*=}"; shift ;;
        --refetch|--re-fetch) MODE="refetch"; shift ;;
        --status) MODE="status"; shift ;;
        --forget) MODE="forget"; shift ;;
        --scope) SCOPE="${2:-}"; shift 2 ;;
        --scope=*) SCOPE="${1#*=}"; shift ;;
        --session) SCOPE=session; shift ;;
        --global|--all) SCOPE=global; shift ;;
        -y|--yes) ASSUME_YES=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) err "unknown option: $1"; usage; exit 64 ;;
    esac
done

if [[ -n "$SCOPE" && "$SCOPE" != session && "$SCOPE" != global ]]; then
    err "--scope must be 'session' (jdebug-only) or 'global' (~/.kube/config)"; exit 64
fi

say()  { printf '%s\n' "$*"; }
hr()   { printf '%s\n' "────────────────────────────────────────────────────────"; }

# current_context — best-effort, never fatal.
current_context() { kubectl config current-context 2>/dev/null || true; }

# show_status — what jdebug is pointed at right now, and does it answer.
show_status() {
    local ctx; ctx="$(current_context)"
    say "jdebug kubeconfig — current state"
    hr
    if [[ "${JDEBUG_KUBECONFIG_SCOPED:-}" == 1 ]]; then
        say "  source     : jdebug-only kubeconfig  ($JDEBUG_SCOPED_KUBECONFIG)"
        say "               (your ~/.kube/config and other tools are untouched)"
    elif [[ -n "${KUBECONFIG:-}" ]]; then
        say "  source     : KUBECONFIG from your environment ($KUBECONFIG)"
    else
        say "  source     : ambient default ($(kubeconfig_global_file))"
    fi
    say "  context    : ${ctx:-<none selected>}"
    if check_cluster >/dev/null 2>&1; then
        say "  cluster    : ✓ reachable — credentials accepted"
    else
        say "  cluster    : ✗ not answering (see the diagnosis below)"
    fi
    hr
}

# choose_scope — resolve $SCOPE, prompting when interactive and unset. The prompt
# is where the clobber trade-off is spelled out.
choose_scope() {
    [[ -n "$SCOPE" ]] && return 0
    if [[ ! -t 0 || "$ASSUME_YES" == 1 ]]; then
        SCOPE=session   # safe, non-clobbering default for non-interactive runs
        return 0
    fi
    say ""
    say "apply this kubeconfig to…"
    say "  1) THIS jdebug only   — kept in ~/.config/jdebug; ~/.kube/config and other"
    say "                          tools stay exactly as they are. Safe. Reversible"
    say "                          (jdebug kubeconfig --forget). [recommended]"
    say "  2) ALL sessions       — merged into ~/.kube/config for every kubectl/tool."
    say "                          A timestamped backup is written first, and existing"
    say "                          contexts are preserved — but this DOES change your"
    say "                          global config."
    local ans
    read -r -p "  choice [1/2, default 1]: " ans || ans=1
    case "${ans:-1}" in
        2) SCOPE=global ;;
        *) SCOPE=session ;;
    esac
}

# confirm <prompt> — y/N gate, auto-yes under --yes / non-interactive.
confirm() {
    [[ "$ASSUME_YES" == 1 || ! -t 0 ]] && return 0
    local ans; read -r -p "$1 [y/N]: " ans || ans=n
    [[ "$ans" == y || "$ans" == Y || "$ans" == yes ]]
}

# apply_candidate <file> — validate → test-connect → choose scope → apply. Shared
# by the file and re-fetch paths. Returns non-zero if nothing was applied.
apply_candidate() {
    local cand="$1"
    if [[ ! -r "$cand" ]]; then
        err "can't read '$cand' — check the path"; return 1
    fi
    if ! kubeconfig_looks_valid "$cand"; then
        err "'$cand' doesn't look like a kubeconfig (no clusters/apiVersion) — not importing it."
        return 1
    fi
    say ""
    say "testing whether that kubeconfig actually reaches a cluster…"
    if kubeconfig_connects "$cand"; then
        say "  ✓ it connects — the cluster answered and accepted the credentials."
    else
        local rc=$?
        if [[ $rc == 2 ]]; then
            say "  · kubectl isn't installed here, so I can't test the connection — importing anyway."
        else
            say "  ✗ it still does NOT connect (same /version probe check_cluster uses)."
            say "    importing a kubeconfig that also fails won't fix anything — likely the token"
            say "    inside it is expired too. Try a provider re-fetch (jdebug kubeconfig --refetch),"
            say "    or re-authenticate (aws sso login / gcloud auth login / az login / oc login)."
            confirm "  import it anyway?" || { say "  left everything unchanged."; return 1; }
        fi
    fi

    choose_scope
    if [[ "$SCOPE" == global ]]; then
        local dest; dest="$(kubeconfig_global_file)"
        say ""
        say "This MERGES '$cand' into $dest (all tools see it)."
        confirm "proceed?" || { say "left everything unchanged."; return 1; }
        local backup
        backup="$(kubeconfig_apply_global "$cand")" || { err "global import failed — your config was NOT changed."; return 1; }
        say "  ✓ merged into $dest"
        [[ -n "$backup" ]] && say "  ↩ previous config backed up: $backup  (restore: cp '$backup' '$dest')"
        # a leftover jdebug-only file would SHADOW the freshly-fixed global one.
        if [[ -r "$JDEBUG_SCOPED_KUBECONFIG" ]]; then
            say ""
            say "note: a jdebug-only kubeconfig is still in place and would override this global"
            say "      one for jdebug. Clearing it so jdebug uses your fixed ~/.kube/config."
            if confirm "  clear the jdebug-only kubeconfig?"; then
                kubeconfig_forget && say "  ✓ cleared." || err "  couldn't remove $JDEBUG_SCOPED_KUBECONFIG"
            else
                say "  kept it — jdebug will keep using the jdebug-only kubeconfig until you --forget it."
            fi
        fi
    else
        local landed
        landed="$(kubeconfig_apply_session "$cand")" || { err "session import failed — nothing changed."; return 1; }
        say ""
        say "  ✓ imported for THIS jdebug only → $landed"
        say "    your ~/.kube/config and every other kubectl tool are untouched."
        say "    undo any time:  jdebug kubeconfig --forget"
    fi
    say ""
    say "done. Re-run what you were doing (e.g. jdebug status) — it should connect now."
    return 0
}

# do_refetch — detect the provider, show the exact command, let the user complete
# it, run it into a scratch file, then apply_candidate on the result.
do_refetch() {
    local prov; prov="$(kubeconfig_detect_provider)"
    say ""
    say "provider re-fetch"
    hr
    if [[ "$prov" == unknown ]]; then
        say "  couldn't tell which managed provider this context uses."
    else
        say "  detected: $prov"
    fi
    say "  suggested command:"
    say "    $(kubeconfig_provider_hint "$prov")"
    say ""
    if [[ ! -t 0 && "$ASSUME_YES" != 1 ]]; then
        err "re-fetch needs an interactive terminal (it runs a provider command you complete)."
        err "run it in your shell:  jdebug kubeconfig --refetch"
        return 64
    fi
    say "  Fill in the full command for your cluster (or paste your own). It will be run"
    say "  with output directed to a scratch file first, so nothing is touched until it"
    say "  connects. Leave blank to cancel."
    local cmd
    read -r -p "  command> " cmd || cmd=""
    if [[ -z "${cmd// }" ]]; then say "  cancelled — nothing changed."; return 1; fi

    local scratch; scratch="$(mktemp)"; trap 'rm -f "$scratch"' RETURN
    say ""
    say "  running (writing to a scratch kubeconfig)…"
    # Direct the provider's output at the scratch file, whichever flag it uses,
    # so ~/.kube/config isn't rewritten as a side effect of the fetch itself.
    local rc=0
    case "$cmd" in
        *aws*eks*update-kubeconfig*) KUBECONFIG="$scratch" bash -c "$cmd --kubeconfig '$scratch'" || rc=$? ;;
        *gcloud*get-credentials*)    KUBECONFIG="$scratch" bash -c "$cmd" || rc=$? ;;
        *az*aks*get-credentials*)    bash -c "$cmd --file '$scratch'" || rc=$? ;;
        *)                            KUBECONFIG="$scratch" bash -c "$cmd" || rc=$? ;;
    esac
    if [[ $rc -ne 0 ]]; then
        err "  the provider command failed (exit $rc) — nothing was changed."
        err "  re-authenticate first if it asked you to (aws sso login / gcloud auth login / az login)."
        return 1
    fi
    if [[ ! -s "$scratch" ]]; then
        err "  the command didn't write a kubeconfig to the scratch file. If it wrote to"
        err "  ~/.kube/config directly, that's your 'global' path already — run: jdebug kubeconfig --status"
        return 1
    fi
    apply_candidate "$scratch"
}

# --- run ---------------------------------------------------------------------

case "$MODE" in
    status)
        show_status
        exit 0 ;;
    forget)
        if kubeconfig_forget; then
            say "✓ forgot the jdebug-only kubeconfig — jdebug is back to your ambient config ($(kubeconfig_global_file))."
        else
            case $? in
                2) say "nothing to forget — jdebug wasn't using a jdebug-only kubeconfig." ;;
                *) err "couldn't remove $JDEBUG_SCOPED_KUBECONFIG"; exit 1 ;;
            esac
        fi
        exit 0 ;;
    file)
        [[ -n "$FILE" ]] || { err "--file needs a path"; exit 64; }
        show_status
        if ! check_cluster >/dev/null 2>&1; then
            say ""; say "why the current config is failing:"; check_cluster || true
        fi
        apply_candidate "$FILE"
        exit $? ;;
    refetch)
        show_status
        if ! check_cluster >/dev/null 2>&1; then
            say ""; say "why the current config is failing:"; check_cluster || true
        fi
        do_refetch
        exit $? ;;
esac

# interactive (no mode chosen)
show_status
if check_cluster >/dev/null 2>&1; then
    say ""
    say "the cluster already connects — you may not need to import anything."
    if [[ ! -t 0 ]]; then exit 0; fi
    confirm "import a different kubeconfig anyway?" || { say "nothing to do."; exit 0; }
else
    say ""
    say "diagnosis (why it can't connect):"
    check_cluster || true
fi

if [[ ! -t 0 ]]; then
    say ""
    say "to fix it, run one of these in your terminal:"
    say "  jdebug kubeconfig --file <path>     import a kubeconfig you have"
    say "  jdebug kubeconfig --refetch         re-fetch from your cloud provider"
    say "  jdebug kubeconfig --status          show what jdebug is using"
    exit 3
fi

say ""
say "how would you like to import a working kubeconfig?"
say "  f) point me at a FILE (downloaded, or from a colleague)"
say "  r) RE-FETCH it from your cloud provider (aws/gcloud/az)"
say "  s) just show STATUS again"
say "  q) quit — change nothing"
read -r -p "  choice [f/r/s/q]: " choice || choice=q
case "${choice:-q}" in
    f|F)
        read -r -p "  path to the kubeconfig: " path || path=""
        [[ -n "${path// }" ]] || { say "no path given — nothing changed."; exit 1; }
        # allow ~ expansion the user typed literally
        path="${path/#\~/$HOME}"
        apply_candidate "$path" ;;
    r|R) do_refetch ;;
    s|S) show_status ;;
    *)   say "nothing changed." ;;
esac
