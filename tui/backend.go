package main

// backend.go — everything that talks to the outside world: the kit's bash CLI
// location, the shared remembered-target config, kubectl enumeration for the
// dropdowns, and the readiness probes. The bash CLI stays the source of truth
// for all captures; this file only reads state.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// target mirrors the bash TUI's remembered target (shared config file).
type target struct {
	Namespace string
	Selector  string
	Container string
	Actuator  string
	// ActuatorAuth is a REFERENCE to pod env vars for a secured actuator
	// ("bearer:VAR" / "basic:USERVAR:PASSVAR"), never a secret value.
	ActuatorAuth string
	Pod          string
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
		case "SAVED_ACTUATOR_AUTH":
			t.ActuatorAuth = v
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
		"# written by jdebug's target editor — delete this file to forget\nSAVED_NAMESPACE=%s\nSAVED_SELECTOR=%s\nSAVED_CONTAINER=%s\nSAVED_ACTUATOR=%s\nSAVED_ACTUATOR_AUTH=%s\nSAVED_POD=%s\n",
		q(t.Namespace), q(t.Selector), q(t.Container), q(t.Actuator), q(t.ActuatorAuth), q(t.Pod))
	_ = os.WriteFile(filepath.Join(dir, "target"), []byte(body), 0o644)
}

// --- kubectl enumeration (identical invocations to the bash TUI) -------------

// enum preserves WHY a list came back empty: "no rows" and "kubectl failed"
// are different answers, and an RBAC denial must never masquerade as
// "nothing exists".
type enum struct {
	items     []string
	raw       string // untouched stdout, for JSON consumers
	err       string // first stderr line when kubectl failed
	forbidden bool   // the failure is an RBAC denial
}

var forbiddenRe = regexp.MustCompile(`(?i)forbidden|cannot (list|get) resource`)

func kenum(args ...string) enum {
	c := exec.Command("kubectl", args...)
	var errb bytes.Buffer
	c.Stderr = &errb
	out, err := c.Output()
	if err != nil {
		msg := firstLine(strings.TrimSpace(errb.String()))
		if msg == "" {
			msg = err.Error()
		}
		return enum{err: msg, forbidden: forbiddenRe.MatchString(msg)}
	}
	e := enum{raw: string(out)}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			e.items = append(e.items, l)
		}
	}
	return e
}

