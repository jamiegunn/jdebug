package main

import (
	"strings"
	"testing"
)

func TestRemoteArtifactsIndicatorAndCleanup(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	m.artifacts = []artifact{
		{owned: true, pod: "pod-a", path: "/tmp/jattach", note: "jattach"},
		{owned: false, pod: "pod-a", path: "/tmp/keep", note: "was already there"},
	}
	if m.ownedArtifacts() != 1 {
		t.Fatalf("only session-staged artifacts are removable, got %d", m.ownedArtifacts())
	}
	// the footer warns that jdebug left something in the pod
	if !strings.Contains(ansiStrip(m.footer("[q] quit")), "staged in the pod") {
		t.Fatal("footer must surface the staged-in-pod indicator")
	}
	// u opens the cleanup interstitial with the full transparency
	out := press(t, m, "u")
	cm := out.(model)
	if cm.scr != scCleanup {
		t.Fatalf("u must open the remote-artifacts screen, got %v", cm.scr)
	}
	v := ansiStrip(cm.cleanupView())
	for _, want := range []string{"REMOTE ARTIFACTS", "/tmp/jattach", "staged by jdebug",
		"pre-existing", "local dumps/", "jdebug cleanup --confirm"} {
		if !strings.Contains(v, want) {
			t.Fatalf("cleanup screen missing %q:\n%s", want, v)
		}
	}
	// y runs the cleanup command (which removes only the staged files)
	res, cmd := cm.cleanupKey("y")
	if cmd == nil || !res.(model).out.running || !strings.Contains(res.(model).out.title, "cleanup") {
		t.Fatalf("y must run jdebug cleanup --confirm, got title %q", res.(model).out.title)
	}
	// quitting with staged artifacts mentions them in the confirm
	if q := press(t, m, "q"); !strings.Contains(q.(model).confirmMsg, "staged in the pod") {
		t.Fatal("quitting with staged artifacts must mention them and offer cleanup")
	}
}

func TestNoRemoteArtifactsNoIndicator(t *testing.T) {
	m := readyModel()
	m.width, m.height = 200, 50
	m.artifacts = nil
	if strings.Contains(ansiStrip(m.footer("[q] quit")), "staged in the pod") {
		t.Fatal("no footer indicator when jdebug staged nothing")
	}
	if !strings.Contains(ansiStrip(m.cleanupView()), "nothing staged") {
		t.Fatal("the cleanup screen must reassure when nothing was staged")
	}
	if q := press(t, m, "q"); !strings.Contains(q.(model).confirmMsg, "quit jdebug?") {
		t.Fatal("a clean quit must not mention artifacts")
	}
}
