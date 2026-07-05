package main

// Update-logic tests: the interaction contracts that matter — the gate blocks
// actions, disruptive actions need a second press of the same key, quitting
// confirms, pickers apply values, and the shared config round-trips.

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func readyModel() model {
	m := demoModel()
	m.scr = scMenu
	return m
}

func press(t *testing.T, m tea.Model, keys ...string) tea.Model {
	t.Helper()
	for _, k := range keys {
		m, _ = m.Update(key(k))
	}
	return m
}

func TestGateBlocksActions(t *testing.T) {
	m := demoModel()
	m.scr = scMenu
	m.remote = probe{OK: false, Cluster: true, When: time.Now().Add(time.Hour)}
	out := press(t, m, "s")
	mm := out.(model)
	if mm.scr != scMenu {
		t.Fatalf("gated menu must swallow action keys, got screen %v", mm.scr)
	}
	if !strings.Contains(mm.menuView(), "SET UP YOUR TARGET FIRST") {
		t.Fatal("gate panel missing")
	}
	if strings.Contains(mm.menuView(), "guided diagnosis") {
		t.Fatal("tools must be hidden while gated")
	}
}

func TestHeapNeedsSecondPress(t *testing.T) {
	m := readyModel()
	out := press(t, m, "H")
	mm := out.(model)
	if mm.scr != scConfirm || !strings.Contains(mm.confirmMsg, "press H again") {
		t.Fatalf("H must ask for a second H, got %q on screen %v", mm.confirmMsg, mm.scr)
	}
	// any other key cancels
	out = press(t, mm, "z")
	mm = out.(model)
	if mm.scr != scMenu || mm.pendHeap {
		t.Fatal("non-H key must cancel the heap confirm")
	}
	// second H proceeds to the tier pick
	out = press(t, readyModel(), "H", "H")
	mm = out.(model)
	if mm.scr != scVia || !mm.pendHeap {
		t.Fatalf("H,H must open the tier pick with heap pending, got screen %v", mm.scr)
	}
}

func TestQuitConfirms(t *testing.T) {
	out := press(t, readyModel(), "q")
	mm := out.(model)
	if mm.scr != scConfirm || !strings.Contains(mm.confirmMsg, "quit") {
		t.Fatal("q must ask before quitting")
	}
	out = press(t, mm, "n")
	if out.(model).scr != scMenu {
		t.Fatal("declining quit must return to the menu")
	}
}

func TestSnapshotDeclineStillRuns(t *testing.T) {
	m := readyModel()
	out := press(t, m, "x")
	mm := out.(model)
	if mm.scr != scConfirm {
		t.Fatal("x must ask about including a heap dump")
	}
	_, cmd := mm.Update(key("n"))
	if cmd == nil {
		t.Fatal("declining the heap question must still run a plain snapshot")
	}
}

func TestPickerAppliesNamespace(t *testing.T) {
	m := readyModel()
	m.scr = scEditor
	m.pick = picker{title: "Namespace", items: []string{"default", "payments"}, kind: pickNamespace}
	m.scr = scPicker
	out := press(t, m, "2")
	mm := out.(model)
	if mm.t.Namespace != "payments" || mm.t.Pod != "" {
		t.Fatalf("picking a namespace must apply it and clear the pod pin, got %q/%q", mm.t.Namespace, mm.t.Pod)
	}
}

func TestAnyPodClearsSelector(t *testing.T) {
	m := readyModel()
	m.t.Selector = "app=payments"
	m.pick = picker{items: []string{"<any pod>", "app=payments"}, kind: pickSelector}
	m.scr = scPicker
	out := press(t, m, "1")
	if out.(model).t.Selector != "" {
		t.Fatal("<any pod> must clear the selector")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JDEBUG_CONFIG_DIR", dir)
	want := target{Namespace: "debug-demo", Selector: "app=x", Container: "app",
		Actuator: "http://localhost:9001/manage", Pod: "pod-b"}
	saveTarget(want)
	got := loadTarget()
	if got != want {
		t.Fatalf("config round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	// bash-compat: file must be sourceable single-quoted assignments
	data, _ := os.ReadFile(dir + "/target")
	if !strings.Contains(string(data), "SAVED_POD='pod-b'") {
		t.Fatalf("config not bash-compatible:\n%s", data)
	}
}

func TestMenuParityStrings(t *testing.T) {
	v := demoModel()
	v.scr = scMenu
	out := v.menuView()
	for _, want := range []string{"INSPECT", "CAPTURE", "LOGS", "guided diagnosis",
		"pauses app", "safe / caution / disruptive", "❯", "[?] help"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu missing %q", want)
		}
	}
	if strings.Contains(out, "fastthread") {
		t.Error("no cloud analyzers may be recommended")
	}
}

func TestVerbosityFlow(t *testing.T) {
	out := press(t, readyModel(), "v")
	mm := out.(model)
	if mm.scr != scInput {
		t.Fatal("v must open the logger input")
	}
	out = press(t, mm, "R", "O", "O", "T", "enter")
	mm = out.(model)
	if mm.scr != scLevel || mm.logger != "ROOT" {
		t.Fatalf("logger entry must lead to the level pick, got %v/%q", mm.scr, mm.logger)
	}
	_, cmd := mm.Update(key("2"))
	if cmd == nil {
		t.Fatal("picking a level must run log-level")
	}
}
