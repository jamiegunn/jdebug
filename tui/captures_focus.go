package main

// captures_focus.go — the full-screen captures browser (d). The dashboard pane
// is a compact click-to-drill preview; this is the keyboard-navigable view a
// junior can trust: a FLAT list of every capture for the pod (newest first,
// across sessions), with filter tabs, an explicit scope + "last refreshed", and
// invalid-heap markers. ↑↓ select · ↵ open · a analyze · Tab filter · esc back.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

var capsFilters = []string{"all", "heaps", "threads", "logs", "snapshots"}

type capsFlatMsg struct{ entries []capEntry }

// capsFocusRoot is the tree the flat browser scans: the selected pod, else all.
func (m model) capsFocusRoot() string {
	if m.t.Pod != "" {
		return filepath.Join(capsRoot(m.kit), m.t.Pod)
	}
	return capsRoot(m.kit)
}

// fetchCapsFlat walks the pod's capture tree and returns every file as one entry
// (Name = "<session>/<file>"), newest first — the source list the focus browser
// filters in memory.
func fetchCapsFlat(kit, root string) tea.Cmd {
	return func() tea.Msg {
		var out []capEntry
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return nil
			}
			sdir := filepath.Dir(p)
			ce := capEntry{Name: filepath.Base(sdir) + "/" + d.Name(), Path: p,
				Size: info.Size(), Mod: info.ModTime()}
			if _, e := os.Stat(filepath.Join(sdir, ".snapshot")); e == nil {
				ce.Snap = true
			}
			if strings.HasSuffix(d.Name(), ".hprof") && !hprofValid(p) {
				ce.Invalid = true
			}
			out = append(out, ce)
			return nil
		})
		sort.SliceStable(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
		return capsFlatMsg{entries: out}
	}
}

// capFileKind classifies a capture file for the filter tabs.
func capFileKind(ce capEntry) string {
	n := strings.ToLower(ce.Name)
	switch {
	case strings.HasSuffix(n, ".hprof"):
		return "heaps"
	case strings.Contains(n, "thread"):
		return "threads"
	case strings.Contains(n, "log"):
		return "logs"
	}
	return "other"
}

func capMatchesFilter(ce capEntry, filter string) bool {
	switch filter {
	case "all", "":
		return true
	case "snapshots":
		return ce.Snap
	default:
		return capFileKind(ce) == filter
	}
}

// capsFocusList is the filtered view over the flat list.
func (m model) capsFocusList() []capEntry {
	var out []capEntry
	for _, ce := range m.capsFlat {
		if capMatchesFilter(ce, m.capsFilter) {
			out = append(out, ce)
		}
	}
	return out
}

func cycleFilter(cur string, dir int) string {
	i := 0
	for k, f := range capsFilters {
		if f == cur {
			i = k
		}
	}
	i = (i + dir + len(capsFilters)) % len(capsFilters)
	return capsFilters[i]
}

// openCapsFocus enters the browser and refreshes its flat list.
func (m model) openCapsFocus() (tea.Model, tea.Cmd) {
	m.capsFocus = true
	m.capsSel = 0
	if m.capsFilter == "" {
		m.capsFilter = "all"
	}
	return m, fetchCapsFlat(m.kit, m.capsFocusRoot())
}

func (m model) capsFocusKey(key string) (tea.Model, tea.Cmd) {
	list := m.capsFocusList()
	switch key {
	case "d", "D", "esc", "q", "Q":
		m.capsFocus = false
		m.capsSel = 0
		return m, nil
	case "up", "k":
		if m.capsSel > 0 {
			m.capsSel--
		}
	case "down", "j":
		if m.capsSel < len(list)-1 {
			m.capsSel++
		}
	case "tab", "right", "l":
		m.capsFilter = cycleFilter(m.capsFilter, +1)
		m.capsSel = 0
	case "shift+tab", "left", "h":
		m.capsFilter = cycleFilter(m.capsFilter, -1)
		m.capsSel = 0
	case "r":
		return m, fetchCapsFlat(m.kit, m.capsFocusRoot())
	case "enter":
		if m.capsSel < len(list) {
			mm, cmd := m.viewFile(list[m.capsSel].Path)
			m2 := mm.(model)
			m2.capsFocus = false // drop back to the dashboard to show the file
			return m2, cmd
		}
	case "a", "A":
		if m.capsSel < len(list) {
			m.capsFocus = false
			return m.quickCLI(false, "analyze", list[m.capsSel].Path)
		}
	}
	return m, nil
}

func (m model) capsFocusView() string {
	w := m.tw()
	head := m.headerRemote(m.remote.Cluster)
	scope := "this pod"
	if m.t.Pod == "" {
		scope = "all pods"
	}
	refreshed := "…"
	if !m.capsWhen.IsZero() {
		refreshed = fmtAge(m.capsWhen) + " ago"
	}
	title := " " + cDim.Render("CAPTURES") + "  " + cFaint.Render(scope+" · refreshed "+refreshed)

	// filter tabs, active one highlighted
	var tabs []string
	for _, f := range capsFilters {
		if f == m.capsFilter {
			tabs = append(tabs, cKey.Render("["+f+"]"))
		} else {
			tabs = append(tabs, cFaint.Render(f))
		}
	}
	tabLine := " " + strings.Join(tabs, cFaint.Render(" "))

	list := m.capsFocusList()
	h := m.height - 6
	if m.height == 0 {
		h = 20
	}
	if h < 4 {
		h = 4
	}
	var rows []string
	if len(list) == 0 {
		rows = append(rows, " "+cFaint.Render("– no captures match this filter –"))
	}
	// keep the selection in view
	start := 0
	if m.capsSel >= h {
		start = m.capsSel - h + 1
	}
	for i := start; i < len(list) && i < start+h; i++ {
		ce := list[i]
		mark := "  "
		st := cMuted
		if ce.Invalid {
			mark = cWarn.Render("⚠ ")
			st = cWarn
		} else if ce.Snap {
			mark = cKey.Render("▸ ")
		}
		next := capHint(ce)
		meta := cFaint.Render(fmtSize(ce.Size) + " · " + fmtAge(ce.Mod) + " · " + next)
		line := mark + st.Render(ce.Name) + "  " + meta
		line = ansi.Truncate(line, w-2, "…")
		if i == m.capsSel {
			line = cFocus.Render(ansi.Truncate(" "+mark+st.Render(ce.Name)+"  "+meta, w, "…"))
		} else {
			line = " " + line
		}
		rows = append(rows, line)
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	foot := " " + cFaint.Render("↑↓ select · ↵ open · a analyze · Tab filter · r refresh · d/esc back")
	body := strings.Join(rows[:h], "\n")
	return head + "\n" + title + "\n" + tabLine + "\n" + body + "\n" + foot
}
