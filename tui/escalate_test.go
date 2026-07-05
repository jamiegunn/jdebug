package main

import (
	"strings"
	"testing"
)

// e = context (app wiring), E = escalate (handoff summary). Distinct keys, both
// stream a read-only CLI verb into the output pane.
func TestContextAndEscalateAreDistinctKeys(t *testing.T) {
	ce, ccmd := readyModel().Update(key("e"))
	if ccmd == nil || !strings.Contains(ce.(model).out.title, "context") {
		t.Fatalf("e must run context, got title %q", ce.(model).out.title)
	}
	ee, ecmd := readyModel().Update(key("E"))
	if ecmd == nil || !ee.(model).out.running || !strings.Contains(ee.(model).out.title, "escalate") {
		t.Fatalf("E must run the escalation handoff, got title %q running=%v",
			ee.(model).out.title, ee.(model).out.running)
	}
}
