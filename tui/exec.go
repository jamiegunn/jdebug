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

// sshBase builds the ssh invocation prefix for a bare-metal remote: keys/agent
// only (BatchMode, so it never blocks on a password prompt), a short connect
// timeout so an unreachable host fails fast, and an explicit port when the host
// is written "user@host:port". Auth relies entirely on the user's ~/.ssh/config
// and agent — jdebug stores no secret.
func sshBase(host string) string {
	h := host
	port := ""
	if i := strings.LastIndex(host, ":"); i >= 0 {
		// "user@host:port" — only treat a trailing all-digit segment as a port
		if p := host[i+1:]; p != "" && strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			h, port = host[:i], p
		}
	}
	// ConnectTimeout bounds dialing; ServerAlive* bounds a wedged CONNECTION
	// (dead peer / dropped network) to ~60s without killing a slow-but-alive
	// command like a big heap dump — that one you let run.
	cmd := "ssh -o BatchMode=yes -o ConnectTimeout=8 -o ServerAliveInterval=15 -o ServerAliveCountMax=4"
	if port != "" {
		cmd += " -p " + shq(port)
	}
	return cmd + " " + shq(h)
}

// localWords builds the argv that runs the self-contained jdebug-local script
// for a bare-metal target. Against this machine it's just `sh <kit>/jdebug-local
// <args>`. Against a remote host (t.SSH set) the POSIX script is piped to `sh
// -s` on the far side over SSH, so nothing has to be installed there; the
// actuator/out/jattach settings and an scp-back hint are prepended as shell
// assignments the remote sh reads before the script's own defaults.
func localWords(kit string, t target, args ...string) []string {
	script := filepath.Join(kit, "jdebug-local")
	if t.SSH == "" {
		return append([]string{"sh", script}, args...)
	}
	var qa []string
	for _, a := range args {
		qa = append(qa, shq(a))
	}
	// remote env: actuator base (the app's localhost on the far side), where
	// captures land, the jattach path, and the host so out_hint can print the
	// scp-back command. Values are single-quoted for the REMOTE shell.
	rq := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	envLines := fmt.Sprintf("ACTUATOR_BASE=%s OUT_DIR=%s JATTACH_BIN=%s JDEBUG_SSH_BACK=%s",
		rq(t.Actuator), rq("/tmp"), rq("/tmp/jattach"), rq(t.SSH))
	if t.JVMPid != "" { // pin the chosen JVM on the remote host too
		envLines += " JVM_PID=" + rq(t.JVMPid)
	}
	// forward the heap data-governance policy so it enforces on the remote host too
	for _, k := range []string{"JDEBUG_REQUIRE_DATA_ACK", "JDEBUG_DATA_ACK"} {
		if v := os.Getenv(k); v != "" {
			envLines += " " + k + "=" + rq(v)
		}
	}
	// { printf '<env> '; cat <script>; } | ssh <host> sh -s -- <args>
	cmd := fmt.Sprintf("{ printf %s; cat %s; } | %s sh -s -- %s",
		shq(envLines+"\n"), shq(script), sshBase(t.SSH), strings.Join(qa, " "))
	return []string{"sh", "-c", cmd}
}

// targetEnv exports the current target the way the bash TUI does, so the CLI
// children inherit it (flags still win inside the CLI).
func targetEnv(t target) []string {
	env := []string{
		"NAMESPACE=" + t.Namespace,
		"SELECTOR=" + t.Selector,
		"APP_CONTAINER=" + t.Container,
		"ACTUATOR_BASE=" + t.Actuator,
		"ACTUATOR_AUTH=" + t.ActuatorAuth, // a reference, not a secret
	}
	if t.JVMPid != "" { // bare metal: the specific JVM the user picked on this host
		env = append(env, "JVM_PID="+t.JVMPid)
	}
	return env
}

// stageJattachWords builds the argv that stages jattach for a bare-metal
// target. Both routes go through capture/stage-jattach.sh, which installs the
// VENDORED, checksum-verified binary (same integrity gate as the in-pod path)
// — nothing is downloaded at runtime, so a tampered/corrupt binary is refused
// before it can run next to the JVM.
func (m model) stageJattachWords() (title string, words []string) {
	script := filepath.Join(m.kit, "capture", "stage-jattach.sh")
	if m.t.SSH == "" {
		return "stage jattach", []string{"bash", script, "local"}
	}
	return "stage jattach on " + m.t.SSH, []string{"bash", script, "ssh", m.t.SSH}
}
