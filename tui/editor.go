package main

// editor.go — the target editor ('g'): each field is one keypress; everything
// the cluster can enumerate opens a live picker (arrow keys + number
// quick-select), with typed fallback when RBAC forbids listing. Plus the
// local-mode settings and the generic picker/input widgets.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type editorState struct{ note string }

// enumeration seams — swappable in tests so the RBAC/error/empty paths can
// be exercised without a cluster (or a kubectl binary).
var (
	namespacesFn = namespacesE
	podsFn       = podsWithStatusE
	containersFn = containersOfE
	selectorsFn  = selectorCandidates
)

// rbacInput drops straight to typed input with a plain-language explanation
// of the RBAC limit — an empty dropdown must never pretend nothing exists.
func (m model) rbacInput(title string, then inputTarget) (tea.Model, tea.Cmd) {
	m.input = inputBox{title: title, then: then}
	m.prev = scEditor
	m.scr = scInput
	return m, nil
}

func (m model) openEditor() (tea.Model, tea.Cmd) {
	m.scr = scEditor
	m.editor.note = ""
	return m, nil
}

func (m model) editorView() string {
	// every field carries a plain-language gloss — nobody should need the
	// glossary mid-incident
	row := func(k, name, val, gloss string) string {
		return "   " + cKey.Render(k) + "  " + cBody.Render(fmt.Sprintf("%-11s", name)) +
			cMuted.Render(fmt.Sprintf("%-34s", val)) + cFaint.Render(gloss)
	}
	sel := m.t.Selector
	if sel == "" {
		sel = "<any pod>"
	}
	pod := m.t.Pod
	if pod == "" {
		pod = "<auto: first match>"
	}
	out := "\n  " + cTitle.Render("TARGET") + cMuted.Render(" — press a letter to change a field · ") +
		cKey.Render("Enter") + cMuted.Render("/") + cKey.Render("b") + cMuted.Render(" back to the menu") + "\n" +
		row("c", "context", currentContext(), "which cluster kubectl talks to") + "\n" +
		row("n", "namespace", m.t.Namespace, "the app's folder in the cluster") + "\n" +
		row("s", "selector", sel, "label that finds your app's pods") + "\n" +
		row("p", "pod", pod, "one running copy of the app") + "\n" +
		row("o", "container", m.t.Container, "the app's box inside the pod") + "\n" +
		row("a", "actuator", m.t.Actuator, "Spring Boot admin endpoint in the app") + "\n"
	if next := m.editorNext(); next != "" {
		out += "\n  " + cOK.Render("▸ ") + cBody.Render(next) + "\n"
	}
	if m.editor.note != "" {
		out += "  " + cWarn.Render(m.editor.note) + "\n"
	}
	return out + prompt()
}

// editorNext points at the field that unblocks the readiness gate next.
func (m model) editorNext() string {
	if m.mode != 1 {
		return ""
	}
	switch {
	case m.t.Pod == "":
		return "Next: press p and pick the pod — the highest restart count usually marks the sick one (wrong app list? n narrows the namespace first)"
	case !m.remote.OK:
		return "Next: press o and pick the app's container — it's usually 'app', not the sidecar"
	}
	return ""
}

