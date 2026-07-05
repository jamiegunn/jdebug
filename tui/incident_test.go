package main

import "testing"

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
