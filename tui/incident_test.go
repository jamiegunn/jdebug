package main

import (
	"strings"
	"testing"
	"time"
)

func TestIncidentModeWeightsNext(t *testing.T) {
	m := readyModel()
	// a multi-symptom pod: crash-loop(down) + OOM(restart) + at-max(scale) + mem(mem)
	m.panel = panelData{When: time.Now(), Waiting: "CrashLoopBackOff",
		LastReason: "OOMKilled", Restarts: 40, MemPct: 95, HPAName: "a", HPACur: 6, HPAMax: 6}
	// no mode → pure severity: crash-loop leads
	base := ansiStrip(strings.Join(m.suggestions(), "\n"))
	if strings.Index(base, "CrashLoopBackOff") > strings.Index(base, "OOMKilled") {
		t.Fatalf("baseline must be severity-ordered (crash before OOM):\n%s", base)
	}
	// memory mode → restart+mem categories float above the crash-loop row
	m.incMode = "memory"
	mem := ansiStrip(strings.Join(m.suggestions(), "\n"))
	oom, crash := strings.Index(mem, "OOMKilled"), strings.Index(mem, "CrashLoopBackOff")
	if oom < 0 || crash < 0 || oom > crash {
		t.Fatalf("memory mode must lead with the memory/OOM row:\n%s", mem)
	}
}

func TestWizardFlowSetsIncidentMode(t *testing.T) {
	if got := press(t, readyModel(), "w", "1").(model).incMode; got != "memory" {
		t.Fatalf("flow 1 (OOM) must set the memory mode, got %q", got)
	}
	if got := press(t, readyModel(), "w", "8").(model).incMode; got != "deployed" {
		t.Fatalf("flow 8 (deploy) must set the deployed mode, got %q", got)
	}
	m := readyModel()
	m.incMode = "cpu"
	if got := press(t, m, "w", "6").(model).incMode; got != "" {
		t.Fatalf("flow 6 (not sure) must clear the mode, got %q", got)
	}
}

// The wizard is jdebug's incident-mode picker; the "a deploy just happened"
// mode must lead with what-changed and include the timeline.
func TestWizardDeployFlow(t *testing.T) {
	for _, f := range wizardFlows {
		if f.key != "8" {
			continue
		}
		if len(f.steps) == 0 || f.steps[0].args[0] != "what-changed" {
			t.Fatal("flow 8 must lead with what-changed")
		}
		hasTimeline := false
		for _, s := range f.steps {
			if len(s.args) > 0 && s.args[0] == "timeline" {
				hasTimeline = true
			}
		}
		if !hasTimeline {
			t.Fatal("flow 8 must include the timeline")
		}
		return
	}
	t.Fatal("the wizard must offer a deploy/what-changed incident flow (key 8)")
}