func (m model) editorKey(key string) (tea.Model, tea.Cmd) {
	m.editor.note = ""
	switch key {
	case "c", "C":
		return m.openPicker("Which cluster? (kube contexts on this machine)", kubeContexts(), currentContext(), false, pickContext)
	case "n", "N":
		res := namespacesFn()
		if res.forbidden {
			return m.rbacInput("Can't list namespaces with your current RBAC — type the namespace name (or ask for list permission):", inputNamespace)
		}
		if res.err != "" {
			m.editor.note = "couldn't list namespaces: " + res.err
			return m, nil
		}
		return m.openPicker("Namespace", res.items, m.t.Namespace, true, pickNamespace)
	case "s", "S":
		items, res := selectorsFn(m.t.Namespace, m.t.Pod)
		if res.forbidden {
			return m.rbacInput("Can't discover selectors — pods can't be listed with your current RBAC. Type one (e.g. app=payments), or pick a known pod by name with p:", inputSelector)
		}
		if res.err != "" {
			m.editor.note = "couldn't list pods for selector suggestions: " + res.err
			return m, nil
		}
		return m.openPicker("Selector — suggestions from pod labels in "+m.t.Namespace, items, orAny(m.t.Selector), true, pickSelector)
	case "p", "P":
		res := podsFn(m.t.Namespace, m.t.Selector)
		if res.forbidden {
			return m.rbacInput("Can't list pods in "+m.t.Namespace+" with your current RBAC — you can still type a pod name if you know it:", inputPod)
		}
		if res.err != "" {
			m.editor.note = "couldn't list pods: " + res.err
			return m, nil
		}
		if len(res.items) == 0 {
			// kubectl succeeded and returned zero rows — this one really is empty
			m.editor.note = "no pods match this target right now — check namespace/selector"
			return m, nil
		}
		return m.openPicker("Which pod? (a high restart count usually marks the sick one)", res.items, m.t.Pod, false, pickPod)
	case "o", "O":
		base := m.t.Pod
		if base == "" {
			if pods := podsFn(m.t.Namespace, m.t.Selector); len(pods.items) > 0 {
				base = strings.Fields(pods.items[0])[0]
			}
		}
		var conts []string
		if base != "" {
			res := containersFn(m.t.Namespace, base)
			if res.forbidden {
				return m.rbacInput("Can't read pod "+base+" with your current RBAC — type the container name (usually 'app'):", inputContainer)
			}
			if res.err != "" {
				m.editor.note = "couldn't read containers: " + res.err
				return m, nil
			}
			conts = res.items
		}
		return m.openPicker("Container"+ternary(base != "", " (in "+base+")", ""), conts, m.t.Container, true, pickContainer)
	case "a", "A":
		m.input = inputBox{title: "actuator base [" + m.t.Actuator + "]:", then: inputActuator}
		m.prev = scEditor
		m.scr = scInput
		return m, nil
	case "b", "B", "enter", "esc":
		saveTarget(m.t)
		m.remote.When = zeroTime() // force re-probe
		m.scr = scMenu
		return m, nil
	}
	return m, nil
}

func orAny(s string) string {
	if s == "" {
		return "<any pod>"
	}
	return s
}
func ternary(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// --- local settings ------------------------------------------------------------

func (m model) openLocalSettings() (tea.Model, tea.Cmd) {
	m.input = inputBox{title: "actuator base URL [" + m.t.Actuator + "]:", then: inputActuatorLocal}
	m.prev = scMenu
	m.scr = scInput
	return m, nil
}

// --- generic picker -------------------------------------------------------------

type pickKind int

const (
	pickContext pickKind = iota
	pickNamespace
	pickSelector
	pickPod
	pickContainer
)

type picker struct {
	title   string
	items   []string
	cursor  int
	current string
	typed   bool // 't' allowed
	kind    pickKind
}

func (m model) openPicker(title string, items []string, current string, typed bool, kind pickKind) (tea.Model, tea.Cmd) {
	if len(items) == 0 {
		if typed {
			m.input = inputBox{title: title + " (nothing to list — no permission to enumerate? type the value):", then: inputForPick(kind)}
			m.prev = scEditor
			m.scr = scInput
			return m, nil
		}
		m.editor.note = "nothing to list"
		m.scr = scEditor
		return m, nil
	}
	m.pick = picker{title: title, items: items, current: current, typed: typed, kind: kind}
	for i, it := range items {
		f := strings.Fields(it)
		if (len(f) > 0 && f[0] == current) || it == current || strings.HasPrefix(it, current+" ") {
			m.pick.cursor = i
		}
	}
	m.prev = scEditor
	m.scr = scPicker
	return m, nil
}

func (m model) pickerView() string {
	var b strings.Builder
	b.WriteString("\n  " + cTitle.Render(m.pick.title) + "\n")
	for i, it := range m.pick.items {
		mark := "  "
		line := "   " + cKey.Render(fmt.Sprintf("%d", i+1)) + "  " + cMuted.Render(it)
		if it == m.pick.current || strings.Fields(it)[0] == m.pick.current {
			line += cFaint.Render("  (current)")
		}
		if i == m.pick.cursor {
			line = cFocus.Render(" ▸" + line[1:])
			_ = mark
		}
		b.WriteString(line + "\n")
	}
	if m.pick.kind == pickPod {
		b.WriteString("   " + cKey.Render("0") + "  " + cMuted.Render("auto — just use the first match each time") + "\n")
	}
	hint := "↑/↓ + Enter · number picks · any other key keeps current"
	if m.pick.typed {
		hint = "↑/↓ + Enter · number picks · t types a value · any other key keeps current"
	}
	b.WriteString("  " + cFaint.Render(hint) + " ")
	return b.String()
}

func (m model) pickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		if m.pick.cursor > 0 {
			m.pick.cursor--
		}
		return m, nil
	case "down":
		if m.pick.cursor < len(m.pick.items)-1 {
			m.pick.cursor++
		}
		return m, nil
	case "enter":
		return m.applyPick(m.pick.items[m.pick.cursor])
	case "t", "T":
		if m.pick.typed {
			m.input = inputBox{title: "value:", then: inputForPick(m.pick.kind)}
			m.scr = scInput
			return m, nil
		}
	case "0":
		if m.pick.kind == pickPod {
			m.t.Pod = ""
			m.staleP = ""
			m.scr = scEditor
			return m, nil
		}
	}
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		i := int(key[0] - '1')
		if i < len(m.pick.items) {
			return m.applyPick(m.pick.items[i])
		}
	}
	m.scr = scEditor
	return m, nil
}

