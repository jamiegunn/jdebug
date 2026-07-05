package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// synthHprof builds a minimal but format-correct hprof: a class with one
// instance field, several instances of it, a byte[] array, and a root record
// (which the parser must skip to reach the instances). idSize = 8.
func synthHprof() []byte {
	var buf bytes.Buffer
	buf.WriteString("JAVA PROFILE 1.0.2\x00")
	binary.Write(&buf, binary.BigEndian, uint32(8)) // id size
	binary.Write(&buf, binary.BigEndian, uint64(0)) // timestamp

	id := func(v uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		return b
	}
	u4 := func(v uint32) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, v)
		return b
	}
	u2 := func(v uint16) []byte {
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, v)
		return b
	}
	record := func(tag byte, body []byte) {
		buf.WriteByte(tag)
		buf.Write(u4(0)) // time delta
		buf.Write(u4(uint32(len(body))))
		buf.Write(body)
	}

	// STRING id=1 -> "com/example/Big"
	record(0x01, append(id(1), []byte("com/example/Big")...))
	// LOAD_CLASS: serial, classObjId=100, stackSerial, nameStringId=1
	var lc bytes.Buffer
	lc.Write(u4(1))
	lc.Write(id(100))
	lc.Write(u4(0))
	lc.Write(id(1))
	record(0x02, lc.Bytes())

	// heap dump segment
	var hd bytes.Buffer
	// ROOT_STICKY_CLASS (0x05): id — must be skipped, not mis-parsed
	hd.WriteByte(0x05)
	hd.Write(id(100))
	// CLASS_DUMP (0x20) for classObjId=100: 0 const pool, 0 static, 1 inst field
	hd.WriteByte(0x20)
	hd.Write(id(100)) // class obj id
	hd.Write(u4(0))   // stack serial
	hd.Write(id(0))   // super
	hd.Write(id(0))   // loader
	hd.Write(id(0))   // signers
	hd.Write(id(0))   // protection domain
	hd.Write(id(0))   // reserved1
	hd.Write(id(0))   // reserved2
	hd.Write(u4(16))  // instance size
	hd.Write(u2(0))   // constant pool size
	hd.Write(u2(0))   // static fields
	hd.Write(u2(1))   // instance fields
	hd.Write(id(1))   // field name string id
	hd.WriteByte(10)  // field type = int
	// three INSTANCE_DUMPs of class 100, 16 bytes payload each
	for i := 0; i < 3; i++ {
		hd.WriteByte(0x21)
		hd.Write(id(uint64(200 + i))) // obj id
		hd.Write(u4(0))               // stack
		hd.Write(id(100))             // class obj id
		hd.Write(u4(16))              // nbytes
		hd.Write(make([]byte, 16))    // payload
	}
	// PRIM_ARRAY_DUMP byte[] of 1000 elements
	hd.WriteByte(0x23)
	hd.Write(id(300))
	hd.Write(u4(0))              // stack
	hd.Write(u4(1000))           // n
	hd.WriteByte(8)              // type = byte
	hd.Write(make([]byte, 1000)) // data
	record(0x0C, hd.Bytes())

	return buf.Bytes()
}

func TestHprofHistogram(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heap.hprof")
	if err := os.WriteFile(path, synthHprof(), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := analyzeHprof(path)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	byName := map[string]classStat{}
	for _, c := range h.classes {
		byName[c.name] = c
	}
	big, ok := byName["com.example.Big"]
	if !ok || big.count != 3 {
		t.Fatalf("expected 3 com.example.Big instances, got %+v (all: %+v)", big, h.classes)
	}
	ba, ok := byName["byte[]"]
	if !ok || ba.count != 1 || ba.bytes < 1000 {
		t.Fatalf("expected one byte[] of >=1000 bytes, got %+v", ba)
	}
	// the byte[] dominates, so it must sort first
	if h.classes[0].name != "byte[]" {
		t.Fatalf("largest consumer should sort first, got %q", h.classes[0].name)
	}
	out := renderHistogram(h, 15)
	if !strings.Contains(out, "byte[]") || !strings.Contains(out, "heap histogram") {
		t.Fatalf("render missing content:\n%s", out)
	}
}

func TestHprofRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.hprof")
	os.WriteFile(path, []byte("HTTP 404 not an hprof"), 0o644)
	if _, err := analyzeHprof(path); err == nil {
		t.Fatal("a non-hprof file must be rejected, not silently parsed")
	}
}

func TestCapturesMarkInvalidHprof(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "good.hprof"), synthHprof(), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.hprof"),
		[]byte("<!DOCTYPE html><html><head><title>Please sign in</title></head></html>"), 0o644)
	msg := fetchCaps(".", dir)().(capsMsg)
	byName := map[string]capEntry{}
	for _, c := range msg.entries {
		byName[c.Name] = c
	}
	if byName["good.hprof"].Invalid {
		t.Fatal("a valid hprof must not be marked invalid")
	}
	if !byName["bad.hprof"].Invalid {
		t.Fatal("an error-page .hprof must be marked invalid in the browser")
	}
	if h := capHint(byName["bad.hprof"]); !strings.Contains(h, "not a heap dump") {
		t.Fatalf("invalid hprof hint must warn, got %q", h)
	}
}

func TestCapturesResetOnPodSwitch(t *testing.T) {
	t.Setenv("JDEBUG_CONFIG_DIR", t.TempDir())
	m := readyModel()
	m.capsCwd = "dumps/pods/some-old-pod/20260101T000000Z" // pinned to a previous pod
	m.capsOff = 3
	out, _ := m.switchPod("app-debug-demo-app-6c6c4b5769-x7k2p")
	mm := out.(model)
	if mm.capsCwd != "" || mm.capsOff != 0 {
		t.Fatalf("switching pod must un-pin the captures browser, got cwd=%q off=%d", mm.capsCwd, mm.capsOff)
	}
}

func TestCapsScope(t *testing.T) {
	m := readyModel()
	m.capsCwd, m.t.Pod = "", "pod-a"
	if s := m.capsScope(); s != "this pod" {
		t.Fatalf("default scope with a pinned pod must be 'this pod', got %q", s)
	}
	m.t.Pod = ""
	if s := m.capsScope(); s != "all pods" {
		t.Fatalf("no pod pinned must scope to 'all pods', got %q", s)
	}
}

func TestClassifyHead(t *testing.T) {
	cases := map[string]string{
		"<!DOCTYPE html><html>login form password": "login page",
		`{"status":500,"error":"Internal Server"}`: "JSON error",
		"HTTP/1.1 401 Unauthorized":                "HTTP error",
		"":                                         "empty",
	}
	for in, want := range cases {
		if got := classifyHead([]byte(in)); !strings.Contains(got, want) {
			t.Errorf("classifyHead(%q) = %q, want contains %q", in, got, want)
		}
	}
}
