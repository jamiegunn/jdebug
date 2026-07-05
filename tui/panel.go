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
	Restarts   int
	LastReason string // e.g. OOMKilled, Error
	MemUse     string // from kubectl top
	MemLimit   string
	MemPct     int // -1 unknown
	CPUUse     string
	CPULimit   string
	HPA        string
	HeapUsed   string // JVM, from actuator
	HeapMax    string
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
			LastState    struct {
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
			d.HeapUsed, d.HeapMax = jvmHeap(t)
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

// jvmHeap reads jvm.memory.used/max{area=heap} through the pod's actuator.
func jvmHeap(t target) (used, max string) {
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
	return get("jvm.memory.used"), get("jvm.memory.max")
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
	if d.Phase != "" && d.Phase != "Running" {
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
	if len(s) > 3 {
		s = s[:3]
	}
	return s
}

// --- render -------------------------------------------------------------------

const panelW = 38

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
		rows = append(rows, line("phase", dash(d.Phase), d.Phase != "" && d.Phase != "Running"))
		rows = append(rows, line("restarts", fmt.Sprintf("%d", d.Restarts), d.Restarts > 3))
		if d.LastReason != "" {
			rows = append(rows, line("last exit", d.LastReason, d.LastReason == "OOMKilled"))
		}
		mem := dash(d.MemUse)
		if d.MemLimit != "" {
			mem += " / " + d.MemLimit
			if d.MemPct >= 0 {
				mem += fmt.Sprintf("  %d%%", d.MemPct)
			}
		}
		rows = append(rows, line("mem", mem, d.MemPct >= 90))
		cpu := dash(d.CPUUse)
		if d.CPULimit != "" {
			cpu += " / " + d.CPULimit
		}
		rows = append(rows, line("cpu", cpu, false))
		rows = append(rows, line("hpa", dash(d.HPA), false))
		heap := dash(d.HeapUsed)
		if d.HeapMax != "" {
			heap += " / " + d.HeapMax
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