func (m model) applyPick(val string) (tea.Model, tea.Cmd) {
	m.scr = scEditor
	switch m.pick.kind {
	case pickContext:
		if val != currentContext() {
			return m.askConfirm("this becomes your kubectl default in every terminal — switch to "+val+"? [y/N]", "",
				func(mm *model) tea.Cmd {
					_ = useContext(val)
					mm.t.Pod = ""
					mm.remote.When = zeroTime()
					return nil
				})
		}
	case pickNamespace:
		m.t.Namespace = val
		m.t.Pod = ""
	case pickSelector:
		// items carry "  matches N pod(s)" annotations — the selector is the
		// first field
		if strings.HasPrefix(val, "<any pod>") {
			m.t.Selector = ""
		} else {
			m.t.Selector = strings.Fields(val)[0]
		}
		m.t.Pod = ""
	case pickPod:
		m.t.Pod = strings.Fields(val)[0]
		m.staleP = ""
	case pickContainer:
		m.t.Container = val
	}
	return m, nil
}

// --- text input -------------------------------------------------------------------

type inputTarget int

const (
	inputActuator inputTarget = iota
	inputActuatorLocal
	inputLogger
	inputJcmd
	inputNamespace
	inputSelector
	inputContainer
	inputPod
)

func inputForPick(k pickKind) inputTarget {
	switch k {
	case pickNamespace:
		return inputNamespace
	case pickSelector:
		return inputSelector
	case pickContainer:
		return inputContainer
	}
	return inputActuator
}

type inputBox struct {
	title string
	val   string
	then  inputTarget
}

func (m model) inputView() string {
	return "\n  " + cMuted.Render(m.input.title) + " " + cBody.Render(m.input.val) +
		"\n  " + cFaint.Render("Enter accepts · Esc cancels · empty keeps current") + "\n"
}

func (m model) inputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		v := strings.TrimSpace(m.input.val)
		back := m.prev
		m.scr = back
		if v == "" {
			return m, nil
		}
		switch m.input.then {
		case inputActuator, inputActuatorLocal:
			m.t.Actuator = v
			saveTarget(m.t)
			if m.input.then == inputActuatorLocal {
				m.local.When = zeroTime()
			}
		case inputLogger:
			m.logger = v
			m.scr = scLevel
		case inputJcmd:
			m.prev = scMenu
			m.scr = scMenu
			if m.mode == 1 {
				return m.quickCLI(true, "jcmd", v)
			}
			return m.quickLocal("jcmd", v)
		case inputNamespace:
			m.t.Namespace = v
			m.t.Pod = ""
			m.scr = scEditor
		case inputSelector:
			m.t.Selector = v
			m.t.Pod = ""
			m.scr = scEditor
		case inputContainer:
			m.t.Container = v
			m.scr = scEditor
		case inputPod:
			m.t.Pod = strings.Fields(v)[0]
			m.staleP = ""
			m.scr = scEditor
		}
		return m, nil
	case "esc":
		m.scr = m.prev
		return m, nil
	case "backspace":
		if len(m.input.val) > 0 {
			m.input.val = m.input.val[:len(m.input.val)-1]
		}
		return m, nil
	}
	if len(k.Runes) > 0 {
		m.input.val += string(k.Runes)
	}
	return m, nil
}
