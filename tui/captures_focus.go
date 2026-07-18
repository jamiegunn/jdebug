package main

// captures_focus.go — the full-screen captures browser (d). The dashboard pane
// is a compact click-to-drill preview; this is the keyboard-navigable view a
// junior can trust: a FLAT list of captures (newest first, across sessions),
// scoped to the current pod — or every pod for the "recent" filter — with
// filter tabs, a per-file route tag (actuator/jattach/jdk), an explicit scope +
// "last refreshed", and invalid-heap markers.
// ↑↓ select · ↵ open · a analyze · Tab filter · r refresh · esc back.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// capStampRe pulls the capture SESSION timestamp (YYYYMMDDThhmmssZ) out of an
// entry's session-dir name. That, not the file's mtime, is what "newest" means:
// mtimes collide when several files are written in the same instant (and their
// sub-second tie-break is filesystem/OS-dependent), so ordering by mtime alone
// is non-deterministic across platforms. The stamp is fixed-width, so a plain
// string compare orders it correctly.
var capStampRe = regexp.MustCompile(`\d{8}T\d{6}Z`)

func capStamp(ce capEntry) string { return capStampRe.FindString(ce.Name) }

// capNewer reports whether a sorts before b in newest-first order: by session
// stamp, then mtime, then path — a total, deterministic order.
func capNewer(a, b capEntry) bool {
	if sa, sb := capStamp(a), capStamp(b); sa != sb {
		return sa > sb
	}
	if !a.Mod.Equal(b.Mod) {
		return a.Mod.After(b.Mod)
	}
	return a.Path < b.Path
}

var capsFilters = []string{"all", "heaps", "threads", "logs", "snapshots", "recent"}

const recentCap = 40 // the "recent" (across-all-pods) view shows at most this many

type capsFlatMsg struct{ entries []capEntry }

// fetchCapsFlat walks the WHOLE captures tree (all pods) once and returns every
// file as one entry, newest first. capsFocusList then scopes to the current pod
// (or shows all pods for the "recent" filter) in memory — the view does no I/O.
func fetchCapsFlat(kit string) tea.Cmd {
	root := capsRoot(kit)
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
			// path under root is <pod>/<session>/<file>
			rel, _ := filepath.Rel(root, p)
			pod := ""
			if parts := strings.Split(rel, string(filepath.Separator)); len(parts) >= 1 {
				pod = parts[0]
			}
			ce := capEntry{Name: filepath.Base(sdir) + "/" + d.Name(), Path: p, Pod: pod,
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
		sort.SliceStable(out, func(i, j int) bool { return capNewer(out[i], out[j]) })
		return capsFlatMsg{entries: out}
	}
}

// capRoute extracts the capture tier from a filename (heap-actuator.hprof →
// actuator), so a junior can see which route produced each artifact.
func capRoute(name string) string {
	base := name
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	for _, r := range []string{"actuator", "jattach", "jdk"} {
		if strings.Contains(base, r) {
			return r
		}
	}
	return ""
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

// capsFocusList is the filtered view over the flat list. Every filter except
// "recent" is scoped to the current pod; "recent" spans all pods (capped).
func (m model) capsFocusList() []capEntry {
	if m.capsFilter == "recent" {
		out := m.capsFlat
		if len(out) > recentCap {
			out = out[:recentCap]
		}
		return out
	}
	var out []capEntry
	for _, ce := range m.capsFlat {
		if m.t.Pod != "" && ce.Pod != m.t.Pod {
			continue
		}
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
	return m, fetchCapsFlat(m.kit)
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
		return m, fetchCapsFlat(m.kit)
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
	if m.capsFilter == "recent" {
		scope = "recent · all pods"
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
		// in the cross-pod "recent" view, prefix each row with its pod
		disp := ce.Name
		if m.capsFilter == "recent" && ce.Pod != "" {
			disp = podShort(ce.Pod, 16) + " " + ce.Name
		}
		metaStr := fmtSize(ce.Size) + " · " + fmtAge(ce.Mod)
		if r := capRoute(ce.Name); r != "" {
			metaStr += " · " + r // which tier produced it: actuator/jattach/jdk
		}
		metaStr += " · " + capHint(ce)
		meta := cFaint.Render(metaStr)
		body := mark + st.Render(disp) + "  " + meta
		if i == m.capsSel {
			rows = append(rows, cFocus.Render(ansi.Truncate(" "+body, w, "…")))
		} else {
			rows = append(rows, " "+ansi.Truncate(body, w-2, "…"))
		}
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	foot := " " + cFaint.Render("↑↓ select · ↵ open · a analyze · Tab filter · r refresh · d/esc back")
	body := strings.Join(rows[:h], "\n")
	return head + "\n" + title + "\n" + tabLine + "\n" + body + "\n" + foot
}
