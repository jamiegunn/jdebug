package main

// wizard.go — guided diagnosis: symptom → a queue of narrated steps. Each
// step shells out via ExecProcess; destructive steps confirm first. Works in
// every mode (kubectl-only steps are skipped in local modes, with a note).

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type wstep struct {
	narr    []string // narration echoed before the command
	args    []string // CLI verb + args ("" verb = narration only)
	confirm string   // ask this y/N first; declining skips just this step
	remote  bool     // needs kubectl — skipped in local modes
	withPod bool
}

type wizardState struct {
	active bool
	queue  []wstep
	after  []string // "Next →" lines shown when the flow completes
	done   bool
}

var wizardFlows = []struct {
	key, label string
	steps      []wstep
	after      []string
}{
	{"1", "Pod OOMKilled / restarts on memory", []wstep{
		{narr: []string{"First question: is the memory going into the Java heap, or somewhere else?",
			"heap ≈ limit → heap pressure or a leak · heap low but container memory high → it's off-heap/native"},
			args: []string{"memory"}, withPod: true},
		{confirm: "capture a HEAP DUMP now? (⚠ pauses the app) [y/N]", args: []string{"heap", "--confirm"}, withPod: true},
		{confirm: "capture native-memory detail (safe, no pause)? [y/N]", args: []string{"jcmd", "VM.native_memory summary"}, withPod: true},
	}, []string{"open the .hprof in Eclipse MAT and run 'Leak Suspects'", "press d in the menu to see every capture"}},

	{"2", "Slow / hung / high latency", []wstep{
		{narr: []string{"A thread dump shows what every thread is doing — BLOCKED threads on one lock = contention."},
			args: []string{"threads"}, withPod: true},
		{narr: []string{"And the app's own health checks — a DOWN dependency explains stalls:"},
			args: []string{"health"}, withPod: true},
	}, []string{"press a (analyze) — it flags deadlocks and blocked pools automatically", "then open the .txt in VisualVM (free, local)"}},

	{"3", "High CPU / autoscaler adding pods", []wstep{
		{narr: []string{"Two thread dumps a few seconds apart — a stack RUNNABLE in both is your hot loop."},
			args: []string{"threads"}, withPod: true},
		{args: []string{"threads"}, withPod: true},
		{args: []string{"top"}, remote: true},
		{narr: []string{"The JVM's own CPU number (0.0–1.0 of what it's allowed to use):"},
			args: []string{"metrics", "process.cpu.usage"}, withPod: true},
	}, []string{"diff the two dumps; the persistently-RUNNABLE stack is eating your CPU",
		`deeper: a 60s flight recording — jcmd "JFR.start duration=60s filename=/tmp/rec.jfr"`}},

	{"4", "Memory creeping up over time (leak)", []wstep{
		{narr: []string{"The number to watch (note VALUE, re-run this flow later; steady growth = leak):"},
			args: []string{"metrics", "jvm.memory.used"}, withPod: true},
		{confirm: "take the BASELINE heap dump now? (⚠ pauses the app) [y/N]", args: []string{"heap", "--confirm"}, withPod: true},
	}, []string{"let the app take traffic, come back, re-run this flow for dump #2",
		"then Eclipse MAT → open both → 'compare to another heap dump'"}},

	{"5", "GC pauses climbing", []wstep{
		{narr: []string{"How full is the heap and how is the collector coping:"},
			args: []string{"jcmd", "GC.heap_info"}, withPod: true},
		{args: []string{"memory"}, withPod: true},
		{narr: []string{"The GC's scorecard — COUNT and TOTAL_TIME; re-run in a minute to trend it:"},
			args: []string{"metrics", "jvm.gc.pause"}, withPod: true},
	}, []string{"TOTAL_TIME growing fast while the heap stays near-full = allocation pressure or a leak → heap dump → MAT"}},

	{"6", "Not sure — capture everything", []wstep{
		{narr: []string{"A safe snapshot first — threads, health, memory, JVM internals. No pause, no risk:"},
			args: []string{"snapshot"}, withPod: true},
		{confirm: "add a heap dump to the evidence too? (⚠ pauses the app) [y/N]",
			args: []string{"heap", "--confirm"}, withPod: true},
	}, []string{"press a (analyze) for a first pass over the whole bundle",
		"threads.txt → VisualVM · heap.hprof → Eclipse MAT (both free, local)"}},

	{"7", "Crash-looping / CrashLoopBackOff", []wstep{
		{narr: []string{"How often is it dying, and what does kubernetes say about why:"},
			args: []string{"status"}, remote: true},
		{narr: []string{"The previous container's last words — the crash reason is almost always in the final lines:"},
			args: []string{"logs", "--previous"}, remote: true, withPod: true},
	}, []string{"exit 137 / OOMKilled in the last lines → it's memory: re-run the wizard, flow 1",
		"a stack trace names the failing class — startup config/dependency is the usual culprit",
		"image or scheduling problems show in the events from the status step"}},
}

