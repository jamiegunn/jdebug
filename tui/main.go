package main

// jdebug-tui — the Bubble Tea frontend for the jdebug kit. It draws and
// handles keys; every action shells out to the tested bash CLI (`jdebug`) or
// the in-pod tool (`jdebug-local`). A full-screen dashboard: quick reads
// render in an in-app scrollable pane; long-lived/interactive commands drop
// to the normal screen (ExecProcess) so their output stays in scrollback and
// the session log, exactly like the classic bash menu.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const version = "1.0.0"

type screen int

const (
	scChooser screen = iota
	scMenu
	scHelp
	scEditor
	scPicker
	scInput
	scWizard
	scConfirm
	scVia
	scJcmd
	scLevel
	scPostRun
	scOutput
	scDetail
	scBlocked
	scRunbook
	scAuth
	scCleanup
)

type model struct {
	kit    string
	mode   int // 0 = ask, 1 remote, 2 in-pod, 3 bare metal
	t      target
	scr    screen
	prev   screen // where PostRun / pickers return to
	width  int
	height int    // 0 = never measured: render content-height
	staleP string // remembered pod that vanished at startup

	// cached probes (20s, like bash)
	remote probe
	local  probe

	// confirm state
	confirmMsg  string
	confirmKey  string // "" = y/N style; else same-key confirm
	confirmThen func(m *model) tea.Cmd
	confirmElse func(m *model) tea.Cmd // optional: runs when declined
	confirmBase screen                 // the screen a confirm renders OVER (its source)

	// picker / input state (editor dropdowns, jcmd free text, logger name)
	pick   picker
	input  inputBox
	editor editorState

	// wizard
	wiz     wizardState
	incMode string // active incident mode (set by the wizard flow), weights NEXT

	// pending capture flavor
	viaFlag  string
	pendHeap bool // snapshot: include heap?
	logger   string

	// live panes (dashboard v3)
	panel        panelData
	logs         logState
	events       []eventLine
	eventsErr    string
	hist         []sample // sparkline history, one point per panel fetch
	caps         []capEntry
	capsCwd      string // CAPTURES browser: explicit browse dir ("" = pod default)
	capsOff      int
	capsWhen     time.Time  // when the captures list was last refreshed
	capsFocus    bool       // the full-screen keyboard captures browser (d) is open
	capsSel      int        // selected row in the focus browser
	capsFilter   string     // focus-browser filter: all|heaps|threads|logs|snapshots
	capsFlat     []capEntry // flat recursive capture list for the focus browser
	pods         []string   // PODS pane: what the selector/namespace matches
	podsScope    string
	podsErr      string
	podsOff      int
	detailAnchor string // transparency cards: key shown first ("" = all)
	detailOff    int
	out          outState   // in-app command output (scOutput)
	artifacts    []artifact // files jdebug staged inside the pod (remote-artifacts.tsv)

	// in-flight fetch guards: a slow cluster must not stack goroutines
	panelBusy bool
	logBusy   bool

	workTab   int    // bottom work area: tabWork | tabLogs | tabEvents
	bgMode    int    // background refresh: bgLive | bgQuiet | bgPaused
	logTickOn bool   // the 5s log ticker is armed (must never double-arm)
	autoRan   bool   // the one-shot startup auto-status already fired
	postExec  string // command to auto-run after an ExecProcess returns

	quitMsg string
}

// background refresh modes: what the TUI does on its own while you just look.
const (
	bgLive   = iota // logs 5s, kubectl 20s, JVM/actuator probe 20s
	bgQuiet         // cheap kubectl 20s only — no log polling, no JVM/actuator poke
	bgPaused        // nothing automatic; r refreshes once
)

// panelFetch/logsFetch dispatch a fetch and raise its busy flag (cleared
// when the reply message lands). probeJVM gates the app/JVM-touching read.
func (m *model) panelFetch(probeJVM bool) tea.Cmd {
	m.panelBusy = true
	return fetchPanel(m.t, probeJVM)
}
func (m *model) logsFetch() tea.Cmd {
	m.logBusy = true
	return fetchLogs(m.t)
}

