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
	t.Setenv("JDEBUG_DUMPS", dir) // capsRoot(kit) honours this
	mk := func(pod, sess, file, body string) {
		d := filepath.Join(dir, "pods", pod, sess)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, file), []byte(body), 0o644)
	}
	// pod-a: a heap, a thread dump, a log; pod-b: an unrelated newer heap
	os.MkdirAll(filepath.Join(dir, "pods", "pod-a", "20260705T103000Z"), 0o755)
	os.WriteFile(filepath.Join(dir, "pods", "pod-a", "20260705T103000Z", "heap-actuator.hprof"), synthHprof(), 0o644)
	mk("pod-a", "20260705T103000Z", "threads-jattach.txt", "Full thread dump")
	mk("pod-a", "20260705T103000Z", "tail-logs.txt", "log line")
	mk("pod-b", "20260705T110000Z", "heap-jdk.hprof", "not a heap")

	// pin every file to the SAME mtime: this reproduces what happens when captures
	// are written in one instant (mtimes collide, and their sub-second tie-break is
	// filesystem/OS-dependent). "recent" ordering must therefore come from the
	// capture's session timestamp, not mtime — this is the CI-vs-local bug guard.
	eq := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			os.Chtimes(p, eq, eq)
		}
		return nil
	})

	entries := fetchCapsFlat(".")().(capsFlatMsg).entries
	if len(entries) != 4 {
		t.Fatalf("flat browser should list every capture file across pods, got %d", len(entries))
	}
	m := readyModel()
	m.capsFlat = entries
	m.t.Pod = "pod-a" // filters (except recent) are scoped to the current pod

	m.capsFilter = "heaps"
	if hl := m.capsFocusList(); len(hl) != 1 || !strings.HasSuffix(hl[0].Name, ".hprof") {
		t.Fatalf("heaps filter (this pod) must show exactly pod-a's hprof, got %+v", hl)
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
	if got := len(m.capsFocusList()); got != 3 {
		t.Fatalf("the all filter (this pod) must show pod-a's 3 files, got %d", got)
	}
	// recent spans all pods, newest first — pod-b's 11:00 heap must lead pod-a's
	m.capsFilter = "recent"
	rec := m.capsFocusList()
	if len(rec) != 4 || rec[0].Pod != "pod-b" {
		t.Fatalf("recent must span all pods newest-first, got %d entries, first pod %q", len(rec), rec[0].Pod)
	}
}

func TestCapRoute(t *testing.T) {
	cases := map[string]string{
		"20260705T1/heap-actuator.hprof": "actuator",
		"s/threads-jattach.txt":          "jattach",
		"s/heap-jdk.hprof":               "jdk",
		"s/memory-report.txt":            "",
	}
	for in, want := range cases {
		if got := capRoute(in); got != want {
			t.Errorf("capRoute(%q) = %q, want %q", in, got, want)
		}
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
		{Name: "s/a.hprof", Path: "/x", Pod: mm.t.Pod, Mod: time.Now()},
		{Name: "s/b.txt", Path: "/x", Pod: mm.t.Pod, Mod: time.Now()},
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

func TestLastCapturePath(t *testing.T) {
	cases := map[string]string{
		"[21:15:21] wrote /d/pods/p/ts/heap-actuator.hprof ( 49M)": "/d/pods/p/ts/heap-actuator.hprof",
		"info: wrote /d/threads-jattach.txt (523 lines)":           "/d/threads-jattach.txt",
		"snapshot complete: /d/pods/p/ts  (7 captured, 0 failed)":  "/d/pods/p/ts",
		"nothing was written here":                                 "",
	}
	for in, want := range cases {
		if got := lastCapturePath([]byte(in)); got != want {
			t.Errorf("lastCapturePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnalyzeIsContextual(t *testing.T) {
	// after a heap capture, `a` analyzes exactly that heap
	m := readyModel()
	m.lastCapture = "/d/pods/p/ts/heap-actuator.hprof"
	if d := m.mustAnalyze(t); !strings.Contains(d, "heap-actuator.hprof") {
		t.Fatalf("a must analyze the last capture, got %q", d)
	}
	// then a thread dump → `a` analyzes the threads, not the heap
	m.lastCapture = "/d/pods/p/ts/threads-jattach.txt"
	if d := m.mustAnalyze(t); !strings.Contains(d, "threads-jattach.txt") {
		t.Fatalf("a must follow the latest capture, got %q", d)
	}
	// viewing a file takes precedence over the last capture
	m.out.filePath = "/d/pods/p/ts/why.txt"
	if d := m.mustAnalyze(t); !strings.Contains(d, "why.txt") {
		t.Fatalf("a must analyze the file being viewed first, got %q", d)
	}
	// nothing captured or viewed → bare analyze (newest session)
	m2 := readyModel()
	if d := m2.mustAnalyze(t); strings.Contains(d, ".hprof") || strings.Contains(d, ".txt") {
		t.Fatalf("with no capture/view, a runs a bare analyze, got %q", d)
	}
}

func (m model) mustAnalyze(t *testing.T) string {
	t.Helper()
	out, _ := m.analyzeContext()
	return out.(model).out.display
}
