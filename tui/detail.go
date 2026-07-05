package main

// detail.go — the transparency layer. Before running (or when a junior wants
// to understand a row), the detail card answers: what command runs, where its
// data comes from, why it's useful, its risk, what it needs, and the
// alternatives when a route is blocked. Opened by `.` (keyboard) or by
// right-clicking a row (mouse) — never hover-only.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type cmdInfo struct {
	key   string
	title string
	runs  string // the command it shells out to
	src   string // data source / API
	why   string // what it's for / what it proves
	risk  string // safe · read-only / PAUSES the JVM / state-changing / sensitive
	needs string // RBAC / metrics-server / actuator / jattach / python3
	alts  string // fallbacks when the route is blocked
}

// cmdCatalog is ordered like the menu; every runnable row has an entry.
var cmdCatalog = []cmdInfo{
	{"w", "guided diagnosis", "jdebug wizard", "you pick a symptom; it runs the right captures",
		"the safest starting point — describe what you see, not which tool you want", "safe",
		"depends on the steps it runs", "—"},
	{"s", "status", "jdebug status", "kubectl pod status + recent events",
		"is the pod running or restarting? first thing to check", "safe · read-only",
		"kubectl (get pods/events)", "jdebug why for the deeper pod story"},
	{"h", "health", "jdebug health", "the app's actuator /health endpoint",
		"is a dependency (db, queue, cache) down? a DOWN component names the culprit", "safe · read-only",
		"actuator reachable (falls back to a plain message if not)", "logs, if the app has no actuator"},
	{"o", "top", "jdebug top", "kubectl top pods + HPA",
		"which pod is eating CPU or memory, and is it near the limit", "safe · read-only",
		"metrics-server (says so plainly if absent)", "the pod spec's requests/limits still show without it"},
	{"m", "memory", "jdebug memory", "cgroup RSS reconciled with JVM heap/non-heap",
		"is the memory in the Java heap or off-heap/native? tells you where to look", "safe · read-only",
		"python3 on the host; actuator or jcmd for JVM numbers", "jdebug jcmd 'VM.native_memory summary'"},
	{"y", "why", "jdebug why", "pod spec + status + cgroup + HPA",
		"the kubernetes-layer deep-dive: limits, probes, exit codes, autoscaling — in plain language", "safe · read-only",
		"kubectl; python3", "the individual panel signals it summarizes"},
	{"W", "workload", "jdebug topology", "Deployment → ReplicaSets → pods, HPA, Services",
		"the workload tree: what owns the pod, rollout state, and Deployment-vs-HPA fights", "safe · read-only",
		"kubectl; python3", "jdebug why for a pod-focused view"},
	{"e", "context", "jdebug context", "pod spec + Services/Endpoints + env/config refs",
		"how the app is wired: what exposes it, its config, its dependencies (Valkey/Redis) — secret values redacted", "safe · read-only",
		"kubectl; python3", "jdebug topology for the rollout tree, jdebug why for the pod layer"},
	{"S", "security", "jdebug security", "pod securityContext + a live `id` in the container",
		"running as root? privileged? network policy? each finding names its one-line fix", "safe · read-only",
		"kubectl; exec to verify the live uid", "reads the spec even when exec is forbidden"},
	{"l", "logs", "jdebug logs", "kubectl logs (--previous on a crash)",
		"what did the app actually say? the crash reason is usually in the last lines", "safe · read-only",
		"kubectl; a selector", "actuator loggers view, if streaming is blocked"},
	{"t", "threads", "jdebug threads", "a JVM thread dump (actuator / jattach / jdk route)",
		"what every thread is doing now — THE tool for slow/hung/high-CPU", "safe · instant",
		"actuator, or jattach, or a jdk debug container", "auto tries the routes safest-first"},
	{"x", "bundle", "jdebug snapshot", "one offline bundle: why + security + health + threads + memory + jcmd",
		"capture everything in one go to analyze offline or hand off", "safe (add --heap to include a heap dump)",
		"actuator and/or jattach", "capture pieces individually"},
	{"H", "heap", "jdebug heap --confirm", "a full JVM heap dump (.hprof)",
		"every object in memory — THE tool for leaks/OOM; analyze histogram with a, or Eclipse MAT", "⚠ PAUSES the JVM · may contain user data",
		"actuator or jattach; disk for the dump", "memory report first (no pause) to decide if you need it"},
	{"j", "jcmd", "jdebug jcmd \"<cmd>\"", "the JVM's own diagnostic commands via jattach",
		"GC info, native memory (NMT), flight recording, flags — advanced JVM introspection", "mostly safe; individual jcmds vary",
		"jattach staged in the pod", "actuator metrics for the common numbers"},
	{"v", "verbosity", "jdebug log-level <logger> <LEVEL>", "the actuator loggers endpoint",
		"turn a logger up/down live to see more — no restart", "changes log volume on every replica",
		"actuator with the loggers endpoint enabled", "—"},
	{"T", "terminal", "kubectl exec -it (or kubectl debug)", "an interactive shell inside the pod",
		"poke around by hand; falls back to a busybox debug container on distroless images", "caution — you're live in the pod",
		"kubectl exec; ephemeralcontainers for the debug fallback", "—"},
	{"R", "re-roll", "jdebug restart --confirm", "kubectl rollout restart on the owning Deployment",
		"cycle every pod to clear a wedged process or pick up a rotated Secret", "⚠ state-changing · restarts every pod",
		"kubectl patch/rollout on the Deployment", "kill one pod instead, to cycle a single sick replica"},
	{"K", "kill pod", "jdebug kill --confirm", "kubectl delete pod",
		"delete one sick pod; a managed pod respawns under a new name", "⚠ state-changing · drops in-flight requests",
		"kubectl delete on the pod", "re-roll to cycle the whole deployment"},
}

