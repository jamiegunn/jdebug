package main

// chooser.go — the opening question: where is the JVM? Plus the self-test.

import (
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) chooserView() string {
	k := func(s string) string { return "   " + cKey.Render(s) + "  " }
	return "\n " + cTitle.Render("jdebug — where is the JVM you want to debug?") + "\n\n" +
		k("1") + cBody.Render("Remote      ") + cMuted.Render("operator machine → kubectl exec into a pod (needs kubectl + a context)") + "\n" +
		k("2") + cBody.Render("In-pod      ") + cMuted.Render("a shell INSIDE the pod, no kubectl (JRE-only image is fine)") + "\n" +
		k("3") + cBody.Render("Bare metal  ") + cMuted.Render("a JVM on THIS host, no Kubernetes at all") + "\n" +
		k("u") + cBody.Render("self-test   ") + cMuted.Render("run the kit's own test suite (~10s, touches nothing of yours)") + "\n\n" +
		"  " + cDim.Render("Not sure? If you normally type kubectl to reach the app, pick 1.") + "\n" +
		prompt()
}

func (m model) chooserKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "1":
		m.mode = 1
	case "2":
		m.mode = 2
	case "3":
		m.mode = 3
	case "u", "U":
		m.prev = scChooser
		return m, runShell(nil, "bash", filepath.Join(m.kit, "tests", "run-tests.sh"))
	case "q", "Q", "ctrl+c":
		return m, tea.Quit
	default:
		m.mode = 1
	}
	m.scr = scMenu
	return m, nil
}
