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

// synthHeapBytes builds a minimal valid heap holding n byte[] arrays of arrLen
// bytes each — enough for the diff to see a class grow between two dumps.
func synthHeapBytes(n, arrLen int) []byte {
	var buf bytes.Buffer
	buf.WriteString("JAVA PROFILE 1.0.2\x00")
	binary.Write(&buf, binary.BigEndian, uint32(8))
	binary.Write(&buf, binary.BigEndian, uint64(0))
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
	var hd bytes.Buffer
	for i := 0; i < n; i++ {
		hd.WriteByte(0x23)             // PRIM_ARRAY_DUMP
		hd.Write(id(uint64(1000 + i))) // obj id
		hd.Write(u4(0))                // stack
		hd.Write(u4(uint32(arrLen)))   // n elements
		hd.WriteByte(8)                // type = byte
		hd.Write(make([]byte, arrLen)) // data
	}
	buf.WriteByte(0x0C)
	buf.Write(u4(0))
	buf.Write(u4(uint32(hd.Len())))
	buf.Write(hd.Bytes())
	return buf.Bytes()
}

// desyncedHprof builds a dump whose INSTANCE_DUMP claims far more field bytes
// than the heap segment actually contains — the classic "we lost sync with the
// record stream" shape. The parser must catch it, not accumulate garbage.
func desyncedHprof() []byte {
	var buf bytes.Buffer
	buf.WriteString("JAVA PROFILE 1.0.2\x00")
	binary.Write(&buf, binary.BigEndian, uint32(8))
	binary.Write(&buf, binary.BigEndian, uint64(0))
	id := func(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
	u4 := func(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
	var hd bytes.Buffer
	hd.WriteByte(0x21)   // INSTANCE_DUMP
	hd.Write(id(200))    // obj id
	hd.Write(u4(0))      // stack
	hd.Write(id(100))    // class obj id
	hd.Write(u4(999999)) // nbytes — but no payload follows and the segment ends here
	buf.WriteByte(0x0C)  // HEAP_DUMP
	buf.Write(u4(0))     // time delta
	buf.Write(u4(uint32(hd.Len())))
	buf.Write(hd.Bytes())
	return buf.Bytes()
}

// KS-A: a desynced parse must be caught and DISCLOSED, never rendered as a
// confident histogram.
func TestHprofDesyncIsFlaggedNotSilent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "desync.hprof")
	if err := os.WriteFile(p, desyncedHprof(), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := analyzeHprof(p)
	if err != nil {
		t.Fatalf("a desync should yield a partial result, not a hard error: %v", err)
	}
	if !h.desynced {
		t.Fatal("a record overrunning its segment must set desynced")
	}
	out := renderHistogram(h, 10)
	if !strings.Contains(out, "doesn't fully recognize") || !strings.Contains(out, "PARTIAL") {
		t.Fatalf("a desynced parse must be disclosed loudly:\n%s", out)
	}
}

// shallow sizing: header (8 mark + idSize klass) + fields, 8-aligned.
func TestObjShallowSizing(t *testing.T) {
	cases := []struct {
		fields, idSize, want int64
	}{
		{16, 8, 32},   // 8+8+16 = 32
		{16, 4, 32},   // 8+4+16 = 28 → align8 → 32
		{20, 4, 32},   // 8+4+20 = 32
		{0, 4, 16},    // 8+4+0 = 12 → align8 → 16
		{100, 8, 120}, // 8+8+100 = 116 → 120
	}
	for _, c := range cases {
		if got := objShallow(c.fields, c.idSize); got != c.want {
			t.Errorf("objShallow(%d,%d)=%d want %d", c.fields, c.idSize, got, c.want)
		}
	}
}

func TestHeapDiffGrowth(t *testing.T) {
	dir := t.TempDir()
	before := filepath.Join(dir, "before.hprof")
	after := filepath.Join(dir, "after.hprof")
	// after has 5 byte[] arrays; before has 1 → byte[] grew by 4.
	if err := os.WriteFile(before, synthHeapBytes(1, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(after, synthHeapBytes(5, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := analyzeHprofDiff(before, after)
	if err != nil {
		t.Fatalf("diff failed: %v", err)
	}
	for _, want := range []string{"grew most", "byte[]", "+4", "GROWTH"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q\n%s", want, out)
		}
	}
	// The reverse diff (shrink) must NOT report growth of byte[].
	rev, err := analyzeHprofDiff(after, before)
	if err != nil {
		t.Fatalf("reverse diff failed: %v", err)
	}
	if !strings.Contains(rev, "shrank most") || !strings.Contains(rev, "nothing grew") {
		t.Errorf("reverse diff should show a shrink and no growth:\n%s", rev)
	}
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

// KS-2: a truncated large-heap histogram must say so honestly — the bytes it
// actually read vs the dump size, and "open MAT for the truth" — never the old
// falsely-reassuring "sampled … proportions still indicative".
func TestHeapTruncationDisclosureIsHonest(t *testing.T) {
	h := &heapHistogram{truncated: true, fileBytes: 38 << 30, walkedBytes: analyzeHprofLimit,
		totalBytes: 100, totalObjs: 5, classes: []classStat{{"byte[]", 2, 100}}}
	out := renderHistogram(h, 10)
	for _, want := range []string{"partial", "NOT the whole heap", "Eclipse MAT", "of a "} {
		if !strings.Contains(out, want) {
			t.Errorf("truncation disclosure missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"indicative", "sampled"} {
		if strings.Contains(out, bad) {
			t.Errorf("disclosure must not falsely reassure with %q:\n%s", bad, out)
		}
	}
	// a complete dump must NOT show the partial warning
	if full := renderHistogram(&heapHistogram{totalBytes: 100, totalObjs: 5,
		classes: []classStat{{"byte[]", 2, 100}}}, 10); strings.Contains(full, "partial") {
		t.Error("a complete dump must not warn about truncation")
	}
}

func TestHistogramRichSections(t *testing.T) {
	// a heap where ONE object dominates → biggest-objects + "ONE object dominates"
	h := &heapHistogram{
		totalBytes: 100 << 20, totalObjs: 1000,
		classes: []classStat{
			{"byte[]", 100, 40 << 20},
			{"com.example.Cache", 50, 5 << 20},
			{"java.lang.String", 200, 3 << 20},
		},
		biggest:  []objSize{{"byte[]", 40 << 20, "40M elems"}},
		dupWaste: 20 << 20, dupGroups: 5000,
	}
	out := renderHistogram(h, 15)
	for _, want := range []string{
		"biggest single objects", "40M elems",
		"duplicate small char[]/byte[]",
		"your app's classes", "com.example.Cache",
		"verdict:", "ONE object dominates",
		"RETAINED size", // still points at MAT for the deep stuff
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rich histogram missing %q:\n%s", want, out)
		}
	}
	// a framework-only heap → the "no app classes" note + baseline verdict
	h2 := &heapHistogram{totalBytes: 10 << 20, classes: []classStat{{"java.lang.String", 100, 2 << 20}}}
	if out2 := renderHistogram(h2, 15); !strings.Contains(out2, "no application classes stand out") {
		t.Errorf("framework-only heap must note no app classes:\n%s", out2)
	}
}

func TestIsFrameworkClass(t *testing.T) {
	for _, n := range []string{"java.lang.String", "org.springframework.X", "byte[]", "jdk.internal.Y"} {
		if !isFrameworkClass(n) {
			t.Errorf("%q should be framework/JDK", n)
		}
	}
	for _, n := range []string{"com.example.Foo", "io.lettuce.core.Bar", "oracle.jdbc.Driver"} {
		if isFrameworkClass(n) {
			t.Errorf("%q should count as an app/dependency class", n)
		}
	}
}

// TestDominatorsAndRetained checks the CHK dominator + retained-size math on a
// hand-built graph: root→1, root→2, 1→3, 2→3, 3→4.
func TestDominatorsAndRetained(t *testing.T) {
	g := &heapGraph{
		shallow: []int64{0, 10, 20, 30, 40, 99}, // node 5 is unreachable garbage
		name:    []int32{0, 0, 0, 0, 0, 0}, names: []string{"x"},
		succ: [][]int32{{1, 2}, {3}, {3}, {4}, {}, {}},
		pred: [][]int32{{}, {0}, {0}, {1, 2}, {3}, {}},
	}
	idom, _, order := g.dominators()
	for v, w := range map[int32]int32{1: 0, 2: 0, 3: 0, 4: 3} {
		if idom[v] != w {
			t.Errorf("idom[%d] = %d, want %d", v, idom[v], w)
		}
	}
	if idom[5] != -1 {
		t.Errorf("unreachable node must have idom -1, got %d", idom[5])
	}
	ret := g.retainedSizes(idom, order)
	if ret[3] != 70 { // 3 dominates 4: 30 + 40
		t.Errorf("retained[3] = %d, want 70", ret[3])
	}
	if ret[1] != 10 { // 1 does NOT dominate 3 (reachable via 2 too)
		t.Errorf("retained[1] = %d, want 10", ret[1])
	}
	if ret[0] != 100 { // root retains everything reachable
		t.Errorf("retained[root] = %d, want 100", ret[0])
	}
}

// synthDeepHprof: a rooted instance holding a byte[], to exercise the whole deep
// pipeline (class layout → ref field → dominators → retained size).
func synthDeepHprof() []byte {
	var b bytes.Buffer
	b.WriteString("JAVA PROFILE 1.0.2\x00")
	binary.Write(&b, binary.BigEndian, uint32(8))
	binary.Write(&b, binary.BigEndian, uint64(0))
	id := func(v uint64) []byte { x := make([]byte, 8); binary.BigEndian.PutUint64(x, v); return x }
	u4 := func(v uint32) []byte { x := make([]byte, 4); binary.BigEndian.PutUint32(x, v); return x }
	u2 := func(v uint16) []byte { x := make([]byte, 2); binary.BigEndian.PutUint16(x, v); return x }
	rec := func(tag byte, body []byte) {
		b.WriteByte(tag)
		b.Write(u4(0))
		b.Write(u4(uint32(len(body))))
		b.Write(body)
	}
	rec(0x01, append(id(1), []byte("Holder")...)) // STRING id=1
	var lc bytes.Buffer                           // LOAD_CLASS: serial, classObj=100, stack, name=1
	lc.Write(u4(1))
	lc.Write(id(100))
	lc.Write(u4(0))
	lc.Write(id(1))
	rec(0x02, lc.Bytes())

	var hd bytes.Buffer
	// CLASS_DUMP for Holder(100): 1 instance field of type object(2)
	hd.WriteByte(0x20)
	hd.Write(id(100))
	hd.Write(u4(0))
	for i := 0; i < 6; i++ {
		hd.Write(id(0)) // super, loader, signers, protdomain, res1, res2
	}
	hd.Write(u4(8)) // instance size
	hd.Write(u2(0)) // constant pool
	hd.Write(u2(0)) // static fields
	hd.Write(u2(1)) // instance fields
	hd.Write(id(1)) // field name id
	hd.WriteByte(2) // type = object
	// PRIM_ARRAY byte[] id=300, 1000 bytes — the retained payload
	hd.WriteByte(0x23)
	hd.Write(id(300))
	hd.Write(u4(0))
	hd.Write(u4(1000))
	hd.WriteByte(8)
	hd.Write(make([]byte, 1000))
	// INSTANCE Holder id=200, field → 300
	hd.WriteByte(0x21)
	hd.Write(id(200))
	hd.Write(u4(0))
	hd.Write(id(100))
	hd.Write(u4(8))
	hd.Write(id(300))
	// ROOT_UNKNOWN → Holder(200)
	hd.WriteByte(0xFF)
	hd.Write(id(200))
	rec(0x0C, hd.Bytes())
	return b.Bytes()
}

func TestDeepRetainedEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep.hprof")
	if err := os.WriteFile(path, synthDeepHprof(), 0o644); err != nil {
		t.Fatal(err)
	}
	g, _, err := buildHeapGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	idom, _, order := g.dominators()
	ret := g.retainedSizes(idom, order)
	var holder, arr int32 = -1, -1
	for i := range g.shallow {
		switch g.names[g.name[i]] {
		case "Holder":
			holder = int32(i)
		case "byte[]":
			arr = int32(i)
		}
	}
	if holder < 0 || arr < 0 {
		t.Fatalf("expected Holder + byte[] nodes, got names %v", g.names)
	}
	// Holder is rooted and holds the byte[] → it retains itself + the array
	if ret[holder] != g.shallow[holder]+g.shallow[arr] {
		t.Fatalf("Holder retained = %d, want %d (self %d + array %d)",
			ret[holder], g.shallow[holder]+g.shallow[arr], g.shallow[holder], g.shallow[arr])
	}
	if idom[arr] != holder {
		t.Fatalf("the byte[] must be dominated by Holder, got idom %d", idom[arr])
	}
}

func TestPathToGCRoots(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep.hprof")
	if err := os.WriteFile(path, synthDeepHprof(), 0o644); err != nil {
		t.Fatal(err)
	}
	g, p, err := buildHeapGraph(path)
	if err != nil {
		t.Fatal(err)
	}
	var arr int32 = -1
	for i := range g.shallow {
		if g.names[g.name[i]] == "byte[]" {
			arr = int32(i)
		}
	}
	if arr < 0 {
		t.Fatal("no byte[] node")
	}
	// shortest path from a GC root: root → Holder → byte[]
	path2 := pathFromRoot(arr, g.bfsParents())
	if len(path2) != 3 || path2[0] != 0 || g.names[g.name[path2[1]]] != "Holder" {
		t.Fatalf("expected root→Holder→byte[], got %v", path2)
	}
	// the field-name label on the Holder→byte[] edge must be recovered
	labels := p.recoverPathLabels(path, map[[2]int32]bool{{path2[1], path2[2]}: true})
	if labels[[2]int32{path2[1], path2[2]}] == "" {
		t.Fatal("expected a field-name label on the Holder→byte[] edge")
	}
}
