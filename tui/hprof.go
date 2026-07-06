package main

// hprof.go — a focused JVM heap-dump reader, dependency-free. In one streaming
// pass it produces more than a class histogram: the biggest INDIVIDUAL objects
// (a class total can hide one giant object vs many small — different bugs), a
// duplicate small char[]/byte[] estimate (≈ wasted duplicate Strings), your
// app's classes split from JDK/framework baseline, and a data-driven verdict.
// It still can't do RETAINED size / dominators / paths-to-GC-roots — those need
// the full object graph and are what Eclipse MAT is for, which it points at.
// Exposed as `jdebug-tui -analyze-heap <file>` for the CLI's analyze and `a`.
//
// Format: header ("JAVA PROFILE 1.0.x\0", u4 id-size, u8 time), then tagged
// records. We use STRING (0x01, names), LOAD_CLASS (0x02, classObjId→name),
// and HEAP_DUMP[_SEGMENT] (0x0C/0x1C), whose sub-records include
// INSTANCE_DUMP (0x21), OBJ_ARRAY_DUMP (0x22), PRIM_ARRAY_DUMP (0x23) — the
// rest (roots, the variable-length CLASS_DUMP 0x20) are skipped by size.

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
)

type classStat struct {
	name  string
	count int64
	bytes int64
}

// objSize is one individual object, tracked so we can surface the single
// biggest allocations — which the per-class histogram sums away.
type objSize struct {
	name  string
	bytes int64
	extra string // e.g. "1.0M elems"
}

type heapHistogram struct {
	classes    []classStat
	biggest    []objSize // the largest individual objects (newest analysis)
	totalBytes int64
	totalObjs  int64
	dupWaste   int64 // bytes wasted on duplicated small char[]/byte[] (≈ strings)
	dupGroups  int64 // how many distinct values are duplicated
	truncated  bool
}

var primArrayName = map[byte]string{
	4: "boolean[]", 5: "char[]", 6: "float[]", 7: "double[]",
	8: "byte[]", 9: "short[]", 10: "int[]", 11: "long[]",
}

// analyzeHprofLimit caps how much of a heap dump we walk so a multi-GB dump
// can't hang the UI; class-dominant leaks show up well within it.
const analyzeHprofLimit = 400 << 20

type dupStat struct {
	count int64
	size  int64
}

type hprofParser struct {
	idSize      int
	strs        map[uint64]string
	classNameOf map[uint64]uint64
	byClass     map[uint64]*classStat
	arrays      map[string]*classStat
	biggest     []objSize           // top-N individual objects by shallow size
	dup         map[uint64]*dupStat // content hash → occurrences (dup-string detect)
	consumed    int64
	truncated   bool
}

const (
	biggestN = 12   // how many "biggest single object" rows to keep
	dupCap   = 4096 // only hash small char[]/byte[] for duplicate detection
)

// noteObj keeps a running top-N of the largest individual objects, sorted
// ascending so biggest[0] is the smallest of the kept set (cheap to evict).
func (p *hprofParser) noteObj(name string, bytes int64, extra string) {
	if len(p.biggest) < biggestN {
		p.biggest = append(p.biggest, objSize{name, bytes, extra})
		if len(p.biggest) == biggestN {
			sort.Slice(p.biggest, func(i, j int) bool { return p.biggest[i].bytes < p.biggest[j].bytes })
		}
		return
	}
	if bytes <= p.biggest[0].bytes {
		return
	}
	p.biggest[0] = objSize{name, bytes, extra}
	sort.Slice(p.biggest, func(i, j int) bool { return p.biggest[i].bytes < p.biggest[j].bytes })
}

// noteDup records a small array's content by hash so duplicated values (the
// classic duplicate-String waste) can be estimated without a graph.
func (p *hprofParser) noteDup(content []byte, size int64) {
	h := uint64(1469598103934665603)
	for _, b := range content {
		h ^= uint64(b)
		h *= 1099511628211
	}
	h ^= uint64(size) // fold length in to cut collisions across sizes
	ds := p.dup[h]
	if ds == nil {
		ds = &dupStat{size: size}
		p.dup[h] = ds
	}
	ds.count++
}