func (m model) openWizard() (tea.Model, tea.Cmd) {
	m.scr = scWizard
	m.wiz = wizardState{}
	return m, nil
}

func (m model) wizardView() string {
	if m.wiz.done {
		out := "\n  " + cTitle.Render("flow complete") + "\n"
		for _, l := range m.wiz.after {
			out += "  " + cMuted.Render("Next → "+l) + "\n"
		}
		return out + "  " + cFaint.Render("any key for the symptom list") + " "
	}
	var b strings.Builder
	b.WriteString("\n  " + cTitle.Render("Guided diagnosis — what are you seeing?") + "\n\n")
	for _, f := range wizardFlows {
		b.WriteString("   " + cKey.Render(f.key) + "  " + cMuted.Render(f.label) + "\n")
	}
	b.WriteString("   " + cKey.Render("b") + "  " + cFaint.Render("back") + "\n")
	tgt := m.t.Namespace + " / " + orAny(m.t.Selector)
	if m.mode != 1 {
		tgt = "this machine (localhost)"
	}
	b.WriteString("\n  " + cFaint.Render("target: "+tgt+" · anything that could hurt the app asks you first"))
	return b.String() + prompt()
}

func (m model) wizardKey(key string) (tea.Model, tea.Cmd) {
	if m.wiz.done {
		m.wiz = wizardState{}
		return m, nil
	}
	if m.wiz.active {
		return m, nil // steps advance via execDoneMsg
	}
	for _, f := range wizardFlows {
		if key == f.key {
			m.wiz = wizardState{active: true, queue: append([]wstep(nil), f.steps...), after: f.after}
			return m.wizardAdvance()
		}
	}
	if key == "b" || key == "B" || key == "enter" || key == "q" || key == "esc" {
		m.scr = scMenu
		return m, nil
	}
	return m, nil
}

// wizardAdvance runs the next step in the queue (called after each execDone).
func (m model) wizardAdvance() (tea.Model, tea.Cmd) {
	for len(m.wiz.queue) > 0 {
		st := m.wiz.queue[0]
		m.wiz.queue = m.wiz.queue[1:]
		if st.remote && m.mode != 1 {
			continue // kubectl-only step in a local mode
		}
		if st.confirm != "" {
			c := st
			c.confirm = ""
			return m.askConfirm2(st.confirm, "",
				func(mm *model) tea.Cmd { return mm.wizStepCmd(c) }, // yes: run it; done → advance
				func(mm *model) tea.Cmd { // no: skip this step, keep going
					mdl, cmd := mm.wizardAdvance()
					*mm = mdl.(model)
					return cmd
				})
		}
		return m, m.wizStepCmd(st)
	}
	m.wiz.active = false
	m.wiz.done = true
	m.scr = scWizard
	return m, nil
}

func (m *model) wizStepCmd(st wstep) tea.Cmd {
	// narration rides along in the echoed command via a leading printf
	if m.mode == 1 {
		return m.runCLI(st.withPod, st.args...)
	}
	return m.runLocal(st.args...)
}