func kout(args ...string) (string, error) {
	out, err := exec.Command("kubectl", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func kubeContexts() []string { // local kubeconfig — no RBAC involved
	return kenum("config", "get-contexts", "-o", "name").items
}
func currentContext() string {
	if ctxOverride != "" {
		return ctxOverride
	}
	s, _ := kout("config", "current-context")
	return s
}
func namespacesE() enum {
	return kenum("get", "namespaces", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
}
func podsWithStatusE(ns, selector string) enum {
	args := []string{"-n", ns, "get", "pods"}
	if selector != "" {
		args = append(args, "-l", selector)
	}
	args = append(args, "-o", `jsonpath={range .items[*]}{.metadata.name}{"  "}{.status.phase}{"  restarts="}{.status.containerStatuses[0].restartCount}{"\n"}{end}`)
	return kenum(args...)
}
func containersOfE(ns, pod string) enum {
	return kenum("-n", ns, "get", "pod", pod, "-o", `jsonpath={range .spec.containers[*]}{.name}{"\n"}{end}`)
}
func containersOf(ns, pod string) []string { return containersOfE(ns, pod).items }
func useContext(ctx string) error {
	return exec.Command("kubectl", "config", "use-context", ctx).Run()
}

// --- selector discovery (conservative, transparent, never auto-picked) -------

// Stable workload labels, most specific first. Rollout internals
// (pod-template-hash & friends) are never suggested — they pin a single
// ReplicaSet revision, which is exactly the wrong-workload trap.
var preferredLabelKeys = []string{
	"app.kubernetes.io/name", "app.kubernetes.io/instance", "app",
	"k8s-app", "component", "service", "workload",
}

type podItem struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
}
type podsJSON struct {
	Items []podItem `json:"items"`
}

// deriveSelectors turns pod labels into ranked "key=value   matches N pod(s)"
// suggestions: labels on the already-selected pod first, then by key
// preference. <any pod> is always last, with a warning when the namespace
// clearly runs more than one app.
func deriveSelectors(pj podsJSON, pinned string) []string {
	var pinLabels map[string]string
	for _, it := range pj.Items {
		if it.Metadata.Name == pinned {
			pinLabels = it.Metadata.Labels
		}
	}
	type cand struct {
		sel   string
		count int
		onPin bool
		rank  int
	}
	byName := map[string]*cand{}
	var order []*cand
	for _, it := range pj.Items {
		for rank, k := range preferredLabelKeys {
			v, ok := it.Metadata.Labels[k]
			if !ok || v == "" {
				continue
			}
			sel := k + "=" + v
			c := byName[sel]
			if c == nil {
				c = &cand{sel: sel, rank: rank, onPin: pinLabels[k] == v}
				byName[sel] = c
				order = append(order, c)
			}
			c.count++
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.onPin != b.onPin {
			return a.onPin
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.count != b.count {
			return a.count > b.count
		}
		return a.sel < b.sel
	})
	// distinct values under any one stable key ≈ distinct apps here
	valsByKey := map[string]map[string]bool{}
	for _, c := range order {
		k := strings.SplitN(c.sel, "=", 2)[0]
		if valsByKey[k] == nil {
			valsByKey[k] = map[string]bool{}
		}
		valsByKey[k][c.sel] = true
	}
	apps := 0
	for _, set := range valsByKey {
		if len(set) > apps {
			apps = len(set)
		}
	}
	var out []string
	for _, c := range order {
		note := ""
		if c.onPin {
			note = " · on your selected pod"
		}
		out = append(out, fmt.Sprintf("%-34s matches %d pod(s)%s", c.sel, c.count, note))
	}
	anyNote := ""
	if apps > 1 {
		anyNote = fmt.Sprintf("   first match wins — risky, this namespace runs %d different apps", apps)
	}
	out = append(out, "<any pod>"+anyNote)
	return out
}

// selectorCandidates enumerates pod labels (one kubectl call). When listing
// is forbidden but a pod is already selected, its own labels may still be
// readable — suggest from those instead of failing outright.
func selectorCandidates(ns, pod string) ([]string, enum) {
	res := kenum("-n", ns, "get", "pods", "-o", "json")
	if res.err == "" {
		var pj podsJSON
		if json.Unmarshal([]byte(res.raw), &pj) == nil && len(pj.Items) > 0 {
			return deriveSelectors(pj, pod), enum{}
		}
		return []string{"<any pod>"}, enum{}
	}
	if pod != "" {
		if single := kenum("-n", ns, "get", "pod", pod, "-o", "json"); single.err == "" {
			var it podItem
			if json.Unmarshal([]byte(single.raw), &it) == nil && len(it.Metadata.Labels) > 0 {
				return deriveSelectors(podsJSON{Items: []podItem{it}}, pod), enum{}
			}
		}
	}
	return nil, res
}

// --- readiness probes (mirror the bash gate; cached by the caller) -----------

type probe struct {
	OK      bool
	Cluster bool // remote: cluster reachable AND credentials accepted
	// Unauthorized: the cluster ANSWERED and rejected the credentials — the
	// most common junior failure (expired EKS/GKE/OIDC token). It must never
	// be shown as "unreachable": "switch context" is the wrong fix; re-auth
	// is the right one.
	Unauthorized bool
	Jattach      bool     // local: jattach staged
	Lines        []string // rendered checklist lines
	When         time.Time
}

func zeroTime() time.Time { return time.Time{} }

var unauthorizedRe = regexp.MustCompile(`(?i)unauthorized|must be logged in|token.{0,20}expired|provide credentials`)

// clusterStatus distinguishes "ok" / "unauthorized" / "unreachable" — the
// three need different fixes, and the /version probe's stderr tells them apart.
func clusterStatus() string {
	c := exec.Command("kubectl", "get", "--raw=/version", "--request-timeout=3s")
	var errb bytes.Buffer
	c.Stderr = &errb
	if c.Run() == nil {
		return "ok"
	}
	if unauthorizedRe.MatchString(errb.String()) {
		return "unauthorized"
	}
	return "unreachable"
}

func clusterReachable() bool { return clusterStatus() == "ok" }

func remoteProbe(t target) probe {
	p := probe{When: time.Now()}
	bad := false
	switch clusterStatus() {
	case "ok":
		p.Cluster = true
		p.Lines = append(p.Lines, cSafe.Render("   ✓")+cMuted.Render(" cluster reachable"))
	case "unauthorized":
		bad = true
		p.Unauthorized = true
		p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" credentials — the cluster is UP but REJECTED your token (expired). Re-authenticate"))
		p.Lines = append(p.Lines, cMuted.Render("     (aws sso login · gcloud auth login · az login · oc login) — switching contexts won't fix this"))
	default:
		p.Lines = append(p.Lines, cDisr.Render("   ✗")+cMuted.Render(" cluster — not reachable (press ")+cKey.Render("c")+cMuted.Render(" for the full why + fix, or ")+cKey.Render("g")+cMuted.Render(" to switch context)"))
		bad = true
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
