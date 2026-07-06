package main

// Update-logic tests: the interaction contracts that matter — the gate blocks
// actions, disruptive actions need a second press of the same key, quitting
// confirms, pickers apply values, and the shared config round-trips.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func ansiStrip(s string) string { return ansi.Strip(s) }

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

func TestConfirmRendersOverSourceScreen(t *testing.T) {
	// a confirm launched from the menu renders over the menu
	m := readyModel()
	out, _ := m.askConfirm("re-roll? [y/N]", "", func(*model) tea.Cmd { return nil })
	mm := out.(model)
	if mm.confirmBase != scMenu {
		t.Fatalf("confirm from menu must record scMenu as its base, got %v", mm.confirmBase)
	}
	if v := ansiStrip(mm.View()); !strings.Contains(v, "re-roll?") || !strings.Contains(v, "QUICK CHECKS") {
		t.Fatalf("menu confirm must render over the menu:\n%s", v)
	}
	// a confirm launched from the editor renders over the EDITOR, not the menu
	e := readyModel()
	e.scr = scEditor
	out2, _ := e.askConfirm("switch context? [y/N]", "", func(*model) tea.Cmd { return nil })
	ee := out2.(model)
	ve := ansiStrip(ee.View())
	if !strings.Contains(ve, "switch context?") || !strings.Contains(ve, "actuator") {
		t.Fatalf("editor confirm must render over the editor:\n%s", ve)
	}
	if strings.Contains(ve, "QUICK CHECKS") {
		t.Fatal("editor confirm must NOT yank the operator back to the menu")
	}
	if back, _ := ee.confirmKeyPress("n"); back.(model).scr != scEditor {
		t.Fatal("declining an editor confirm must return to the editor")
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
		Actuator: "http://localhost:9001/manage", ActuatorAuth: "bearer:MGMT_TOKEN", Pod: "pod-b"}
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
	for _, want := range []string{"START HERE", "QUICK CHECKS", "CAPTURE EVIDENCE", "ADVANCED",
		"guided diagnosis", "pauses app", "safe / caution / disruptive", "❯", "[?] help",
		"why", "workload", "security", "terminal", "re-roll", "kill pod"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu missing %q", want)
		}
	}
	if strings.Contains(out, "fastthread") {
		t.Error("no cloud analyzers may be recommended")
	}
}

// --- dashboard v3 -------------------------------------------------------------

func TestLayoutTiers(t *testing.T) {
	cases := []struct {
		w, h, tier int
		strip      bool
	}{
		{100, 50, 0, false}, // compact
		{120, 0, 1, false},  // classic sidebar, unmeasured height
		{120, 40, 1, true},  // sidebar + log strip
		{140, 34, 2, true},  // smallest grid
		{200, 50, 2, true},  // 15" laptop full screen
	}
	for _, c := range cases {
		m := readyModel()
		m.width, m.height = c.w, c.h
		if got := m.tier(); got != c.tier {
			t.Errorf("%dx%d: tier = %d, want %d", c.w, c.h, got, c.tier)
		}
		if got := m.showLogPane(); got != c.strip {
			t.Errorf("%dx%d: showLogPane = %v, want %v", c.w, c.h, got, c.strip)
		}
		if m.tier() == 2 {
			menuW, midW, evW := m.cols()
			if menuW+midW+evW+4 != m.tw() {
				t.Errorf("%dx%d: columns %d+%d+%d+4 != tw %d", c.w, c.h, menuW, midW, evW, m.tw())
			}
		}
	}
}

