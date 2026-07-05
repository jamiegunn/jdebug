package main

// output.go — the in-app output pane (hybrid execution's "quick" half).
// Short reads (status, memory, analyze, jcmd, …) run captured and render in
// a scrollable pane inside the dashboard; long-lived or interactive commands
// (log follow, heap dump, snapshot, wizard) keep the ExecProcess drop-out in
// exec.go. Both paths append the same transcript block to the session log.

import (
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

type outState struct {
	title   string
	raw     string    // ANSI-stripped full text
	lines   []logLine // wrapped + severity-classified
	off     int       // scroll offset from the top
	running bool
	done    bool
	ok      bool
	errStr  string
}

type cmdOutMsg struct {
	title string
	out   []byte
	err   error
}

// quickCLI runs `jdebug <args...>` captured, into the scOutput pane.
func (m model) quickCLI(withPod bool, args ...string) (tea.Model, tea.Cmd) {
	words := append([]string{filepath.Join(m.kit, "jdebug")}, args...)
	if withPod && m.t.Pod != "" {
		words = append(words, m.t.Pod)
	}
	return m.openQuick("jdebug "+strings.Join(args, " "), targetEnv(m.t), words...)
}

// quickLocal runs `sh <kit>/jdebug-local <args...>` captured.
func (m model) quickLocal(args ...string) (tea.Model, tea.Cmd) {
	words := append([]string{"sh", filepath.Join(m.kit, "jdebug-local")}, args...)
	return m.openQuick("jdebug-local "+strings.Join(args, " "), targetEnv(m.t), words...)
}

func (m model) openQuick(title string, env []string, words ...string) (tea.Model, tea.Cmd) {
	m.out = outState{title: title, running: true}
	m.prev = scMenu
	m.scr = scOutput
	return m, runQuick(title, env, words...)
}

func runQuick(title string, env []string, words ...string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, words[0], words[1:]...)
		c.Env = append(os.Environ(), append(env, "NO_COLOR=1")...)
		out, err := c.CombinedOutput()
		appendSessionLog(strings.Join(words, " "), out, err)
		return cmdOutMsg{title: title, out: out, err: err}
	}
}

// rewrapOut re-flows the captured text to the current width (called on
// arrival and on terminal resize).
func (m *model) rewrapOut() {
	w := m.tw() - 4
	if w < 40 {
		w = 40
	}
	wrapped := ansi.Hardwrap(m.out.raw, w, true)
	m.out.lines = classifyLogs(strings.Split(strings.TrimRight(wrapped, "\n"), "\n"))
	m.clampOutOff()
}

func (m *model) clampOutOff() {
	max := len(m.out.lines) - m.outVisible()
	if max < 0 {
		max = 0
	}
	if m.out.off > max {
		m.out.off = max
	}
	if m.out.off < 0 {
		m.out.off = 0
	}
}

// outVisible: content rows the pane can show (frame chrome is 7 rows).
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

func (m model) outputView() string {
	w := m.tw()
	var b strings.Builder
	if m.mode == 1 {
		b.WriteString(m.headerRemote(m.remote.Cluster))
	} else {
		b.WriteString(m.headerLocal(m.local.Jattach))
	}
	status := cFaint.Render("⣾ running…")
	if m.out.done {
		if m.out.ok {
			status = cSafe.Render("✓")
		} else {
			status = cDisr.Render("✗ " + m.out.errStr)
		}
	}
	b.WriteString("\n " + cKey.Render("$ "+m.out.title) + "  " + status + "\n" + rule(w) + "\n")

	vis := m.outVisible()
	shown := 0
	if len(m.out.lines) == 0 {
		msg := "– collecting… –"
		if m.out.done {
			msg = "– no output –"
		}
		b.WriteString(" " + cFaint.Render(msg) + "\n")
		shown = 1
	} else {
		end := m.out.off + vis
		if end > len(m.out.lines) {
			end = len(m.out.lines)
		}
		for _, l := range m.out.lines[m.out.off:end] {
			st := cBody
			switch l.Sev {
			case 2:
				st = cDisr
			case 1:
				st = cWarn
			}
			b.WriteString("  " + st.Render(ansi.Truncate(l.Text, w-4, "…")) + "\n")
			shown++
		}
	}
	if m.height > 0 {
		for ; shown < vis; shown++ {
			b.WriteString("\n")
		}
	}

	pos := ""
	if len(m.out.lines) > vis {
		pos = fmt.Sprintf("%d–%d of %d lines", m.out.off+1, mini(m.out.off+vis, len(m.out.lines)), len(m.out.lines))
	}
	hints := "↑↓/jk scroll · space/b page · g/G ends · q back"
	pad := w - 1 - lipgloss.Width(hints) - lipgloss.Width(pos) - 1
	if pad < 1 {
		pad = 1
	}
	b.WriteString(rule(w) + "\n " + cFaint.Render(hints) + strings.Repeat(" ", pad) + cDim.Render(pos))
	return b.String()
}

func (m model) outputKey(key string) (tea.Model, tea.Cmd) {
	page := m.outVisible() - 1
	if page < 1 {
		page = 1
	}
	switch key {
	case "q", "Q", "esc", "enter":
		m.scr = m.prev
		return m, tea.Batch(m.panelFetch(), fetchCaps(m.kit))
	case "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		m.out.off--
	case "down", "j":
		m.out.off++
	case "pgup", "b":
		m.out.off -= page
	case "pgdown", "space":
		m.out.off += page
	case "g":
		m.out.off = 0
	case "G":
		m.out.off = len(m.out.lines)
	}
	m.clampOutOff()
	return m, nil
}
