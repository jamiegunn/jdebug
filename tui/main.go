package main

// jdebug-tui — the Bubble Tea frontend for the jdebug kit. It draws and
// handles keys; every action shells out to the tested bash CLI (`jdebug`) or
// the in-pod tool (`jdebug-local`). Runs inline (no altscreen) so command
// output stays in your scrollback and the session log, exactly like the
// classic bash menu.

import (
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
)

type model struct {
	kit    string
	mode   int // 0 = ask, 1 remote, 2 in-pod, 3 bare metal
	t      target
	scr    screen
	prev   screen // where PostRun / pickers return to
	width  int
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

	lastOK  bool
	quitMsg string
}

func (m model) Init() tea.Cmd { return nil }

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
		m.width = v.Width
		return m, nil
	case execDoneMsg:
		m.lastOK = v.err == nil
		if m.scr == scWizard && m.wiz.active {
			return m.wizardAdvance()
		}
		m.scr = scPostRun
		return m, nil
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
	case scPostRun:
		if m.prev == scChooser {
			m.scr = scChooser
		} else {
			m.scr = scMenu
		}
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
	case scPostRun:
		return m.postRunView()
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
	}
	return ""
}

func (m model) postRunView() string {
	mark := cOK.Render("✓ done")
	if !m.lastOK {
		mark = cDisr.Render("✗ that didn't work — the messages above say why and what to try next")
	}
	return "\n " + mark + "\n " +
		cFaint.Render("any key for the menu — output saved to "+sessionLog) + "\n"
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
	renderFlag := flag.String("render", "", "print a screen with canned demo state and exit (menu|gate|help|chooser|local)")
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

	// inline (no altscreen): output + menus share the normal scrollback.
	// When stdin is a pipe (tests, headless drives), read keys from it rather
	// than demanding /dev/tty.
	var opts []tea.ProgramOption
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		opts = append(opts, tea.WithInput(os.Stdin))
	}
	p := tea.NewProgram(m, opts...)
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if fm, ok := final.(model); ok && fm.quitMsg != "" {
		fmt.Println(fm.quitMsg)
	}
}
