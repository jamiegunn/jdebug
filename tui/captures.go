package main

// captures.go — the dumps/ browser: a navigable tree over the organized
// layout (dumps/pods/<pod>/<ts>/<file>). Starts pre-filtered to the selected
// pod; click a folder to drill in, `..` to go up, a file to VIEW it in the
// output pane below (not a Finder window). `a` then analyzes whatever is in
// view. Also the source of truth for "is there evidence yet".

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type capEntry struct {
	Name    string
	Path    string // absolute path (set for flat focus-browser entries)
	Pod     string // owning pod (set for flat focus-browser entries)
	Size    int64
	Mod     time.Time
	Dir     bool
	Snap    bool // directory is a snapshot bundle (has a .snapshot marker)
	Invalid bool // a .hprof that isn't a heap dump (bad magic — an error page)
}

// hprofValid reports whether a file starts with the "JAVA PROFILE" magic. A
// .hprof that fails this is an error/login page captured from a secured or
// wrong-path actuator — not something Eclipse MAT can open.
func hprofValid(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 12)
	n, _ := io.ReadFull(f, b)
	return n >= 12 && strings.HasPrefix(string(b[:n]), "JAVA PROFILE")
}

// classifyHead names what a non-heap-dump capture actually looks like, mirroring
// the CLI's classify_capture so the browser and analyze agree.
func classifyHead(b []byte) string {
	s := strings.ToLower(string(b))
	switch {
	case len(b) == 0:
		return "the file is empty — the download returned nothing"
	case strings.Contains(s, "<html") || strings.Contains(s, "<!doctype html"):
		if strings.Contains(s, "login") || strings.Contains(s, "password") || strings.Contains(s, "sign in") {
			return "it looks like an HTML login page — the endpoint is secured"
		}
		return "it looks like an HTML error page"
	case strings.HasPrefix(strings.TrimSpace(s), "{") || strings.Contains(s, `"status"`) || strings.Contains(s, `"error"`):
		return "it looks like a JSON error response (a Spring/actuator error)"
	case strings.HasPrefix(s, "http/"):
		return "it looks like a raw HTTP error response"
	}
	return "it doesn't start with the JAVA PROFILE magic"
}

type capsMsg struct {
	dir     string
	entries []capEntry
}

// capsRoot is the top of the browsable tree — never navigate above it.
func capsRoot(kit string) string { return filepath.Join(dumpsDir(kit), "pods") }

// capsDir is the directory currently shown: an explicit browse target, else
// the selected pod's folder (pre-filter), else the pods root.
func (m model) capsDir() string {
	if m.capsCwd != "" {
		return m.capsCwd
	}
	if m.t.Pod != "" {
		p := filepath.Join(capsRoot(m.kit), m.t.Pod)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return capsRoot(m.kit)
}

func fetchCaps(kit, dir string) tea.Cmd {
	return func() tea.Msg {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return capsMsg{dir: dir} // dir may not exist yet — empty, not an error
		}
		var caps []capEntry
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "session-") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			ce := capEntry{Name: e.Name(), Mod: info.ModTime(), Dir: e.IsDir()}
			if ce.Dir {
				full := filepath.Join(dir, e.Name())
				ce.Size = dirSize(full)
				if _, err := os.Stat(filepath.Join(full, ".snapshot")); err == nil {
					ce.Snap = true
				}
			} else {
				ce.Size = info.Size()
				if strings.HasSuffix(ce.Name, ".hprof") && !hprofValid(filepath.Join(dir, e.Name())) {
					ce.Invalid = true
				}
			}
			caps = append(caps, ce)
		}
		// folders first (they're the sessions/pods to drill into), then files;
		// each newest-first
		sort.SliceStable(caps, func(i, j int) bool {
			if caps[i].Dir != caps[j].Dir {
				return caps[i].Dir
			}
			return caps[i].Mod.After(caps[j].Mod)
		})
		return capsMsg{dir: dir, entries: caps}
	}
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// capHint names the next step for an artifact so juniors don't have to know
// which desktop tool opens which file.
func capHint(ce capEntry) string {
	switch {
	case ce.Dir:
		return "drill in"
	case strings.HasSuffix(ce.Name, ".hprof"):
		if ce.Invalid {
			return "⚠ not a heap dump — a explains"
		}
		return "a → histogram"
	case strings.HasSuffix(ce.Name, ".jfr"):
		return "JDK Mission Control"
	case strings.HasSuffix(ce.Name, ".txt"), strings.HasSuffix(ce.Name, ".json"):
		return "view · a"
	}
	return "view"
}

