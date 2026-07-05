package main

// render_demo.go — `-render <screen>` prints a screen with canned state and
// exits. No kubectl, no tty: this is how the kit's test suite asserts parity
// between the Go and bash frontends.

import "time"

var ctxOverride string

func demoModel() model {
	m := model{
		kit:   ".",
		mode:  1,
		width: 100,
		t: target{Namespace: "debug-demo", Selector: "", Container: "app",
			Actuator: "http://localhost:8080/actuator", Pod: "app-debug-demo-app-6c6c4b5769-s9jdg"},
	}
	ctxOverride = "ddk3s"
	m.remote = probe{OK: true, Cluster: true, When: time.Now().Add(time.Hour)}
	m.local = probe{OK: true, Jattach: true, When: time.Now().Add(time.Hour),
		Lines: []string{cSafe.Render("   ✓") + cMuted.Render(" actuator answering"), cSafe.Render("   ✓") + cMuted.Render(" jattach staged")}}
	return m
}

func renderDemo(what string) string {
	m := demoModel()
	switch what {
	case "menu":
		m.scr = scMenu
		return m.menuView()
	case "gate":
		m.t.Pod = ""
		m.remote = probe{OK: false, Cluster: true, When: time.Now().Add(time.Hour), Lines: []string{
			cSafe.Render("   ✓") + cMuted.Render(" cluster reachable"),
			cDisr.Render("   ✗") + cMuted.Render(" pod — none selected yet (press ") + cKey.Render("g") + cMuted.Render(", then ") + cKey.Render("p") + cMuted.Render(", and pick the exact pod)"),
			cFaint.Render("   · container — checked once a pod is selected"),
		}}
		return m.menuView()
	case "local":
		m.mode = 2
		return m.menuView()
	case "help":
		return m.helpView()
	case "chooser":
		return m.chooserView()
	case "editor":
		return m.editorView()
	case "wizard":
		m.scr = scWizard
		return m.wizardView()
	}
	return "unknown screen: " + what + " (menu|gate|local|help|chooser|editor|wizard)"
}
