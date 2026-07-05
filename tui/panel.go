package main

// panel.go — the live target panel on the right of the menu, and the NEXT
// box: the app brings the mental model. Data is fetched asynchronously
// (kubectl + an in-pod actuator read), refreshed on a timer and after every
// command, and turned into concrete "press X" suggestions.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type panelData struct {
	When       time.Time
	Phase      string
	Waiting    string // container waiting reason, e.g. CrashLoopBackOff
	Restarts   int
	LastReason string // e.g. OOMKilled, Error
	MemUse     string // from kubectl top
	MemLimit   string
	MemPct     int // -1 unknown
	CPUUse     string
	CPULimit   string
	HPA        string
	HeapUsed   string // JVM heap, live
	HeapMax    string
	HeapVia    string // "actuator" or "jcmd" — which route answered
	ActuatorOK bool
}

type panelMsg panelData
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(20*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// --- fetch -----------------------------------------------------------------

type podJSON struct {
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			RestartCount int    `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
			} `json:"state"`
			LastState struct {
				Terminated *struct {
					Reason string `json:"reason"`
				} `json:"terminated"`
			} `json:"lastState"`
		} `json:"containerStatuses"`
	} `json:"status"`
	Spec struct {
		Containers []struct {
			Name      string `json:"name"`
			Resources struct {
				Limits map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
}

func fetchPanel(t target) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		d := panelData{When: time.Now(), MemPct: -1}
		if t.Pod != "" {
			if raw, err := exec.CommandContext(ctx, "kubectl", "-n", t.Namespace, "get", "pod", t.Pod, "-o", "json").Output(); err == nil {
				var pj podJSON
				if json.Unmarshal(raw, &pj) == nil {
					d.Phase = pj.Status.Phase
					for _, cs := range pj.Status.ContainerStatuses {
						if cs.Name == t.Container || len(pj.Status.ContainerStatuses) == 1 {
							d.Restarts = cs.RestartCount
							if cs.LastState.Terminated != nil {
								d.LastReason = cs.LastState.Terminated.Reason
							}
							if cs.State.Waiting != nil {
								d.Waiting = cs.State.Waiting.Reason
							}
						}
					}
					for _, c := range pj.Spec.Containers {
						if c.Name == t.Container {
							d.MemLimit = c.Resources.Limits["memory"]
							d.CPULimit = c.Resources.Limits["cpu"]
						}
					}
				}
			}
			if out, err := exec.CommandContext(ctx, "kubectl", "-n", t.Namespace, "top", "pod", t.Pod, "--no-headers").Output(); err == nil {
				f := strings.Fields(string(out))
				if len(f) >= 3 {
					d.CPUUse, d.MemUse = f[1], f[2]
					d.MemPct = pctOf(d.MemUse, d.MemLimit)
				}
			}
			d.HeapUsed, d.HeapMax, d.HeapVia = jvmHeap(t)
			d.ActuatorOK = d.HeapVia == "actuator"
		}
		if hpa := klines("-n", t.Namespace, "get", "hpa", "--no-headers"); len(hpa) > 0 {
			f := strings.Fields(hpa[0])
			if len(f) >= 6 {
				d.HPA = f[0] + " " + f[len(f)-1] + " replicas"
			}
		}
		return panelMsg(d)
	}
}

var memRe = regexp.MustCompile(`^([0-9.]+)(Ki|Mi|Gi|Ti|m|k|M|G)?`)

func toBytes(s string) float64 {
	m := memRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "Ki", "k":
		v *= 1 << 10
	case "Mi", "M":
		v *= 1 << 20
	case "Gi", "G":
		v *= 1 << 30
	case "Ti":
		v *= 1 << 40
	}
	return v
}

func pctOf(use, limit string) int {
	u, l := toBytes(use), toBytes(limit)
	if u <= 0 || l <= 0 {
		return -1
	}
	return int(u * 100 / l)
}

// jvmHeap reads live heap numbers non-invasively: the actuator first, then
// `jcmd GC.heap_info` inside the pod (read-only, works on any JDK image —
// no actuator required). Returns which route answered.
func jvmHeap(t target) (used, max, via string) {
	get := func(metric string) string {
		snippet := fmt.Sprintf(
			`if command -v curl >/dev/null 2>&1; then curl -fsS --max-time 3 '%s/metrics/%s?tag=area:heap'; elif command -v wget >/dev/null 2>&1; then wget -qO- '%s/metrics/%s?tag=area:heap'; fi`,
			t.Actuator, metric, t.Actuator, metric)
		out, err := exec.Command("kubectl", "-n", t.Namespace, "exec", t.Pod, "-c", t.Container, "--", "sh", "-c", snippet).Output()
		if err != nil {
			return ""
		}
		if i := strings.Index(string(out), `"value":`); i >= 0 {
			rest := string(out)[i+8:]
			if j := strings.IndexAny(rest, ",}"); j > 0 {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rest[:j]), 64); err == nil {
					return fmt.Sprintf("%.0fMi", v/(1<<20))
				}
			}
		}
		return ""
	}
	if u, m := get("jvm.memory.used"), get("jvm.memory.max"); u != "" {
		return u, m, "actuator"
	}
	// fallback: jcmd ships with every JDK; GC.heap_info is a read-only probe
	snippet := `PID=$(pidof java 2>/dev/null || jps -q 2>/dev/null | head -n1); [ -n "$PID" ] || PID=1; jcmd "$PID" GC.heap_info 2>/dev/null`
	out, err := exec.Command("kubectl", "-n", t.Namespace, "exec", t.Pod, "-c", t.Container, "--", "sh", "-c", snippet).Output()
	if err == nil {
		if u, m := parseHeapInfo(string(out)); u != "" {
			return u, m, "jcmd"
		}
	}
	return "", "", ""
}

var heapUsedRe = regexp.MustCompile(`used (\d+)K`)
var heapCommRe = regexp.MustCompile(`committed (\d+)K`)
var heapTotalRe = regexp.MustCompile(` total (\d+)K`)

// parseHeapInfo sums used/committed across the heap lines of `jcmd
// GC.heap_info` (G1 has one line; the generational collectors list young +
// old). Metaspace and class space are non-heap — stop there.
func parseHeapInfo(out string) (used, max string) {
	var u, c, t float64
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Metaspace") || strings.Contains(line, "class space") {
			break
		}
		if m := heapUsedRe.FindStringSubmatch(line); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			u += v
		}
		if m := heapCommRe.FindStringSubmatch(line); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			c += v
		}
		if m := heapTotalRe.FindStringSubmatch(line); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			t += v
		}
	}
	if u <= 0 {
		return "", ""
	}
	denom := c
	if denom <= 0 {
		denom = t
	}
	used = fmt.Sprintf("%.0fMi", u/1024)
	if denom > 0 {
		max = fmt.Sprintf("%.0fMi", denom/1024)
	}
	return used, max
}

