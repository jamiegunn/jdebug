package main

// menu.go — the redesigned main screen: 2-line header + status line, hero
// banner, INSPECT/CAPTURE/LOGS sections with right-pinned risk dots, footer
// legend, prompt. Gated behind target readiness, per mode.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
)

func (m model) tw() int {
	w := m.width
	if w < 78 {
		w = 78
	}
	if w > 132 {
		w = 132
	}
	return w
}

// showPanel: the live target panel needs room; drop it on narrow terminals.
func (m model) showPanel() bool { return m.tw() >= 104 }

// leftW is the width the menu body uses (full width minus the panel column).
func (m model) leftW() int {
	if m.showPanel() {
		return m.tw() - panelW - 2
	}
	return m.tw()
}

func rule(w int) string { return " " + cRule.Render(strings.Repeat("─", w-2)) }

func (m model) modeLabel() string {
	switch m.mode {
	case 2:
		return "in-pod · localhost"
	case 3:
		return "bare metal · localhost"
	}
	return "remote · kubectl → pod"
}

func (m model) headerRemote(reachable bool) string {
	w := m.tw()
	title := " jvm debug kit"
	right := m.modeLabel() + " "
	pad := w - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	var b strings.Builder
	b.WriteString(cTitle.Render(title) + strings.Repeat(" ", pad) + cDim.Render(right) + "\n")

	dot := cOK.Render("●")
	extra := ""
	if !reachable {
		dot = cDisr.Render("●")
		extra = " " + cDisr.Render("unreachable — [c] explains why")
	}
	pod := m.t.Pod
	if len(pod) > 18 {
		pod = "…" + pod[len(pod)-15:]
	}
	tgt := m.t.Namespace + " / " + m.t.Container
	if pod != "" {
		tgt += " · " + pod
	}
	act := strings.TrimPrefix(m.t.Actuator, "http://localhost")
	sep := cFaint.Render("  ·  ")
	b.WriteString(" " + dot + " " + cMuted.Render(currentContext()) + extra +
		sep + cMuted.Render(tgt) + sep + cMuted.Render(act) +
		sep + cFaint.Render("[g] retarget  [M] mode") + "\n")
	if m.staleP != "" {
		b.WriteString("   " + cWarn.Render("your previous pin "+m.staleP+" no longer exists — back to auto ([g] to re-pick)") + "\n")
	}
	b.WriteString(rule(w))
	return b.String()
}

func (m model) headerLocal(jattachOK bool) string {
	w := m.tw()
	title := " jvm debug kit"
	right := m.modeLabel() + " "
	pad := w - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	jat := cDisr.Render("jattach missing")
	if jattachOK {
		jat = cMuted.Render("jattach ok")
	}
	act := strings.TrimPrefix(m.t.Actuator, "http://localhost")
	sep := cFaint.Render("  ·  ")
	return cTitle.Render(title) + strings.Repeat(" ", pad) + cDim.Render(right) + "\n" +
		" " + cOK.Render("●") + " " + cMuted.Render(act) + sep + jat +
		sep + cFaint.Render("[s] settings  [M] mode") + "\n" + rule(w)
}

func (m model) banner() string {
	desc := "— describe the symptom, it runs the right captures"
	if m.leftW() < 96 {
		desc = "— describe the symptom"
	}
	return "\n " + cAcc.Render("▎") + cKey.Render("▸ w") + "  " +
		cTitle.Render("guided diagnosis") + " " + cMuted.Render(desc) +
		"  " + cOK.Render("← start here") + "\n"
}

func (m model) section(label, sub string) string {
	w := m.leftW()
	used := 1 + len(label) + 1
	if sub != "" {
		used += 2 + lipgloss.Width(sub)
	}
	fill := w - used - 1
	if fill < 3 {
		fill = 3
	}
	s := " " + cDim.Render(label)
	if sub != "" {
		s += "  " + cFaint.Render(sub)
	}
	return s + " " + cRule.Render(strings.Repeat("─", fill))
}

type action struct {
	key, name, desc, risk, riskText string
}

func (m model) row(a action) string {
	w := m.leftW()
	dot := cSafe
	switch a.risk {
	case "caution":
		dot = cCaut
	case "disruptive":
		dot = cDisr
	}
	right := "●"
	if a.riskText != "" {
		right += " " + a.riskText
	}
	left := fmt.Sprintf("   %s   %-12s%s", a.key, a.name, a.desc)
	pad := w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if pad < 1 {
		pad = 1
	}
	return "   " + cKey.Render(a.key) + "   " + cBody.Render(fmt.Sprintf("%-12s", a.name)) +
		cMuted.Render(a.desc) + strings.Repeat(" ", pad) + dot.Render(right)
}

