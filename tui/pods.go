package main

// pods.go — the PODS pane: every pod the selector matches (whole namespace
// when the selector is empty or matches nothing), refreshed on the 20s tick.
// Click a pod to retarget everything at it; wheel scrolls the list.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type podsMsg struct {
	lines []string // "name  phase  restarts=N"
	scope string   // "selector" or "namespace" (fallback)
	err   string
}

func fetchPodList(t target) tea.Cmd {
	return func() tea.Msg {
		if t.Selector != "" {
			res := podsWithStatusE(t.Namespace, t.Selector)
			if res.err == "" && len(res.items) > 0 {
				return podsMsg{lines: res.items, scope: "selector"}
			}
		}
		// no/invalid selector: show the whole namespace
		res := podsWithStatusE(t.Namespace, "")
		if res.err != "" {
			return podsMsg{err: res.err}
		}
		return podsMsg{lines: res.items, scope: "namespace"}
	}
}

// podsRows renders exactly h rows at width w.
func (m model) podsRows(w, h int) []string {
	scope := m.podsScope
	if scope == "" {
		scope = "…"
	}
	right := "click switches · " + scope
	rows := []string{paneTitle(w, "PODS", m.t.Namespace, right)}
	visible := h - 1
	switch {
	case m.podsErr != "" && len(m.pods) == 0:
		rows = append(rows, " "+cFaint.Render("– pods unavailable: "+m.podsErr+" –"))
	case len(m.pods) == 0:
		rows = append(rows, " "+cFaint.Render("– no pods here –"))
	default:
		off := m.podsOff
		if max := len(m.pods) - visible; off > max {
			off = max
		}
		if off < 0 {
			off = 0
		}
		end := off + visible
		if end > len(m.pods) {
			end = len(m.pods)
		}
		for _, line := range m.pods[off:end] {
			name := strings.Fields(line)[0]
			mark, st := "  ", cMuted
			if name == m.t.Pod {
				mark, st = "▸ ", cBody
			}
			warn := strings.Contains(line, "restarts=") && !strings.Contains(line, "restarts=0")
			if strings.Contains(line, "Running") && !warn {
				rows = append(rows, " "+st.Render(mark+ansi.Truncate(line, w-4, "…")))
			} else {
				rows = append(rows, " "+cWarn.Render(mark)+st.Render(ansi.Truncate(line, w-4, "…")))
			}
		}
		if hidden := len(m.pods) - end; hidden > 0 {
			rows[len(rows)-1] = " " + cFaint.Render(fmt.Sprintf("… +%d more — wheel scrolls", hidden+1))
		}
	}
	for len(rows) < h {
		rows = append(rows, "")
	}
	return rows[:h]
}

// podsClickTarget maps a click at terminal cell (x, y) to a pod name, or ""
// when the click is outside the pods pane. Geometry is recomputed from the
// same deterministic layout math the renderer uses.
func (m model) podsClickTarget(x, y int) string {
	inside, row := m.podsHit(x, y)
	if !inside || row < 1 { // row 0 is the title
		return ""
	}
	off := m.podsOff
	if max := len(m.pods) - (m.podsPaneH() - 1); off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}
	i := off + row - 1
	if i < 0 || i >= len(m.pods) {
		return ""
	}
	return strings.Fields(m.pods[i])[0]
}

// podsHit: is (x,y) inside the pods pane, and on which of its rows?
func (m model) podsHit(x, y int) (bool, int) {
	if m.tier() != 2 || m.scr != scMenu || !m.remote.OK {
		return false, 0
	}
	menuW, midW, evW := m.cols()
	x0 := menuW + midW + 4
	if x < x0 || x >= x0+evW {
		return false, 0
	}
	y0 := 3 // header is three rows
	if y < y0 || y >= y0+m.podsPaneH() {
		return false, 0
	}
	return true, y - y0
}

// right-column vertical split: PODS on top, WORKLOAD context, then CAPTURES.
func rightHeights(topH int) (podH, workH, capH int) {
	podH = topH * 2 / 5
	workH = (topH - podH) / 2
	capH = topH - podH - workH
	return
}

func (m model) podsPaneH() int {
	body := m.remoteBody()
	topH := strings.Count(body, "\n") + 1
	podH, _, _ := rightHeights(topH)
	return podH
}

// panelHit: is (x,y) inside the mid TARGET-LIVE panel? Clicking it drills
// into the pod deep-dive (`why`), which decodes the exact fields shown —
// last-exit code, limits, probes, and autoscale — in plain language.
func (m model) panelHit(x, y int) bool {
	if m.tier() != 2 || m.scr != scMenu || !m.remote.OK {
		return false
	}
	menuW, midW, _ := m.cols()
	x0 := menuW + 2
	if x < x0 || x >= x0+midW {
		return false
	}
	topH := strings.Count(m.remoteBody(), "\n") + 1
	return y >= 3 && y < 3+topH // the mid column (TARGET LIVE + TRENDS + NEXT)
}

// switchPod retargets everything at the clicked pod.
func (m model) switchPod(pod string) (tea.Model, tea.Cmd) {
	if pod == "" || pod == m.t.Pod {
		return m, nil
	}
	m.t.Pod = pod
	m.staleP = ""
	m.logs = logState{}
	m.hist = nil
	saveTarget(m.t)
	m.remote = remoteProbe(m.t)
	cmds := []tea.Cmd{m.panelFetch(m.bgMode == bgLive), fetchEvents(m.t), fetchPodList(m.t)}
	if !m.logBusy {
		cmds = append(cmds, m.logsFetch())
	}
	return m, tea.Batch(cmds...)
}
