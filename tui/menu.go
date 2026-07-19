package main

// menu.go — the redesigned main screen: 2-line header + status line, hero
// banner, INSPECT/CAPTURE/LOGS sections with right-pinned risk dots, footer
// legend, prompt. Gated behind target readiness, per mode.

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// (tw/tier/showPanel/leftW live in layout.go)

func rule(w int) string { return " " + cRule.Render(strings.Repeat("─", w-2)) }

func (m model) modeLabel() string {
	if m.mode == 2 { // bare metal
		if m.t.SSH != "" {
			return "bare metal · ssh " + m.t.SSH
		}
		return "bare metal · this host"
	}
	return "kubernetes · kubectl → pod"
}

func (m model) headerRemote(reachable bool) string {
	w := m.tw()
	title := " jdebug"
	right := m.modeLabel() + " "
	pad := w - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	var b strings.Builder
	b.WriteString(cTitle.Render(title) + strings.Repeat(" ", pad) + cDim.Render(right) + "\n")

	// reachability reads without colour too: green dot + "ok", or red + why
	dot := cOK.Render("●") + cFaint.Render(" ok")
	extra := ""
	if !reachable {
		dot = cDisr.Render("●")
		extra = " " + cDisr.Render("unreachable — [c] explains why")
	}
	// namespace / pod / container, never truncated — this is the one line
	// that must be copy-pasteable into kubectl
	tgt := m.t.Namespace
	if m.t.Pod != "" {
		tgt += " / " + m.t.Pod
	}
	tgt += " / " + m.t.Container
	act := strings.TrimPrefix(m.t.Actuator, "http://localhost")
	actSeg := cMuted.Render(act)
	if m.t.Pod != "" && !m.panel.When.IsZero() && !m.panel.ActuatorOK {
		actSeg = cFaint.Render(act) + cWarn.Render(" ✗")
	}
	sep := cFaint.Render("  ·  ")
	hints := cFaint.Render("[g] retarget  [M] mode")
	statusLine := " " + dot + " " + cMuted.Render(currentContext()) + extra +
		sep + cMuted.Render(tgt) + sep + actSeg + sep + hints
	if lipgloss.Width(statusLine) <= w {
		b.WriteString(statusLine + "\n")
	} else {
		// narrow terminal: the untruncated target gets its own line instead
		// of wrapping mid-name
		b.WriteString(" " + dot + " " + cMuted.Render(currentContext()) + extra +
			sep + actSeg + sep + hints + "\n")
		b.WriteString("   " + cMuted.Render(tgt) + "\n")
	}
	if m.autoPod > 0 {
		note := "auto-picked the only matching pod — [g] to change target"
		if m.autoPod > 1 {
			note = fmt.Sprintf("showing 1 of %d pods the selector matches — [g] p to pick another", m.autoPod)
		}
		b.WriteString("   " + cFaint.Render(note) + "\n")
	}
	if m.staleP != "" {
		b.WriteString("   " + cWarn.Render("your previous pin "+m.staleP+" no longer exists — back to auto ([g] to re-pick)") + "\n")
	}
	b.WriteString(rule(w))
	return b.String()
}

// headerH is the header's real height in rows. It is NOT a constant: the target
// line gets its own row when it's too wide to fit (long pod names), and a stale
// pin adds another. Click hit-testers must use this, never a hardcoded 3, or the
// whole right column is off by a row and every click lands one entry too far.
func (m model) headerH() int {
	return strings.Count(m.headerRemote(m.remote.Cluster), "\n") + 1
}

func (m model) headerLocal(jattachOK bool) string {
	w := m.tw()
	title := " jdebug"
	right := m.modeLabel() + " "
	pad := w - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	jat := cDisr.Render("jattach missing")
	if jattachOK {
		jat = cMuted.Render("jattach ok")
	}
	if m.t.SSH != "" {
		jat = cFaint.Render("jattach on " + m.t.SSH) // staged remotely; can't stat from here
	}
	act := strings.TrimPrefix(m.t.Actuator, "http://localhost")
	where := cMuted.Render(act)
	if m.t.SSH != "" {
		where = cMuted.Render(act) + cFaint.Render(" on "+m.t.SSH)
	}
	jvm := cFaint.Render("jvm auto")
	if m.t.JVMPid != "" {
		jvm = cMuted.Render("jvm " + m.t.JVMPid)
	}
	sep := cFaint.Render("  ·  ")
	return cTitle.Render(title) + strings.Repeat(" ", pad) + cDim.Render(right) + "\n" +
		" " + cOK.Render("●") + " " + where + sep + jat + sep + jvm +
		sep + cFaint.Render("[p] jvm  [g] host  [s] settings  [M] mode") + "\n" + rule(w)
}

