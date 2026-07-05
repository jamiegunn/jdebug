package main

// backend.go — everything that talks to the outside world: the kit's bash CLI
// location, the shared remembered-target config, kubectl enumeration for the
// dropdowns, and the readiness probes. The bash CLI stays the source of truth
// for all captures; this file only reads state.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// target mirrors the bash TUI's remembered target (shared config file).
type target struct {
	Namespace string
	Selector  string
	Container string
	Actuator  string
	Pod       string
}

func defaultTarget() target {
	return target{Namespace: "default", Container: "app", Actuator: "http://localhost:8080/actuator"}
}

// kitRoot finds the bash kit: $JDEBUG_KIT, the binary's parent dir (tui/ lives
// inside the kit), or the `jdebug` on PATH resolved through its symlink.
func kitRoot() string {
	if k := os.Getenv("JDEBUG_KIT"); k != "" {
		return k
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			cand := filepath.Dir(filepath.Dir(resolved)) // <kit>/tui/jdebug-tui
			if _, err := os.Stat(filepath.Join(cand, "jdebug")); err == nil {
				return cand
			}
		}
	}
	if p, err := exec.LookPath("jdebug"); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Dir(resolved)
		}
	}
	return "."
}

func configDir() string {
	if d := os.Getenv("JDEBUG_CONFIG_DIR"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "jdebug")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "jdebug")
}

func dumpsDir(kit string) string {
	if d := os.Getenv("JDEBUG_DUMPS"); d != "" {
		return d
	}
	return filepath.Join(kit, "dumps")
}

// loadTarget parses the bash-format config (SAVED_X=value, single-quote aware).
func loadTarget() target {
	t := defaultTarget()
	data, err := os.ReadFile(filepath.Join(configDir(), "target"))
	if err != nil {
		return t
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.Trim(v, "'\"")
		switch k {
		case "SAVED_NAMESPACE":
			if v != "" {
				t.Namespace = v
			}
		case "SAVED_SELECTOR":
			t.Selector = v
		case "SAVED_CONTAINER":
			if v != "" {
				t.Container = v
			}
		case "SAVED_ACTUATOR":
			if v != "" {
				t.Actuator = v
			}
		case "SAVED_POD":
			t.Pod = v
		}
	}
	// environment outranks saved, matching the CLI's precedence
	if v := os.Getenv("JDEBUG_NAMESPACE"); v != "" {
		t.Namespace = v
	}
	if v, set := os.LookupEnv("JDEBUG_SELECTOR"); set {
		t.Selector = v
	}
	if v := os.Getenv("JDEBUG_CONTAINER"); v != "" {
		t.Container = v
	}
	if v := os.Getenv("ACTUATOR_BASE"); v != "" {
		t.Actuator = v
	}
	return t
}

func saveTarget(t target) {
	dir := configDir()
	_ = os.MkdirAll(dir, 0o755)
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	body := fmt.Sprintf(
		"# written by jdebug's target editor — delete this file to forget\nSAVED_NAMESPACE=%s\nSAVED_SELECTOR=%s\nSAVED_CONTAINER=%s\nSAVED_ACTUATOR=%s\nSAVED_POD=%s\n",
		q(t.Namespace), q(t.Selector), q(t.Container), q(t.Actuator), q(t.Pod))
	_ = os.WriteFile(filepath.Join(dir, "target"), []byte(body), 0o644)
}

// --- kubectl enumeration (identical invocations to the bash TUI) -------------

func kout(args ...string) (string, error) {
	out, err := exec.Command("kubectl", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func klines(args ...string) []string {
	out, _ := kout(args...)
	if out == "" {
		return nil
	}
	var r []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			r = append(r, l)
		}
	}
	return r
}

