#!/usr/bin/env bash
#
# cleanup.sh — remove the files jdebug STAGED inside pods this session (jattach,
# jdebug-local). It never removes files that were already there before jdebug
# ran, and never touches local dumps/ evidence. Lists by default; --confirm
# actually removes.
#
# Usage:
#   ./cleanup.sh            # list the remote artifacts jdebug recorded
#   ./cleanup.sh --confirm  # remove the ones jdebug staged (owned=1)

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

MF="$(artifacts_manifest)"
CONFIRM=0
[[ "${1:-}" == --confirm ]] && CONFIRM=1

echo "== remote artifacts — files jdebug touched inside pods this session =="
if [[ ! -s "$MF" ]]; then
    echo "  none — jdebug hasn't staged anything in a pod this session."
    echo "  (jattach and 'push-local' are the only things jdebug copies in; both go under /tmp.)"
    exit 0
fi

staged=0
while IFS=$'\t' read -r owned ns pod cont path note; do
    [[ -z "$path" ]] && continue
    if [[ "$owned" == 1 ]]; then
        printf '  %s  in %s  — %s  (staged by jdebug)\n' "$path" "$pod" "$note"
        staged=$((staged + 1))
    else
        printf '  %s  in %s  — %s  (pre-existing — will NOT be removed)\n' "$path" "$pod" "$note"
    fi
done < "$MF"

echo
echo "will NOT remove: files that existed before this session · anything not staged by jdebug · local dumps/"

if [[ $CONFIRM -ne 1 ]]; then
    if [[ $staged -gt 0 ]]; then
        echo "to remove the $staged staged file(s):  jdebug cleanup --confirm"
    fi
    exit 0
fi

# remove session-owned; keep pre-existing + any that fail to delete
check_cluster || exit 1
TMP="$(mktemp)"; ERR="$(mktemp)"; trap 'rm -f "$TMP" "$ERR"' EXIT
removed=0
while IFS=$'\t' read -r owned ns pod cont path note; do
    [[ -z "$path" ]] && continue
    if [[ "$owned" != 1 ]]; then
        printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$owned" "$ns" "$pod" "$cont" "$path" "$note" >> "$TMP"
        continue
    fi
    show_cmd "kubectl -n $ns exec $pod -c $cont -- rm -f $path"
    if kubectl -n "$ns" exec "$pod" -c "$cont" -- rm -f "$path" 2>"$ERR"; then
        info "removed $path from $pod"
        removed=$((removed + 1))
    else
        err "couldn't remove $path from $pod: $(head -n1 "$ERR")"
        err "  manual: kubectl -n $ns exec $pod -c $cont -- rm -f $path"
        err "  (if the pod was replaced, that file went with it — not an error)"
        printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$owned" "$ns" "$pod" "$cont" "$path" "$note" >> "$TMP"
    fi
done < "$MF"
mv "$TMP" "$MF"
echo
info "cleanup done — removed $removed staged file(s). Local dumps/ evidence is untouched."