func (m model) banner() string {
	desc := "— pick the symptom, it runs the right captures · safest when unsure"
	if m.leftW() < 96 {
		desc = "— pick the symptom · safest when unsure"
	}
	return "\n" + m.section("START HERE", "") + "\n" +
		" " + cAcc.Render("▎") + cKey.Render("▸ w") + "  " +
		cTitle.Render("guided diagnosis") + " " + cMuted.Render(desc) + "\n"
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
	// risk must read WITHOUT colour (NO_COLOR, screenshots, colour-blind):
	// caution/disruptive = dot + a word. SAFE rows show NOTHING — a wall of
	// green "safe" dots on the 13 read-only rows only dilutes the 5 that carry
	// real danger; the section header ("read-only — can't hurt anything")
	// already says the quick checks are safe. An explicit riskText wins.
	rt := a.riskText
	if rt == "" && a.risk != "safe" {
		rt = a.risk
	}
	right := ""
	if a.risk != "safe" {
		right = "● " + rt
	}
	// truncate the description so a long row never wraps at any width
	prefix := fmt.Sprintf("   %s   %-12s", a.key, a.name)
	avail := w - lipgloss.Width(prefix) - lipgloss.Width(right) - 2
	if avail < 8 {
		avail = 8
	}
	desc := ansi.Truncate(a.desc, avail, "…")
	pad := w - lipgloss.Width(prefix) - lipgloss.Width(desc) - lipgloss.Width(right) - 1
	if pad < 1 {
		pad = 1
	}
	return "   " + cKey.Render(a.key) + "   " + cBody.Render(fmt.Sprintf("%-12s", a.name)) +
		cMuted.Render(desc) + strings.Repeat(" ", pad) + dot.Render(right)
}

// --- click-to-run: a menu row runs by clicking its label, not just its key ---

// menuActions returns every runnable action for the current mode, so a click
// can be validated against the real menu.
func (m model) menuActions() []action {
	if m.mode == 1 {
		out := append([]action{}, remoteActions.quick...)
		out = append(out, remoteActions.capture...)
		return append(out, remoteActions.advanced...)
	}
	out := append([]action{}, localActions.quick...)
	out = append(out, localActions.capture...)
	return append(out, localActions.advanced...)
}

// menuKeyRe matches a rendered action row: "   <key>   <name…>".
var menuKeyRe = regexp.MustCompile(`^\s{3}(\S)\s{3}\S`)

// menuRowClick maps a left-click at (x,y) to the action key on that row, by
// reading the rendered menu column — tier-agnostic, no fragile geometry. The
// caller dispatches through menuKey so confirmation gates are preserved.
func (m model) menuRowClick(x, y int) (string, bool) {
	if m.scr != scMenu || !m.remote.OK && m.mode == 1 {
		return "", false
	}
	lw := m.leftW()
	if x < 0 || x >= lw { // only the menu column (panels sit to the right)
		return "", false
	}
	lines := strings.Split(m.menuView(), "\n")
	if y < 0 || y >= len(lines) {
		return "", false
	}
	seg := ansi.Strip(ansi.Truncate(lines[y], lw, ""))
	mm := menuKeyRe.FindStringSubmatch(seg)
	if mm == nil {
		return "", false
	}
	key := mm[1]
	for _, a := range m.menuActions() {
		if a.key == key {
			return key, true
		}
	}
	return "", false
}

// hasLiveWarning reports whether the panel is currently showing a real problem
// signal (not just an info nudge) — the same signal that populates NEXT.
func (m model) hasLiveWarning() bool {
	for _, s := range m.suggestionRows() {
		if s.conf != "" { // likely / possible / unknown — a genuine signal
			return true
		}
	}
	return false
}