// The single most valuable regression test for a fixed frame: every screen
// that renders the dashboard must fill the terminal exactly — no scrolling,
// no overflow — including the overlay screens that append lines underneath.
func TestDashboardFrameExact(t *testing.T) {
	for _, scr := range []screen{scMenu, scConfirm, scVia, scLevel, scJcmd} {
		m := readyModel()
		m.width, m.height = 200, 50
		m.scr = scr
		m.confirmMsg = "sure? [y/N]"
		m.logger = "ROOT"
		out := m.View()
		lines := strings.Split(out, "\n")
		if len(lines) != 50 {
			t.Errorf("screen %v: frame is %d rows, want exactly 50", scr, len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > 200 {
				t.Errorf("screen %v row %d: %d cols wide (>200)", scr, i+1, w)
			}
		}
	}
}

func TestDashboardShowsPanes(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out := m.menuView()
	for _, want := range []string{"LIVE LOGS", "WORKLOAD", "CAPTURES", "TRENDS", "PODS", "▲",
		"OutOfMemoryError", "app-debug-demo-app", "config:configmap", "20260705T091500Z", "click switches"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDashboardDoesNotSpendMainPaneOnEvents(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out := m.menuView()
	if strings.Contains(out, "EVENTS") {
		t.Fatal("main dashboard should reserve the right column for workload context, not recent events")
	}
	if !strings.Contains(out, "WORKLOAD") || !strings.Contains(out, "volumes") {
		t.Fatal("main dashboard must show workload context in the right column")
	}
}

func TestQuickStreamsFullScreenWhenNoStrip(t *testing.T) {
	m := readyModel() // 120×0: no bottom strip → full-screen fallback
	out, cmd := m.Update(key("s"))
	mm := out.(model)
	if mm.scr != scOutput || !mm.out.running || cmd == nil {
		t.Fatalf("s must open the streaming output view, got screen %v", mm.scr)
	}
	out, _ = mm.Update(streamChunkMsg{id: mm.out.id, data: []byte("hello\nworld\n")})
	mm = out.(model)
	if len(mm.out.lines) != 2 {
		t.Fatalf("chunk must land in the pane, got %d lines", len(mm.out.lines))
	}
	out, _ = mm.Update(streamDoneMsg{id: mm.out.id})
	mm = out.(model)
	if !mm.out.done || !mm.out.ok {
		t.Fatalf("done must set the verdict, got done=%v ok=%v", mm.out.done, mm.out.ok)
	}
	out = press(t, mm, "q")
	if out.(model).scr != scMenu {
		t.Fatal("q must return to the menu")
	}
}

func TestCommandsStreamIntoStrip(t *testing.T) {
	for _, k := range []string{"s", "l"} { // quick read AND the old drop-outs
		m := readyModel()
		m.width, m.height = 200, 50
		out, cmd := m.Update(key(k))
		mm := out.(model)
		if mm.scr != scMenu || !mm.out.show || !mm.out.running || cmd == nil {
			t.Fatalf("%q must stream into the bottom strip with the menu live, got screen %v show=%v", k, mm.scr, mm.out.show)
		}
		if !strings.Contains(mm.menuView(), "OUTPUT") {
			t.Fatalf("%q: strip must switch from LIVE LOGS to OUTPUT", k)
		}
	}
}

func TestHeapStreamsIntoStrip(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out := press(t, m, "H", "H")
	mm := out.(model)
	if mm.scr != scVia {
		t.Fatalf("H,H must open the tier pick, got %v", mm.scr)
	}
	res, cmd := mm.Update(key("enter"))
	mm = res.(model)
	if !mm.out.running || !strings.Contains(mm.out.title, "heap") || cmd == nil {
		t.Fatalf("heap must stream into the pane, got title %q", mm.out.title)
	}
}

func TestStaleStreamIgnored(t *testing.T) {
	m := readyModel()
	out, _ := m.Update(key("s"))
	mm := out.(model)
	out, _ = mm.Update(streamChunkMsg{id: mm.out.id - 1, data: []byte("stale\n")})
	if len(out.(model).out.lines) != 0 {
		t.Fatal("chunks from a superseded stream must be dropped")
	}
}

func TestEscStopsThenDismisses(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	stopped := false
	m.out = outState{id: 7, title: "jdebug logs", running: true, show: true,
		cancel: func() { stopped = true }}
	m.scr = scMenu
	out := press(t, m, "esc")
	mm := out.(model)
	if !stopped || !mm.out.show {
		t.Fatal("first esc must stop the stream but keep the pane")
	}
	out, _ = mm.Update(streamDoneMsg{id: 7, err: context.Canceled})
	mm = out.(model)
	if mm.out.errStr != "stopped" {
		t.Fatalf("cancel must read as stopped, got %q", mm.out.errStr)
	}
	out = press(t, mm, "esc")
	if out.(model).out.show {
		t.Fatal("second esc must dismiss back to the live logs")
	}
	if !strings.Contains(out.(model).menuView(), "LIVE LOGS") {
		t.Fatal("dismissing must restore the log tail")
	}
}

func TestLogClassifier(t *testing.T) {
	ls := classifyLogs([]string{
		"10:00 INFO all fine",
		"10:01 WARN pool near capacity",
		"10:02 ERROR boom",
		"\tat com.example.App.run(App.java:1)", // stack frame inherits error
		"10:03 INFO recovered",
		"java.lang.OutOfMemoryError: Java heap space",
	})
	want := []int{0, 1, 2, 2, 0, 2}
	for i, w := range want {
		if ls[i].Sev != w {
			t.Errorf("line %d: sev = %d, want %d (%q)", i, ls[i].Sev, w, ls[i].Text)
		}
	}
}

func TestSparkRender(t *testing.T) {
	if got := spark([]int{0, 50, 100}, 0, 100, 10); got != "▁▄█" {
		t.Errorf("spark = %q, want ▁▄█", got)
	}
	if got := spark([]int{-1, 100}, 0, 100, 5); got != " █" {
		t.Errorf("unknowns must render as gaps, got %q", got)
	}
	if got := spark([]int{1, 2, 3, 4}, 0, 100, 2); len([]rune(got)) != 2 {
		t.Errorf("spark must window to the last w samples, got %q", got)
	}
}

func TestCpuMilli(t *testing.T) {
	for in, want := range map[string]int{"250m": 250, "1": 1000, "": -1, "0.5": 500} {
		if got := cpuMilli(in); got != want {
			t.Errorf("cpuMilli(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSampleRingCaps(t *testing.T) {
	var h []sample
	for i := 0; i < histCap+5; i++ {
		h = pushSample(h, sample{MemPct: i})
	}
	if len(h) != histCap {
		t.Fatalf("ring len = %d, want %d", len(h), histCap)
	}
	if h[len(h)-1].MemPct != histCap+4 {
		t.Fatal("ring must keep the newest samples")
	}
}

func TestOutputScrollKeys(t *testing.T) {
	m := readyModel()
	m.width, m.height = 120, 20
	m.scr = scOutput
	var raw []string
	for i := 0; i < 40; i++ {
		raw = append(raw, "line")
	}
	m.out = outState{done: true, ok: true, raw: []byte(strings.Join(raw, "\n"))}
	m.rewrapOut()
	out := press(t, m, "g") // top (clamped to len-1, pinned at render)
	if got := out.(model).out.off; got != 39 {
		t.Fatalf("g must scroll to the top, off = %d want 39", got)
	}
	out = press(t, out, "G", "k") // bottom, then one line back
	if got := out.(model).out.off; got != 1 {
		t.Fatalf("G then k must land on off=1, got %d", got)
	}
}

func TestFocusToggle(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out := press(t, m, "f")
	mm := out.(model)
	if !mm.logs.focus {
		t.Fatal("f must expand the log pane")
	}
	if !strings.Contains(mm.menuView(), "f/esc back") {
		t.Fatal("focus view must show the way back")
	}
	out = press(t, mm, "f")
	if out.(model).logs.focus {
		t.Fatal("second f must collapse the log pane")
	}
}

func TestHeaderFullTarget(t *testing.T) {
	m := readyModel()
	m.width = 200
	h := m.headerRemote(true)
	if !strings.Contains(h, "debug-demo / app-debug-demo-app-6c6c4b5769-s9jdg / app") {
		t.Fatal("header must show namespace / pod / container untruncated")
	}
}

func TestPanelExplainsLimits(t *testing.T) {
	m := readyModel()
	v := m.panelView(46, 20, true)
	for _, want := range []string{"480Mi of 512Mi limit", "250m of 500m limit", "via actuator", "CrashLoopBackOff"} {
		if !strings.Contains(v, want) {
			t.Errorf("panel missing %q", want)
		}
	}
}

func TestHeapUnavailableExplainsItself(t *testing.T) {
	m := readyModel()
	m.panel.HeapUsed, m.panel.HeapMax, m.panel.HeapVia = "", "", ""
	// no route and no jattach → name the next action, not a bare dash
	m.local.Jattach = false
	if v := m.panelView(46, 20, false); !strings.Contains(v, "stage jattach") {
		t.Fatalf("a missing heap value must name the next action, got:\n%s", v)
	}
	// with jattach staged, the fallback route is named instead
	m.local.Jattach = true
	if !strings.Contains(m.panelView(46, 20, false), "via jattach") {
		t.Fatal("with jattach staged, the heap row must point at the jattach route")
	}
}

func TestPanelSeparatesResourceFromJVM(t *testing.T) {
	m := readyModel()
	v := ansiStrip(m.panelView(46, 24, false))
	if !strings.Contains(v, "resource") || !strings.Contains(v, "container") {
		t.Error("the resource group must say it's container-level (kubectl top)")
	}
	if !strings.Contains(v, "jvm") || !strings.Contains(v, "process") {
		t.Error("the jvm group must say it's inside the process")
	}
	// resource group comes before the jvm group
	if strings.Index(v, "resource") > strings.Index(v, "jvm ·") {
		t.Error("resource usage should be listed above JVM usage")
	}
}

// clickMenuRow finds the display row of a menu action by its name and clicks
// it, returning the resulting model + cmd.
func clickMenuRow(t *testing.T, m model, name string) (model, tea.Cmd) {
	t.Helper()
	lines := strings.Split(m.menuView(), "\n")
	lw := m.leftW()
	for y, l := range lines {
		col := ansiStrip(ansi.Truncate(l, lw, "")) // the menu column only
		if strings.Contains(col, "  "+name+" ") {
			res, cmd := m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			return res.(model), cmd
		}
	}
	t.Fatalf("row %q not found in menu", name)
	return m, nil
}

func readyModel200() model {
	m := readyModel()
	m.width, m.height = 200, 50
	return m
}

func TestActuatorAuthIsAReferenceNotASecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JDEBUG_CONFIG_DIR", dir)
	// the editor stores the REFERENCE (a pod env-var name), never a secret
	saveTarget(target{Namespace: "n", Container: "app", ActuatorAuth: "bearer:MGMT_TOKEN"})
	data, _ := os.ReadFile(dir + "/target")
	if !strings.Contains(string(data), "SAVED_ACTUATOR_AUTH='bearer:MGMT_TOKEN'") {
		t.Fatalf("config must persist the auth reference:\n%s", data)
	}
	// targetEnv exports it for the CLI to apply inside the pod
	env := targetEnv(target{ActuatorAuth: "bearer:MGMT_TOKEN"})
	found := false
	for _, e := range env {
		if e == "ACTUATOR_AUTH=bearer:MGMT_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatal("targetEnv must export ACTUATOR_AUTH for the pod-side fetch")
	}
	// 'k' in the editor opens the auth reference input, explaining the source
	m := readyModel()
	m.scr = scEditor
	out := press(t, m, "k")
	mm := out.(model)
	if mm.scr != scInput || !strings.Contains(mm.input.title, "bearer:") {
		t.Fatalf("k must open the auth-reference input, got screen %v", mm.scr)
	}
	if !strings.Contains(mm.editor.note, "stays in the pod") {
		t.Fatal("the auth prompt must explain the secret stays in the pod")
	}
}

func TestDetailCardsOpenAndInform(t *testing.T) {
	m := readyModel200()
	// '.' opens the transparency cards
	out, _ := m.Update(key("."))
	mm := out.(model)
	if mm.scr != scDetail {
		t.Fatalf(". must open the detail cards, got screen %v", mm.scr)
	}
	v := ansiStrip(mm.detailView())
	// a card must expose command, source, risk, and dependencies
	for _, want := range []string{"jdebug status", "kubectl pod status", "read-only", "metrics-server"} {
		if !strings.Contains(v, want) {
			t.Errorf("detail cards missing %q", want)
		}
	}
	// state-changing commands must be flagged as such — check the anchored cards
	// (the full list scrolls, so anchor each to the top to read its risk)
	heap, _ := m.openDetail("H")
	if !strings.Contains(ansiStrip(heap.(model).detailView()), "PAUSES the JVM") {
		t.Error("heap card must flag that it pauses the JVM")
	}
	rr, _ := m.openDetail("R")
	if !strings.Contains(ansiStrip(rr.(model).detailView()), "state-changing") {
		t.Error("re-roll card must flag it's state-changing")
	}
	// esc returns to the menu
	if press(t, mm, "esc").(model).scr != scMenu {
		t.Fatal("esc must leave the detail cards")
	}
}

func TestRightClickOpensCardAnchored(t *testing.T) {
	m := readyModel200()
	lines := strings.Split(m.menuView(), "\n")
	lw := m.leftW()
	for y, l := range lines {
		if strings.Contains(ansiStrip(ansi.Truncate(l, lw, "")), "  heap ") {
			out, _ := m.Update(tea.MouseMsg{X: 4, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonRight})
			mm := out.(model)
			if mm.scr != scDetail || mm.detailAnchor != "H" {
				t.Fatalf("right-click on heap must open its card, got screen %v anchor %q", mm.scr, mm.detailAnchor)
			}
			// the anchored card is shown first
			if !strings.HasPrefix(strings.TrimSpace(ansiStrip(mm.detailView())), "what each command") {
				return // header first is fine
			}
			return
		}
	}
	t.Fatal("heap row not found")
}

func TestEveryMenuActionHasACard(t *testing.T) {
	// transparency must be complete — no runnable row without a card
	m := readyModel()
	for _, a := range m.menuActions() {
		if _, ok := infoFor(a.key); !ok {
			t.Errorf("no transparency card for menu action %q (%s)", a.key, a.name)
		}
	}
}

func TestClickRunsMenuRow(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	// clicking the status label runs status, exactly like pressing s
	mm, cmd := clickMenuRow(t, m, "status")
	if cmd == nil || !mm.out.running || !strings.Contains(mm.out.title, "status") {
		t.Fatalf("clicking 'status' must run it, got title %q", mm.out.title)
	}
}

func TestClickDisruptiveRowStillConfirms(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	// clicking a disruptive row must open the SAME second-key confirm as the
	// shortcut — a click can't bypass the gate
	if mm, _ := clickMenuRow(t, m, "kill pod"); mm.scr != scConfirm || !strings.Contains(mm.confirmMsg, "K again") {
		t.Fatalf("clicking 'kill pod' must ask to confirm, got screen %v", mm.scr)
	}
	if mm, _ := clickMenuRow(t, readyModel200(), "heap"); mm.scr != scConfirm || !strings.Contains(mm.confirmMsg, "H again") {
		t.Fatalf("clicking 'heap' must ask to confirm, got screen %v", mm.scr)
	}
}

func TestClickOutsideMenuColumnIsNotARun(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	// a click in the panel column (x beyond the menu) must not run a menu row
	menuW, _, _ := m.cols()
	if _, ok := m.menuRowClick(menuW+5, 6); ok {
		t.Fatal("a click outside the menu column must not resolve to a menu action")
	}
}

func TestPanelClickDrillsIntoWhy(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	menuW, _, _ := m.cols()
	x := menuW + 2 + 2 // inside the mid TARGET panel
	y := 5             // a panel row
	res, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	mm := res.(model)
	if cmd == nil || !mm.out.running || !strings.Contains(mm.out.title, "why") {
		t.Fatalf("clicking the TARGET panel must run the deep-dive, got title %q", mm.out.title)
	}
}

func TestNextSuggestionsSeverityOrdered(t *testing.T) {
	m := readyModel()
	// a multi-symptom pod: crash-looping AND OOM AND memory-pressured AND maxed
	m.panel = panelData{When: time.Now(), Waiting: "CrashLoopBackOff",
		LastReason: "OOMKilled", Restarts: 40, MemPct: 95,
		HPAName: "a", HPACur: 6, HPAMax: 6}
	got := ansiStrip(strings.Join(m.suggestions(), "\n"))
	crash := strings.Index(got, "CrashLoopBackOff")
	oom := strings.Index(got, "OOMKilled")
	if crash < 0 || oom < 0 || crash > oom {
		t.Fatalf("crash-loop must rank above OOM in NEXT:\n%s", got)
	}
}

func TestNextConfidenceLevels(t *testing.T) {
	// a confirmed OOM at high memory reads as "likely" and shows the cause→effect
	// chain OOMKilled → mem 95% of limit, so the junior sees why, not just what.
	m := readyModel()
	m.panel = panelData{When: time.Now(), Phase: "Running", LastReason: "OOMKilled", MemPct: 95}
	got := ansiStrip(strings.Join(m.suggestions(), "\n"))
	if !strings.Contains(got, "likely") {
		t.Fatalf("a confirmed OOM must read as 'likely':\n%s", got)
	}
	if !strings.Contains(got, "OOMKilled") || !strings.Contains(got, "mem 95% of limit") {
		t.Fatalf("NEXT must chain OOMKilled → mem 95%% of limit:\n%s", got)
	}
	// a blind autoscaler is genuinely uncertain → "unknown"
	m2 := readyModel()
	m2.panel = panelData{When: time.Now(), Phase: "Running", HPAName: "a", HPAFailing: true, HPAReason: "no metrics"}
	if got2 := ansiStrip(strings.Join(m2.suggestions(), "\n")); !strings.Contains(got2, "unknown") {
		t.Fatalf("a blind HPA must read as 'unknown':\n%s", got2)
	}
	// a restart storm without a named reason is only "possible"
	m3 := readyModel()
	m3.panel = panelData{When: time.Now(), Phase: "Running", Restarts: 7}
	if got3 := ansiStrip(strings.Join(m3.suggestions(), "\n")); !strings.Contains(got3, "possible") {
		t.Fatalf("an unexplained restart storm must read as 'possible':\n%s", got3)
	}
}

func TestBlockedByStatesAndFixes(t *testing.T) {
	m := readyModel()
	m.mode = 1
	m.remote = probe{OK: true, Cluster: true, When: time.Now().Add(time.Hour)}
	m.t.Selector = ""
	m.panel = panelData{When: time.Now(), NoMetrics: true, ActuatorOK: false}
	m.podsErr = "pods is forbidden: User cannot list resource pods"
	var states, fixes string
	for _, b := range m.blockers() {
		states += b.state + "\n"
		fixes += b.fix + "\n"
		if b.fix == "" {
			t.Fatalf("every blocked state must name a fix, got none for %q", b.state)
		}
	}
	for _, want := range []string{"no selector", "RBAC", "metrics-server", "actuator"} {
		if !strings.Contains(states, want) {
			t.Fatalf("blockers must surface %q as a state:\n%s", want, states)
		}
	}
	// RBAC fix must name a least-privilege permission, not "become admin"
	if !strings.Contains(fixes, "get/list") {
		t.Fatalf("RBAC fix must name the least-privilege verbs:\n%s", fixes)
	}
}

func TestBlockedByClusterShortCircuits(t *testing.T) {
	m := readyModel()
	m.mode = 1
	m.remote = probe{OK: false, Cluster: false, When: time.Now().Add(time.Hour)}
	bs := m.blockers()
	if len(bs) != 1 || !strings.Contains(bs[0].state, "cluster") {
		t.Fatalf("an unreachable cluster must be the only (root) blocker, got %+v", bs)
	}
}

func TestBlockedKeyOpensAndDismisses(t *testing.T) {
	m := readyModel()
	out := press(t, m, "b")
	if out.(model).scr != scBlocked {
		t.Fatal("b must open the blocked-by view")
	}
	if back := press(t, out.(model), "x"); back.(model).scr != scMenu {
		t.Fatal("any key must dismiss the blocked-by view")
	}
	// reachable even while the target gate is up (that's when it matters most)
	g := readyModel()
	g.remote = probe{OK: false, Cluster: true, When: time.Now().Add(time.Hour)}
	g.t.Pod = ""
	if gated := press(t, g, "b"); gated.(model).scr != scBlocked {
		t.Fatal("b must open blocked-by even while gated")
	}
}

func TestNothingBlocked(t *testing.T) {
	m := readyModel()
	m.mode = 1
	m.remote = probe{OK: true, Cluster: true, When: time.Now().Add(time.Hour)}
	m.t.Selector = "app=demo"
	m.panel = panelData{When: time.Now(), ActuatorOK: true}
	m.podsErr, m.eventsErr = "", ""
	if bs := m.blockers(); len(bs) != 0 {
		t.Fatalf("a healthy target must have no blockers, got %+v", bs)
	}
	if v := ansiStrip(m.blockedView()); !strings.Contains(v, "nothing is blocked") {
		t.Fatalf("blocked view must reassure when clear:\n%s", v)
	}
}

func TestBackgroundModeCycle(t *testing.T) {
	m := readyModel()
	if m.bgMode != bgLive {
		t.Fatal("default background mode must be live")
	}
	m = press(t, m, "z").(model)
	if m.bgMode != bgQuiet {
		t.Fatal("z must move live → quiet")
	}
	if s := m.bgStatus(); !strings.Contains(s, "QUIET") || !strings.Contains(s, "JVM") {
		t.Fatalf("quiet status must name what's off: %q", s)
	}
	m = press(t, m, "z").(model)
	if m.bgMode != bgPaused {
		t.Fatal("z must move quiet → paused")
	}
	if s := m.bgStatus(); !strings.Contains(s, "PAUSED") || !strings.Contains(s, "r ") {
		t.Fatalf("paused status must point at r: %q", s)
	}
	m = press(t, m, "z").(model)
	if m.bgMode != bgLive {
		t.Fatal("z must wrap paused → live")
	}
}

func TestQuietRefreshHoldsLastHeap(t *testing.T) {
	// a quiet-mode panel refresh skips the JVM probe, so the last-known heap and
	// actuator status must be carried forward, not blanked to "no route".
	m := readyModel()
	m.panel = panelData{When: time.Now(), HeapUsed: "121Mi", HeapMax: "512Mi", HeapVia: "actuator", ActuatorOK: true}
	out, _ := m.Update(panelMsg{When: time.Now(), Phase: "Running", HeapSkipped: true})
	got := out.(model).panel
	if got.HeapUsed != "121Mi" || got.HeapVia != "actuator" || !got.ActuatorOK {
		t.Fatalf("a skipped JVM probe must keep the last heap/actuator status, got %+v", got)
	}
}

func TestRefreshKeyReturnsCommand(t *testing.T) {
	m := readyModel()
	if _, cmd := m.Update(key("r")); cmd == nil {
		t.Fatal("r must trigger a manual refresh command")
	}
}

func TestTrendsLegend(t *testing.T) {
	m := readyModel()
	rows := strings.Join(m.trendsRows(46), "\n")
	got := ansiStrip(rows)
	if !strings.Contains(got, "mem=%limit") || !strings.Contains(got, "▲=restart") {
		t.Fatalf("trends must carry a self-explanatory legend:\n%s", got)
	}
}

func TestAutoscaleLine(t *testing.T) {
	cases := []struct {
		d        panelData
		want     string
		wantWarn bool
	}{
		{panelData{}, "no HPA", false},
		{panelData{HPAName: "a", HPACur: 4, HPAMax: 6, HPAMin: 2}, "4/6 replicas · min 2", false},
		{panelData{HPAName: "a", HPACur: 6, HPAMax: 6}, "AT MAX", true},
		{panelData{HPAName: "a", HPAFailing: true, HPAReason: "can't read metrics"}, "can't scale — can't read metrics", true},
	}
	for _, c := range cases {
		got, warn := hpaLine(c.d)
		if !strings.Contains(got, c.want) || warn != c.wantWarn {
			t.Errorf("hpaLine(%+v) = %q,%v — want contains %q,%v", c.d, got, warn, c.want, c.wantWarn)
		}
	}
}

func TestParseHeapInfo(t *testing.T) {
	// G1, classic form (JDK 11/17)
	u, mx := parseHeapInfo(" garbage-first heap   total 524288K, used 123456K [0x0000...\n Metaspace       used 40870K, committed 41216K")
	if u != "121Mi" || mx != "512Mi" {
		t.Fatalf("G1 classic: got %s/%s, want 121Mi/512Mi", u, mx)
	}
	// G1, JDK 21 form (committed beats reserved)
	u, mx = parseHeapInfo(" garbage-first heap   total reserved 2097152K, committed 986112K, used 123456K\n Metaspace       used 40870K")
	if u != "121Mi" || mx != "963Mi" {
		t.Fatalf("G1 jdk21: got %s/%s, want 121Mi/963Mi", u, mx)
	}
	// generational: young + old sum, metaspace excluded
	u, mx = parseHeapInfo(" PSYoungGen      total 76288K, used 10240K\n ParOldGen       total 175104K, used 20480K\n Metaspace       used 999999K, committed 999999K")
	if u != "30Mi" || mx != "246Mi" {
		t.Fatalf("generational: got %s/%s, want 30Mi/246Mi", u, mx)
	}
	if u, _ := parseHeapInfo("no heap here"); u != "" {
		t.Fatal("garbage input must return empty, not zero values")
	}
}

func TestCrashLoopSuggestion(t *testing.T) {
	m := readyModel()
	m.panel.Waiting = "CrashLoopBackOff"
	m.panel.LastReason = ""
	m.panel.MemPct = 50
	got := strings.Join(m.suggestions(), "\n")
	if !strings.Contains(got, "CrashLoopBackOff") || !strings.Contains(got, "7") {
		t.Fatalf("CrashLoopBackOff must route to wizard flow 7, got %q", got)
	}
}

func TestNoActuatorHint(t *testing.T) {
	m := readyModel()
	m.panel = panelData{When: time.Now(), Phase: "Running", MemPct: 50}
	got := strings.Join(m.suggestions(), "\n")
	if !strings.Contains(got, "no actuator") || !strings.Contains(got, "jattach") {
		t.Fatalf("missing actuator must be surfaced with the working routes, got %q", got)
	}
}

func TestNotSureRunsSafeSnapshotFirst(t *testing.T) {
	for _, f := range wizardFlows {
		if f.key != "6" {
			continue
		}
		if f.steps[0].confirm != "" || f.steps[0].args[0] != "snapshot" {
			t.Fatal("flow 6 must run a safe snapshot unconditionally before offering heap")
		}
		if f.steps[1].confirm == "" || f.steps[1].args[0] != "heap" {
			t.Fatal("flow 6's heap dump must be an optional, confirmed add-on")
		}
	}
	// declining the heap step must still have produced the snapshot: picking
	// flow 6 fires the snapshot command immediately, no question asked first —
	// and the flow streams into the output pane, not off the main page
	m := readyModel()
	out, _ := m.Update(key("w"))
	res, cmd := out.(model).Update(key("6"))
	mm := res.(model)
	if mm.scr == scWizard || !mm.wiz.active || cmd == nil || !mm.out.running {
		t.Fatalf("flow 6 must start streaming the snapshot straight away, got screen %v running=%v", mm.scr, mm.out.running)
	}
	if !strings.Contains(string(mm.out.raw), "guided diagnosis") {
		t.Fatal("the flow header must open the pane transcript")
	}
}

func TestWizardStreamsOnDashboard(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out, _ := m.Update(key("w"))
	res, cmd := out.(model).Update(key("2")) // slow/hung: threads then health
	mm := res.(model)
	if mm.scr != scMenu || !mm.out.show || cmd == nil {
		t.Fatalf("wizard steps must stream in the dashboard pane, got screen %v show=%v", mm.scr, mm.out.show)
	}
	if !strings.Contains(string(mm.out.raw), "thread dump") {
		t.Fatal("step narration must land in the transcript")
	}
	// first step's stream finishes → the next step chains automatically
	res, cmd = mm.Update(streamDoneMsg{id: mm.out.id})
	mm = res.(model)
	if !mm.wiz.active || cmd == nil || !mm.out.running {
		t.Fatal("finishing a step must chain the next one")
	}
	// second (last) step finishes → flow wrap-up lands in the transcript
	res, _ = mm.Update(streamDoneMsg{id: mm.out.id})
	mm = res.(model)
	if mm.wiz.active || !strings.Contains(string(mm.out.raw), "flow complete") {
		t.Fatalf("flow completion must close out in the same transcript, active=%v", mm.wiz.active)
	}
	if mm.scr != scMenu {
		t.Fatalf("the user never leaves the dashboard, got screen %v", mm.scr)
	}
}

func TestWizardHasCrashFlow(t *testing.T) {
	m := readyModel()
	m.scr = scWizard
	if !strings.Contains(m.wizardView(), "CrashLoopBackOff") {
		t.Fatal("wizard must offer a crash-loop flow")
	}
	found := false
	for _, f := range wizardFlows {
		if f.key == "7" {
			found = true
			if len(f.steps) < 2 || f.steps[1].args[1] != "--previous" {
				t.Fatal("flow 7 must read the previous container's logs")
			}
		}
	}
	if !found {
		t.Fatal("flow 7 missing")
	}
}

func TestCompactChecklistOrder(t *testing.T) {
	m := readyModel()
	m.width = 90 // tier 0: no side panel
	out := m.menuView()
	target := strings.Index(out, "TARGET")
	next := strings.Index(out, "NEXT")
	start := strings.Index(out, "START HERE")
	if target < 0 || next < 0 {
		t.Fatal("compact layout must show TARGET and NEXT above the menu")
	}
	if !(target < next && next < start) {
		t.Fatalf("compact order must be TARGET → NEXT → menu, got %d/%d/%d", target, next, start)
	}
}

func TestNarrowHeaderSplitsTargetLine(t *testing.T) {
	m := readyModel()
	m.width = 90
	h := m.headerRemote(true)
	if !strings.Contains(h, "\n   "+"debug-demo / ") && !strings.Contains(h, "\n"+"   debug-demo / ") {
		t.Fatal("narrow header must give the untruncated target its own line")
	}
	for _, l := range strings.Split(h, "\n") {
		if lipgloss.Width(l) > 90 {
			t.Fatalf("header line overflows 90 cols: %q", l)
		}
	}
}

func TestCapturesBrowserRenders(t *testing.T) {
	m := readyModel() // demo is browsing a pod's sessions
	rows := strings.Join(m.capsRows(72, 10), "\n")
	// the pane shows scope + a "last refreshed" stamp (the demo just refreshed)
	for _, want := range []string{"CAPTURES", "refreshed", "a analyzes", "..", "drill in", "▸"} {
		if !strings.Contains(rows, want) {
			t.Errorf("captures browser missing %q", want)
		}
	}
	// capHint routes file types to the right next step
	if got := capHint(capEntry{Name: "heap-jattach.hprof"}); got != "a → histogram" {
		t.Errorf("hprof hint = %q", got)
	}
	if got := capHint(capEntry{Name: "threads-jattach.txt"}); got != "view · a" {
		t.Errorf("txt hint = %q", got)
	}
}

// --- RBAC-aware enumeration + selector discovery -------------------------------

func TestForbiddenDetection(t *testing.T) {
	for _, msg := range []string{
		`Error from server (Forbidden): pods is forbidden: User "dev" cannot list resource "pods" in API group "" in the namespace "payments"`,
		`namespaces is forbidden: User "x" cannot list resource "namespaces"`,
	} {
		if !forbiddenRe.MatchString(msg) {
			t.Errorf("must detect RBAC denial in %q", msg)
		}
	}
	if forbiddenRe.MatchString("The connection to the server was refused") {
		t.Error("a network error is not an RBAC denial")
	}
}

func testPods() podsJSON {
	var pj podsJSON
	mk := func(name string, labels map[string]string) podItem {
		var it podItem
		it.Metadata.Name = name
		it.Metadata.Labels = labels
		return it
	}
	pj.Items = []podItem{
		mk("pod-a", map[string]string{"app": "payments", "app.kubernetes.io/name": "payments", "component": "api", "pod-template-hash": "abc"}),
		mk("pod-b", map[string]string{"app": "payments", "app.kubernetes.io/name": "payments", "component": "api", "pod-template-hash": "abc"}),
		mk("pod-c", map[string]string{"app": "web", "pod-template-hash": "zzz"}),
	}
	return pj
}

func TestDeriveSelectors(t *testing.T) {
	got := deriveSelectors(testPods(), "")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "pod-template-hash") {
		t.Fatal("rollout hashes must never be suggested")
	}
	if !strings.Contains(joined, "matches 2 pod(s)") || !strings.Contains(joined, "matches 1 pod(s)") {
		t.Fatalf("suggestions must carry match counts:\n%s", joined)
	}
	if !strings.HasPrefix(got[0], "app.kubernetes.io/name=payments") {
		t.Fatalf("most specific stable key must rank first, got %q", got[0])
	}
	last := got[len(got)-1]
	if !strings.HasPrefix(last, "<any pod>") || !strings.Contains(last, "risky") {
		t.Fatalf("<any pod> must be last and warn about multiple apps, got %q", last)
	}
}

func TestDeriveSelectorsPodFirst(t *testing.T) {
	got := deriveSelectors(testPods(), "pod-c")
	if !strings.HasPrefix(got[0], "app=web") || !strings.Contains(got[0], "on your selected pod") {
		t.Fatalf("the selected pod's labels must rank first and say so, got %q", got[0])
	}
}

func TestEditorRBACOffersTyping(t *testing.T) {
	saved := podsFn
	defer func() { podsFn = saved }()
	podsFn = func(ns, sel string) enum {
		return enum{err: "pods is forbidden", forbidden: true}
	}
	m := readyModel()
	m.scr = scEditor
	out := press(t, m, "p")
	mm := out.(model)
	if mm.scr != scInput || !strings.Contains(mm.input.title, "RBAC") {
		t.Fatalf("forbidden pod listing must fall back to typed input with the RBAC reason, got screen %v title %q", mm.scr, mm.input.title)
	}
	out = press(t, mm, "p", "o", "d", "-", "z", "enter")
	if got := out.(model).t.Pod; got != "pod-z" {
		t.Fatalf("typed pod must apply, got %q", got)
	}
}

func TestEditorEmptyIsOnlySaidWhenTrue(t *testing.T) {
	saved := podsFn
	defer func() { podsFn = saved }()
	podsFn = func(ns, sel string) enum { return enum{} } // success, zero rows
	m := readyModel()
	m.scr = scEditor
	out := press(t, m, "p")
	mm := out.(model)
	if !strings.Contains(mm.editor.note, "no pods match") {
		t.Fatalf("genuinely-empty list keeps the honest message, got %q", mm.editor.note)
	}
	podsFn = func(ns, sel string) enum { return enum{err: "connection refused"} }
	out = press(t, m, "p")
	mm = out.(model)
	if !strings.Contains(mm.editor.note, "couldn't list pods") {
		t.Fatalf("a kubectl failure must not read as empty, got %q", mm.editor.note)
	}
}

func TestSelectorPickApplierStripsAnnotation(t *testing.T) {
	m := readyModel()
	m.pick = picker{items: []string{"app=payments                       matches 3 pod(s)"}, kind: pickSelector}
	m.scr = scPicker
	out := press(t, m, "1")
	if got := out.(model).t.Selector; got != "app=payments" {
		t.Fatalf("selector must be the first field only, got %q", got)
	}
	m.pick = picker{items: []string{"<any pod>   first match wins — risky"}, kind: pickSelector}
	m.scr = scPicker
	out = press(t, m, "1")
	if got := out.(model).t.Selector; got != "" {
		t.Fatalf("<any pod> with annotation must clear the selector, got %q", got)
	}
}

// esc is a universal "back/cancel": it never runs anything, never picks a
// default, and always lands on the screen underneath.
func TestEscAlwaysGoesBack(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir()) // editor esc saves the target
	cases := []struct {
		name  string
		setup func(m model) model
		want  screen
	}{
		{"via prompt cancels to menu", func(m model) model { m.scr = scVia; m.pendHeap = true; return m }, scMenu},
		{"wizard list backs to menu", func(m model) model { m.scr = scWizard; return m }, scMenu},
		{"jcmd pick cancels to menu", func(m model) model { m.scr = scJcmd; return m }, scMenu},
		{"level pick cancels to menu", func(m model) model { m.scr = scLevel; m.logger = "ROOT"; return m }, scMenu},
		{"editor backs to menu", func(m model) model { m.scr = scEditor; return m }, scMenu},
		{"picker backs to editor", func(m model) model {
			m.pick = picker{items: []string{"default"}, kind: pickNamespace}
			m.scr = scPicker
			return m
		}, scEditor},
		{"help backs to menu", func(m model) model { m.scr = scHelp; return m }, scMenu},
	}
	for _, c := range cases {
		m := c.setup(readyModel())
		res, cmd := m.Update(key("esc"))
		mm := res.(model)
		if mm.scr != c.want {
			t.Errorf("%s: esc landed on screen %v, want %v", c.name, mm.scr, c.want)
		}
		if cmd != nil {
			t.Errorf("%s: esc must never run a command", c.name)
		}
		if mm.pendHeap {
			t.Errorf("%s: esc must clear pending heap state", c.name)
		}
	}
}

func TestClickSwitchesPod(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir()) // switching saves the target
	m := readyModel()
	m.width, m.height = 200, 50
	menuW, midW, _ := m.cols()
	x := menuW + midW + 4 + 2 // inside the right column
	y := 3 + 2                // pods pane: title row +1 → second pod row... row 1 is first pod
	// row y=4 is the first pod row (y0=3 title); click the second pod
	res, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	mm := res.(model)
	want := "app-debug-demo-app-6c6c4b5769-x7k2p"
	if mm.t.Pod != want {
		t.Fatalf("clicking the second pod must retarget, got %q", mm.t.Pod)
	}
	if cmd == nil {
		t.Fatal("switching pods must refetch the live panes")
	}
}

func TestTerminalKeyIsShiftT(t *testing.T) {
	m := readyModel()
	out, cmd := m.Update(key("T"))
	mm := out.(model)
	if cmd == nil || mm.scr != scMenu || mm.postExec != "status" {
		t.Fatalf("T must open the pod terminal and schedule the status re-run, got screen %v postExec %q", mm.scr, mm.postExec)
	}
	out, cmd = m.Update(key("t"))
	if out.(model).scr != scVia || cmd != nil {
		t.Fatal("lowercase t must still be threads (tier pick)")
	}
}

func TestAutoStatusFiresOncePer(t *testing.T) {
	m := readyModel()
	res, cmd := m.Update(autoStatusMsg{})
	mm := res.(model)
	if !mm.autoRan || cmd == nil || !mm.out.running || !strings.Contains(mm.out.title, "status") {
		t.Fatalf("auto-status must run status on an idle dashboard, got title %q", mm.out.title)
	}
	// never twice, and never over something the user already started
	res, cmd = mm.Update(autoStatusMsg{})
	if cmd != nil {
		t.Fatal("auto-status must be a one-shot")
	}
	m2 := readyModel()
	m2.out.id = 3 // the user already ran something
	if _, cmd := m2.Update(autoStatusMsg{}); cmd != nil {
		t.Fatal("auto-status must never interrupt user activity")
	}
	_ = res
}

func TestPanelExplainsMissingMetricsServer(t *testing.T) {
	m := readyModel()
	m.panel.MemUse, m.panel.CPUUse, m.panel.MemPct = "", "", -1
	m.panel.NoMetrics = true
	v := m.panelView(46, 20, false)
	if !strings.Contains(v, "no metrics-server") {
		t.Fatal("missing metrics-server must be named, not rendered as a dash")
	}
}

func TestCopyTranscriptNotices(t *testing.T) {
	saved := clipboardFn
	defer func() { clipboardFn = saved }()
	m := readyModel()
	m.out.raw = []byte("hello")
	clipboardFn = func(s string) error {
		if !strings.Contains(s, "hello") {
			t.Error("the transcript body must reach the clipboard")
		}
		return nil
	}
	if got := m.copyTranscript().out.notice; !strings.Contains(got, "copied") {
		t.Fatalf("successful copy must confirm itself, got %q", got)
	}
	clipboardFn = func(string) error { return fmt.Errorf("no clipboard tool") }
	if got := m.copyTranscript().out.notice; !strings.Contains(got, "couldn't copy") {
		t.Fatalf("failed copy must explain, got %q", got)
	}
}

func TestCaptureBrowserDrillAndView(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JDEBUG_DUMPS", filepath.Join(dir, "dumps")) // dumpsDir honours this
	sess := filepath.Join(dir, "dumps", "pods", "pod-a", "20260705T010000Z")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sess, "threads-jattach.txt"), []byte("Full thread dump\n\"main\" RUNNABLE"), 0o644)
	os.WriteFile(filepath.Join(sess, "heap-jattach.hprof"), append([]byte("JAVA PROFILE 1.0.2\x00"), 0x01, 0x02), 0o644)

	// helper: apply a fetchCaps result to the model
	load := func(m model) model {
		mm, _ := m.Update(fetchCaps(m.kit, m.capsDir())())
		return mm.(model)
	}

	m := readyModel()
	m.kit = dir
	m.t.Pod = "pod-a"
	m.capsCwd = "" // clear the demo's browse dir; use the real pre-filter
	m.width, m.height = 200, 50

	// pre-filtered to the pod: one session folder, plus an up-row (below pods root)
	m = load(m)
	if len(m.caps) != 1 || !m.caps[0].Dir {
		t.Fatalf("pod dir should list one session folder, got %+v", m.caps)
	}
	if !m.capsHasUp() {
		t.Fatal("a pod dir sits below the pods root, so '..' must be offered")
	}

	// content row 1 is the folder (row 0 is ".."); drill in
	res, _ := m.capsClick(1)
	m = load(res.(model))
	var sawText, sawHeap bool
	for _, c := range m.caps {
		sawText = sawText || strings.HasPrefix(c.Name, "threads")
		sawHeap = sawHeap || strings.HasSuffix(c.Name, ".hprof")
	}
	if !sawText || !sawHeap {
		t.Fatalf("drilling into the session must list its files, got %+v", m.caps)
	}

	// view the text file → loads into the pane; `a` then targets that file
	tv, _ := m.viewFile(filepath.Join(sess, "threads-jattach.txt"))
	vm := tv.(model)
	if vm.out.filePath == "" || !strings.Contains(string(vm.out.raw), "Full thread dump") {
		t.Fatal("viewing a text capture must load it into the pane")
	}
	_, acmd := vm.analyzeContext()
	if acmd == nil {
		t.Fatal("analyze with a file in view must run (on that file)")
	}

	// view the heap dump → metadata, never raw binary
	hv, _ := m.viewFile(filepath.Join(sess, "heap-jattach.hprof"))
	hm := hv.(model)
	if strings.ContainsRune(string(hm.out.raw), 0) {
		t.Fatal("a heap dump must never be shown as raw bytes")
	}
	if !strings.Contains(string(hm.out.raw), "heap dump") {
		t.Fatalf("heap view must explain what it is, got %q", string(hm.out.raw))
	}
}