// --- suggestions: the app tells you what to do next --------------------------

func (m model) suggestions() []string {
	d := m.panel
	var s []string
	if d.LastReason == "OOMKilled" {
		s = append(s, cDisr.Render("⚠ OOMKilled last restart")+cMuted.Render(" → ")+cKey.Render("w")+cMuted.Render(" flow 1"))
	}
	if d.MemPct >= 90 {
		s = append(s, cDisr.Render(fmt.Sprintf("⚠ memory %d%% of limit", d.MemPct))+cMuted.Render(" → ")+cKey.Render("m"))
	} else if d.MemPct >= 75 {
		s = append(s, cWarn.Render(fmt.Sprintf("! memory %d%% of limit", d.MemPct))+cMuted.Render(" → ")+cKey.Render("m"))
	}
	if d.Waiting == "CrashLoopBackOff" {
		s = append(s, cDisr.Render("⚠ CrashLoopBackOff")+cMuted.Render(" → ")+cKey.Render("w")+cMuted.Render(" flow 7"))
	} else if d.Phase != "" && d.Phase != "Running" {
		s = append(s, cWarn.Render("! pod is "+d.Phase)+cMuted.Render(" → ")+cKey.Render("s")+cMuted.Render(" events"))
	} else if d.Restarts > 3 && d.LastReason != "OOMKilled" {
		s = append(s, cWarn.Render(fmt.Sprintf("! %d restarts", d.Restarts))+cMuted.Render(" → ")+cKey.Render("w")+cMuted.Render(" diagnose"))
	}
	if len(s) == 0 {
		if len(m.caps) == 0 {
			s = append(s, cMuted.Render("new here? ")+cKey.Render("w")+cMuted.Render(" — describe the symptom"))
		} else {
			s = append(s, cMuted.Render("evidence captured → ")+cKey.Render("a")+cMuted.Render(" analyzes it"))
		}
	}
	if !d.When.IsZero() && !d.ActuatorOK && len(s) < 3 {
		s = append(s, cMuted.Render("no actuator — captures still work (jattach/jcmd)"))
	}
	if len(s) > 3 {
		s = s[:3]
	}
	return s
}

// --- render -------------------------------------------------------------------

const panelW = 38

