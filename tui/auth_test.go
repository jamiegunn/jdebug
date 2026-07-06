package main

import (
	"strings"
	"testing"
)

func TestActuatorAuthInterstitial(t *testing.T) {
	// k in the editor opens the guided auth screen
	m := readyModel()
	m.scr = scEditor
	out := press(t, m, "k")
	mm := out.(model)
	if mm.scr != scAuth {
		t.Fatalf("k must open the guided ACTUATOR AUTH screen, got %v", mm.scr)
	}
	v := ansiStrip(mm.authView())
	// title + intent + both formats with examples
	for _, want := range []string{
		"ACTUATOR AUTH", "Intent:",
		"bearer:MANAGEMENT_TOKEN", "basic:ACTUATOR_USER:ACTUATOR_PASSWORD",
		"REFERENCE, not the secret", // stores a reference, never a value
		"never paste secret values",
		"env | grep",   // a safe way to find env var NAMES
		"without HTTP", // jattach fallback
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("auth screen missing %q:\n%s", want, v)
		}
	}
}

func TestActuatorAuthSavesReferenceOnly(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir())
	// choosing "none" clears the auth reference
	m := readyModel()
	m.t.ActuatorAuth = "bearer:TOK"
	m.scr = scAuth
	if got := press(t, m, "1").(model).t.ActuatorAuth; got != "" {
		t.Fatalf("choosing none must clear the auth reference, got %q", got)
	}
	// choosing bearer prefills the input with the format (not a secret value)
	b := readyModel()
	b.scr = scAuth
	ib := press(t, b, "2").(model)
	if ib.scr != scInput || ib.input.val != "bearer:" {
		t.Fatalf("bearer must open the input prefilled with 'bearer:', got scr=%v val=%q", ib.scr, ib.input.val)
	}
	// typing an env-var NAME and accepting saves only the reference string
	ib.input.val = "bearer:MANAGEMENT_TOKEN"
	saved := press(t, ib, "enter").(model)
	if saved.t.ActuatorAuth != "bearer:MANAGEMENT_TOKEN" {
		t.Fatalf("auth must persist the reference string, got %q", saved.t.ActuatorAuth)
	}
}
