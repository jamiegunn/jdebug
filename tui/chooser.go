package main

// chooser.go — the opening question: where is the JVM? Plus the self-test.

import (
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) chooserView() string {
	k := func(s string) string { return "   " + cKey.Render(s) + "  " }
	return "\n " + cTitle.Render("jdebug — where is the JVM you want to debug?") + "\n\n" +
		k("1") + cBody.Render("Kubernetes (kubectl)  ") + cMuted.Render("operator machine → kubectl exec into a pod (needs kubectl + a context)") + "\n" +
		k("2") + cBody.Render("Bare metal            ") + cMuted.Render("a JVM on this host, or on a VM/box you reach over SSH — no Kubernetes") + "\n" +
		k("u") + cBody.Render("self-test             ") + cMuted.Render("run the kit's own test suite (~10s, touches nothing of yours)") + "\n\n" +
		"  " + cDim.Render("Not sure? If you normally type kubectl to reach the app, pick 1.") + "\n" +
		prompt()
}

func (m model) chooserKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "1", "enter": // Enter takes the recommended default
		m.mode = 1
	case "2":
		// bare metal: ask where the JVM is (this machine, or a host to SSH to)
		// before landing on the menu, so the first probe hits the right place.
		m.mode = 2
		return m.openSSHHost()
	case "u", "U":
		m.prev = scChooser
		return m, runShell(nil, "bash", filepath.Join(m.kit, "tests", "run-tests.sh"))
	case "q", "Q", "ctrl+c":
		return m, tea.Quit
	default:
		return m, nil // stray keys (esc included) never pick a mode
	}
	m.scr = scMenu
	// probe the chosen mode now so the menu's first frame is truthful
	if m.mode == 1 {
		m.remote = remoteProbe(m.t)
		cmds := []tea.Cmd{fetchPanel(m.t, true), fetchEvents(m.t), fetchCaps(m.kit, m.capsDir()),
			fetchPodList(m.t), autoStatusCmd()}
		if m.t.Pod != "" {
			cmds = append(cmds, fetchLogs(m.t))
		}
		return m, tea.Batch(cmds...)
	}
	m.local = localProbe(m.kit, m.t)
	return m, nil
}
