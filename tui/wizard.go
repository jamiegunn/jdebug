package main

// wizard.go — guided diagnosis: symptom → a queue of narrated steps. Each
// step shells out via ExecProcess; destructive steps confirm first. Works in
// every mode (kubectl-only steps are skipped in local modes, with a note).

import (
	"path/filepath"
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
	after  []string // "Next →" lines appended when the flow completes
}

// flowMode maps each wizard flow to the incident mode it sets — running a flow
// tells the dashboard "this is what I'm chasing", so NEXT leads with it after.
// "not sure" (6) clears the mode.
var flowMode = map[string]string{
	"1": "memory", "2": "slow", "3": "cpu", "4": "memory",
	"5": "memory", "6": "", "7": "restarting", "8": "deployed",
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
		{narr: []string{"A hot loop is the SAME stack RUNNABLE in two dumps taken a few seconds apart.",
			"Capturing dump #1 now:"},
			args: []string{"threads"}, withPod: true},
		{confirm: "dump #1 saved. Let the app run under load for ~5 seconds, THEN press y for dump #2 — comparing the two is what reveals the hot loop (any other key skips it) [y/N]",
			args: []string{"threads"}, withPod: true},
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
		{narr: []string{"First the kubernetes layer — exit code, limits, probes decoded in plain language:"},
			args: []string{"why"}, remote: true, withPod: true},
		{narr: []string{"The previous container's last words — the crash reason is almost always in the final lines:"},
			args: []string{"logs", "--previous"}, remote: true, withPod: true},
	}, []string{"exit 137 / OOMKilled above → it's memory: re-run the wizard, flow 1",
		"a stack trace names the failing class — startup config/dependency is the usual culprit",
		"a failing liveness probe restarting a healthy-but-slow app → loosen the probe, not the app"}},

	{"8", "A deploy just happened — did that break it?", []wstep{
		{narr: []string{"What the new revision changed — image digest, rollout timing, restart reason, scale intent:"},
			args: []string{"what-changed"}, remote: true, withPod: true},
		{narr: []string{"The chronology — the pod's events and your captures in time order:"},
			args: []string{"timeline"}, remote: true, withPod: true},
		{narr: []string{"And the previous container's last words, if it restarted after the rollout:"},
			args: []string{"logs", "--previous"}, remote: true, withPod: true},
	}, []string{"a fresh OOM/crash right after the deploy → suspect the new revision (roll back to test)",
		"an OLD ReplicaSet still serving pods → the rollout is stuck: jdebug topology"}},
}

func (m model) openWizard() (tea.Model, tea.Cmd) {
	m.scr = scWizard
	m.wiz = wizardState{}
	return m, nil
}

func (m model) wizardView() string {
	var b strings.Builder
	b.WriteString("\n  " + cTitle.Render("guided diagnosis — what are you seeing?") + "\n")
	// several symptoms overlap for a restarting pod (1 memory, 4 leak, 7 crash),
	// so give the one tiebreak that matters: start at the crash flow, which hands
	// you to the memory flow if that's the actual cause.
	b.WriteString("  " + cFaint.Render("keeps restarting? start at 7 — it routes you to 1 if it's memory") + "\n\n")
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
	if m.wiz.active {
		return m, nil // steps advance via streamDoneMsg
	}
	for _, f := range wizardFlows {
		if key == f.key {
			// flows run ON the dashboard: every step streams into the
			// bottom output pane, so the live panels stay in view
			m.wiz = wizardState{active: true, queue: append([]wstep(nil), f.steps...), after: f.after}
			m.incMode = flowMode[f.key] // this flow sets the incident mode → weights NEXT
			m.scr = scMenu
			if m.out.running && m.out.cancel != nil {
				m.out.cancel()
			}
			m.out = outState{id: m.out.id} // fresh transcript for this flow
			m.out.raw = []byte("── guided diagnosis: " + f.label + " ──\n")
			return m.wizardAdvance()
		}
	}
	if key == "b" || key == "B" || key == "enter" || key == "q" || key == "esc" {
		m.scr = scMenu
		return m, nil
	}
	return m, nil
}

// wizardAdvance runs the next step in the queue (called after each step's
// stream completes).
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
				func(mm *model) tea.Cmd { return mm.wizRunTo(c) }, // yes: run it; done → advance
				func(mm *model) tea.Cmd { // no: skip this step, keep going
					mdl, cmd := mm.wizardAdvance()
					*mm = mdl.(model)
					return cmd
				})
		}
		return m.wizRun(st)
	}
	// flow complete — the wrap-up lands in the same transcript
	m.wiz.active = false
	tail := "\n── flow complete ──\n"
	for _, l := range m.wiz.after {
		tail += "Next → " + l + "\n"
	}
	(&m).appendChunk([]byte(tail))
	if m.scr == scWizard {
		m.scr = scMenu
	}
	return m, nil
}

// wizRun streams one wizard step into the output pane, narration first.
func (m model) wizRun(st wstep) (tea.Model, tea.Cmd) {
	var prefix []byte
	if len(m.out.raw) > 0 {
		prefix = append(prefix, '\n')
	}
	for _, n := range st.narr {
		prefix = append(prefix, []byte("· "+n+"\n")...)
	}
	verb := "jdebug " + strings.Join(st.args, " ")
	var words []string
	if m.mode == 1 {
		words = append([]string{filepath.Join(m.kit, "jdebug")}, st.args...)
		if st.withPod && m.t.Pod != "" {
			words = append(words, m.t.Pod)
		}
	} else {
		verb = "jdebug-local " + strings.Join(st.args, " ")
		if m.t.SSH != "" {
			verb += " · ssh " + m.t.SSH
		}
		words = localWords(m.kit, m.t, st.args...)
	}
	return m.startPane("guided diagnosis — "+verb, targetEnv(m.t), prefix, true, words...)
}

func (mm *model) wizRunTo(st wstep) tea.Cmd {
	mdl, cmd := mm.wizRun(st)
	*mm = mdl.(model)
	return cmd
}
