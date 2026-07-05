package main

// exec.go — running backend commands. tea.ExecProcess releases the terminal
// to the child, so output streams to the real tty (scrollback intact, Ctrl-C
// stops the child not the TUI) and is tee'd to the session log — the same
// contract as the bash TUI's run().

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type execDoneMsg struct{ err error }

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

var sessionLog string

func initSessionLog(kit string) {
	sessionLog = filepath.Join(dumpsDir(kit), "session-"+time.Now().Format("20060102-150405")+".log")
}

// appendSessionLog writes the same "$ cmd … ✓/✗" transcript block that
// runShell's tee produces, so in-app (captured) runs keep the session-log
// contract of the drop-out path.
func appendSessionLog(display string, out []byte, err error) {
	_ = os.MkdirAll(filepath.Dir(sessionLog), 0o755)
	f, ferr := os.OpenFile(sessionLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	verdict := "✓ done"
	if err != nil {
		verdict = "✗ " + err.Error()
	}
	fmt.Fprintf(f, "\n$ %s\n\n%s\n%s\n", display, strings.TrimRight(string(out), "\n"), verdict)
}

// runShell echoes the command, runs it on the NORMAL screen (the dashboard is
// an altscreen app; ExecProcess drops out of it), tees to the session log,
// prints ✓/✗, and pauses for a key so the output can be read before the
// dashboard resumes. Output therefore lands in scrollback AND the log.
func runShell(env []string, words ...string) tea.Cmd {
	var qs []string
	for _, w := range words {
		qs = append(qs, shq(w))
	}
	joined := strings.Join(qs, " ")
	_ = os.MkdirAll(filepath.Dir(sessionLog), 0o755)
	script := fmt.Sprintf(`set -o pipefail
printf '\n$ %%s\n\n' %s | tee -a %s
{ %s; } 2>&1 | tee -a %s; rc=$?
if [ $rc -eq 0 ]; then printf '\n\033[1;32m✓ done\033[0m'
else printf '\n\033[1;31m✗ that didn'\''t work (exit %%s) — the messages above say why\033[0m' "$rc"; fi
printf '\n\033[2many key for the menu — output saved to %%s\033[0m ' %s
IFS= read -rsn1 _ </dev/tty || true
printf '\n'
exit $rc`,
		shq(strings.Join(words, " ")), shq(sessionLog), joined, shq(sessionLog), shq(sessionLog))
	c := exec.Command("bash", "-c", script)
	c.Env = append(os.Environ(), env...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{err} })
}

// podTerminal opens an interactive shell inside the target pod. This is the
// one action that genuinely needs the real tty, so it drops out of the
// altscreen; exiting the shell (exit / Ctrl-D) lands back on the dashboard,
// which then re-runs status automatically to re-orient you. When the image
// has no shell at all (distroless), it explains that and attaches an
// ephemeral busybox DEBUG container targeting the app instead.
func (m *model) podTerminal() tea.Cmd {
	m.postExec = "status"
	script := fmt.Sprintf(`kubectl -n %s exec -it %s -c %s -- sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh' || {
printf '\n→ no shell in that container (distroless image?) — attaching a busybox DEBUG container that shares its process/network space\n'
printf '  (needs the pods/ephemeralcontainers permission; the container lingers in the pod spec until restart — harmless)\n\n'
kubectl -n %s debug -it %s --image=busybox:1.36 --target=%s -- sh
}`,
		shq(m.t.Namespace), shq(m.t.Pod), shq(m.t.Container),
		shq(m.t.Namespace), shq(m.t.Pod), shq(m.t.Container))
	c := exec.Command("bash", "-c", script)
	return tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{err} })
}

// targetEnv exports the current target the way the bash TUI does, so the CLI
// children inherit it (flags still win inside the CLI).
func targetEnv(t target) []string {
	return []string{
		"NAMESPACE=" + t.Namespace,
		"SELECTOR=" + t.Selector,
		"APP_CONTAINER=" + t.Container,
		"ACTUATOR_BASE=" + t.Actuator,
	}
}

// jattachScript downloads the arch-matched jattach for THIS machine into
// the shared cache and copies it to $JATTACH_BIN (mirrors the bash helper).
// Runs through the streaming output pane.
func jattachScript() string {
	cache := os.Getenv("JDEBUG_CACHE_DIR")
	if cache == "" {
		if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
			cache = filepath.Join(x, "jdebug")
		} else {
			home, _ := os.UserHomeDir()
			cache = filepath.Join(home, ".cache", "jdebug")
		}
	}
	ver := os.Getenv("JATTACH_VERSION")
	if ver == "" {
		ver = "v2.2"
	}
	return fmt.Sprintf(`set -e
BIN=%s; CACHE=%s; VER=%s
[ -x "$BIN" ] && { echo "jattach already staged at $BIN"; exit 0; }
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64|Linux-amd64)  ASSET="jattach-linux-x64.tgz" ;;
  Linux-aarch64|Linux-arm64) ASSET="jattach-linux-arm64.tgz" ;;
  Darwin-*)                  ASSET="jattach-macos.zip" ;;
  *) echo "no prebuilt jattach for $(uname -s)/$(uname -m) — place one at $BIN yourself" >&2; exit 1 ;;
esac
F="$CACHE/jattach-$(uname -s)-$(uname -m)-$VER"
if [ ! -f "$F" ]; then
  mkdir -p "$CACHE"; T=$(mktemp -d)
  echo "downloading https://github.com/jattach/jattach/releases/download/$VER/$ASSET"
  curl -fsSL -o "$T/$ASSET" "https://github.com/jattach/jattach/releases/download/$VER/$ASSET"
  tar -xf "$T/$ASSET" -C "$T" && mv "$T/jattach" "$F" && chmod +x "$F"; rm -rf "$T"
fi
cp "$F" "$BIN" && chmod +x "$BIN" && echo "staged jattach at $BIN"`,
		shq(jattachBin()), shq(cache), shq(ver))
}
