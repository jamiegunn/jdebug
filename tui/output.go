package main

// output.go — the streaming output pane. Every command (quick reads AND the
// long-lived ones that used to drop to the raw terminal) now streams into
// the dashboard's bottom pane, replacing the live log tail while it runs:
// the menu stays up, esc stops/dismisses, ↑↓ scrolls, and the tail follows
// like a terminal. Small terminals (no bottom strip) fall back to the
// full-screen scOutput view — same state, different frame. Only the wizard
// keeps the ExecProcess drop-out (its narrated step chain rides on
// execDoneMsg). Both paths append the same transcript block to the session
// log.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type streamChunkMsg struct {
	id   int
	data []byte
}
type streamDoneMsg struct {
	id  int
	err error
}

type outState struct {
	id       int // guards against messages from a superseded stream
	title    string
	display  string // full command line, for the session log
	raw      []byte
	lines    []logLine
	off      int // back from the tail; 0 = follow
	running  bool
	done     bool
	ok       bool
	errStr   string
	notice   string // transient feedback, e.g. "copied to clipboard ✓"
	filePath string // a capture is being VIEWED — `a` analyzes this file
	show     bool   // rendering in the bottom strip (vs scOutput fallback)
	spin     int    // spinner frame, advanced by spinTick while running
	ch       chan tea.Msg
	cancel   context.CancelFunc
}

// spinTickMsg advances the streaming spinner. It carries the stream id so a
// tick from a superseded/finished stream stops the loop instead of spinning
// forever. spinFrames is a smooth braille cycle.
type spinTickMsg struct{ id int }

var spinFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

func spinCmd(id int) tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinTickMsg{id} })
}

// copyTranscript puts the pane's ANSI-free transcript on the system
// clipboard. Seam-injected for tests.
var clipboardFn = copyToClipboard

func copyToClipboard(s string) error {
	for _, c := range [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-ib"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		return cmd.Run()
	}
	return fmt.Errorf("no clipboard tool found (pbcopy / xclip / wl-copy)")
}

func (m model) copyTranscript() model {
	text := "$ " + m.out.display + "\n\n" + ansi.Strip(string(m.out.raw))
	if err := clipboardFn(text); err != nil {
		m.out.notice = "couldn't copy: " + err.Error()
	} else {
		m.out.notice = "copied to clipboard ✓"
	}
	return m
}

const outRawCap = 256 << 10 // keep the newest 256K of a long stream

// quickCLI streams `jdebug <args...>` into the output pane.
func (m model) quickCLI(withPod bool, args ...string) (tea.Model, tea.Cmd) {
	words := append([]string{filepath.Join(m.kit, "jdebug")}, args...)
	if withPod && m.t.Pod != "" {
		words = append(words, m.t.Pod)
	}
	return m.openQuick("jdebug "+strings.Join(args, " "), targetEnv(m.t), words...)
}

// quickLocal streams jdebug-local <args...> — on this machine, or piped over
// SSH to the bare-metal remote when one is set.
func (m model) quickLocal(args ...string) (tea.Model, tea.Cmd) {
	words := localWords(m.kit, m.t, args...)
	title := "jdebug-local " + strings.Join(args, " ")
	if m.t.SSH != "" {
		title += " · ssh " + m.t.SSH
	}
	return m.openQuick(title, targetEnv(m.t), words...)
}

// quickTo/quickToLocal: pointer-receiver adapters for confirm closures,
// which must mutate the model in place and return only a Cmd.
func (mm *model) quickTo(withPod bool, args ...string) tea.Cmd {
	mdl, cmd := mm.quickCLI(withPod, args...)
	*mm = mdl.(model)
	return cmd
}

func (mm *model) quickToLocal(args ...string) tea.Cmd {
	mdl, cmd := mm.quickLocal(args...)
	*mm = mdl.(model)
	return cmd
}

func (m model) openQuick(title string, env []string, words ...string) (tea.Model, tea.Cmd) {
	return m.startPane(title, env, nil, false, words...)
}

