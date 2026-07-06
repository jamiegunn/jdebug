package main

// worktabs.go — the bottom work area, split into three tabs so its three jobs
// stop competing: WORK (the command you launched + its output), LOGS (the live
// tail), EVENTS (recent pod events, back from the right column the WORKLOAD pane
// reclaimed). tab/shift-tab switch; a launched command auto-selects WORK. The
// active tab is marked with brackets — legible without colour.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	tabWork = iota
	tabLogs
	tabEvents
)

// workTabLabels returns the plain (unstyled) text of the three tab labels —
// shared by the renderer and the click hit-tester so their widths always agree.
func (m model) workTabLabels() (work, logs, ev string) {
	work = "WORK"
	if m.out.running {
		work = "WORK ●"
	} else if m.out.id != 0 {
		work = "WORK ✓"
	}
	warn := 0
	for _, e := range m.events {
		if e.Type == "Warning" {
			warn++
		}
	}
	ev = "EVENTS"
	if warn > 0 {
		ev = fmt.Sprintf("EVENTS %dW", warn)
	}
	return work, "LOGS", ev
}

// workTabStrip is the one-line header replacing the individual pane titles: the
// three tabs (active bracketed), lightweight counts, and a per-tab context hint.
func (m model) workTabStrip(w int) string {
	tab := func(id int, label string) string {
		if m.workTab == id {
			return cKey.Render("[" + label + "]")
		}
		return cFaint.Render(" " + label + " ")
	}
	work, logs, ev := m.workTabLabels()
	left := " " + tab(tabWork, work) + cFaint.Render("│") + tab(tabLogs, logs) + cFaint.Render("│") + tab(tabEvents, ev)

	right := "click a tab · tab/shift-tab"
	switch m.workTab {
	case tabWork:
		right = m.outStatus(true) + " · C copy · esc stops/dismisses"
	case tabLogs:
		right = "live tail · f expand · click/tab to switch"
	case tabEvents:
		right = "pod events · click/tab to switch"
	}
	fill := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if fill < 1 {
		fill = 1
	}
	return left + strings.Repeat(" ", fill) + cFaint.Render(right) + " "
}

// workTabHit maps a left-click at (x,y) to a tab id, if it landed on the strip.
// The tab X ranges mirror workTabStrip's layout: a leading space, then each
// label rendered [n] or ␣n␣ (both 2 cols wider than the label), │-separated.
func (m model) workTabHit(x, y int) (int, bool) {
	sy, ok := m.bottomStripY()
	if !ok || y != sy {
		return 0, false
	}
	work, logs, ev := m.workTabLabels()
	x0 := 1 // the strip's one leading space
	for _, t := range []struct {
		id, w int
	}{
		{tabWork, lipgloss.Width(work) + 2},
		{tabLogs, lipgloss.Width(logs) + 2},
		{tabEvents, lipgloss.Width(ev) + 2},
	} {
		if x >= x0 && x < x0+t.w {
			return t.id, true
		}
		x0 += t.w + 1 // + the │ separator
	}
	return 0, false
}

// workPane is the WORK tab body: the launched command's output, or an empty
// state before anything has run.
func (m model) workPane(w, h int) string {
	if m.out.id == 0 {
		rows := []string{paneTitle(w, "WORK", "", ""),
			" " + cFaint.Render("– no command run yet — pick an action from the menu; its output lands here –")}
		for len(rows) < h {
			rows = append(rows, "")
		}
		return strings.Join(rows[:h], "\n")
	}
	return m.outPane(w, h)
}

// cycleWorkTab moves to the next/previous bottom tab.
func (m *model) cycleWorkTab(dir int) {
	m.workTab = (m.workTab + dir + 3) % 3
}