// compactStatus is the narrow-terminal stand-in for the side panel: an
// incident-checklist header — what's happening, then what to press — shown
// between the header and the menu when there's no room for TARGET LIVE.
func (m model) compactStatus() string {
	d := m.panel
	if m.mode != 1 || d.When.IsZero() {
		return ""
	}
	phase := d.Phase
	if d.Waiting != "" {
		phase = d.Waiting
	}
	if phase == "" {
		phase = "–"
	}
	ps := cMuted
	if d.Waiting != "" || (d.Phase != "" && d.Phase != "Running") {
		ps = cWarn
	}
	segs := []string{ps.Render(phase), cMuted.Render(fmt.Sprintf("restarts %d", d.Restarts))}
	if d.MemUse != "" && d.MemLimit != "" {
		v := fmt.Sprintf("mem %s of %s", d.MemUse, d.MemLimit)
		if d.MemPct >= 0 {
			v += fmt.Sprintf(" (%d%%)", d.MemPct)
		}
		ms := cMuted
		if d.MemPct >= 90 {
			ms = cDisr
		}
		segs = append(segs, ms.Render(v))
	}
	out := " " + cDim.Render("TARGET") + "  " + strings.Join(segs, cFaint.Render(" · ")) + "\n"
	for i, sug := range m.suggestions() {
		label := "NEXT"
		if i > 0 {
			label = ""
		}
		out += " " + cDim.Render(fmt.Sprintf("%-6s", label)) + "  " + sug + "\n"
	}
	return out
}

// panelView renders the TARGET LIVE column at width w, padded to h rows.
// trends adds the sparkline section (tier-2 only; tier 1 keeps the classic
// 38-col layout byte-identical for frontend-parity tests).
func (m model) panelView(w, h int, trends bool) string {
	d := m.panel
	line := func(k, v string, warn bool) string {
		vs := cMuted
		if warn {
			vs = cWarn
		}
		return " " + cFaint.Render(fmt.Sprintf("%-10s", k)) + vs.Render(v)
	}
	dash := func(s string) string {
		if s == "" {
			return "–"
		}
		return s
	}
	var rows []string
	rows = append(rows, " "+cDim.Render("TARGET LIVE")+"  "+cRule.Render(strings.Repeat("─", w-15)))
	if m.mode == 1 {
		pod := m.t.Pod
		if len(pod) > 24 {
			pod = "…" + pod[len(pod)-22:]
		}
		rows = append(rows, line("pod", dash(pod), false))
		phase := dash(d.Phase)
		if d.Waiting != "" {
			phase = d.Waiting // the waiting reason is the real story
		}
		rows = append(rows, line("phase", phase, d.Waiting != "" || (d.Phase != "" && d.Phase != "Running")))
		rows = append(rows, line("restarts", fmt.Sprintf("%d", d.Restarts), d.Restarts > 3))
		if d.LastReason != "" {
			rows = append(rows, line("last exit", d.LastReason, d.LastReason == "OOMKilled"))
		}
		// "usage of limit" — usage from kubectl top, limit from the pod spec
		mem := dash(d.MemUse)
		if d.MemUse != "" && d.MemLimit != "" {
			mem = d.MemUse + " of " + d.MemLimit + " limit"
			if d.MemPct >= 0 {
				mem += fmt.Sprintf("  %d%%", d.MemPct)
			}
		} else if d.MemUse != "" {
			mem = d.MemUse + " used · no limit set"
		}
		rows = append(rows, line("mem", mem, d.MemPct >= 90))
		cpu := dash(d.CPUUse)
		if d.CPUUse != "" && d.CPULimit != "" {
			cpu = d.CPUUse + " of " + d.CPULimit + " limit"
		} else if d.CPUUse != "" {
			cpu = d.CPUUse + " used · no limit set"
		}
		rows = append(rows, line("cpu", cpu, false))
		rows = append(rows, line("autoscale", dash(d.HPA), false))
		heap := d.HeapUsed
		switch {
		case heap != "" && d.HeapMax != "":
			heap += " / " + d.HeapMax + "  via " + d.HeapVia
		case heap != "":
			heap += "  via " + d.HeapVia
		case d.When.IsZero():
			heap = "–"
		default:
			heap = "– needs actuator or jcmd"
		}
		rows = append(rows, line("jvm heap", heap, false))
	} else {
		rows = append(rows, line("actuator", strings.TrimPrefix(m.t.Actuator, "http://localhost"), false))
		jat := "missing"
		if m.local.Jattach {
			jat = "staged"
		}
		rows = append(rows, line("jattach", jat, !m.local.Jattach))
	}
	rows = append(rows, "")
	if trends && m.mode == 1 {
		rows = append(rows, m.trendsRows(w)...)
		rows = append(rows, "")
	}
	rows = append(rows, " "+cDim.Render("NEXT")+"  "+cRule.Render(strings.Repeat("─", w-8)))
	for _, s := range m.suggestions() {
		rows = append(rows, " "+s)
	}
	age := "…"
	if !d.When.IsZero() {
		age = fmt.Sprintf("%ds ago", int(time.Since(d.When).Seconds()))
	}
	rows = append(rows, "")
	rows = append(rows, " "+cFaint.Render("refreshes every 20s · "+age))
	for len(rows) < h {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}
