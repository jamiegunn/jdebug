#!/usr/bin/env bash
#
# stage-jattach.sh — install the vendored, checksum-verified jattach onto a
# BARE-METAL target: this host, or a remote host reached over SSH. No kubectl.
#
# This is the bare-metal/SSH counterpart to capture/jattach.sh's in-pod install.
# Both go through lib/common.sh's jattach_verified_path, so NOTHING is
# downloaded at runtime and a tampered vendored binary is refused before it can
# run next to a JVM — the same integrity gate the in-pod path enforces. An
# operator who sets $JATTACH_BINARY supplies their own copy and bypasses the
# vendored path (their explicit choice).
#
# Usage:
#   stage-jattach.sh local [dest]            # onto this machine (default /tmp/jattach)
#   stage-jattach.sh ssh <user@host[:port]>  # onto a remote host over SSH (its arch)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_ROOT="$SCRIPT_DIR"; while [[ "$SCRIPTS_ROOT" != / && ! -f "$SCRIPTS_ROOT/lib/common.sh" ]]; do SCRIPTS_ROOT="$(dirname "$SCRIPTS_ROOT")"; done
# shellcheck source=lib/common.sh
source "$SCRIPTS_ROOT/lib/common.sh"

: "${JATTACH_VENDOR_DIR:=$SCRIPTS_ROOT/vendor/jattach}"
DEST="${JATTACH_BIN:-/tmp/jattach}"

# Policy: JDEBUG_NO_STAGE forbids installing any binary onto the target host —
# the bare-metal counterpart to the in-pod policy gate.
if [[ -n "${JDEBUG_NO_STAGE:-}" ]]; then
    err "jattach staging is disabled by policy (JDEBUG_NO_STAGE) — nothing installed."
    err "  → use the actuator tier (needs no jattach), or an already-present jattach."
    exit 1
fi

stage_local() {
    local dest="${1:-$DEST}" src
    if [[ -n "${JATTACH_BINARY:-}" ]]; then      # operator's own copy — explicit bypass
        [[ -f "$JATTACH_BINARY" ]] || { err "\$JATTACH_BINARY not found: $JATTACH_BINARY"; exit 1; }
        cp "$JATTACH_BINARY" "$dest" && chmod +x "$dest"
        echo "staged jattach at $dest (operator-provided, unverified by choice)"
        return
    fi
    if [[ -x "$dest" ]]; then echo "jattach already staged at $dest"; return; fi
    local dir; dir="$(dirname "$dest")"
    if ! ( touch "$dir/.jdebug-wtest" 2>/dev/null && rm -f "$dir/.jdebug-wtest" 2>/dev/null ); then
        err "can't stage jattach: '$dir' is not writable — jattach needs a writable path."
        err "  → use the actuator tier (needs no jattach), or set JATTACH_BIN=/writable/jattach"
        exit 1
    fi
    src="$(jattach_verified_path "$(uname -s)" "$(uname -m)")" || exit 1
    info "vendored jattach verified: $src"
    cp "$src" "$dest" && chmod +x "$dest"
    echo "staged jattach at $dest (vendored, checksum-verified)"
}

stage_ssh() {
    local hostspec="${1:?ssh needs user@host[:port]}" host="$hostspec" port="" src
    # split a trailing :port when it's all digits ("user@host:2222")
    if [[ "$hostspec" == *:* ]]; then
        local maybe="${hostspec##*:}"
        if [[ "$maybe" =~ ^[0-9]+$ ]]; then host="${hostspec%:*}"; port="$maybe"; fi
    fi
    local ssh_opts=(-o BatchMode=yes -o ConnectTimeout=8 -o ServerAliveInterval=15 -o ServerAliveCountMax=4)
    [[ -n "$port" ]] && ssh_opts+=(-p "$port")

    if [[ -n "${JATTACH_BINARY:-}" ]]; then      # operator's own copy — explicit bypass
        [[ -f "$JATTACH_BINARY" ]] || { err "\$JATTACH_BINARY not found: $JATTACH_BINARY"; exit 1; }
        src="$JATTACH_BINARY"
    else
        # the REMOTE arch decides which vendored binary we verify + send
        local r_os r_arch
        r_os="$(ssh "${ssh_opts[@]}" "$host" 'uname -s' 2>/dev/null)" || { err "can't reach $host over ssh (keys/agent? BatchMode is on, so no password prompt)"; exit 1; }
        r_arch="$(ssh "${ssh_opts[@]}" "$host" 'uname -m' 2>/dev/null)"
        src="$(jattach_verified_path "$r_os" "$r_arch")" || exit 1
        info "vendored jattach verified for $host ($r_os/$r_arch): $src"
    fi

    # pre-flight: is the remote staging dir writable? (read-only FS / restricted
    # host) — steer to the actuator tier instead of a half-written binary
    local rdir; rdir="$(dirname "$DEST")"
    if ! ssh "${ssh_opts[@]}" "$host" "touch '$rdir/.jdebug-wtest' 2>/dev/null && rm -f '$rdir/.jdebug-wtest' 2>/dev/null"; then
        err "can't stage jattach on $host: '$rdir' is not writable — jattach needs a writable path."
        err "  → use the actuator tier (needs no jattach), or set JATTACH_BIN=/writable/jattach"
        exit 1
    fi

    # send the verified bytes, then re-verify on the far side so a truncated or
    # mangled transfer can't leave a broken binary next to the JVM
    local want
    if command -v sha256sum >/dev/null 2>&1; then want="$(sha256sum "$src" | awk '{print $1}')"
    else want="$(shasum -a 256 "$src" | awk '{print $1}')"; fi
    ssh "${ssh_opts[@]}" "$host" "cat > '$DEST' && chmod +x '$DEST'" < "$src"
    local got
    got="$(ssh "${ssh_opts[@]}" "$host" "sha256sum '$DEST' 2>/dev/null | awk '{print \$1}' || shasum -a 256 '$DEST' | awk '{print \$1}'")"
    if [[ -n "$want" && -n "$got" && "$want" != "$got" ]]; then
        err "jattach on $host FAILED its post-transfer checksum — the copy is corrupt."
        err "  expected $want"
        err "  got      $got"
        exit 1
    fi
    echo "staged jattach at $host:$DEST (vendored, checksum-verified end to end)"
}

case "${1:-}" in
    local) shift; stage_local "${1:-}" ;;
    ssh)   shift; stage_ssh "${1:-}" ;;
    -h|--help|"") err "usage: stage-jattach.sh local [dest] | ssh <user@host[:port]>"; exit 64 ;;
    *) err "unknown target: ${1:-} (want 'local' or 'ssh')"; exit 64 ;;
esac