func (m model) footer(nav string) string {
	w := m.tw()
	legendPlain := "●●● safe / caution / disruptive"
	pad := w - 1 - 5 - len(nav) - lipgloss.Width(legendPlain) - 1
	if pad < 2 {
		pad = 2
	}
	legend := cSafe.Render("●") + cCaut.Render("●") + cDisr.Render("●") + " " +
		cFaint.Render("safe / caution / disruptive")
	return rule(w) + "\n " + cFaint.Render("more") + "  " + cDim.Render(nav) +
		strings.Repeat(" ", pad) + legend
}

func prompt() string { return "\n " + cOK.Render("❯") + " " }

// --- views ---------------------------------------------------------------------

var remoteActions = struct {
	inspect, capture, logs []action
}{
	inspect: []action{
		{"s", "status", "pods up? restarts, recent events", "safe", ""},
		{"h", "health", "app checks — db, queue, disk", "safe", ""},
		{"o", "top", "CPU + memory per pod, autoscaler", "safe", ""},
		{"m", "memory", "container total vs JVM heap/non-heap", "safe", ""},
	},
	capture: []action{
		{"t", "threads", "what every thread is doing now", "safe", ""},
		{"j", "jcmd", "advanced JVM — GC, JFR, native", "caution", ""},
		{"H", "heap", "every object, for leak hunting", "disruptive", "pauses app"},
		{"x", "snapshot", "everything in one offline bundle", "safe", ""},
	},
	logs: []action{
		{"l", "logs", "live stream from every replica", "safe", ""},
		{"v", "verbosity", "change log level, no restart", "caution", ""},
	},
}

var localActions = struct{ inspect, capture []action }{
	inspect: []action{
		{"h", "health", "app checks — db, queue, disk", "safe", ""},
		{"e", "metrics", "browse JVM metrics, or one live value", "safe", ""},
		{"m", "memory", "container total vs JVM heap/non-heap", "safe", ""},
	},
	capture: []action{
		{"t", "threads", "what every thread is doing now", "safe", ""},
		{"j", "jcmd", "advanced JVM — GC, JFR, native", "caution", ""},
		{"H", "heap", "every object, for leak hunting", "disruptive", "pauses app"},
		{"x", "snapshot", "everything in one offline bundle", "safe", ""},
	},
}

func (m model) menuView() string {
	var b strings.Builder
	if m.mode == 1 {
		p := m.remote
		b.WriteString(m.headerRemote(p.Cluster))
		if !p.OK {
			b.WriteString("\n  " + cWarn.Render("⚠ SET UP YOUR TARGET FIRST") +
				cMuted.Render(" — the tools appear when every line below is ") + cSafe.Render("✓") + cMuted.Render(":") + "\n\n")
			b.WriteString(strings.Join(p.Lines, "\n") + "\n\n")
			b.WriteString("  " + cBody.Render("Press ") + cKey.Render("g") + cBody.Render(" to open the target editor") +
				cFaint.Render(" (Enter works too)") + "\n")
			b.WriteString("\n " + cFaint.Render("more") + "  " + cDim.Render("[g] target  [c] check setup  [?] help  [M] mode  [q] quit"))
			b.WriteString(prompt())
			return b.String()
		}
		var body strings.Builder
		body.WriteString(m.banner() + "\n")
		body.WriteString(m.section("INSPECT", "read-only") + "\n")
		for _, a := range remoteActions.inspect {
			body.WriteString(m.row(a) + "\n")
		}
		body.WriteString("\n" + m.section("CAPTURE", "saves to dumps/ · [d] browse") + "\n")
		for _, a := range remoteActions.capture {
			body.WriteString(m.row(a) + "\n")
		}
		body.WriteString("\n" + m.section("LOGS", "") + "\n")
		for _, a := range remoteActions.logs {
			body.WriteString(m.row(a) + "\n")
		}
		b.WriteString("\n" + m.withPanel(body.String()))
		b.WriteString("\n" + m.footer("[a] analyze  [c] check setup  [?] help  [q] quit"))
		b.WriteString(prompt())
		return b.String()
	}

	p := m.local
	b.WriteString(m.headerLocal(p.Jattach))
	if !p.OK {
		b.WriteString("\n  " + cWarn.Render("⚠ SET UP A ROUTE TO THE JVM FIRST") +
			cMuted.Render(" — the tools appear when at least one line is ") + cSafe.Render("✓") + cMuted.Render(":") + "\n\n")
		b.WriteString(strings.Join(p.Lines, "\n") + "\n")
		b.WriteString("\n " + cFaint.Render("more") + "  " + cDim.Render("[s] settings  [i] stage jattach  [?] help  [M] mode  [q] quit"))
		b.WriteString(prompt())
		return b.String()
	}
	var body strings.Builder
	body.WriteString(m.banner() + "\n")
	body.WriteString(m.section("INSPECT", "read-only") + "\n")
	for _, a := range localActions.inspect {
		body.WriteString(m.row(a) + "\n")
	}
	body.WriteString("\n" + m.section("CAPTURE", "saves to /tmp · [d] browse") + "\n")
	for _, a := range localActions.capture {
		body.WriteString(m.row(a) + "\n")
	}
	b.WriteString("\n" + m.withPanel(body.String()))
	b.WriteString("\n" + m.footer("[a] analyze  [i] stage jattach  [s] settings  [?] help  [q] quit"))
	b.WriteString(prompt())
	return b.String()
}