// dashNav is the dashboard footer's key legend. When a live warning is firing it
// leads with the "I'm stuck" trio ([n] what this means, [E] escalate) so the
// paged junior discovers the runbook + senior-handoff without first pressing ?.
func (m model) dashNav() string {
	nav := "[a] analyze  [c] check setup  [?] help  [q] quit"
	// only on wide terminals — narrower footers can't take the extra keys without
	// wrapping, and the runbook/escalate keys still work (and show under ?) there.
	if m.mode == 1 && m.tw() >= 140 && m.hasLiveWarning() {
		nav = "[n] what's wrong  [E] escalate  " + nav
	}
	return nav
}

func (m model) footer(nav string) string {
	w := m.tw()
	// keys are shown per row; on wide terminals (mouse territory) also tell
	// users the rows are clickable. Dropped when narrow so it can't wrap.
	lead := ""
	if w >= 140 {
		lead = "press a key or click a row · "
	}
	staged := m.ownedArtifacts()
	if staged > 0 { // the staged-in-pod indicator takes priority
		lead = fmt.Sprintf("⚠ %d file(s) staged in the pod · u cleanup · ", staged)
	}
	legendPlain := "●●● safe / caution / disruptive"
	// drop the optional mouse hint if a long nav (e.g. the stuck-help keys) would
	// otherwise push the footer past the terminal width and wrap. The staged-file
	// warning is a safety notice, so it's never dropped.
	if staged == 0 && 1+5+lipgloss.Width(lead)+lipgloss.Width(nav)+lipgloss.Width(legendPlain)+1 > w {
		lead = ""
	}
	// esc at the root flashes how to actually leave, replacing the lead until the
	// next keypress — esc is "back" everywhere else, so this explains the no-op.
	if m.escHint {
		lead = "you're at the top — q quits · g retargets · "
	}
	pad := w - 1 - 5 - lipgloss.Width(lead) - lipgloss.Width(nav) - lipgloss.Width(legendPlain) - 1
	if pad < 2 {
		pad = 2
	}
	legend := cSafe.Render("●") + cCaut.Render("●") + cDisr.Render("●") + " " +
		cFaint.Render("safe / caution / disruptive")
	return rule(w) + "\n " + cFaint.Render("more") + "  " + cFaint.Render(lead) + cDim.Render(nav) +
		strings.Repeat(" ", pad) + legend
}

func prompt() string { return "\n " + cOK.Render("❯") + " " }

// --- views ---------------------------------------------------------------------

// Sections mirror a junior SRE's mental model: symptom first, then the
// question each check answers, evidence next, power tools last.
var remoteActions = struct {
	quick, capture, advanced []action
}{
	quick: []action{
		{"s", "status", "is the pod running or restarting?", "safe", ""},
		{"h", "health", "is a dependency — db, queue — down?", "safe", ""},
		{"o", "top", "which pod is eating CPU or memory?", "safe", ""},
		{"m", "memory", "is the app near its memory limit?", "safe", ""},
		{"W", "workload", "the tree + limits, probes, exit codes, autoscaling", "safe", ""},
		{"e", "context", "services, env, probes, deps — how it's wired", "safe", ""},
		{"S", "security", "root? privileged? network policy?", "safe", ""},
		{"l", "logs", "what did the app say? (live stream)", "safe", ""},
	},
	capture: []action{
		{"t", "threads", "safe snapshot of what the code is doing", "safe", ""},
		{"x", "bundle", "everything in one safe offline bundle", "safe", ""},
		{"H", "heap", "every object in memory — for leak hunting", "disruptive", "pauses app"},
	},
	advanced: []action{
		{"j", "jcmd", "raw JVM commands — GC, profiling, native memory", "caution", ""},
		{"v", "verbosity", "change log level live, no restart", "caution", ""},
		{"T", "terminal", "a shell inside the pod — exit returns here", "caution", ""},
		{"R", "restart", "rolling-restart the pods", "disruptive", "restarts app"},
		{"K", "kill pod", "delete one pod", "disruptive", "drops the pod"},
	},
}

var localActions = struct{ quick, capture, advanced []action }{
	quick: []action{
		{"h", "health", "is a dependency — db, queue — down?", "safe", ""},
		{"e", "metrics", "browse the JVM's live numbers", "safe", ""},
		{"m", "memory", "is the app near its memory limit?", "safe", ""},
	},
	capture: []action{
		{"t", "threads", "safe snapshot of what the code is doing", "safe", ""},
		{"x", "bundle", "everything in one safe offline bundle", "safe", ""},
		{"H", "heap", "every object in memory — for leak hunting", "disruptive", "pauses app"},
	},
	advanced: []action{
		{"j", "jcmd", "raw JVM commands — GC, profiling, native memory", "caution", ""},
	},
}