// className resolves a class-object id to a readable name (fallback "").
func (p *hprofParser) className(classObjId uint64) string {
	if sid, ok := p.classNameOf[classObjId]; ok {
		if n, ok := p.strs[sid]; ok {
			return javaClassName(n)
		}
	}
	return ""
}

func analyzeHprof(path string) (*heapHistogram, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)

	ver, err := readCString(r)
	if err != nil || !strings.HasPrefix(ver, "JAVA PROFILE") {
		return nil, fmt.Errorf("not an hprof file (bad header)")
	}
	idSize32, err := readU4(r)
	if err != nil || (idSize32 != 4 && idSize32 != 8) {
		return nil, fmt.Errorf("unsupported hprof id size")
	}
	if _, err := discard(r, 8); err != nil { // timestamp
		return nil, err
	}

	p := &hprofParser{
		idSize:      int(idSize32),
		strs:        map[uint64]string{},
		classNameOf: map[uint64]uint64{},
		byClass:     map[uint64]*classStat{},
		arrays:      map[string]*classStat{},
		dup:         map[uint64]*dupStat{},
	}

	for {
		tag, err := r.ReadByte()
		if err != nil {
			break // EOF
		}
		if _, err := discard(r, 4); err != nil { // time delta
			break
		}
		length, err := readU4(r)
		if err != nil {
			break
		}
		p.consumed += int64(length)
		if p.consumed > analyzeHprofLimit {
			p.truncated = true
			break
		}
		switch tag {
		case 0x01: // STRING
			id, err := p.readID(r)
			if err != nil {
				return p.result(), nil
			}
			buf := make([]byte, int(length)-p.idSize)
			if _, err := readFull(r, buf); err != nil {
				return p.result(), nil
			}
			p.strs[id] = string(buf)
		case 0x02: // LOAD_CLASS: serial, classObjId, stackSerial, nameStringId
			if _, err := readU4(r); err != nil {
				return p.result(), nil
			}
			classObjId, err := p.readID(r)
			if err != nil {
				return p.result(), nil
			}
			if _, err := readU4(r); err != nil {
				return p.result(), nil
			}
			nameId, err := p.readID(r)
			if err != nil {
				return p.result(), nil
			}
			p.classNameOf[classObjId] = nameId
		case 0x0C, 0x1C: // HEAP_DUMP / _SEGMENT
			if err := p.walkHeap(r, int64(length)); err != nil {
				return p.result(), nil // stop cleanly on a parse snag
			}
		default:
			if _, err := discard(r, int(length)); err != nil {
				return p.result(), nil
			}
		}
	}
	return p.result(), nil
}

func (p *hprofParser) readID(r *bufio.Reader) (uint64, error) { return readIDN(r, p.idSize) }

func (p *hprofParser) statFor(classObjId uint64) *classStat {
	cs := p.byClass[classObjId]
	if cs == nil {
		name := "unknown"
		if sid, ok := p.classNameOf[classObjId]; ok {
			if n, ok := p.strs[sid]; ok {
				name = javaClassName(n)
			}
		}
		cs = &classStat{name: name}
		p.byClass[classObjId] = cs
	}
	return cs
}

func (p *hprofParser) arrayStat(name string) *classStat {
	if name == "" {
		name = "array"
	}
	cs := p.arrays[name]
	if cs == nil {
		cs = &classStat{name: name}
		p.arrays[name] = cs
	}
	return cs
}

func (p *hprofParser) result() *heapHistogram {
	h := &heapHistogram{truncated: p.truncated}
	add := func(cs *classStat) {
		if cs.count == 0 {
			return
		}
		h.classes = append(h.classes, *cs)
		h.totalObjs += cs.count
		h.totalBytes += cs.bytes
	}
	for _, cs := range p.byClass {
		add(cs)
	}
	for _, cs := range p.arrays {
		add(cs)
	}
	sort.Slice(h.classes, func(i, j int) bool { return h.classes[i].bytes > h.classes[j].bytes })

	// biggest individual objects, largest first
	h.biggest = append(h.biggest, p.biggest...)
	sort.Slice(h.biggest, func(i, j int) bool { return h.biggest[i].bytes > h.biggest[j].bytes })

	// duplicate small char[]/byte[] → wasted bytes ≈ (occurrences-1) * size
	for _, ds := range p.dup {
		if ds.count > 1 {
			h.dupWaste += (ds.count - 1) * ds.size
			h.dupGroups++
		}
	}
	return h
}