// capsHasUp: can the browser go up from here? (never above the pods root)
func (m model) capsHasUp() bool {
	return filepath.Clean(m.capsDir()) != filepath.Clean(capsRoot(m.kit))
}

// capsEntryAt maps a content-row index (0-based, below the title) to an
// action: ("up",""), ("dir"/"file", absPath), or ("","").
func (m model) capsEntryAt(row int) (kind, path string) {
	if m.capsHasUp() {
		if row == 0 {
			return "up", ""
		}
		row--
	}
	if row < 0 || row+m.capsOff >= len(m.caps) {
		return "", ""
	}
	ce := m.caps[row+m.capsOff]
	kind = "file"
	if ce.Dir {
		kind = "dir"
	}
	return kind, filepath.Join(m.capsDir(), ce.Name)
}

// capsHit: is (x,y) inside the CAPTURES pane, and on which content row
// (0-based, below the title)? Mirrors the layout math the renderer uses.
func (m model) capsHit(x, y int) (bool, int) {
	if m.tier() != 2 || m.scr != scMenu || !m.remote.OK {
		return false, 0
	}
	menuW, midW, evW := m.cols()
	x0 := menuW + midW + 4
	if x < x0 || x >= x0+evW {
		return false, 0
	}
	body := m.remoteBody()
	topH := strings.Count(body, "\n") + 1
	podH, evH, capH := rightHeights(topH)
	y0 := 3 + podH + evH // header rows + PODS + EVENTS above
	if y < y0+1 || y >= y0+capH {
		return false, 0 // y0 is the title row
	}
	return true, y - y0 - 1
}

// capsClick dispatches a click at content-row index.
func (m model) capsClick(row int) (tea.Model, tea.Cmd) {
	kind, path := m.capsEntryAt(row)
	switch kind {
	case "up":
		m.capsCwd = filepath.Dir(m.capsDir())
		m.capsOff = 0
		return m, fetchCaps(m.kit, m.capsCwd)
	case "dir":
		m.capsCwd = path
		m.capsOff = 0
		return m, fetchCaps(m.kit, m.capsCwd)
	case "file":
		return m.viewFile(path)
	}
	return m, nil
}

const viewCap = 256 << 10 // never load more than 256K of a file into the pane

// viewFile loads a capture into the output pane. Text is shown inline; binary
// (heap dumps) shows metadata + how to analyze, never raw bytes.
func (m model) viewFile(path string) (tea.Model, tea.Cmd) {
	info, err := os.Stat(path)
	if err != nil {
		m.out.notice = "can't open: " + firstLine(err.Error())
		return m, nil
	}
	f, err := os.Open(path)
	if err != nil {
		m.out.notice = "can't open: " + firstLine(err.Error())
		return m, nil
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	base := filepath.Base(path)
	var body string
	switch {
	case strings.HasSuffix(base, ".hprof"):
		// a .hprof is either a real heap dump or a captured error page; the
		// latter must NOT be sent to Eclipse MAT, so say so plainly.
		if strings.HasPrefix(string(head), "JAVA PROFILE") {
			body = fmt.Sprintf("%s — JVM heap dump, %s\n(not shown as text)\n\n"+
				"Press a to analyze it here (class histogram — the biggest memory consumers).\n"+
				"Deeper: Eclipse MAT → File → Open Heap Dump → 'Leak Suspects' (free, local).",
				base, fmtSize(info.Size()))
		} else {
			body = fmt.Sprintf("%s — ⚠ INVALID heap dump, %s\n\n"+
				"This file is NOT a heap dump, so Eclipse MAT can't open it — %s.\n"+
				"It's a capture-ROUTE problem, not a heap to analyze. Recover it:\n"+
				"  · secured / disabled actuator → set auth (k), or: jdebug heap --via jattach --confirm\n"+
				"  · app can't serve HTTP → jdebug heap --via jdk --confirm\n"+
				"  · wrong actuator URL / base path → fix it in the target editor (g)\n\n"+
				"Press a for the full first-pass classification.",
				base, fmtSize(info.Size()), classifyHead(head))
		}
	case isBinary(head):
		body = fmt.Sprintf("%s — binary file, %s\n(not shown as text)", base, fmtSize(info.Size()))
	default:
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			data, _ := io.ReadAll(io.LimitReader(f, viewCap))
			body = string(data)
			if info.Size() > viewCap {
				body += fmt.Sprintf("\n\n… truncated at %s — open the file for the rest: %s", fmtSize(viewCap), path)
			}
		}
	}

	m.out = outState{id: m.out.id + 1, title: base, display: path, filePath: path,
		done: true, ok: true, raw: []byte(body)}
	if m.mode == 1 && m.remote.OK && m.showLogPane() {
		m.out.show = true
	} else {
		m.prev = scMenu
		m.scr = scOutput
	}
	(&m).rewrapOut()
	return m, nil
}

func isBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// analyzeContext — `a` analyzes whatever is in view: the open file if one is
// loaded, otherwise the whole dumps tree.
func (m model) analyzeContext() (tea.Model, tea.Cmd) {
	if m.out.filePath != "" {
		return m.quickCLI(false, "analyze", m.out.filePath)
	}
	return m.quickCLI(false, "analyze")
}

// capsScope names what the browser is currently showing, so a junior always
// knows whether they're looking at this pod, all pods, or a drilled-in session.
func (m model) capsScope() string {
	if m.capsCwd == "" {
		if m.t.Pod != "" {
			return "this pod"
		}
		return "all pods"
	}
	if filepath.Clean(m.capsDir()) == filepath.Clean(capsRoot(m.kit)) {
		return "all pods"
	}
	return strings.TrimPrefix(m.capsDir(), capsRoot(m.kit)+string(filepath.Separator))
}

// capsRows renders exactly h rows at width w.
func (m model) capsRows(w, h int) []string {
	right := "click opens · a analyzes"
	if !m.capsWhen.IsZero() {
		right = "refreshed " + fmtAge(m.capsWhen) + " ago · a analyzes"
	}
	rows := []string{paneTitle(w, "CAPTURES", m.capsScope(), right)}
	visible := h - 1

	var lines []string
	if m.capsHasUp() {
		lines = append(lines, " "+cKey.Render(" ↑ ..")+cFaint.Render("  (up)"))
	}
	if len(m.caps) == 0 && !m.capsHasUp() {
		lines = append(lines, " "+cFaint.Render("– nothing captured yet –"))
	}
	end := m.capsOff + visible
	if end > len(m.caps) {
		end = len(m.caps)
	}
	if m.capsOff < 0 {
		m.capsOff = 0
	}
	for _, ce := range m.caps[mini(m.capsOff, len(m.caps)):end] {
		name := ce.Name
		if ce.Dir {
			name += "/"
		}
		right := fmt.Sprintf("%6s %4s", fmtSize(ce.Size), fmtAge(ce.Mod))
		if hint := capHint(ce); hint != "" && w >= 60 {
			right += " · " + hint
		}
		nameW := w - lipgloss.Width(right) - 3
		if nameW < 8 {
			nameW = 8
		}
		st := cMuted
		if ce.Dir {
			st = cBody
		}
		disp := ansi.Truncate(name, nameW, "…")
		if ce.Snap {
			disp = ansi.Truncate("▸ "+name, nameW, "…")
		}
		if ce.Invalid {
			st = cWarn
			disp = ansi.Truncate("⚠ "+name, nameW, "…")
		}
		pad := w - 1 - lipgloss.Width(disp) - lipgloss.Width(right) - 1
		if pad < 1 {
			pad = 1
		}
		lines = append(lines, " "+st.Render(disp)+strings.Repeat(" ", pad)+cFaint.Render(right))
	}
	rows = append(rows, lines...)
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}