func infoFor(key string) (cmdInfo, bool) {
	for _, c := range cmdCatalog {
		if c.key == key {
			return c, true
		}
	}
	return cmdInfo{}, false
}

// detailView renders the transparency cards, the anchored one first.
func (m model) detailView() string {
	w := m.tw()
	var b strings.Builder
	b.WriteString("\n  " + cTitle.Render("what each command does — before you run it") + "\n")
	b.WriteString("  " + cFaint.Render("source · why · risk · needs · alternatives   ·   q/esc back · ↑↓ scroll") + "\n")

	ordered := cmdCatalog
	if m.detailAnchor != "" {
		if c, ok := infoFor(m.detailAnchor); ok {
			ordered = append([]cmdInfo{c}, filterOut(cmdCatalog, m.detailAnchor)...)
		}
	}
	field := func(label, val string) string {
		return "      " + cFaint.Render(fmt.Sprintf("%-6s", label)) + cMuted.Render(val) + "\n"
	}
	var lines []string
	for _, c := range ordered {
		riskStyle := cSafe
		if strings.Contains(c.risk, "⚠") {
			riskStyle = cDisr
		} else if strings.Contains(c.risk, "caution") || strings.Contains(c.risk, "changes") {
			riskStyle = cCaut
		}
		card := "\n  " + cKey.Render(c.key) + "  " + cBody.Render(c.title) +
			cFaint.Render("   $ "+c.runs) + "\n"
		card += field("from", c.src)
		card += field("why", c.why)
		card += "      " + cFaint.Render("risk  ") + riskStyle.Render(c.risk) + "\n"
		card += field("needs", c.needs)
		card += field("alt", c.alts)
		lines = append(lines, strings.Split(strings.TrimRight(card, "\n"), "\n")...)
	}

	// scroll window
	vis := m.height - 6
	if m.height == 0 || vis < 6 {
		vis = len(lines)
	}
	off := m.detailOff
	if off > len(lines)-vis {
		off = len(lines) - vis
	}
	if off < 0 {
		off = 0
	}
	end := off + vis
	if end > len(lines) {
		end = len(lines)
	}
	for _, l := range lines[off:end] {
		b.WriteString(l + "\n")
	}
	_ = w
	return b.String()
}

func filterOut(cs []cmdInfo, key string) []cmdInfo {
	var out []cmdInfo
	for _, c := range cs {
		if c.key != key {
			out = append(out, c)
		}
	}
	return out
}

func (m model) detailKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "Q", "esc", "enter", ".":
		m.scr = m.prev
		m.detailOff = 0
		m.detailAnchor = ""
		return m, nil
	case "up", "k":
		if m.detailOff > 0 {
			m.detailOff--
		}
	case "down", "j":
		m.detailOff++
	case "pgup", "b":
		m.detailOff -= 10
	case "pgdown", "space":
		m.detailOff += 10
	}
	if m.detailOff < 0 {
		m.detailOff = 0
	}
	return m, nil
}

// openDetail shows the transparency cards, optionally anchored to one key.
func (m model) openDetail(anchor string) (tea.Model, tea.Cmd) {
	m.prev = m.scr
	m.scr = scDetail
	m.detailAnchor = anchor
	m.detailOff = 0
	return m, nil
}
