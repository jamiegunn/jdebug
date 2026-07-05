package main

// panel.go — the live target panel on the right of the menu, and the NEXT
// box: the app brings the mental model. Data is fetched asynchronously
// (kubectl + an in-pod actuator read), refreshed on a timer and after every
// command, and turned into concrete "press X" suggestions.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type panelData struct {
	When        time.Time
	Phase       string
	Waiting     string // container waiting reason, e.g. CrashLoopBackOff
	Restarts    int
	LastReason  string // e.g. OOMKilled, Error
	OwnerKind   string // Kubernetes owner for this pod, usually ReplicaSet/Job/StatefulSet
	OwnerName   string
	DeployName  string // owning Deployment when the pod is owned through a ReplicaSet
	NodeName    string
	ServiceAcct string
	Volumes     []string // compact "name:type" storage/config refs from the pod spec
	MemUse      string   // from kubectl top
	MemLimit    string
	MemPct      int // -1 unknown
	CPUUse      string
	CPULimit    string
	HPAName     string // autoscale (HPA)
	HPACur      int
	HPAMax      int
	HPAMin      int
	HPAFailing  bool
	HPAReason   string // plain-language why it can't scale
	HeapUsed    string // JVM heap, live
	HeapMax     string
	HeapVia     string // "actuator" or "jcmd" — which route answered
	ActuatorOK  bool
	NoMetrics   bool // kubectl top failed because metrics-server is absent
	HeapSkipped bool // quiet mode: the JVM/actuator probe was deliberately not run
}