// withPanel joins the menu body with the live target panel when there's room.
func (m model) withPanel(body string) string {
	if !m.showPanel() {
		return body
	}
	h := strings.Count(body, "\n") + 1
	left := lipgloss.NewStyle().Width(m.leftW()).Render(body)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, m.panelView(h))
}

// --- key handling ---------------------------------------------------------------

func (m model) menuKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.mode == 1 {
		m.probeRemote(false)
		if !m.remote.OK {
			switch key {
			case "enter", "g", "G":
				return m.openEditor()
			case "?":
				m.scr = scHelp
				return m, nil
			case "c", "C":
				m.prev = scMenu
				return m, m.runCLI(false, "doctor")
			case "M":
				m.scr = scChooser
				return m, nil
			case "q", "Q":
				return m.askConfirm("quit jdebug? [y/N]", "", func(mm *model) tea.Cmd {
					mm.quitMsg = cFaint.Render("transcript of everything from this session: " + sessionLog)
					return tea.Quit
				})
			}
			return m, nil
		}
		return m.remoteKey(key)
	}
	m.probeLocal(false)
	if !m.local.OK {
		switch key {
		case "enter", "s", "S":
			return m.openLocalSettings()
		case "i", "I":
			m.prev = scMenu
			return m, m.stageJattachLocal()
		case "?":
			m.scr = scHelp
			return m, nil
		case "M":
			m.scr = scChooser
			return m, nil
		case "q", "Q":
			return m.askConfirm("quit jdebug? [y/N]", "", func(mm *model) tea.Cmd { return tea.Quit })
		}
		return m, nil
	}
	return m.localKey(key)
}

func (m model) remoteKey(key string) (tea.Model, tea.Cmd) {
	m.prev = scMenu
	switch key {
	case "w", "W":
		return m.openWizard()
	case "s", "S":
		return m, m.runCLI(false, "status")
	case "h":
		return m, m.runCLI(true, "health")
	case "o", "O":
		return m, m.runCLI(false, "top")
	case "m":
		return m, m.runCLI(true, "memory")
	case "t", "T":
		m.scr = scVia
		return m, nil
	case "j", "J":
		m.scr = scJcmd
		return m, nil
	case "H":
		return m.askConfirm("heap dump pauses the app while it runs — press H again to confirm, any other key cancels", "H",
			func(mm *model) tea.Cmd { mm.viaFlag = ""; mm.scr = scVia; mm.pendHeap = true; return nil })
	case "x", "X":
		return m.askConfirm2("include a heap dump in the bundle? (PAUSES the JVM) [y/N]", "",
			func(mm *model) tea.Cmd { return mm.runCLI(true, "snapshot", "--heap", "--confirm") },
			func(mm *model) tea.Cmd { return mm.runCLI(true, "snapshot") })
	case "l", "L":
		return m, m.runCLI(false, "logs")
	case "v", "V":
		m.input = inputBox{title: "logger (e.g. com.example.debugdemo, ROOT):", then: inputLogger}
		m.scr = scInput
		return m, nil
	case "?":
		m.scr = scHelp
		return m, nil
	case "c", "C":
		return m, m.runCLI(false, "doctor")
	case "a", "A":
		return m, m.runCLI(false, "analyze")
	case "d", "D":
		return m, m.runCLI(false, "dumps")
	case "i", "I":
		return m, m.runCLI(true, "install-jattach")
	case "p", "P":
		return m, m.runCLI(true, "push-local")
	case "g", "G", "enter":
		return m.openEditor()
	case "M":
		m.scr = scChooser
		return m, nil
	case "q", "Q", "ctrl+c":
		return m.askConfirm("quit jdebug? [y/N]", "", func(mm *model) tea.Cmd {
			mm.quitMsg = cFaint.Render("transcript of everything from this session: " + sessionLog)
			return tea.Quit
		})
	}
	return m, nil
}

