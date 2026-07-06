package main

import (
	"strings"
	"testing"
)

// The interstitial-consistency contract: every temporary screen answers "where
// am I?" (a title) and "how do I leave?" (a visible dismiss hint). This is the
// safety property an operator under pressure relies on — no per-screen surprises.
func TestInterstitialsHaveTitleAndDismiss(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	m.logger = "ROOT"
	m.artifacts = []artifact{{owned: true, pod: "pod-a", path: "/tmp/jattach"}}

	cases := []struct {
		name, title, view string
	}{
		{"blocked", "blocked-by", ansiStrip(m.blockedView())},
		{"runbook", "runbook", ansiStrip(m.runbookView())},
		{"auth", "ACTUATOR AUTH", ansiStrip(m.authView())},
		{"cleanup", "REMOTE ARTIFACTS", ansiStrip(m.cleanupView())},
		{"help", "jdebug help", ansiStrip(m.helpView())},
		{"detail", "what each command does", ansiStrip(m.detailView())},
		{"via", "CAPTURE ROUTE", ansiStrip(m.viaView())},
		{"jcmd", "JCMD", ansiStrip(m.jcmdView())},
		{"level", "LOG LEVEL", ansiStrip(m.levelView())},
	}
	for _, c := range cases {
		if !strings.Contains(c.view, c.title) {
			t.Errorf("%s interstitial missing its title %q", c.name, c.title)
		}
		low := strings.ToLower(c.view)
		if !strings.Contains(low, "esc") && !strings.Contains(low, "back") &&
			!strings.Contains(low, "any key") && !strings.Contains(low, "returns") &&
			!strings.Contains(low, "cancel") {
			t.Errorf("%s interstitial has no visible dismiss hint (esc/back/cancel/any key)", c.name)
		}
	}
}
