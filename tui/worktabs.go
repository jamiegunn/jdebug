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
	tabCaptures
	numWorkTabs
)

type wtab struct {
	id    int
	label string
}

// workTabList is the ordered set of bottom tabs with their live labels (counts
// and status glyphs baked in). One source of truth for the strip renderer and
// the click hit-tester so their column math can never disagree.
func (m model) workTabList() []wtab {
	work := "WORK"
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
	ev := "EVENTS"
	if warn > 0 {
		ev = fmt.Sprintf("EVENTS %dW", warn)
	}
	caps := "CAPTURES"
	if n := len(m.caps); n > 0 {
		caps = fmt.Sprintf("CAPTURES %d", n)
	}
	return []wtab{{tabWork, work}, {tabLogs, "LOGS"}, {tabEvents, ev}, {tabCaptures, caps}}
}

// workTabStrip is the one-line header replacing the individual pane titles: the
// tabs (active bracketed), lightweight counts, and a per-tab context hint.
func (m model) workTabStrip(w int) string {
	render := func(t wtab) string {
		if m.workTab == t.id {
			return cKey.Render("[" + t.label + "]")
		}
		return cFaint.Render(" " + t.label + " ")
	}
	var parts []string
	for _, t := range m.workTabList() {
		parts = append(parts, render(t))
	}
	left := " " + strings.Join(parts, cFaint.Render("│"))

	right := "click a tab · tab/shift-tab"
	switch m.workTab {
	case tabWork:
		right = m.outStatus(true) + " · C copy · esc stops/dismisses"
	case tabLogs:
		right = "live tail · f expand · click/tab to switch"
	case tabEvents:
		right = "pod events · click/tab to switch"
	case tabCaptures:
		right = m.capsScope() + " · click opens · a analyzes"
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
	sy, _, ok := m.bottomGeom()
	if !ok || y != sy {
		return 0, false
	}
	x0 := 1 // the strip's one leading space
	for _, t := range m.workTabList() {
		wdt := lipgloss.Width(t.label) + 2
		if x >= x0 && x < x0+wdt {
			return t.id, true
		}
		x0 += wdt + 1 // + the │ separator
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
	m.workTab = (m.workTab + dir + numWorkTabs) % numWorkTabs
}
