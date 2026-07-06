package main

import (
	"strings"
	"testing"
)

func TestWorkTabsCycleAndContent(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	if m.workTab != tabLogs {
		t.Fatalf("the bottom work area must default to the LOGS tab, got %d", m.workTab)
	}
	// tab cycles LOGS → EVENTS → CAPTURES → TRENDS → WORK → LOGS, no menu action
	ev := press(t, m, "tab").(model)
	if ev.workTab != tabEvents {
		t.Fatalf("tab must advance to EVENTS, got %d", ev.workTab)
	}
	if !strings.Contains(ev.menuView(), "Back-off restarting") {
		t.Fatal("the EVENTS tab must show recent pod events")
	}
	cap := press(t, ev, "tab").(model)
	if cap.workTab != tabCaptures {
		t.Fatalf("tab must advance to CAPTURES, got %d", cap.workTab)
	}
	if !strings.Contains(ansiStrip(cap.workTabStrip(cap.tw())), "[CAPTURES") {
		t.Fatal("the CAPTURES tab must be the bracketed active tab once selected")
	}
	tr := press(t, cap, "tab").(model)
	if tr.workTab != tabTrends {
		t.Fatalf("tab must advance to TRENDS, got %d", tr.workTab)
	}
	if !strings.Contains(tr.menuView(), "heap") {
		t.Fatal("the TRENDS tab must show the metric rows (heap, mem, cpu…)")
	}
	wk := press(t, tr, "tab").(model)
	if wk.workTab != tabWork {
		t.Fatalf("tab must advance to WORK, got %d", wk.workTab)
	}
	if !strings.Contains(wk.menuView(), "no command run yet") {
		t.Fatal("the WORK tab must show an empty state before anything runs")
	}
	if press(t, wk, "tab").(model).workTab != tabLogs {
		t.Fatal("tab must wrap back to LOGS")
	}
	// shift-tab steps backward: LOGS → WORK
	if press(t, m, "shift+tab").(model).workTab != tabWork {
		t.Fatal("shift+tab from LOGS must step back to WORK")
	}
}

func TestWorkTabStripMarksActiveAndCounts(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	strip := ansiStrip(m.workTabStrip(m.tw()))
	// active tab bracketed (legible without colour); events warning count shown
	if !strings.Contains(strip, "[LOGS]") {
		t.Fatalf("active tab must be bracketed:\n%s", strip)
	}
	if !strings.Contains(strip, "EVENTS 2W") {
		t.Fatalf("the events tab must show its warning count:\n%s", strip)
	}
}