func kubeContexts() []string { return klines("config", "get-contexts", "-o", "name") }
func currentContext() string {
	if ctxOverride != "" {
		return ctxOverride
	}
	s, _ := kout("config", "current-context")
	return s
}
func namespaces() []string {
	return klines("get", "namespaces", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
}
func appSelectors(ns string) []string {
	labels := klines("-n", ns, "get", "pods", "-o", `jsonpath={range .items[*]}{.metadata.labels.app}{"\n"}{end}`)
	seen := map[string]bool{}
	out := []string{"<any pod>"}
	for _, l := range labels {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, "app="+l)
		}
	}
	return out
}
func podsWithStatus(ns, selector string) []string {
	args := []string{"-n", ns, "get", "pods"}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	args = append(args, "-o", `jsonpath={range .items[*]}{.metadata.name}{"  "}{.status.phase}{"  restarts="}{.status.containerStatuses[0].restartCount}{"\n"}{end}`)
	return klines(args...)
}
func containersOf(ns, pod string) []string {
	return klines("-n", ns, "get", "pod", pod, "-o", `jsonpath={range .spec.containers[*]}{.name}{"\n"}{end}`)
}
func useContext(ctx string) error {
	return exec.Command("kubectl", "config", "use-context", ctx).Run()
}

// --- readiness probes (mirror the bash gate; cached by the caller) -----------

type probe struct {
	OK      bool
	Cluster bool     // remote: cluster reachable
	Jattach bool     // local: jattach staged
	Lines   []string // rendered checklist lines
	When    time.Time
}

func zeroTime() time.Time { return time.Time{} }

func clusterReachable() bool {
	return exec.Command("kubectl", "get", "--raw=/version", "--request-timeout=3s").Run() == nil
}

func remoteProbe(t target) probe {
	p := probe{When: time.Now()}
	bad := false
	p.Cluster = clusterReachable()
	if p.Cluster {
		p.Lines = append(p.Lines, cSafe.Render("   ✓")+cMuted.Render(" cluster reachable"))
	} else {
		bad = true
		p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" cluster — not reachable (press ")+cKey.Render("c")+cMuted.Render(" for the full why + fix, or ")+cKey.Render("g")+cMuted.Render(" to switch context)"))
	}
	switch {
	case t.Pod == "":
		bad = true
		p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" pod — none selected yet (press ")+cKey.Render("g")+cMuted.Render(", then ")+cKey.Render("p")+cMuted.Render(", and pick the exact pod)"))
		p.Lines = append(p.Lines, cFaint.Render("   · container — checked once a pod is selected"))
	case p.Cluster:
		conts := containersOf(t.Namespace, t.Pod)
		if len(conts) == 0 {
			bad = true
			p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" pod — "+t.Pod+" no longer exists (press ")+cKey.Render("g")+cMuted.Render(", then ")+cKey.Render("p")+cMuted.Render(", to re-pick)"))
		} else {
			p.Lines = append(p.Lines, cSafe.Render("   ✓")+cMuted.Render(" pod "+t.Pod))
			found := false
			for _, c := range conts {
				if c == t.Container {
					found = true
				}
			}
			if found {
				p.Lines = append(p.Lines, cSafe.Render("   ✓")+cMuted.Render(" container "+t.Container))
			} else {
				bad = true
				p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(fmt.Sprintf(" container — '%s' is not in that pod (it has: %s) — press ", t.Container, strings.Join(conts, " ")))+cKey.Render("g")+cMuted.Render(", then ")+cKey.Render("o"))
			}
		}
	default:
		p.Lines = append(p.Lines, cFaint.Render("   · pod + container — checked once the cluster answers"))
	}
	p.OK = !bad
	return p
}

func localProbe(kit string, t target) probe {
	p := probe{When: time.Now()}
	act := exec.Command("sh", filepath.Join(kit, "jdebug-local"), "health").Run() == nil
	jat := false
	if fi, err := os.Stat(jattachBin()); err == nil && fi.Mode()&0o111 != 0 {
		jat = true
	}
	p.Jattach = jat
	if act {
		p.Lines = append(p.Lines, cSafe.Render("   ✓")+cMuted.Render(" actuator answering at "+t.Actuator))
	} else {
		p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" actuator — nothing answering at "+t.Actuator+" (press ")+cKey.Render("s")+cMuted.Render(" to fix the URL/port)"))
	}
	if jat {
		p.Lines = append(p.Lines, cSafe.Render("   ✓")+cMuted.Render(" jattach staged at "+jattachBin()))
	} else {
		p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" jattach — not staged (press ")+cKey.Render("i")+cMuted.Render(" to download it, ~80 KB)"))
	}
	p.OK = act || jat
	return p
}

func jattachBin() string {
	if b := os.Getenv("JATTACH_BIN"); b != "" {
		return b
	}
	return "/tmp/jattach"
}