// refreshNow does one full refresh regardless of the background mode — the
// manual 'r' escape hatch when paused/quiet, or just "update now".
func (m *model) refreshNow() tea.Cmd {
	cmds := []tea.Cmd{fetchEvents(m.t), fetchCaps(m.kit, m.capsDir()), fetchPodList(m.t)}
	if !m.panelBusy {
		cmds = append(cmds, m.panelFetch(true))
	}
	if m.t.Pod != "" && !m.logBusy {
		cmds = append(cmds, m.logsFetch())
	}
	return tea.Batch(cmds...)
}

// bgStatus is the one-line "what's running in the background" label shown under
// the panel, so the screen's idle activity is never a mystery.
func (m model) bgStatus() string {
	switch m.bgMode {
	case bgQuiet:
		return "QUIET · 20s kubectl · JVM+logs off · z pauses"
	case bgPaused:
		return "PAUSED · r refreshes · z resumes"
	default:
		return "auto 20s · logs 5s · z quiets"
	}
}

func (m model) Init() tea.Cmd {
	// (the 5s log ticker arms on the first WindowSizeMsg — the one event
	// every entry path shares exactly once)
	if m.mode == 1 {
		cmds := []tea.Cmd{fetchPanel(m.t, true), fetchEvents(m.t), fetchCaps(m.kit, m.capsDir()),
			fetchPodList(m.t), fetchArtifacts(m.kit), tickCmd(), autoStatusCmd()}
		if m.t.Pod != "" {
			cmds = append(cmds, fetchLogs(m.t))
		}
		return tea.Batch(cmds...)
	}
	return tea.Batch(fetchCaps(m.kit, m.capsDir()), tickCmd())
}

// autoStatusCmd: the dashboard's opening move — after two quiet seconds it
// runs `status` for you, so the first screen already answers "what's
// happening" without a keypress.
type autoStatusMsg struct{}

func autoStatusCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return autoStatusMsg{} })
}