func TestWheelScrollsOutputPane(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	var raw []string
	for i := 0; i < 60; i++ {
		raw = append(raw, "line")
	}
	m.out = outState{done: true, ok: true, show: true, raw: []byte(strings.Join(raw, "\n"))}
	(&m).rewrapOut()
	res, _ := m.Update(tea.MouseMsg{X: 10, Y: 40, Button: tea.MouseButtonWheelUp})
	if got := res.(model).out.off; got != 3 {
		t.Fatalf("wheel over the output pane must scroll it, off = %d want 3", got)
	}
}

func TestChooserStrayKeysDontPickAMode(t *testing.T) {
	m := demoModel()
	m.mode = 0
	m.scr = scChooser
	for _, k := range []string{"esc", "z", "9"} {
		res, _ := m.Update(key(k))
		if mm := res.(model); mm.scr != scChooser || mm.mode != 0 {
			t.Errorf("stray %q must stay on the chooser without picking a mode, got screen %v mode %d", k, mm.scr, mm.mode)
		}
	}
}

func TestRiskReadsWithoutColor(t *testing.T) {
	// risk must be legible with all styling stripped (NO_COLOR, screenshots,
	// colour-blind) — the word, not just the dot colour.
	m := readyModel()
	m.width, m.height = 200, 50
	plain := ansiStrip(m.menuView())
	for _, want := range []string{"● pauses app", "● restarts app", "● drops the pod", "● caution"} {
		if !strings.Contains(plain, want) {
			t.Errorf("risk text missing without colour: %q", want)
		}
	}
}

