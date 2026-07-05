package main

// Update-logic tests: the interaction contracts that matter — the gate blocks
// actions, disruptive actions need a second press of the same key, quitting
// confirms, pickers apply values, and the shared config round-trips.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	for _, want := range []string{"START HERE", "QUICK CHECKS", "CAPTURE EVIDENCE", "ADVANCED",
		"guided diagnosis", "pauses app", "safe / caution / disruptive", "❯", "[?] help"} {
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
	for _, want := range []string{"LIVE LOGS", "EVENTS", "CAPTURES", "TRENDS", "▲",
		"OutOfMemoryError", "BackOff", "threads-pod-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q", want)
		}
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
	v := m.panelView(46, 20, false)
	if !strings.Contains(v, "needs actuator or jcmd") {
		t.Fatal("a missing heap value must say why and name the routes")
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
	// flow 6 fires the snapshot command immediately, no question asked first
	m := readyModel()
	out, _ := m.Update(key("w"))
	res, cmd := out.(model).Update(key("6"))
	mm := res.(model)
	if mm.scr != scWizard || !mm.wiz.active || cmd == nil {
		t.Fatalf("flow 6 must start running the snapshot straight away, got screen %v", mm.scr)
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
