package main

// auth.go — the guided ACTUATOR AUTH interstitial (k in the target editor). The
// old one-line prompt ("bearer:ENV_VAR or basic:USER_VAR:PASS_VAR") was correct
// but told an operator nothing about WHAT to type, WHERE to find it, or WHY
// jdebug wants an env-var reference instead of the secret. This full screen
// answers all three, with examples and a jattach fallback.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) openAuth() (tea.Model, tea.Cmd) {
	m.prev = scEditor
	m.scr = scAuth
	return m, nil
}

func (m model) authView() string {
	ns := m.t.Namespace
	dep := m.panel.DeployName
	if dep == "" {
		dep = "<deployment>"
	}
	li := func(k, s string) string { return "    " + cKey.Render(k) + "  " + cMuted.Render(s) + "\n" }
	sub := func(s string) string { return "         " + cFaint.Render(s) + "\n" }
	return "\n  " + cTitle.Render("ACTUATOR AUTH") + "\n" +
		"  " + cFaint.Render("Intent: choose how jdebug authenticates to secured actuator endpoints.") + "\n\n" +
		"  " + cMuted.Render("jdebug stores a REFERENCE, not the secret. The token/password should already") + "\n" +
		"  " + cMuted.Render("exist in the pod as an env var; jdebug asks the pod to expand it at call time.") + "\n\n" +
		"  " + cFaint.Render("current: ") + cBody.Render(authDisplay(m.t.ActuatorAuth)) + "\n" +
		"  " + cFaint.Render("affects: health · metrics · heap-via-actuator · log-level (future actuator calls)") + "\n\n" +
		"  " + cDim.Render("CHOOSE") + "\n" +
		li("1", "none — actuator is open on localhost, or you'll use jattach instead") +
		li("2", "bearer token from a pod env var   →  bearer:MANAGEMENT_TOKEN") +
		sub("sends:  Authorization: Bearer $MANAGEMENT_TOKEN") +
		li("3", "basic auth from pod env vars      →  basic:ACTUATOR_USER:ACTUATOR_PASSWORD") +
		sub(`sends:  -u "$ACTUATOR_USER:$ACTUATOR_PASSWORD"`) +
		li("e", "type the reference yourself") +
		"\n  " + cDim.Render("HOW TO FIND THE ENV VAR NAMES") + cFaint.Render("  (never paste secret values)") + "\n" +
		"    " + cMuted.Render("safe:  ") + cFaint.Render("W workload → Environment / Secret references") + "\n" +
		"    " + cMuted.Render("shell: ") + cFaint.Render("T terminal → env | grep -Ei 'actuator|management|token|password'") + "\n" +
		"    " + cMuted.Render("k8s:   ") + cFaint.Render("kubectl -n "+ns+" get deploy "+dep+" -o yaml") + "\n" +
		"\n  " + cWarn.Render("No credentials? Capture JVM evidence without HTTP: ") +
		cMuted.Render("threads ") + cKey.Render("t") + cMuted.Render("→jattach · heap ") + cKey.Render("H") + cMuted.Render("→jattach · ") + cKey.Render("j") + cMuted.Render(" jcmd") + "\n" +
		"\n  " + cFaint.Render("1/2/3/e choose · esc back") + " "
}

func (m model) authKey(key string) (tea.Model, tea.Cmd) {
	openRef := func(prefill string) (tea.Model, tea.Cmd) {
		m.input = inputBox{title: "auth ref — env var NAME(s), not the secret:", val: prefill, then: inputActuatorAuth}
		m.prev = scEditor // saving returns to the editor
		m.scr = scInput
		return m, nil
	}
	switch key {
	case "1":
		m.t.ActuatorAuth = "" // none
		saveTarget(m.t)
		m.remote.When, m.local.When = zeroTime(), zeroTime()
		m.scr = scEditor
		return m, nil
	case "2":
		return openRef("bearer:")
	case "3":
		return openRef("basic:")
	case "e", "E", "enter":
		cur := m.t.ActuatorAuth
		return openRef(cur)
	default: // esc, q, b — back to the editor
		if strings.HasPrefix(key, "esc") || key == "q" || key == "Q" || key == "b" || key == "B" {
			m.scr = scEditor
		}
		return m, nil
	}
}
