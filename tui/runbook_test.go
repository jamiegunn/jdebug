package main

import (
	"strings"
	"testing"
	"time"
)

func TestRunbookActiveSignals(t *testing.T) {
	// the demo panel is crash-looping + OOMKilled + at-max + mem 94% → several
	// cards, each with a safe command and a "what to tell the next person"
	m := readyModel()
	cards := m.activeRunbooks()
	if len(cards) < 3 {
		t.Fatalf("a multi-symptom panel should fire several runbook cards, got %d", len(cards))
	}
	for _, c := range cards {
		if c.safeCmd == "" || c.tellNext == "" {
			t.Fatalf("runbook card %q must name a safe command and a tell-next line", c.signal)
		}
	}
	v := ansiStrip(m.runbookView())
	for _, want := range []string{"OOMKilled", "safe", "disruptive", "tell next"} {
		if !strings.Contains(v, want) {
			t.Fatalf("runbook view missing %q:\n%s", want, v)
		}
	}
	// a healthy panel has no active signals → the view falls back to the reference
	h := readyModel()
	h.panel = panelData{When: time.Now(), Phase: "Running", ActuatorOK: true}
	if got := h.activeRunbooks(); len(got) != 0 {
		t.Fatalf("a healthy panel must fire no runbook cards, got %d", len(got))
	}
	if !strings.Contains(ansiStrip(h.runbookView()), "full reference") {
		t.Fatal("with nothing wrong the runbook must show the full reference, not an empty screen")
	}
}

func TestRunbookKeyOpensAndEscalates(t *testing.T) {
	out := press(t, readyModel(), "n")
	if out.(model).scr != scRunbook {
		t.Fatal("n must open the runbook")
	}
	// E jumps straight to the escalation handoff
	e, cmd := out.(model).Update(key("E"))
	if cmd == nil || !strings.Contains(e.(model).out.title, "escalate") {
		t.Fatalf("E from the runbook must build the escalation handoff, got %q", e.(model).out.title)
	}
	// any other key returns to the menu
	if back := press(t, out.(model), "x"); back.(model).scr != scMenu {
		t.Fatal("any key must dismiss the runbook")
	}
}