type panelMsg panelData
type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(20*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// --- fetch -----------------------------------------------------------------

type podJSON struct {
	Metadata struct {
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
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
		NodeName           string `json:"nodeName"`
		ServiceAccountName string `json:"serviceAccountName"`
		Containers         []struct {
			Name      string `json:"name"`
			Resources struct {
				Limits map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
		Volumes []struct {
			Name                  string    `json:"name"`
			PersistentVolumeClaim *struct{} `json:"persistentVolumeClaim"`
			ConfigMap             *struct{} `json:"configMap"`
			Secret                *struct{} `json:"secret"`
			EmptyDir              *struct{} `json:"emptyDir"`
			Projected             *struct{} `json:"projected"`
			DownwardAPI           *struct{} `json:"downwardAPI"`
			HostPath              *struct{} `json:"hostPath"`
		} `json:"volumes"`
	} `json:"spec"`
}

type replicaSetJSON struct {
	Metadata struct {
		OwnerReferences []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
}

// fetchPanel reads the pod status + kubectl top + HPA. probeJVM gates the one
// app/JVM-touching read (the actuator heap metric, with its jcmd fallback) so
// quiet mode can keep the cheap kubectl reads without poking the process.
func fetchPanel(t target, probeJVM bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		d := panelData{When: time.Now(), MemPct: -1}
		if t.Pod != "" {
			if raw, err := exec.CommandContext(ctx, "kubectl", "-n", t.Namespace, "get", "pod", t.Pod, "-o", "json").Output(); err == nil {
				var pj podJSON
				if json.Unmarshal(raw, &pj) == nil {
					d.Phase = pj.Status.Phase
					d.NodeName = pj.Spec.NodeName
					d.ServiceAcct = pj.Spec.ServiceAccountName
					if len(pj.Metadata.OwnerReferences) > 0 {
						d.OwnerKind = pj.Metadata.OwnerReferences[0].Kind
						d.OwnerName = pj.Metadata.OwnerReferences[0].Name
					}
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
					for _, v := range pj.Spec.Volumes {
						d.Volumes = append(d.Volumes, v.Name+":"+volumeKind(v))
					}
				}
			}
			if d.OwnerKind == "ReplicaSet" && d.OwnerName != "" {
				if raw, err := exec.CommandContext(ctx, "kubectl", "-n", t.Namespace, "get", "rs", d.OwnerName, "-o", "json").Output(); err == nil {
					var rj replicaSetJSON
					if json.Unmarshal(raw, &rj) == nil {
						for _, owner := range rj.Metadata.OwnerReferences {
							if owner.Kind == "Deployment" {
								d.DeployName = owner.Name
								break
							}
						}
					}
				}
			}
			topCmd := exec.CommandContext(ctx, "kubectl", "-n", t.Namespace, "top", "pod", t.Pod, "--no-headers")
			var topErr bytes.Buffer
			topCmd.Stderr = &topErr
			if out, err := topCmd.Output(); err == nil {
				f := strings.Fields(string(out))
				if len(f) >= 3 {
					d.CPUUse, d.MemUse = f[1], f[2]
					d.MemPct = pctOf(d.MemUse, d.MemLimit)
				}
			} else if s := strings.ToLower(topErr.String()); strings.Contains(s, "metrics") {
				d.NoMetrics = true // absence explained, not a silent dash
			}
			if probeJVM {
				d.HeapUsed, d.HeapMax, d.HeapVia = jvmHeap(t)
				d.ActuatorOK = d.HeapVia == "actuator"
			} else {
				d.HeapSkipped = true // carried heap shown; not re-probed while quiet
			}
		}
		parseHPA(ctx, t.Namespace, &d)
		return panelMsg(d)
	}
}

func volumeKind(v struct {
	Name                  string    `json:"name"`
	PersistentVolumeClaim *struct{} `json:"persistentVolumeClaim"`
	ConfigMap             *struct{} `json:"configMap"`
	Secret                *struct{} `json:"secret"`
	EmptyDir              *struct{} `json:"emptyDir"`
	Projected             *struct{} `json:"projected"`
	DownwardAPI           *struct{} `json:"downwardAPI"`
	HostPath              *struct{} `json:"hostPath"`
}) string {
	switch {
	case v.PersistentVolumeClaim != nil:
		return "pvc"
	case v.ConfigMap != nil:
		return "configmap"
	case v.Secret != nil:
		return "secret"
	case v.EmptyDir != nil:
		return "emptydir"
	case v.Projected != nil:
		return "projected"
	case v.DownwardAPI != nil:
		return "downwardapi"
	case v.HostPath != nil:
		return "hostpath"
	default:
		return "volume"
	}
}

type hpaJSON struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Min *int `json:"minReplicas"`
			Max int  `json:"maxReplicas"`
		} `json:"spec"`
		Status struct {
			Current    int `json:"currentReplicas"`
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// parseHPA fills the autoscale fields — current/max/min, whether it's at the
// ceiling, and whether it's actually able to scale (ScalingActive) — so the
// panel can say more than a bare replica count.
func parseHPA(ctx context.Context, ns string, d *panelData) {
	out, err := exec.CommandContext(ctx, "kubectl", "-n", ns, "get", "hpa", "-o", "json").Output()
	if err != nil {
		return
	}
	var hj hpaJSON
	if json.Unmarshal(out, &hj) != nil || len(hj.Items) == 0 {
		return
	}
	h := hj.Items[0]
	d.HPAName = h.Metadata.Name
	d.HPACur, d.HPAMax = h.Status.Current, h.Spec.Max
	if h.Spec.Min != nil {
		d.HPAMin = *h.Spec.Min
	}
	for _, c := range h.Status.Conditions {
		if c.Type == "ScalingActive" && c.Status == "False" {
			d.HPAFailing = true
			// translate the common reasons into plain language
			switch {
			case strings.Contains(c.Reason, "Metric"), strings.Contains(c.Message, "metrics"):
				d.HPAReason = "can't read metrics"
			case strings.Contains(strings.ToLower(c.Reason), "invalid"):
				d.HPAReason = "no valid rules"
			default:
				d.HPAReason = c.Reason
			}
		}
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

// a suggestion is one NEXT line: a confidence level (how sure we are it's a
// real problem), the headline signal, an optional evidence hop that shows the
// cause→effect chain (OOMKilled → mem 94% of limit), and the key to press.
// conf == "" marks an informational row (no problem asserted).
type suggestion struct {
	conf  string         // "likely" | "possible" | "unknown" | "" (info)
	msg   string         // the headline signal
	ev    string         // optional evidence hop that explains the headline
	key   string         // the action hint, e.g. "w flow 1" ("" = none)
	style lipgloss.Style // headline colour (severity)
}

// confStyle maps a confidence word to a colour: certainty → visual weight, so a
// junior can tell "likely: memory limit hit" from "unknown: metrics missing".
func confStyle(conf string) lipgloss.Style {
	switch conf {
	case "likely":
		return cDisr
	case "possible":
		return cWarn
	default: // unknown
		return cMuted
	}
}

// render turns a suggestion into a coloured line. Problem rows read
// "<conf>  <signal> → <evidence> → <key>"; info rows drop the tag and arrow.
func (s suggestion) render() string {
	renderKey := func(k string) string {
		if k == "" {
			return ""
		}
		head, rest, found := strings.Cut(k, " ")
		out := cKey.Render(head)
		if found {
			out += cMuted.Render(" " + rest)
		}
		return out
	}
	if s.conf == "" { // informational — no confidence, no arrow
		out := s.style.Render(s.msg)
		if s.key != "" {
			out += " " + renderKey(s.key)
		}
		return out
	}
	out := confStyle(s.conf).Render(s.conf) + "  " + s.style.Render(s.msg)
	if s.ev != "" {
		out += cFaint.Render(" → ") + cMuted.Render(s.ev)
	}
	if s.key != "" {
		out += cFaint.Render(" → ") + renderKey(s.key)
	}
	return out
}

// suggestionRows builds the NEXT list in SEVERITY order — the most operationally
// urgent signal first — so a multi-symptom incident doesn't bury the thing that
// matters. Order: unavailable/crash-loop → OOM/restart-storm → autoscale
// failed/maxed → resource pressure → observability gaps. Each row also carries a
// confidence level and, where the signals connect, a cause→effect chain.
func (m model) suggestionRows() []suggestion {
	d := m.panel
	var s []suggestion
	// 1. app unavailable / crash-looping
	if d.Waiting == "CrashLoopBackOff" {
		s = append(s, suggestion{"likely", "CrashLoopBackOff — won't stay up", "", "w flow 7", cDisr})
	} else if d.Phase != "" && d.Phase != "Running" {
		s = append(s, suggestion{"possible", "pod is " + d.Phase, "", "s events", cWarn})
	}
	// 2. OOM / restart storm — chain OOM to the memory reading when we have it
	if d.LastReason == "OOMKilled" {
		ev := ""
		if d.MemPct >= 75 {
			ev = fmt.Sprintf("mem %d%% of limit", d.MemPct)
		}
		s = append(s, suggestion{"likely", "OOMKilled last restart", ev, "w flow 1", cDisr})
	} else if d.Restarts > 3 {
		s = append(s, suggestion{"possible", fmt.Sprintf("%d restarts", d.Restarts), "", "w diagnose", cWarn})
	}
	// 3. autoscale failed / maxed — "blind" means we genuinely can't tell
	if d.HPAFailing {
		s = append(s, suggestion{"unknown", "autoscale blind — " + d.HPAReason, "", "W", cWarn})
	} else if d.HPAMax > 0 && d.HPACur >= d.HPAMax {
		s = append(s, suggestion{"possible", "at max replicas", "", "W workload", cWarn})
	}
	// 4. resource pressure
	if d.MemPct >= 90 {
		s = append(s, suggestion{"likely", fmt.Sprintf("memory %d%% of limit", d.MemPct), "", "m", cDisr})
	} else if d.MemPct >= 75 {
		s = append(s, suggestion{"possible", fmt.Sprintf("memory %d%% of limit", d.MemPct), "", "m", cWarn})
	}
	// 5. observability gaps — only when nothing more urgent is showing
	if len(s) == 0 {
		if len(m.caps) == 0 {
			s = append(s, suggestion{"", "new here?", "", "w — describe the symptom", cMuted})
		} else {
			s = append(s, suggestion{"", "evidence captured →", "", "a analyzes it", cMuted})
		}
	}
	if !d.When.IsZero() && !d.ActuatorOK && len(s) < 3 {
		s = append(s, suggestion{"", "no actuator — captures still work (jattach/jcmd)", "", "", cMuted})
	}
	if len(s) > 3 {
		s = s[:3]
	}
	return s
}

// suggestions renders the NEXT rows to coloured (unclipped) lines. Callers clip
// to their column width with ansi.Truncate.
func (m model) suggestions() []string {
	rows := m.suggestionRows()
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.render()
	}
	return out
}

// --- render -------------------------------------------------------------------

const panelW = 38

// hpaLine renders the autoscale row: current/max (+ min), whether it's at the
// ceiling, or — the incident-relevant bit — whether it can scale at all.
// Returns (text, isWarning).
func hpaLine(d panelData) (string, bool) {
	if d.HPAName == "" {
		return "no HPA — replicas are fixed", false
	}
	if d.HPAFailing {
		reason := d.HPAReason
		if reason == "" {
			reason = "not active"
		}
		return "✗ can't scale — " + reason, true
	}
	s := fmt.Sprintf("%d/%d replicas", d.HPACur, d.HPAMax)
	if d.HPAMin > 0 {
		s += fmt.Sprintf(" · min %d", d.HPAMin)
	}
	if d.HPAMax > 0 && d.HPACur >= d.HPAMax {
		return s + " · AT MAX", true // nowhere left to scale = a real signal
	}
	return s, false
}

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
		out += " " + cDim.Render(fmt.Sprintf("%-6s", label)) + "  " + ansi.Truncate(sug, m.tw()-11, "…") + "\n"
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
	title := " " + cDim.Render("TARGET LIVE")
	hint := ""
	if trends && m.mode == 1 { // tier-2 panel is clickable → the deep-dive
		hint = " " + cFaint.Render("click → why")
	}
	fill := w - lipgloss.Width(title) - lipgloss.Width(hint) - 2
	if fill < 3 {
		fill = 3
	}
	rows = append(rows, title+" "+cRule.Render(strings.Repeat("─", fill))+hint)
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
		// two groups, each labelled with where the number comes from, so a
		// junior doesn't conflate container memory with JVM heap
		groupRule := func(label, from string) string {
			head := " " + cFaint.Render(label) + " " + cFaint.Render(from) + " "
			fill := w - lipgloss.Width(head)
			if fill < 3 {
				fill = 3
			}
			return head + cRule.Render(strings.Repeat("─", fill))
		}
		rows = append(rows, groupRule("resource", "· container, from kubectl top"))
		// "usage of limit" — usage from kubectl top, limit from the pod spec
		mem := dash(d.MemUse)
		if d.NoMetrics && d.MemUse == "" {
			mem = "no metrics-server · limit " + dash(d.MemLimit)
		}
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
		if d.NoMetrics && d.CPUUse == "" {
			cpu = "no metrics-server · limit " + dash(d.CPULimit)
		}
		if d.CPUUse != "" && d.CPULimit != "" {
			cpu = d.CPUUse + " of " + d.CPULimit + " limit"
		} else if d.CPUUse != "" {
			cpu = d.CPUUse + " used · no limit set"
		}
		rows = append(rows, line("cpu", cpu, false))
		hpaStr, hpaWarn := hpaLine(d)
		rows = append(rows, line("autoscale", hpaStr, hpaWarn))

		rows = append(rows, groupRule("jvm", "· inside the process"))
		heap := d.HeapUsed
		switch {
		case heap != "" && d.HeapMax != "":
			heap += " / " + d.HeapMax + " · via " + d.HeapVia
		case heap != "":
			heap += " · via " + d.HeapVia
		case d.When.IsZero():
			heap = "–"
		case m.local.Jattach:
			heap = "– no actuator; capture via jattach"
		default:
			heap = "– no route (stage jattach: i)"
		}
		rows = append(rows, line("heap", heap, false))
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
		rows = append(rows, " "+ansi.Truncate(s, w-2, "…"))
	}
	age := "…"
	if !d.When.IsZero() {
		age = fmt.Sprintf("%ds ago", int(time.Since(d.When).Seconds()))
	}
	rows = append(rows, "")
	rows = append(rows, " "+cFaint.Render(ansi.Truncate(m.bgStatus()+" · "+age, w-2, "…")))
	for len(rows) < h {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}
