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

	// picker / input state (editor dropdowns, jcmd free text, logger name)
	pick   picker
	input  inputBox
	editor editorState

	// wizard
	wiz wizardState

	// pending capture flavor
	viaFlag  string
	pendHeap bool // snapshot: include heap?
	logger   string

	// live panes (dashboard v3)
	panel     panelData
	logs      logState
	events    []eventLine
	eventsErr string
	hist      []sample // sparkline history, one point per panel fetch
	caps      []capEntry
	out       outState // in-app command output (scOutput)

	// in-flight fetch guards: a slow cluster must not stack goroutines
	panelBusy bool
	logBusy   bool

	quitMsg string
}

// panelFetch/logsFetch dispatch a fetch and raise its busy flag (cleared
// when the reply message lands).
func (m *model) panelFetch() tea.Cmd {
	m.panelBusy = true
	return fetchPanel(m.t)
}
func (m *model) logsFetch() tea.Cmd {
	m.logBusy = true
	return fetchLogs(m.t)
}

func (m model) Init() tea.Cmd {
	if m.mode == 1 {
		cmds := []tea.Cmd{fetchPanel(m.t), fetchEvents(m.t), fetchCaps(m.kit), tickCmd(), logTickCmd()}
		if m.t.Pod != "" {
			cmds = append(cmds, fetchLogs(m.t))
		}
		return tea.Batch(cmds...)
	}
	return tea.Batch(fetchCaps(m.kit), tickCmd())
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
		return m, nil
	case execDoneMsg:
		if m.scr == scWizard && m.wiz.active {
			return m.wizardAdvance()
		}
		if m.scr != scChooser {
			m.scr = scMenu
		}
		m.out.show = false // a wizard run supersedes any held output pane
		cmds := []tea.Cmd{m.panelFetch(), fetchCaps(m.kit)}
		if m.mode == 1 {
			cmds = append(cmds, fetchEvents(m.t))
			if m.showLogPane() && !m.logBusy {
				cmds = append(cmds, m.logsFetch())
			}
		}
		return m, tea.Batch(cmds...)
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
		cmds := []tea.Cmd{m.panelFetch(), fetchCaps(m.kit)}
		if m.mode == 1 {
			cmds = append(cmds, fetchEvents(m.t))
		}
		return m, tea.Batch(cmds...)
	case panelMsg:
		m.panel = panelData(v)
		m.panelBusy = false
		if m.mode == 1 && v.Phase != "" {
			m.hist = pushSample(m.hist, sample{When: v.When, MemPct: v.MemPct,
				CPUMilli: cpuMilli(v.CPUUse), Restarts: v.Restarts})
		}
		return m, nil
	case logMsg:
		m.logs.lines = v.lines
		m.logs.err = v.err
		m.logs.when = time.Now()
		m.logBusy = false
		return m, nil
	case eventsMsg:
		m.events = v.lines
		m.eventsErr = v.err
		return m, nil
	case capsMsg:
		m.caps = []capEntry(v)
		return m, nil
	case tickMsg:
		if m.scr == scMenu && m.mode == 1 && m.remote.OK {
			cmds := []tea.Cmd{fetchEvents(m.t), fetchCaps(m.kit), tickCmd()}
			if !m.panelBusy {
				cmds = append(cmds, m.panelFetch())
			}
			return m, tea.Batch(cmds...)
		}
		return m, tickCmd()
	case logTickMsg:
		// skip while the strip is showing command output — no point tailing
		// logs nobody can see
		if m.scr == scMenu && m.mode == 1 && m.remote.OK && m.showLogPane() && !m.logBusy && !m.out.show {
			return m, tea.Batch(m.logsFetch(), logTickCmd())
		}
		return m, logTickCmd()
	case tea.KeyMsg:
		return m.handleKey(v)
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
		return m.menuView() + "\n  " + cWarn.Render(m.confirmMsg) + " "
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
	m.scr = scConfirm
	return m, nil
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
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	applyTheme(os.Getenv("JDEBUG_THEME"))

	if *showVersion {
		fmt.Println("jdebug-tui " + version)
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

	m := model{kit: kit, t: loadTarget(), width: 100}
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
	opts := []tea.ProgramOption{tea.WithAltScreen()}
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