func (m *model) probeRemote(force bool) probe {
	if force || time.Since(m.remote.When) > 20*time.Second {
		m.remote = remoteProbe(m.t)
	}
	return m.remote
}
func (m *model) probeLocal(force bool) probe {
	if force || time.Since(m.local.When) > 20*time.Second {
		m.local = localProbe(m.kit, m.t)
	}
	return m.local
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		if m.scr == scOutput {
			m.rewrapOut()
		}
		if !m.logTickOn {
			m.logTickOn = true
			return m, logTickCmd()
		}
		return m, nil
	case execDoneMsg:
		if m.scr != scChooser {
			m.scr = scMenu
		}
		cmds := []tea.Cmd{m.panelFetch(m.bgMode == bgLive), fetchCaps(m.kit, m.capsDir()), fetchArtifacts(m.kit)}
		if m.mode == 1 {
			cmds = append(cmds, fetchEvents(m.t), fetchPodList(m.t))
			if m.showLogPane() && !m.logBusy {
				cmds = append(cmds, m.logsFetch())
			}
		}
		if m.postExec == "status" { // e.g. back from the pod terminal
			m.postExec = ""
			mm, cmd := m.quickCLI(false, "status")
			return mm, tea.Batch(append(cmds, cmd)...)
		}
		return m, tea.Batch(cmds...)
	case autoStatusMsg:
		// only fire on an untouched, ready dashboard — never interrupt
		if m.scr == scMenu && m.mode == 1 && m.remote.OK && !m.autoRan && m.out.id == 0 && !m.wiz.active {
			m.autoRan = true
			return m.quickCLI(false, "status")
		}
		return m, nil
	case streamChunkMsg:
		if v.id != m.out.id {
			return m, nil // superseded stream
		}
		m.appendChunk(v.data)
		return m, waitStream(m.out.ch)
	case streamDoneMsg:
		if v.id != m.out.id {
			return m, nil
		}
		m.out.running = false
		m.out.done = true
		m.out.ok = v.err == nil
		m.out.cancel = nil
		if errors.Is(v.err, context.Canceled) {
			m.out.errStr = "stopped"
		} else if v.err != nil {
			m.out.errStr = firstLine(v.err.Error())
		}
		appendSessionLog(m.out.display, m.out.raw, v.err)
		cmds := []tea.Cmd{m.panelFetch(m.bgMode == bgLive), fetchCaps(m.kit, m.capsDir())}
		if m.mode == 1 {
			cmds = append(cmds, fetchEvents(m.t))
		}
		if m.wiz.active { // a wizard step finished — chain the next one
			mdl, cmd := m.wizardAdvance()
			return mdl, tea.Batch(append(cmds, cmd)...)
		}
		return m, tea.Batch(cmds...)
	case panelMsg:
		nd := panelData(v)
		if nd.HeapSkipped { // quiet refresh: keep the last-known heap/actuator status
			nd.HeapUsed, nd.HeapMax, nd.HeapVia, nd.ActuatorOK =
				m.panel.HeapUsed, m.panel.HeapMax, m.panel.HeapVia, m.panel.ActuatorOK
		}
		m.panel = nd
		m.panelBusy = false
		if m.mode == 1 && v.Phase != "" {
			m.hist = pushSample(m.hist, sample{When: v.When, MemPct: v.MemPct,
				CPUMilli: cpuMilli(v.CPUUse), Restarts: v.Restarts})
		}
		return m, nil
	case logMsg:
		m.logs.lines = v.lines
		m.logs.prev = v.prev
		m.logs.err = v.err
		m.logs.when = time.Now()
		m.logBusy = false
		return m, nil
	case eventsMsg:
		m.events = v.lines
		m.eventsErr = v.err
		return m, nil
	case capsMsg:
		// ignore a stale fetch that resolved after the user navigated away
		if v.dir == m.capsDir() {
			m.caps = v.entries
			m.capsWhen = time.Now()
			if m.capsOff > len(m.caps) {
				m.capsOff = 0
			}
		}
		return m, nil
	case artifactsMsg:
		m.artifacts = v.list
		return m, nil
	case capsFlatMsg:
		m.capsFlat = v.entries
		m.capsWhen = time.Now()
		if m.capsSel >= len(m.capsFocusList()) {
			m.capsSel = 0
		}
		return m, nil
	case podsMsg:
		m.pods = v.lines
		m.podsScope = v.scope
		m.podsErr = v.err
		return m, nil
	case tea.MouseMsg:
		return m.handleMouse(v)
	case tickMsg:
		// paused → reschedule only; quiet → cheap kubectl reads but no JVM probe
		if m.bgMode != bgPaused && m.scr == scMenu && m.mode == 1 && m.remote.OK {
			cmds := []tea.Cmd{fetchEvents(m.t), fetchCaps(m.kit, m.capsDir()), fetchPodList(m.t), tickCmd()}
			if !m.panelBusy {
				cmds = append(cmds, m.panelFetch(m.bgMode == bgLive))
			}
			return m, tea.Batch(cmds...)
		}
		return m, tickCmd()
	case logTickMsg:
		// only live mode polls logs (quiet/paused stop it); and skip while the
		// strip is showing command output — no point tailing logs nobody sees
		if m.bgMode == bgLive && m.scr == scMenu && m.mode == 1 && m.remote.OK && m.showLogPane() && !m.logBusy && !m.out.show {
			return m, tea.Batch(m.logsFetch(), logTickCmd())
		}
		return m, logTickCmd()
	case tea.KeyMsg:
		return m.handleKey(v)
	}
	return m, nil
}