// startPane starts a streamed command. keep=true appends to the pane's
// existing transcript (wizard flows chain their steps into one story);
// prefix bytes (narration) render above the command's output.
func (m model) startPane(title string, env []string, prefix []byte, keep bool, words ...string) (tea.Model, tea.Cmd) {
	if m.out.running && m.out.cancel != nil {
		m.out.cancel() // a new command supersedes the previous stream
	}
	id := m.out.id + 1
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 32)
	st := outState{id: id, title: title, display: strings.Join(words, " "),
		running: true, ch: ch, cancel: cancel}
	if keep {
		st.raw = m.out.raw
	}
	m.out = st
	if len(prefix) > 0 {
		(&m).appendChunk(prefix)
		m.out.running = true // appendChunk doesn't touch it, but be explicit
	}
	if m.mode == 1 && m.remote.OK && m.showLogPane() {
		m.out.show = true   // render in the bottom strip; menu stays live
		m.workTab = tabWork // a launched command auto-selects the WORK tab
	} else {
		m.prev = scMenu
		m.scr = scOutput
	}
	go streamCmd(ctx, id, env, ch, words...)
	return m, tea.Batch(waitStream(ch), spinCmd(id))
}

// streamCmd runs the command with stdout+stderr on one pipe and forwards
// chunks to the Update loop. EOF arrives when the child exits (it holds the
// only write end); cancel kills the child via CommandContext.
func streamCmd(ctx context.Context, id int, env []string, ch chan tea.Msg, words ...string) {
	c := exec.CommandContext(ctx, words[0], words[1:]...)
	c.Env = append(os.Environ(), append(env, "NO_COLOR=1")...)
	pr, pw, err := os.Pipe()
	if err != nil {
		ch <- streamDoneMsg{id, err}
		return
	}
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		pw.Close()
		pr.Close()
		ch <- streamDoneMsg{id, err}
		return
	}
	pw.Close() // child holds the write end now
	buf := make([]byte, 8192)
	for {
		n, rerr := pr.Read(buf)
		if n > 0 {
			ch <- streamChunkMsg{id, append([]byte(nil), buf[:n]...)}
		}
		if rerr != nil {
			break
		}
	}
	pr.Close()
	werr := c.Wait()
	if ctx.Err() != nil {
		werr = context.Canceled
	}
	ch <- streamDoneMsg{id, werr}
}