func (m model) localKey(key string) (tea.Model, tea.Cmd) {
	m.prev = scMenu
	switch key {
	case "w", "W":
		return m.openWizard()
	case "h":
		return m, m.runLocal("health")
	case "e", "E":
		return m, m.runLocal("metrics")
	case "m":
		return m, m.runLocal("memory")
	case "t", "T":
		return m, m.runLocal("threads")
	case "j", "J":
		m.scr = scJcmd
		return m, nil
	case "H":
		return m.askConfirm("heap dump pauses the app while it runs — press H again to confirm, any other key cancels", "H",
			func(mm *model) tea.Cmd { return mm.runLocal("heap", "--confirm") })
	case "x", "X":
		return m.askConfirm2("include a heap dump in the bundle? (PAUSES the JVM) [y/N]", "",
			func(mm *model) tea.Cmd { return mm.runLocal("snapshot", "--heap") },
			func(mm *model) tea.Cmd { return mm.runLocal("snapshot") })
	case "?":
		m.scr = scHelp
		return m, nil
	case "a", "A":
		return m, runShell(nil, m.kit+"/observe/analyze.sh", "/tmp")
	case "d", "D":
		return m, m.runLocal("dumps")
	case "i", "I":
		return m, m.stageJattachLocal()
	case "s", "S":
		return m.openLocalSettings()
	case "M":
		m.scr = scChooser
		return m, nil
	case "q", "Q", "ctrl+c":
		return m.askConfirm("quit jdebug? [y/N]", "", func(mm *model) tea.Cmd { return tea.Quit })
	}
	return m, nil
}

// --- tier pick (via) --------------------------------------------------------------

func (m model) viaView() string {
	return m.menuView() + "\n" +
		cFaint.Render("  auto tries the safest route first and announces each fallback") + "\n" +
		"  " + cMuted.Render("[Enter] auto (recommended) · [o] actuator · [j] jattach · [d] jdk") + " "
}

func (m model) viaKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "o", "O":
		m.viaFlag = "actuator"
	case "j", "J":
		m.viaFlag = "jattach"
	case "d", "D":
		m.viaFlag = "jdk"
	default:
		m.viaFlag = ""
	}
	m.scr = scMenu
	m.prev = scMenu
	args := []string{"threads"}
	if m.pendHeap {
		args = []string{"heap"}
	}
	if m.viaFlag != "" {
		args = append(args, "--via", m.viaFlag)
	}
	if m.pendHeap {
		args = append(args, "--confirm")
		m.pendHeap = false
	}
	return m, m.runCLI(true, args...)
}

// --- jcmd quick pick ---------------------------------------------------------------

var jcmdPicks = []struct{ key, cmd, why string }{
	{"1", "GC.heap_info", "how full is the heap, which collector"},
	{"2", "VM.native_memory summary", "off-heap breakdown (needs NMT)"},
	{"3", "Thread.print -l", "thread dump via the attach socket"},
	{"4", "VM.flags", "the flags the JVM actually started with"},
	{"5", "JFR.start duration=60s filename=/tmp/rec.jfr", "60s profiling recording"},
}

func (m model) jcmdView() string {
	var b strings.Builder
	b.WriteString(m.menuView() + "\n")
	b.WriteString(cFaint.Render("  the useful jcmd commands:") + "\n")
	for _, j := range jcmdPicks {
		b.WriteString("   " + cKey.Render(j.key) + "  " + cBody.Render(fmt.Sprintf("%-44s", j.cmd)) + cMuted.Render(j.why) + "\n")
	}
	b.WriteString("  " + cMuted.Render("pick 1-5 · t to type any jcmd · anything else cancels") + " ")
	return b.String()
}

func (m model) jcmdKey(key string) (tea.Model, tea.Cmd) {
	m.scr = scMenu
	for _, j := range jcmdPicks {
		if key == j.key {
			m.prev = scMenu
			if m.mode == 1 {
				return m, m.runCLI(true, "jcmd", j.cmd)
			}
			return m, m.runLocal("jcmd", j.cmd)
		}
	}
	if key == "t" || key == "T" {
		m.input = inputBox{title: "jcmd command:", then: inputJcmd}
		m.scr = scInput
	}
	return m, nil
}

// --- log level pick -----------------------------------------------------------------

var levels = []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "OFF"}

func (m model) levelView() string {
	return m.menuView() + "\n  " +
		cMuted.Render("level for "+m.logger+":  1 TRACE · 2 DEBUG · 3 INFO · 4 WARN · 5 ERROR · 6 OFF") + " "
}

func (m model) levelKey(key string) (tea.Model, tea.Cmd) {
	m.scr = scMenu
	if key >= "1" && key <= "6" {
		lv := levels[key[0]-'1']
		m.prev = scMenu
		return m, m.runCLI(false, "log-level", m.logger, lv)
	}
	return m, nil
}