// isFrameworkClass reports whether a class name is JDK/runtime/framework noise
// (so "your app's classes" can be surfaced separately from Spring/JDK baseline).
func isFrameworkClass(n string) bool {
	for _, pre := range []string{
		"java.", "javax.", "jakarta.", "jdk.", "sun.", "com.sun.", "kotlin.", "scala.",
		"org.springframework.", "org.apache.", "org.aspectj.", "org.hibernate.",
		"org.slf4j.", "ch.qos.", "io.micrometer.", "io.netty.", "com.fasterxml.",
		"[", "byte[]", "char[]", "int[]", "long[]", "short[]", "float[]", "double[]",
		"boolean[]", "java.lang.Object[]",
	} {
		if strings.HasPrefix(n, pre) {
			return true
		}
	}
	return false
}

// walkHeap consumes EVERY sub-record in a heap-dump segment, counting objects
// and arrays and skipping roots + the variable-length CLASS_DUMP by size
// (CLASS_DUMPs are interleaved before instances — mis-skip one and the
// histogram is empty).
func (p *hprofParser) walkHeap(r *bufio.Reader, length int64) error {
	remaining := length
	skip := func(n int) error {
		if n < 0 {
			return fmt.Errorf("negative skip")
		}
		if _, err := discard(r, n); err != nil {
			return err
		}
		remaining -= int64(n)
		return nil
	}
	id := p.idSize
	for remaining > 0 {
		sub, err := r.ReadByte()
		if err != nil {
			return err
		}
		remaining--
		switch sub {
		case 0x21: // INSTANCE_DUMP: id, u4 stack, id class, u4 nbytes, [bytes]
			if err := skip(id + 4); err != nil {
				return err
			}
			classObjId, err := p.readID(r)
			if err != nil {
				return err
			}
			remaining -= int64(id)
			nbytes, err := readU4(r)
			if err != nil {
				return err
			}
			remaining -= 4
			if err := skip(int(nbytes)); err != nil {
				return err
			}
			cs := p.statFor(classObjId)
			cs.count++
			b := int64(nbytes) + int64(id)*2 // header estimate
			cs.bytes += b
			p.noteObj(cs.name, b, "")
		case 0x22: // OBJ_ARRAY_DUMP: id, u4 stack, u4 n, id arrClass, [n ids]
			if err := skip(id + 4); err != nil {
				return err
			}
			n, err := readU4(r)
			if err != nil {
				return err
			}
			remaining -= 4
			arrClass, err := p.readID(r) // the real element type, not just Object[]
			if err != nil {
				return err
			}
			remaining -= int64(id)
			if err := skip(int(n) * id); err != nil {
				return err
			}
			name := p.className(arrClass)
			if name == "" {
				name = "java.lang.Object[]"
			}
			cs := p.arrayStat(name)
			cs.count++
			b := int64(n)*int64(id) + 16
			cs.bytes += b
			p.noteObj(name, b, humanCount(int64(n))+" refs")
		case 0x23: // PRIM_ARRAY_DUMP: id, u4 stack, u4 n, u1 type, [n*sz]
			if err := skip(id + 4); err != nil {
				return err
			}
			n, err := readU4(r)
			if err != nil {
				return err
			}
			remaining -= 4
			etype, err := r.ReadByte()
			if err != nil {
				return err
			}
			remaining--
			sz := int64(basicTypeSize(etype, id))
			content := int(int64(n) * sz)
			// duplicate detection: hash small char[]/byte[] (≈ String backing);
			// everything else (and big arrays) is skipped, cost-free
			if (etype == 5 || etype == 8) && content > 0 && content <= dupCap {
				dbuf := make([]byte, content)
				if _, err := readFull(r, dbuf); err != nil {
					return err
				}
				remaining -= int64(content)
				p.noteDup(dbuf, int64(content)+16)
			} else if err := skip(content); err != nil {
				return err
			}
			cs := p.arrayStat(primArrayName[etype])
			cs.count++
			b := int64(n)*sz + 16
			cs.bytes += b
			p.noteObj(primArrayName[etype], b, humanCount(int64(n))+" elems")
		case 0x20: // CLASS_DUMP — variable length
			if err := p.skipClassDump(r, &remaining); err != nil {
				return err
			}
		case 0xFF, 0x05, 0x07: // ROOT_UNKNOWN / STICKY_CLASS / MONITOR_USED: id
			err = skip(id)
		case 0x01: // ROOT_JNI_GLOBAL: id, id
			err = skip(id * 2)
		case 0x02, 0x03, 0x08: // JNI_LOCAL / JAVA_FRAME / THREAD_OBJECT: id, u4, u4
			err = skip(id + 8)
		case 0x04, 0x06: // NATIVE_STACK / THREAD_BLOCK: id, u4
			err = skip(id + 4)
		default:
			return fmt.Errorf("unknown heap sub-record 0x%02x", sub)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *hprofParser) skipClassDump(r *bufio.Reader, remaining *int64) error {
	id := p.idSize
	skip := func(n int) error {
		if _, err := discard(r, n); err != nil {
			return err
		}
		*remaining -= int64(n)
		return nil
	}
	u2 := func() (int, error) {
		var b [2]byte
		if _, err := readFull(r, b[:]); err != nil {
			return 0, err
		}
		*remaining -= 2
		return int(uint16(b[0])<<8 | uint16(b[1])), nil
	}
	// id class, u4 stack, 6× id, u4 instance size
	if err := skip(id + 4 + 6*id + 4); err != nil {
		return err
	}
	cp, err := u2() // constant pool: u2 idx, u1 type, value
	if err != nil {
		return err
	}
	for i := 0; i < cp; i++ {
		if err := skip(2); err != nil {
			return err
		}
		t, err := r.ReadByte()
		if err != nil {
			return err
		}
		*remaining--
		if err := skip(basicTypeSize(t, id)); err != nil {
			return err
		}
	}
	sf, err := u2() // static fields: id name, u1 type, value
	if err != nil {
		return err
	}
	for i := 0; i < sf; i++ {
		if err := skip(id); err != nil {
			return err
		}
		t, err := r.ReadByte()
		if err != nil {
			return err
		}
		*remaining--
		if err := skip(basicTypeSize(t, id)); err != nil {
			return err
		}
	}
	inf, err := u2() // instance fields: id name, u1 type
	if err != nil {
		return err
	}
	return skip(inf * (id + 1))
}

// basicTypeSize is the on-disk size of an HPROF basic-type value.
func basicTypeSize(t byte, idSize int) int {
	switch t {
	case 2:
		return idSize
	case 4, 8: // boolean, byte
		return 1
	case 5, 9: // char, short
		return 2
	case 6, 10: // float, int
		return 4
	case 7, 11: // double, long
		return 8
	}
	return 0
}

// javaClassName turns "[Ljava/lang/String;" / "java/lang/String" readable.
func javaClassName(n string) string {
	switch {
	case strings.HasPrefix(n, "[["):
		return javaClassName(n[1:]) + "[]"
	case strings.HasPrefix(n, "[L"):
		return strings.ReplaceAll(strings.TrimSuffix(n[2:], ";"), "/", ".") + "[]"
	case strings.HasPrefix(n, "[B"):
		return "byte[]"
	case strings.HasPrefix(n, "[C"):
		return "char[]"
	case strings.HasPrefix(n, "[I"):
		return "int[]"
	}
	return strings.ReplaceAll(n, "/", ".")
}

// renderHistogram formats the top consumers for the analyze output.
func renderHistogram(h *heapHistogram, top int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "heap histogram — %s across %s objects (top consumers first)\n",
		fmtSize(h.totalBytes), humanCount(h.totalObjs))
	if h.truncated {
		b.WriteString("  (large dump — sampled the first portion; proportions still indicative)\n")
	}
	cls := h.classes
	if len(cls) > top {
		cls = cls[:top]
	}
	for _, cs := range cls {
		pct := 0.0
		if h.totalBytes > 0 {
			pct = float64(cs.bytes) * 100 / float64(h.totalBytes)
		}
		fmt.Fprintf(&b, "  %5.1f%%  %9s  %10s  %s\n", pct, fmtSize(cs.bytes), humanCount(cs.count), cs.name)
	}

	// biggest single objects — the histogram sums by class, hiding whether a
	// class's bytes are one giant object or many small ones (very different bugs)
	if len(h.biggest) > 0 && h.biggest[0].bytes > 64<<10 {
		b.WriteString("\nbiggest single objects (one class total can hide one huge object OR many small):\n")
		n := len(h.biggest)
		if n > 6 {
			n = 6
		}
		for _, o := range h.biggest[:n] {
			extra := ""
			if o.extra != "" {
				extra = "  (" + o.extra + ")"
			}
			fmt.Fprintf(&b, "  %9s  %s%s\n", fmtSize(o.bytes), o.name, extra)
		}
	}

	// duplicate small strings/arrays — a classic, real waste MAT also reports
	if h.dupWaste > 256<<10 {
		fmt.Fprintf(&b, "\nduplicate small char[]/byte[]: ~%s wasted across %s repeated value(s)\n",
			fmtSize(h.dupWaste), humanCount(h.dupGroups))
		b.WriteString("  → likely duplicate Strings — intern hot values or cache them once.\n")
	}

	// your app's own classes, split from JDK/framework baseline
	var app []classStat
	for _, cs := range h.classes {
		if !isFrameworkClass(cs.name) {
			app = append(app, cs)
			if len(app) == 5 {
				break
			}
		}
	}
	if len(app) > 0 {
		b.WriteString("\nyour app's classes (excluding JDK/Spring/framework):\n")
		for _, cs := range app {
			fmt.Fprintf(&b, "  %9s  %10s  %s\n", fmtSize(cs.bytes), humanCount(cs.count), cs.name)
		}
	} else {
		b.WriteString("\nno application classes stand out — the heap is dominated by JDK/framework types.\n")
	}

	b.WriteString("\n" + heapVerdict(h) + "\n")
	b.WriteString("shallow-size first pass. For RETAINED size (what each object actually KEEPS alive):\n")
	b.WriteString("run  jdebug analyze --deep <heap>  — or Eclipse MAT for reference chains (paths to GC roots).")
	return b.String()
}

// heapVerdict turns the numbers into one plain-language read of the heap's shape.
func heapVerdict(h *heapHistogram) string {
	if h.totalBytes == 0 {
		return "verdict: the heap is essentially empty."
	}
	topPct, topName := 0.0, ""
	if len(h.classes) > 0 {
		topName = h.classes[0].name
		topPct = float64(h.classes[0].bytes) * 100 / float64(h.totalBytes)
	}
	bigPct := 0.0
	if len(h.biggest) > 0 {
		bigPct = float64(h.biggest[0].bytes) * 100 / float64(h.totalBytes)
	}
	dupPct := float64(h.dupWaste) * 100 / float64(h.totalBytes)
	switch {
	case bigPct >= 15:
		return fmt.Sprintf("verdict: ONE object dominates — a %s at %.0f%% of the heap. That's a single big\n"+
			"  buffer/collection, not scattered growth. In MAT, look at what holds that one object.", h.biggest[0].name, bigPct)
	case dupPct >= 10:
		return fmt.Sprintf("verdict: string-heavy — ~%.0f%% of the heap is DUPLICATE small arrays (likely\n"+
			"  duplicate Strings). Interning/caching hot values would reclaim most of it.", dupPct)
	case topPct >= 40:
		return fmt.Sprintf("verdict: %s holds %.0f%% of the heap — chase what keeps that many alive (a growing\n"+
			"  cache or collection?) in MAT's dominator tree.", topName, topPct)
	default:
		return "verdict: no single class or object runs away — this looks like baseline framework/JDK\n" +
			"  footprint, not an obvious leak. If memory still climbs, take a SECOND dump under load and diff."
	}
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// --- binary readers -----------------------------------------------------------

func readCString(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		c, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if c == 0 {
			return b.String(), nil
		}
		b.WriteByte(c)
	}
}

func readU4(r *bufio.Reader) (uint32, error) {
	var b [4]byte
	if _, err := readFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

func readIDN(r *bufio.Reader, idSize int) (uint64, error) {
	var b [8]byte
	if _, err := readFull(r, b[:idSize]); err != nil {
		return 0, err
	}
	if idSize == 4 {
		return uint64(binary.BigEndian.Uint32(b[:4])), nil
	}
	return binary.BigEndian.Uint64(b[:8]), nil
}

func readFull(r *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := r.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func discard(r *bufio.Reader, n int) (int, error) { return r.Discard(n) }
