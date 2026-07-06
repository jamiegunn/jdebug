package main

// layout.go — dashboard v3 geometry. Three tiers by terminal size:
//
//	tier 0  (<104 cols)              compact single column, no side panel
//	tier 1  (104–139 cols, or short) menu + TARGET LIVE sidebar; a full-width
//	                                 log strip appears when there's height
//	tier 2  (≥140 cols and ≥34 rows) the full grid — menu | live panel +
//	                                 trends + NEXT | events + captures, with
//	                                 the log tail filling the rest
//
// Height comes from tea.WindowSizeMsg; 0 means "never measured" (-render,
// odd terminals) and always falls back to content-height rendering.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// tw is the rendering width: never squeeze below 78. Remote/incident mode
// (mode 1) fills the WHOLE terminal — no cap — so wide monitors get every
// column and long values (pod names, actuator URLs) stop truncating. Local
// mode keeps the classic 132 so plain command output stays readable.
func (m model) tw() int {
	w := m.width
	if w < 78 {
		w = 78
	}
	if m.mode == 1 {
		return w // fill the screen
	}
	if w > 132 {
		w = 132
	}
	return w
}

func (m model) tier() int {
	if m.tw() < 104 {
		return 0
	}
	if m.mode == 1 && m.width >= 140 && m.height >= 34 {
		return 2
	}
	return 1
}

// showPanel: the live target panel needs room; drop it on narrow terminals.
func (m model) showPanel() bool { return m.tier() >= 1 }

// showLogPane: the live log strip needs a pod to tail and vertical room.
func (m model) showLogPane() bool {
	return m.mode == 1 && m.t.Pod != "" && m.tier() >= 1 && m.height >= 34
}

// leftW is the width the menu body uses (full width minus the panel column).
func (m model) leftW() int {
	switch m.tier() {
	case 2:
		w, _, _ := m.cols()
		return w
	case 1:
		return m.tw() - panelW - 2
	}
	return m.tw()
}

// cols splits tier-2 width into menu | mid (target/trends/next) | events,
// with two 2-char gutters. At 140: 62/38/36 · at 200: 78/46/72.
// cols splits the tier-2 width into three EQUAL columns (menu | mid | right),
// each ~1/3, separated by two 2-col gutters. The remainder lands in the right
// column so menuW+midW+evW+4 == tw() exactly.
func (m model) cols() (menuW, midW, evW int) {
	inner := m.tw() - 4 // two 2-col gutters between the three columns
	menuW = inner / 3
	midW = inner / 3
	evW = inner - menuW - midW
	return
}

// overlayLines: screens that draw *under* the menu (confirm, via, jcmd,
// level) add lines below the prompt; a fixed-height frame must reserve them
// or the header scrolls off the top.
func (m model) overlayLines() int {
	switch m.scr {
	case scConfirm:
		return 1
	case scVia:
		return 2
	case scLevel:
		return 1
	case scJcmd:
		return 2 + len(jcmdPicks)
	}
	return 0
}

// paneTitle renders " LABEL  sub ────────── right" clipped to w.
func paneTitle(w int, label, sub, right string) string {
	s := " " + cDim.Render(label)
	r := ""
	if right != "" {
		r = " " + cFaint.Render(right)
	}
	if sub != "" {
		// fit the subtitle (often a pod name) to whatever's left after the
		// label, the right hint, the gaps, and a minimum rule — so it shrinks
		// with an ellipsis instead of overflowing the column
		avail := w - lipgloss.Width(s) - lipgloss.Width(r) - 2 - 3 - 2
		if avail < 6 {
			avail = 6
		}
		s += "  " + cFaint.Render(ansi.Truncate(sub, avail, "…"))
	}
	fill := w - lipgloss.Width(s) - lipgloss.Width(r) - 2
	if fill < 3 {
		fill = 3
	}
	return s + " " + cRule.Render(strings.Repeat("─", fill)) + r
}

// dashboardView is the tier-2 frame: exactly m.height lines (minus whatever
// overlay the current screen appends), so the altscreen never scrolls.
func (m model) dashboardView() string {
	w := m.tw()
	prefix := m.dashPrefix()
	suffix := "\n" + m.footer("[a] analyze  [c] check setup  [?] help  [q] quit") + prompt()
	logH := m.height - m.overlayLines() - (strings.Count(prefix, "\n") + 1) - strings.Count(suffix, "\n") - 1
	if m.showLogPane() && logH >= 6 {
		return prefix + "\n" + rule(w) + "\n" + m.bottomPane(w, logH) + suffix
	}
	return prefix + suffix
}

// dashPrefix is everything the tier-2 dashboard draws above the bottom rule:
// the header over the three top columns. Shared with bottomStripY so the click
// math and the render can never disagree about where the work strip lands.
func (m model) dashPrefix() string {
	menuW, midW, evW := m.cols()
	body := m.remoteBody()
	topH := strings.Count(body, "\n") + 1
	left := lipgloss.NewStyle().Width(menuW).Render(body)
	mid := lipgloss.NewStyle().Width(midW).Render(m.panelView(midW, topH, true))
	right := lipgloss.NewStyle().Width(evW).Render(strings.Join(m.rightColumn(evW, topH), "\n"))
	top := lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", mid, "  ", right)
	return m.headerRemote(m.remote.Cluster) + "\n" + top
}

// bottomGeom returns the screen row of the WORK/LOGS/EVENTS/CAPTURES tab strip,
// the total height of the bottom work pane (strip + body), and whether it's on
// screen. It reconstructs the same "prefix" both render paths build above the
// rule (dashboardView for tier 2, the generic menu view for tier 1); the strip
// sits one row below that rule, and the body fills the rows beneath it.
func (m model) bottomGeom() (stripY, paneH int, ok bool) {
	if m.scr != scMenu || !m.remote.OK || !m.showLogPane() || m.capsFocus || m.logs.focus {
		return 0, 0, false
	}
	var prefix string
	if m.tier() == 2 {
		prefix = m.dashPrefix()
	} else {
		prefix = m.headerRemote(m.remote.Cluster) + "\n" + m.withPanel(m.remoteBody())
	}
	prefixLines := strings.Count(prefix, "\n") + 1
	suffix := "\n" + m.footer("[a] analyze  [c] check setup  [?] help  [q] quit") + prompt()
	logH := m.height - m.overlayLines() - prefixLines - strings.Count(suffix, "\n") - 1
	if logH < 6 {
		return 0, 0, false
	}
	return prefixLines + 1, logH, true // + 1 for the rule row between prefix and the strip
}

// rightColumn stacks PODS and WORKLOAD into exactly h rows. (Captures moved to
// the bottom CAPTURES tab, so the right column is no longer split three ways.)
func (m model) rightColumn(w, h int) []string {
	podH, workH := rightHeights(h)
	rows := m.podsRows(w, podH)
	rows = append(rows, m.workloadRows(w, workH)...)
	return rows
}
