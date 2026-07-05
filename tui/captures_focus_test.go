package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapsFocusFlatAndFilter(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "pods", "pod-a")
	s1 := filepath.Join(root, "20260705T103000Z")
	if err := os.MkdirAll(s1, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s1, "heap-actuator.hprof"), synthHprof(), 0o644)
	os.WriteFile(filepath.Join(s1, "threads-actuator.txt"), []byte("Full thread dump"), 0o644)
	os.WriteFile(filepath.Join(s1, "tail-logs.txt"), []byte("log line"), 0o644)

	entries := fetchCapsFlat(".", root)().(capsFlatMsg).entries
	if len(entries) != 3 {
		t.Fatalf("flat browser should list every capture file, got %d", len(entries))
	}
	m := readyModel()
	m.capsFlat = entries
	m.capsFilter = "heaps"
	if hl := m.capsFocusList(); len(hl) != 1 || !strings.HasSuffix(hl[0].Name, ".hprof") {
		t.Fatalf("heaps filter must show exactly the hprof, got %+v", hl)
	}
	m.capsFilter = "threads"
	if len(m.capsFocusList()) != 1 {
		t.Fatal("threads filter must show exactly the thread dump")
	}
	m.capsFilter = "logs"
	if len(m.capsFocusList()) != 1 {
		t.Fatal("logs filter must show exactly the log file")
	}
	m.capsFilter = "all"
	if len(m.capsFocusList()) != 3 {
		t.Fatal("the all filter must show everything")
	}
}

func TestCapsFocusKeys(t *testing.T) {
	m := readyModel()
	out, cmd := m.Update(key("d"))
	mm := out.(model)
	if !mm.capsFocus || cmd == nil {
		t.Fatal("d must open the captures focus browser and refresh its list")
	}
	mm.capsFlat = []capEntry{
		{Name: "s/a.hprof", Path: "/x", Mod: time.Now()},
		{Name: "s/b.txt", Path: "/x", Mod: time.Now()},
	}
	// the view shows filter tabs and a keyboard hint
	v := ansiStrip(mm.capsFocusView())
	if !strings.Contains(v, "[all]") || !strings.Contains(v, "↑↓ select") {
		t.Fatalf("focus view must show filter tabs + keyboard hints:\n%s", v)
	}
	// Tab cycles the filter; down moves the selection
	f0 := mm.capsFilter
	cyc := press(t, mm, "tab").(model)
	if cyc.capsFilter == f0 {
		t.Fatal("Tab must cycle the filter")
	}
	moved := press(t, mm, "down").(model)
	if moved.capsSel != 1 {
		t.Fatalf("down must move the selection, got %d", moved.capsSel)
	}
	// esc closes back to the dashboard
	if press(t, moved, "esc").(model).capsFocus {
		t.Fatal("esc must close the focus browser")
	}
}