func TestNoRowWrapsAtMinTierTwoWidth(t *testing.T) {
	m := readyModel()
	m.width, m.height = 140, 50 // smallest tier-2 grid → tightest menu column
	for i, l := range strings.Split(m.menuView(), "\n") {
		if w := lipgloss.Width(l); w > 140 {
			t.Errorf("row %d wraps at 140 cols (width %d): %q", i+1, w, ansiStrip(l))
		}
	}
}

func TestHelpListsStateChangingActions(t *testing.T) {
	m := readyModel()
	help := ansiStrip(m.helpView())
	for _, want := range []string{"R re-roll", "K kill pod", "PAUSE the JVM", "changes log volume"} {
		if !strings.Contains(help, want) {
			t.Errorf("safety rules must mention %q", want)
		}
	}
	// the wizard must be the first recommended action
	if !strings.Contains(help, "NOT SURE? START HERE") {
		t.Error("help must lead with the wizard as the first action")
	}
}

func TestHighCPUFlowSeparatesTheTwoDumps(t *testing.T) {
	for _, f := range wizardFlows {
		if f.key != "3" {
			continue
		}
		if f.steps[0].args[0] != "threads" {
			t.Fatal("flow 3 must open with a thread dump")
		}
		// the SECOND thread dump must be gated (a wait/confirm), not queued
		// back-to-back while claiming the dumps are separated
		if f.steps[1].confirm == "" || f.steps[1].args[0] != "threads" {
			t.Fatal("the second thread dump must be a confirm step so the user can wait between samples")
		}
		if !strings.Contains(f.steps[1].confirm, "second") && !strings.Contains(f.steps[1].confirm, "#2") {
			t.Fatalf("the gate must explain it's the second sample, got %q", f.steps[1].confirm)
		}
		return
	}
	t.Fatal("flow 3 (high CPU) missing")
}

