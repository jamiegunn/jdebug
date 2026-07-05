package main

// logs.go — the live log tail. Polls `kubectl logs --tail=200` on its own 5s
// tick (polling beats a streaming goroutine here: each poll is a
// self-consistent window, no dedup, no lifecycle) and replaces the pane
// wholesale. ERROR/Exception lines render red (stack frames inherit), WARN
// amber. `f` expands the pane full-height with scrollback.

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type logLine struct {
	Text string
	Sev  int // 0 plain · 1 warn · 2 error
}

type logState struct {
	lines []logLine
	when  time.Time
	focus bool
	prev  bool // showing the PREVIOUS container's logs (current one can't serve any)
	off   int  // scroll offset back from the tail; 0 = follow
	err   string
}

type logTickMsg struct{}
type logMsg struct {
	lines []logLine
	prev  bool
	err   string
}

func logTickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return logTickMsg{} })
}

func fetchLogs(t target) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		get := func(previous bool) ([]byte, error) {
			args := []string{"-n", t.Namespace, "logs", t.Pod, "--tail=200"}
			if previous {
				args = append(args, "--previous")
			}
			if t.Container != "" {
				args = append(args, "-c", t.Container)
			}
			return exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		}
		out, err := get(false)
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return logMsg{lines: classifyLogs(strings.Split(strings.TrimRight(string(out), "\n"), "\n"))}
		}
		// a crash-looping container can't serve logs while it waits to
		// restart — the PREVIOUS container's last words are the story anyway
		if prev, perr := get(true); perr == nil && len(strings.TrimSpace(string(prev))) > 0 {
			return logMsg{prev: true, lines: classifyLogs(strings.Split(strings.TrimRight(string(prev), "\n"), "\n"))}
		}
		msg := firstLine(strings.TrimSpace(string(out)))
		if msg == "" && err != nil {
			msg = firstLine(err.Error())
		}
		if msg == "" {
			msg = "no log output yet"
		}
		return logMsg{err: msg}
	}
}

var logErrRe = regexp.MustCompile(`(?i)\bERROR\b|\bSEVERE\b|\bFATAL\b|Exception|OutOfMemory|\bOOM\b`)
var logWarnRe = regexp.MustCompile(`(?i)\bWARN(ING)?\b`)

func classifyLogs(raw []string) []logLine {
	ls := make([]logLine, 0, len(raw))
	prev := 0
	for _, r := range raw {
		if len(r) > 2048 {
			r = r[:2048]
		}
		r = ansi.Strip(r)
		sev := 0
		switch {
		case logErrRe.MatchString(r):
			sev = 2
		case logWarnRe.MatchString(r):
			sev = 1
		case prev == 2 && stackFrame(r):
			sev = 2
		}
		ls = append(ls, logLine{Text: r, Sev: sev})
		prev = sev
	}
	return ls
}

// stackFrame: indented continuation of a java stack trace.
func stackFrame(s string) bool {
	t := strings.TrimLeft(s, " \t")
	return t != s && (strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "...") || strings.HasPrefix(t, "Caused by"))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func podShort(p string, n int) string {
	if len(p) > n && n > 1 {
		return "…" + p[len(p)-(n-1):]
	}
	return p
}

// logPane renders exactly h rows (title + h-1 content) at width w.
func (m model) logPane(w, h int) string {
	ls := m.logs
	right := "5s · [f] expand"
	if ls.focus {
		right = "[f]/esc back"
	}
	if ls.off > 0 {
		right = fmt.Sprintf("paused ↑%d · G resumes · ", ls.off) + right
	} else if !ls.when.IsZero() {
		right = fmt.Sprintf("%ds ago · ", int(time.Since(ls.when).Seconds())) + right
	}
	sub := podShort(m.t.Pod, 28)
	if ls.prev {
		sub += " · previous container (it crashed — these are its last words)"
	}
	rows := []string{paneTitle(w, "LIVE LOGS", sub, right)}
	switch {
	case ls.err != "" && len(ls.lines) == 0:
		rows = append(rows, " "+cFaint.Render("– logs unavailable: "+ls.err+" –"))
	case len(ls.lines) == 0:
		rows = append(rows, " "+cFaint.Render("– waiting for logs… –"))
	default:
		rows = append(rows, renderTail(ls.lines, ls.off, h-1, w)...)
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return strings.Join(rows[:h], "\n")
}

// renderTail renders a window of severity-colored lines that ends `off`
// lines back from the tail (0 = follow), pinned so scrolling past the top
// still shows a full window.
func renderTail(ls []logLine, off, visible, w int) []string {
	end := len(ls) - off
	if end < visible {
		end = visible
	}
	if end > len(ls) {
		end = len(ls)
	}
	start := end - visible
	if start < 0 {
		start = 0
	}
	var rows []string
	for _, l := range ls[start:end] {
		st := cMuted
		switch l.Sev {
		case 2:
			st = cDisr
		case 1:
			st = cWarn
		}
		rows = append(rows, " "+st.Render(ansi.Truncate(l.Text, w-2, "…")))
	}
	return rows
}

// logFocusView: the expanded pane — header, one-line target summary, logs
// filling everything else, hint line. Exactly m.height rows.
func (m model) logFocusView() string {
	w := m.tw()
	head := m.headerRemote(m.remote.Cluster)
	d := m.panel
	mem := d.MemUse
	if d.MemPct >= 0 {
		mem += fmt.Sprintf(" (%d%%)", d.MemPct)
	}
	sum := " " + cFaint.Render(fmt.Sprintf("phase %s · restarts %d · mem %s", dashIfEmpty(d.Phase), d.Restarts, dashIfEmpty(mem)))
	foot := " " + cFaint.Render("↑↓/jk line · space/b page · g/G ends · f/esc back")
	h := m.height - 5
	if m.height == 0 {
		h = 20
	}
	if h < 4 {
		h = 4
	}
	return head + "\n" + sum + "\n" + m.logPane(w, h) + "\n" + foot
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// logFocusKey drives the expanded pane; off counts back from the tail.
func (m model) logFocusKey(key string) (tea.Model, tea.Cmd) {
	page := m.height - 8
	if page < 5 {
		page = 5
	}
	switch key {
	case "f", "F", "esc", "q", "Q", "enter":
		m.logs.focus = false
		m.logs.off = 0
		return m, nil
	case "up", "k":
		m.logs.off++
	case "down", "j":
		m.logs.off--
	case "pgup", "b":
		m.logs.off += page
	case "pgdown", "space":
		m.logs.off -= page
	case "g":
		m.logs.off = len(m.logs.lines)
	case "G":
		m.logs.off = 0
	}
	if m.logs.off < 0 {
		m.logs.off = 0
	}
	if max := len(m.logs.lines) - 1; m.logs.off > max && max >= 0 {
		m.logs.off = max
	}
	return m, nil
}