// handleMouse: click a pod to retarget, click a capture to open it, wheel
// scrolls whichever pane is under the pointer (output pane included).
func (m model) handleMouse(v tea.MouseMsg) (tea.Model, tea.Cmd) {
	outVisible := m.out.show || m.scr == scOutput
	switch {
	case v.Action == tea.MouseActionPress && v.Button == tea.MouseButtonRight:
		// right-click a row → its transparency card (what runs, risk, deps)
		if key, ok := m.menuRowClick(v.X, v.Y); ok {
			return m.openDetail(key)
		}
	case v.Action == tea.MouseActionPress && v.Button == tea.MouseButtonLeft:
		if pod := m.podsClickTarget(v.X, v.Y); pod != "" {
			return m.switchPod(pod)
		}
		if in, row := m.capsHit(v.X, v.Y); in {
			return m.capsClick(row)
		}
		if m.panelHit(v.X, v.Y) {
			return m.quickCLI(true, "why") // drill into what the panel summarizes
		}
		if key, ok := m.menuRowClick(v.X, v.Y); ok {
			return m.menuKey(key) // same path as pressing the key (confirms fire)
		}
	case v.Button == tea.MouseButtonWheelUp:
		if in, _ := m.podsHit(v.X, v.Y); in {
			if m.podsOff > 0 {
				m.podsOff--
			}
		} else if in, _ := m.capsHit(v.X, v.Y); in {
			if m.capsOff > 0 {
				m.capsOff--
			}
		} else if outVisible {
			return m.outScroll("pgup", 3), nil // 3 lines per wheel notch
		}
	case v.Button == tea.MouseButtonWheelDown:
		if in, _ := m.podsHit(v.X, v.Y); in {
			if m.podsOff < len(m.pods)-1 {
				m.podsOff++
			}
		} else if in, _ := m.capsHit(v.X, v.Y); in {
			if m.capsOff < len(m.caps)-1 {
				m.capsOff++
			}
		} else if outVisible {
			return m.outScroll("pgdown", 3), nil
		}
	}
	return m, nil
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	switch m.scr {
	case scChooser:
		return m.chooserKey(key)
	case scMenu:
		return m.menuKey(key)
	case scHelp:
		m.scr = scMenu
		return m, nil
	case scConfirm:
		return m.confirmKeyPress(key)
	case scVia:
		return m.viaKey(key)
	case scJcmd:
		return m.jcmdKey(key)
	case scLevel:
		return m.levelKey(key)
	case scEditor:
		return m.editorKey(key)
	case scPicker:
		return m.pickerKey(key)
	case scInput:
		return m.inputKey(k)
	case scWizard:
		return m.wizardKey(key)
	case scOutput:
		return m.outputKey(key)
	case scDetail:
		return m.detailKey(key)
	case scBlocked:
		m.scr = scMenu
		return m, nil
	case scRunbook:
		if key == "E" { // jump straight to the escalation handoff
			return m.quickCLI(true, "escalate")
		}
		m.scr = scMenu
		return m, nil
	case scAuth:
		return m.authKey(key)
	case scCleanup:
		return m.cleanupKey(key)
	}
	return m, nil
}

func (m model) View() string {
	switch m.scr {
	case scChooser:
		return m.chooserView()
	case scMenu:
		return m.menuView()
	case scHelp:
		return m.helpView()
	case scConfirm:
		return m.baseView() + "\n  " + cWarn.Render(m.confirmMsg) + " "
	case scVia:
		return m.viaView()
	case scJcmd:
		return m.jcmdView()
	case scLevel:
		return m.levelView()
	case scEditor:
		return m.editorView()
	case scPicker:
		return m.pickerView()
	case scInput:
		return m.inputView()
	case scWizard:
		return m.wizardView()
	case scOutput:
		return m.outputView()
	case scDetail:
		return m.detailView()
	case scBlocked:
		return m.blockedView()
	case scRunbook:
		return m.runbookView()
	case scAuth:
		return m.authView()
	case scCleanup:
		return m.cleanupView()
	}
	return ""
}

// --- confirm helper -----------------------------------------------------------

