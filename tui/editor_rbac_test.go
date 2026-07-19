package main

import (
	"strings"
	"testing"
)

// When RBAC forbids listing a field, the editor must NOT show an empty picker
// (which reads as "nothing exists") — it must drop to typed input with the
// reason, and the typed value must apply. One test per enumerable field.

func TestEditorNamespaceRBACOffersTyping(t *testing.T) {
	saved := namespacesFn
	defer func() { namespacesFn = saved }()
	namespacesFn = func() enum {
		return enum{err: `namespaces is forbidden: User "dev" cannot list resource "namespaces"`, forbidden: true}
	}
	m := readyModel()
	m.scr = scEditor
	out := press(t, m, "n").(model)
	if out.scr != scInput || out.input.then != inputNamespace || !strings.Contains(out.input.title, "RBAC") {
		t.Fatalf("forbidden namespace listing must fall back to typed input with the reason, got screen %v then %v title %q",
			out.scr, out.input.then, out.input.title)
	}
	// typing a namespace applies it (and clears the pod, since the app moved)
	typed := press(t, out, "p", "r", "o", "d", "enter").(model)
	if typed.t.Namespace != "prod" {
		t.Fatalf("a typed namespace must apply, got %q", typed.t.Namespace)
	}
}

func TestEditorSelectorRBACOffersTyping(t *testing.T) {
	saved := selectorsFn
	defer func() { selectorsFn = saved }()
	selectorsFn = func(ns, pod string) ([]string, enum) {
		return nil, enum{err: "pods is forbidden", forbidden: true}
	}
	m := readyModel()
	m.scr = scEditor
	out := press(t, m, "s").(model)
	if out.scr != scInput || out.input.then != inputSelector || !strings.Contains(out.input.title, "RBAC") {
		t.Fatalf("forbidden selector discovery must fall back to typed input, got screen %v then %v title %q",
			out.scr, out.input.then, out.input.title)
	}
	// a hand-typed label selector applies verbatim
	typed := press(t, out, "a", "p", "p", "=", "p", "a", "y", "enter").(model)
	if typed.t.Selector != "app=pay" {
		t.Fatalf("a typed selector must apply, got %q", typed.t.Selector)
	}
}

func TestEditorContainerRBACOffersTyping(t *testing.T) {
	saved := containersFn
	defer func() { containersFn = saved }()
	containersFn = func(ns, pod string) enum {
		return enum{err: "pods is forbidden", forbidden: true}
	}
	m := readyModel()
	m.scr = scEditor
	m.t.Pod = "pod-a" // a pinned pod, so 'o' reads that pod's containers directly
	out := press(t, m, "o").(model)
	if out.scr != scInput || out.input.then != inputContainer || !strings.Contains(out.input.title, "RBAC") {
		t.Fatalf("forbidden container read must fall back to typed input, got screen %v then %v title %q",
			out.scr, out.input.then, out.input.title)
	}
	typed := press(t, out, "a", "p", "p", "enter").(model)
	if typed.t.Container != "app" {
		t.Fatalf("a typed container must apply, got %q", typed.t.Container)
	}
}

// The three "can't enumerate" outcomes must stay distinct: an RBAC denial drops
// to typed input, a genuine kubectl failure surfaces the error (not "empty"),
// and a real zero-row result is the only thing allowed to say "nothing matches".
func TestNamespaceEnumerationOutcomesAreDistinct(t *testing.T) {
	saved := namespacesFn
	defer func() { namespacesFn = saved }()

	// 1. RBAC forbidden → typed input
	namespacesFn = func() enum { return enum{err: "forbidden", forbidden: true} }
	if out := press(t, editorModel(), "n").(model); out.scr != scInput {
		t.Fatal("forbidden namespaces must offer typing, not an error note")
	}

	// 2. kubectl failed (not RBAC) → an honest error note, still on the editor
	namespacesFn = func() enum { return enum{err: "Unable to connect to the server"} }
	out := press(t, editorModel(), "n").(model)
	if out.scr != scEditor || !strings.Contains(out.editor.note, "couldn't list namespaces") {
		t.Fatalf("a kubectl failure must surface as an error note, got screen %v note %q", out.scr, out.editor.note)
	}
}

func editorModel() model {
	m := readyModel()
	m.scr = scEditor
	return m
}