// remoteBody builds START HERE / QUICK CHECKS / CAPTURE EVIDENCE / ADVANCED.
func (m model) remoteBody() string {
	var body strings.Builder
	body.WriteString(m.banner() + "\n")
	body.WriteString(m.section("QUICK CHECKS", "read-only — can't hurt anything") + "\n")
	for _, a := range remoteActions.quick {
		body.WriteString(m.row(a) + "\n")
	}
	body.WriteString("\n" + m.section("CAPTURE EVIDENCE", "saves to dumps/ · [d] browse") + "\n")
	for _, a := range remoteActions.capture {
		body.WriteString(m.row(a) + "\n")
	}
	body.WriteString("\n" + m.section("ADVANCED", "") + "\n")
	for _, a := range remoteActions.advanced {
		body.WriteString(m.row(a) + "\n")
	}
	return body.String()
}

func (m model) menuView() string {
	var b strings.Builder
	if m.mode == 1 {
		p := m.remote
		if p.OK && m.capsFocus {
			return m.capsFocusView()
		}
		if p.OK && m.logs.focus {
			return m.logFocusView()
		}
		if p.OK && m.tier() == 2 {
			return m.dashboardView()
		}
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
		if m.tier() == 0 {
			// no room for the side panel: incident-checklist order — status
			// and NEXT first, then the menu
			if cs := m.compactStatus(); cs != "" {
				b.WriteString("\n" + cs)
			}
		}
		b.WriteString("\n" + m.withPanel(m.remoteBody()))
		suffix := "\n" + m.footer(m.dashNav()) + prompt()
		if m.showLogPane() {
			logH := m.height - m.overlayLines() - (strings.Count(b.String(), "\n") + 1) - strings.Count(suffix, "\n") - 1
			if logH >= 6 {
				b.WriteString("\n" + rule(m.tw()) + "\n" + m.bottomPane(m.tw(), logH))
			}
		}
		b.WriteString(suffix)
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
	body.WriteString(m.section("QUICK CHECKS", "read-only — can't hurt anything") + "\n")
	for _, a := range localActions.quick {
		body.WriteString(m.row(a) + "\n")
	}
	body.WriteString("\n" + m.section("CAPTURE EVIDENCE", "saves to /tmp · [d] browse") + "\n")
	for _, a := range localActions.capture {
		body.WriteString(m.row(a) + "\n")
	}
	body.WriteString("\n" + m.section("ADVANCED", "") + "\n")
	for _, a := range localActions.advanced {
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
	return lipgloss.JoinHorizontal(lipgloss.Top, left, m.panelView(panelW, h, false))
}

// --- key handling ---------------------------------------------------------------

func (m model) menuKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if key != "esc" {
		m.escHint = false // any other key clears the "you're at the top" hint
	}
	if m.capsFocus {
		return m.capsFocusKey(key)
	}
	if m.logs.focus {
		return m.logFocusKey(key)
	}
	// tab/shift-tab switch the bottom work area (WORK / LOGS / EVENTS)
	if m.showLogPane() && (key == "tab" || key == "shift+tab") {
		dir := 1
		if key == "shift+tab" {
			dir = -1
		}
		(&m).cycleWorkTab(dir)
		return m, nil
	}
	// output-pane keys (stop/copy/scroll) apply while the WORK tab is active
	if m.workTab == tabWork && m.out.id != 0 {
		if mm, cmd, handled := m.menuOutKey(key); handled {
			return mm, cmd
		}
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
			case "b", "B":
				m.prev = m.scr
				m.scr = scBlocked
				return m, nil
			case "n", "N":
				m.prev = m.scr
				m.scr = scRunbook
				return m, nil
			case "c", "C":
				return m.quickCLI(false, "doctor")
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
			title, words := m.stageJattachWords()
			return m.openQuick(title, nil, words...)
		case "?":
			m.scr = scHelp
			return m, nil
		case "b", "B":
			m.prev = m.scr
			m.scr = scBlocked
			return m, nil
		case "n", "N":
			m.prev = m.scr
			m.scr = scRunbook
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
	case "esc": // the menu is the root: flash how to leave instead of doing nothing
		m.escHint = true
		return m, nil
	case "w": // lowercase only — W is workload (shifted, like S/H/T)
		return m.openWizard()
	case "s":
		return m.quickCLI(false, "status")
	case "y", "Y", "W": // y kept as an alias — its old "why" view now lives inside workload
		return m.quickCLI(true, "workload")
	case "e":
		return m.quickCLI(true, "context")
	case "E": // one-key handoff brief for asking a senior for help
		return m.quickCLI(true, "escalate")
	case "S": // shifted deliberately: s = status is the most-pressed key
		return m.quickCLI(true, "security")
	case "h":
		return m.quickCLI(true, "health")
	case "o", "O":
		return m.quickCLI(false, "top")
	case "m":
		return m.quickCLI(true, "memory")
	case "t":
		m.scr = scVia
		return m, nil
	case "T": // shifted on purpose, like H/M: it takes over the screen
		if m.t.Pod == "" {
			return m, nil
		}
		return m, m.podTerminal()
	case "R": // restart: shifted, disruptive — confirm with a distinct key so a
		// key-repeat of R can't fire it; pressing R again cancels
		return m.askConfirm("restart the deployment? this rolling-restarts EVERY pod (in-flight requests + in-memory state on each are lost) — press y to confirm; esc or any other key (including R) cancels", "y",
			func(mm *model) tea.Cmd { return mm.quickTo(true, "restart", "--confirm") })
	case "K": // kill pod: shifted, disruptive — confirm with a distinct key so a
		// key-repeat of K can't fire it; pressing K again cancels
		return m.askConfirm("delete this pod? a managed pod respawns under a new name; an unmanaged one is gone — press y to confirm; esc or any other key (including K) cancels", "y",
			func(mm *model) tea.Cmd { return mm.quickTo(true, "kill", "--confirm") })
	case "f", "F":
		if m.showLogPane() {
			m.logs.focus = true
			m.logs.off = 0
		}
		return m, nil
	case "j", "J":
		m.scr = scJcmd
		return m, nil
	case "H":
		return m.askConfirm("heap dump pauses the JVM while it runs and can contain real user data — press H again to confirm, any other key cancels", "H",
			func(mm *model) tea.Cmd { mm.viaFlag = ""; mm.scr = scVia; mm.pendHeap = true; return nil })
	case "x", "X":
		return m.askConfirm2("include a heap dump in the bundle? (PAUSES the JVM) [y/N]", "",
			func(mm *model) tea.Cmd { return mm.quickTo(true, "snapshot", "--heap", "--confirm") },
			func(mm *model) tea.Cmd { return mm.quickTo(true, "snapshot") })
	case "l", "L":
		return m.quickCLI(false, "logs")
	case "v", "V":
		m.input = inputBox{title: "logger (e.g. com.example.debugdemo, ROOT):", then: inputLogger}
		m.scr = scInput
		return m, nil
	case "?":
		m.scr = scHelp
		return m, nil
	case ".":
		return m.openDetail("") // transparency cards for every command
	case "b", "B":
		m.prev = m.scr
		m.scr = scBlocked
		return m, nil
	case "n", "N": // runbook cards for the live warnings
		m.prev = m.scr
		m.scr = scRunbook
		return m, nil
	case "u", "U": // remote artifacts jdebug staged in the pod + cleanup
		m.prev = m.scr
		m.scr = scCleanup
		return m, fetchArtifacts(m.kit)
	case "r": // refresh the dashboard now (works even while quiet/paused)
		return m, m.refreshNow()
	case "z", "Z": // cycle background refresh: live → quiet → paused → live
		m.bgMode = (m.bgMode + 1) % 3
		return m, nil
	case "c", "C":
		return m.quickCLI(false, "doctor")
	case "a", "A":
		return m.analyzeContext() // the open file, else the whole tree
	case "d", "D":
		return m.openCapsFocus() // full-screen keyboard captures browser
	case "i", "I":
		return m.quickCLI(true, "install-jattach")
	case "p", "P":
		return m.quickCLI(true, "push-local")
	case "g", "G", "enter":
		return m.openEditor()
	case "M":
		m.scr = scChooser
		return m, nil
	case "q", "Q", "ctrl+c":
		qmsg := "quit jdebug? [y/N]"
		if n := m.ownedArtifacts(); n > 0 {
			qmsg = fmt.Sprintf("quit? jdebug left %d file(s) staged in the pod — press u to review/clean them first, or y to quit and leave them [y/N]", n)
		}
		return m.askConfirm(qmsg, "", func(mm *model) tea.Cmd {
			mm.quitMsg = cFaint.Render("transcript of everything from this session: " + sessionLog)
			return tea.Quit
		})
	}
	return m, nil
}

func (m model) localKey(key string) (tea.Model, tea.Cmd) {
	m.prev = scMenu
	switch key {
	case "esc":
		m.escHint = true
		return m, nil
	case "w", "W":
		return m.openWizard()
	case "h":
		return m.quickLocal("health")
	case "e", "E":
		return m.quickLocal("metrics")
	case "m":
		return m.quickLocal("memory")
	case "t", "T":
		return m.quickLocal("threads")
	case "j", "J":
		m.scr = scJcmd
		return m, nil
	case "H":
		return m.askConfirm("heap dump pauses the JVM while it runs and can contain real user data — press H again to confirm, any other key cancels", "H",
			func(mm *model) tea.Cmd { return mm.quickToLocal("heap", "--confirm") })
	case "x", "X":
		return m.askConfirm2("include a heap dump in the bundle? (PAUSES the JVM) [y/N]", "",
			func(mm *model) tea.Cmd { return mm.quickToLocal("snapshot", "--heap") },
			func(mm *model) tea.Cmd { return mm.quickToLocal("snapshot") })
	case "?":
		m.scr = scHelp
		return m, nil
	case ".":
		return m.openDetail("")
	case "b", "B":
		m.prev = m.scr
		m.scr = scBlocked
		return m, nil
	case "n", "N":
		m.prev = m.scr
		m.scr = scRunbook
		return m, nil
	case "a", "A":
		return m.openQuick("analyze /tmp", nil, m.kit+"/observe/analyze.sh", "/tmp")
	case "d", "D":
		return m.quickLocal("dumps")
	case "i", "I":
		title, words := m.stageJattachWords()
		return m.openQuick(title, nil, words...)
	case "s", "S":
		return m.openLocalSettings()
	case "g", "G": // change where we're debugging: this host, or a host to SSH to
		return m.openSSHHost()
	case "p", "P": // pick which JVM to debug when several run on this host
		return m.openJVMPicker()
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
		"  " + cTitle.Render("CAPTURE ROUTE") + cFaint.Render("  auto tries the safest route first, announcing each fallback") + "\n" +
		"  " + cMuted.Render("[Enter] auto (recommended) · [o] actuator · [j] jattach · [d] jdk · esc cancels") + " "
}

func (m model) viaKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "o", "O":
		m.viaFlag = "actuator"
	case "j", "J":
		m.viaFlag = "jattach"
	case "d", "D":
		m.viaFlag = "jdk"
	case "enter":
		m.viaFlag = "" // auto
	default:
		// esc (or any stray key) cancels — it must never fire a capture
		m.viaFlag = ""
		m.pendHeap = false
		m.scr = scMenu
		return m, nil
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
	return m.quickCLI(true, args...)
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
	b.WriteString("  " + cTitle.Render("JCMD") + cFaint.Render("  the JVM's own diagnostic commands, via jattach:") + "\n")
	for _, j := range jcmdPicks {
		b.WriteString("   " + cKey.Render(j.key) + "  " + cBody.Render(fmt.Sprintf("%-44s", j.cmd)) + cMuted.Render(j.why) + "\n")
	}
	b.WriteString("  " + cMuted.Render("pick 1-5 · t to type any jcmd · esc cancels") + " ")
	return b.String()
}

func (m model) jcmdKey(key string) (tea.Model, tea.Cmd) {
	m.scr = scMenu
	for _, j := range jcmdPicks {
		if key == j.key {
			m.prev = scMenu
			if m.mode == 1 {
				return m.quickCLI(true, "jcmd", j.cmd)
			}
			return m.quickLocal("jcmd", j.cmd)
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
		cTitle.Render("LOG LEVEL "+m.logger) +
		cMuted.Render(":  1 TRACE · 2 DEBUG · 3 INFO · 4 WARN · 5 ERROR · 6 OFF · esc cancels") + " "
}

func (m model) levelKey(key string) (tea.Model, tea.Cmd) {
	m.scr = scMenu
	if key >= "1" && key <= "6" {
		lv := levels[key[0]-'1']
		m.prev = scMenu
		return m.quickCLI(false, "log-level", m.logger, lv)
	}
	return m, nil
}