func waitStream(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// rewrapOut re-flows the buffered stream to the current width.
func (m *model) rewrapOut() {
	w := m.tw() - 4
	if w < 40 {
		w = 40
	}
	raw := string(m.out.raw)
	wrapped := ansi.Hardwrap(ansi.Strip(raw), w, true)
	m.out.lines = classifyLogs(strings.Split(strings.TrimRight(wrapped, "\n"), "\n"))
	if len(m.out.raw) == 0 {
		m.out.lines = nil
	}
}

// appendChunk buffers a stream chunk, trimming the front on a line boundary
// once the cap is reached.
func (m *model) appendChunk(data []byte) {
	m.out.raw = append(m.out.raw, data...)
	if len(m.out.raw) > outRawCap {
		cut := len(m.out.raw) - outRawCap
		if i := bytes.IndexByte(m.out.raw[cut:], '\n'); i >= 0 {
			cut += i + 1
		}
		m.out.raw = append([]byte(nil), m.out.raw[cut:]...)
	}
	m.rewrapOut()
}

// outStatus renders the right-hand side of the pane title.
func (m model) outStatus(strip bool) string {
	if m.out.notice != "" {
		return cSafe.Render(m.out.notice)
	}
	back := "esc back to logs"
	if !strip {
		back = "q back"
	}
	switch {
	case m.out.running:
		return cFaint.Render(spinFrames[m.out.spin%len(spinFrames)] + " streaming · esc stops")
	case m.out.errStr == "stopped":
		return cWarn.Render("◼ stopped") + cFaint.Render(" · "+back)
	case m.out.ok:
		return cSafe.Render("✓") + cFaint.Render(" · ↑↓/wheel · C copies · "+back)
	default:
		return cDisr.Render("✗ "+m.out.errStr) + cFaint.Render(" · "+back)
	}
}

// outPane renders the bottom-strip version: exactly h rows.
func (m model) outPane(w, h int) string {
	rows := []string{paneTitle(w, "OUTPUT", "$ "+m.out.title, m.outStatus(true))}
	if len(m.out.lines) == 0 {
		msg := "– collecting… –"
		if m.out.done {
			msg = "– no output –"
		}
		rows = append(rows, " "+cFaint.Render(msg))
	} else {
		rows = append(rows, renderTail(m.out.lines, m.out.off, h-1, w)...)
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return strings.Join(rows[:h], "\n")
}

// bottomPane: the dashboard's bottom work area — a WORK/LOGS/EVENTS tab strip
// over the active tab's content (its own pane title swapped for the strip).
func (m model) bottomPane(w, h int) string {
	if h < 2 {
		h = 2
	}
	var body string
	switch m.workTab {
	case tabEvents:
		body = strings.Join(m.eventsRows(w, h), "\n")
	case tabWork:
		body = m.workPane(w, h)
	case tabCaptures:
		body = strings.Join(m.capsRows(w, h), "\n") // the roomy, full-width evidence browser
	case tabTrends:
		body = strings.Join(m.metricsTabRows(w, h), "\n") // full-width metric sparklines
	default:
		body = m.logPane(w, h)
	}
	lines := strings.Split(body, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	lines[0] = m.workTabStrip(w) // the tab strip replaces the pane's own title row
	return strings.Join(lines, "\n")
}

// outVisible: content rows the full-screen scOutput frame can show.
func (m model) outVisible() int {
	if m.height == 0 {
		if n := len(m.out.lines); n > 0 {
			return n
		}
		return 1
	}
	v := m.height - 7
	if v < 5 {
		v = 5
	}
	return v
}

// outputView is the full-screen fallback for terminals without the strip.
func (m model) outputView() string {
	w := m.tw()
	var b strings.Builder
	if m.mode == 1 {
		b.WriteString(m.headerRemote(m.remote.Cluster))
	} else {
		b.WriteString(m.headerLocal(m.local.Jattach))
	}
	b.WriteString("\n " + cKey.Render("$ "+m.out.title) + "  " + m.outStatus(false) + "\n" + rule(w) + "\n")

	vis := m.outVisible()
	var rows []string
	if len(m.out.lines) == 0 {
		msg := "– collecting… –"
		if m.out.done {
			msg = "– no output –"
		}
		rows = []string{" " + cFaint.Render(msg)}
	} else {
		rows = renderTail(m.out.lines, m.out.off, vis, w)
	}
	for _, r := range rows {
		b.WriteString(" " + r + "\n")
	}
	if m.height > 0 {
		for i := len(rows); i < vis; i++ {
			b.WriteString("\n")
		}
	}

	pos := ""
	if len(m.out.lines) > vis {
		pos = fmt.Sprintf("%d lines · ↑%d", len(m.out.lines), m.out.off)
	}
	hints := "↑↓/jk scroll · space/b page · g/G ends · q back"
	pad := w - 1 - lipgloss.Width(hints) - lipgloss.Width(pos) - 1
	if pad < 1 {
		pad = 1
	}
	b.WriteString(rule(w) + "\n " + cFaint.Render(hints) + strings.Repeat(" ", pad) + cDim.Render(pos))
	return b.String()
}

// outScroll adjusts the tail offset (shared by scOutput and the strip).
func (m model) outScroll(key string, page int) model {
	m.out.notice = "" // any movement clears transient feedback
	if page < 1 {
		page = 1
	}
	switch key {
	case "up", "k":
		m.out.off++
	case "down", "j":
		m.out.off--
	case "pgup", "b":
		m.out.off += page
	case "pgdown", "space":
		m.out.off -= page
	case "g":
		m.out.off = len(m.out.lines) // pinned at the top by renderTail
	case "G":
		m.out.off = 0
	}
	if m.out.off < 0 {
		m.out.off = 0
	}
	if max := len(m.out.lines) - 1; m.out.off > max && max >= 0 {
		m.out.off = max
	}
	return m
}

func (m model) outputKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "Q", "esc", "enter":
		if m.out.running && m.out.cancel != nil {
			m.out.cancel() // first press stops the stream; verdict lands via done
			return m, nil
		}
		m.scr = m.prev
		m.out.show = false
		return m, tea.Batch(m.panelFetch(m.bgMode == bgLive), fetchCaps(m.kit, m.capsDir()))
	case "C":
		return m.copyTranscript(), nil
	case "ctrl+c":
		if m.out.cancel != nil {
			m.out.cancel()
		}
		return m, tea.Quit
	}
	return m.outScroll(key, m.outVisible()-1), nil
}

// menuOutKey handles the pane keys while the strip shows output on the menu
// screen. Returns handled=false for everything else so the menu keeps
// working — you can fire the next command without dismissing the pane.
func (m model) menuOutKey(key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "esc":
		if m.out.running && m.out.cancel != nil {
			m.out.cancel()
			return m, nil, true
		}
		m.out.show = false
		m.workTab = tabLogs // dismissed the output → back to the live log tail
		return m, tea.Batch(m.panelFetch(m.bgMode == bgLive), fetchCaps(m.kit, m.capsDir())), true
	case "C":
		return m.copyTranscript(), nil, true
	case "up", "down", "pgup", "pgdown":
		page := m.height / 3
		return m.outScroll(key, page), nil, true
	}
	return m, nil, false
}