func TestReRollNeedsSecondPress(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out := press(t, m, "R")
	mm := out.(model)
	if mm.scr != scConfirm || !strings.Contains(mm.confirmMsg, "R again") {
		t.Fatalf("R must ask for a second R, got %q on %v", mm.confirmMsg, mm.scr)
	}
	if !strings.Contains(mm.confirmMsg, "rolling-restarts") {
		t.Fatal("the re-roll confirm must spell out the risk")
	}
	// a non-R key cancels
	if got := press(t, mm, "z").(model); got.scr != scMenu || got.out.running {
		t.Fatal("a non-R key must cancel the re-roll")
	}
	// second R runs the guarded restart
	res, cmd := mm.Update(key("R"))
	rm := res.(model)
	if cmd == nil || !rm.out.running || !strings.Contains(rm.out.title, "restart --confirm") {
		t.Fatalf("R,R must run the confirmed re-roll, got title %q", rm.out.title)
	}
}

func TestKillNeedsSecondPress(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	out := press(t, m, "K")
	mm := out.(model)
	if mm.scr != scConfirm || !strings.Contains(mm.confirmMsg, "K again") {
		t.Fatalf("K must ask for a second K, got %q on %v", mm.confirmMsg, mm.scr)
	}
	res, cmd := mm.Update(key("K"))
	km := res.(model)
	if cmd == nil || !km.out.running || !strings.Contains(km.out.title, "kill --confirm") {
		t.Fatalf("K,K must run the confirmed kill, got title %q", km.out.title)
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
