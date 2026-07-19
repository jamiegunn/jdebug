package main

// runbook.go — runbook cards ('n'). For each warning the live panel is showing,
// a small card answers the questions a junior actually has mid-incident: what
// this means, why it usually happens, what to check first, the SAFE command, the
// RISKY one (if any), and — the part that's hard to know — what to tell the next
// person. Driven by the current panel state, so it shows the cards that matter
// right now; with nothing wrong it falls back to the full reference.

import "strings"

type runbookCard struct {
	signal   string // the live signal this card is about
	means    string // what it means
	why      string // why it usually happens
	check    string // what to check first
	safeCmd  string // a safe (read-only) command
	riskyCmd string // the risky option, if any ("" = none)
	tellNext string // what to hand the next person
}

// runbookCatalog is the reference set, ordered by operational severity.
var runbookCatalog = []runbookCard{
	{"CrashLoopBackOff",
		"the container keeps starting then exiting, so Kubernetes backs off restarting it",
		"a crash on startup — bad config, a missing dependency, or an immediate OOM",
		"the previous container's last log lines, then the pod-layer why",
		"jdebug logs --previous · jdebug why",
		"roll back the last deploy if the loop started right after a rollout",
		"CrashLoopBackOff; last exit reason+code; the previous-log stack trace or exit 137"},
	{"OOMKilled",
		"the kernel killed the container for exceeding its memory limit",
		"heap too small for the load, a leak, or off-heap/native growth outside the heap",
		"where the memory is — Java heap vs container RSS",
		"jdebug memory · jdebug jcmd \"GC.heap_info\"",
		"raise the limit, or capture a heap dump to find the leak (⚠ the dump PAUSES the app)",
		"OOMKilled at N% of limit; heap used/max; the memory report (heap vs RSS split)"},
	{"autoscale blind / at max",
		"the HPA can't scale (it can't read metrics) or is already at its ceiling",
		"metrics-server missing/misconfigured, or genuine saturation at max replicas",
		"the workload tree and current vs max replicas",
		"jdebug workload · jdebug top",
		"raise maxReplicas or fix metrics-server (a cluster-level change)",
		"HPA current/max; ScalingActive=False reason, or 'at max and still saturated'"},
	{"memory pressure",
		"container memory is close to its limit — an OOM-kill is near if it climbs",
		"heap or off-heap growth, or the limit is simply set too tight for the workload",
		"the memory anatomy (is it heap or off-heap?) before deciding heap vs limit",
		"jdebug memory",
		"raise the memory limit, or heap-dump to hunt a leak (⚠ the dump pauses the app)",
		"mem N% of limit; whether it's heap or off-heap; trend over the last few minutes"},
	{"secured / absent actuator",
		"the app's health URL didn't answer, so actuator-based views are unavailable",
		"the actuator is secured (needs auth), on a different path, or not exposed",
		"whether it's secured (401/403) vs absent (404) — jdebug health says which",
		"jdebug health · set auth with k in the target editor",
		"— (capture via jattach needs no HTTP at all)",
		"actuator not answering (secured/absent); which route you fell back to (jattach)"},
}

// activeRunbooks returns the cards for the signals the panel is showing right
// now, most-severe first; empty when nothing is wrong.
func (m model) activeRunbooks() []runbookCard {
	d := m.panel
	want := map[string]bool{}
	if d.Waiting == "CrashLoopBackOff" {
		want["CrashLoopBackOff"] = true
	}
	if d.LastReason == "OOMKilled" {
		want["OOMKilled"] = true
	}
	if d.HPAFailing || (d.HPAMax > 0 && d.HPACur >= d.HPAMax) {
		want["autoscale blind / at max"] = true
	}
	if d.MemPct >= 75 {
		want["memory pressure"] = true
	}
	if !d.When.IsZero() && !d.ActuatorOK {
		want["secured / absent actuator"] = true
	}
	var out []runbookCard
	for _, c := range runbookCatalog {
		if want[c.signal] {
			out = append(out, c)
		}
	}
	return out
}

func (m model) runbookView() string {
	cards := m.activeRunbooks()
	var b strings.Builder
	if len(cards) == 0 {
		cards = runbookCatalog
		b.WriteString("\n  " + cTitle.Render("runbook — the common incident signals and what to do") + "\n")
		b.WriteString("  " + cFaint.Render("nothing is firing on the panel right now; this is the full reference") + "\n")
	} else {
		b.WriteString("\n  " + cTitle.Render("runbook — what your live warnings mean and what to do") + "\n")
	}
	field := func(label, val string) {
		if val != "" {
			b.WriteString("      " + cFaint.Render(label) + "  " + cMuted.Render(val) + "\n")
		}
	}
	for _, c := range cards {
		b.WriteString("\n    " + cWarn.Render("● "+c.signal) + "\n")
		field("means    ", c.means)
		field("why      ", c.why)
		field("check    ", c.check)
		// vocabulary matches the menu's risk scale (safe / caution / disruptive)
		// so a junior cross-referencing a warning card against a menu row sees
		// the same word for the same danger.
		b.WriteString("      " + cFaint.Render("safe        ") + cOK.Render(c.safeCmd) + "\n")
		if c.riskyCmd != "" {
			b.WriteString("      " + cDisr.Render("disruptive  ") + cDisr.Render(c.riskyCmd) + "\n")
		}
		field("tell next", c.tellNext)
	}
	b.WriteString("\n  " + cFaint.Render("E builds a full escalation handoff · any key returns") + " ")
	return b.String()
}