func (m model) askConfirm(msg, sameKey string, then func(*model) tea.Cmd) (tea.Model, tea.Cmd) {
	return m.askConfirm2(msg, sameKey, then, nil)
}

func (m model) askConfirm2(msg, sameKey string, then, els func(*model) tea.Cmd) (tea.Model, tea.Cmd) {
	m.confirmMsg = msg
	m.confirmKey = sameKey
	m.confirmThen = then
	m.confirmElse = els
	m.prev = m.scr
	m.confirmBase = m.scr // a confirm renders over the screen that launched it
	m.scr = scConfirm
	return m, nil
}

// baseView renders the screen a confirmation sits on top of, so a confirm never
// visually yanks the operator back to the menu when it was launched elsewhere.
func (m model) baseView() string {
	switch m.confirmBase {
	case scEditor:
		return m.editorView()
	case scDetail:
		return m.detailView()
	case scWizard:
		return m.wizardView()
	case scBlocked:
		return m.blockedView()
	case scRunbook:
		return m.runbookView()
	default:
		return m.menuView()
	}
}

func (m model) confirmKeyPress(key string) (tea.Model, tea.Cmd) {
	ok := false
	if m.confirmKey != "" {
		ok = key == m.confirmKey
	} else {
		ok = key == "y" || key == "Y"
	}
	then, els := m.confirmThen, m.confirmElse
	m.confirmThen, m.confirmElse = nil, nil
	m.scr = m.prev
	if !ok {
		if els == nil {
			return m, nil
		}
		cmd := els(&m)
		return m, cmd
	}
	cmd := then(&m)
	return m, cmd
}

// --- entry ---------------------------------------------------------------------

func main() {
	renderFlag := flag.String("render", "", "print a screen with canned demo state and exit (menu|dashboard|focus|output|gate|local|help|chooser|editor|wizard)")
	heapFlag := flag.String("analyze-heap", "", "print a class histogram for an hprof heap dump and exit")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	applyTheme(os.Getenv("JDEBUG_THEME"))

	if *showVersion {
		fmt.Println("jdebug-tui " + version)
		return
	}
	if *heapFlag != "" {
		h, err := analyzeHprof(*heapFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(renderHistogram(h, 15))
		return
	}
	if *renderFlag != "" {
		fmt.Println(renderDemo(*renderFlag))
		return
	}

	kit := kitRoot()
	if _, err := os.Stat(filepath.Join(kit, "jdebug")); err != nil {
		fmt.Fprintln(os.Stderr, "error: can't find the jdebug kit (set JDEBUG_KIT to its directory)")
		os.Exit(1)
	}
	initSessionLog(kit)

	m := model{kit: kit, t: loadTarget(), width: 100, workTab: tabLogs}
	switch os.Getenv("JDEBUG_MODE") {
	case "1":
		m.mode = 1
	case "2":
		m.mode = 2
	case "3":
		m.mode = 3
	}
	if m.mode == 0 {
		m.scr = scChooser
	} else {
		m.scr = scMenu
	}
	// a remembered pod pin may have died since last session
	if m.mode == 1 && m.t.Pod != "" && clusterReachable() {
		if len(containersOf(m.t.Namespace, m.t.Pod)) == 0 {
			m.staleP = m.t.Pod
			m.t.Pod = ""
		}
	}
	// probe before the first frame so the opening screen is truthful
	switch m.mode {
	case 1:
		m.remote = remoteProbe(m.t)
	case 2, 3:
		m.local = localProbe(kit, m.t)
	}

	// inline (no altscreen): output + menus share the normal scrollback.
	// When stdin is a pipe (tests, headless drives), read keys from it rather
	// than demanding /dev/tty.
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		opts = append(opts, tea.WithInput(os.Stdin))
	}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if fm, ok := final.(model); ok {
		if fm.out.cancel != nil {
			fm.out.cancel() // don't orphan a streaming child
		}
		if fm.quitMsg != "" {
			fmt.Println(fm.quitMsg)
		}
	}
}
