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
	classes     []classStat
	biggest     []objSize // the largest individual objects (newest analysis)
	totalBytes  int64
	totalObjs   int64
	dupWaste    int64 // bytes wasted on duplicated small char[]/byte[] (≈ strings)
	dupGroups   int64 // how many distinct values are duplicated
	truncated   bool
	fileBytes   int64 // full dump size on disk (0 = unknown)
	walkedBytes int64 // how many bytes of records we actually walked
	desynced    bool  // a record ran past its segment → parse gave up (numbers partial)
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
	desynced    bool  // set when a length field runs past its segment (parse desync)
	fileBytes   int64 // total dump size on disk, to honestly frame a truncated walk
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

func analyzeHprof(path string) (hist *heapHistogram, err error) {
	// A corrupt dump (damaged record length, bit rot mid-kubectl-cp) must
	// produce a readable message, never a Go stack trace — a junior's most
	// likely artifact is a damaged capture, and "errors that teach" is the
	// whole product promise.
	defer func() {
		if rec := recover(); rec != nil {
			hist, err = nil, fmt.Errorf("corrupt heap dump (parser aborted: %v) — the file is damaged; recapture, or try Eclipse MAT", rec)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var fileBytes int64
	if fi, e := f.Stat(); e == nil {
		fileBytes = fi.Size()
	}
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
		fileBytes:   fileBytes,
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
			// a corrupt length (< idSize, or absurdly large) must not reach
			// make([]byte, negative) — that's a panic, not a diagnosis.
			if int64(length) < int64(p.idSize) || length > 1<<24 {
				if _, err := discard(r, int(length)); err != nil {
					return p.result(), nil
				}
				continue
			}
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
	h := &heapHistogram{truncated: p.truncated, desynced: p.desynced, fileBytes: p.fileBytes, walkedBytes: p.consumed}
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
			// desync tripwire: an instance can't carry more field bytes than the
			// segment has left. If it claims to, we've lost sync with the record
			// stream — bail honestly instead of accumulating garbage as "the heap".
			if int64(nbytes) > remaining {
				p.desynced = true
				return fmt.Errorf("desync: instance nbytes %d > %d left in segment", nbytes, remaining)
			}
			if err := skip(int(nbytes)); err != nil {
				return err
			}
			cs := p.statFor(classObjId)
			cs.count++
			// shallow size = object header (mark word 8 + klass ptr idSize) + field
			// bytes, rounded up to 8-byte alignment — matches how the JVM/MAT size
			// an object far better than the old flat idSize*2 header guess.
			b := objShallow(int64(nbytes), int64(id))
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
			if int64(n)*int64(id) > remaining { // desync tripwire (see INSTANCE_DUMP)
				p.desynced = true
				return fmt.Errorf("desync: obj-array of %d refs exceeds %d left in segment", n, remaining)
			}
			if err := skip(int(n) * id); err != nil {
				return err
			}
			name := p.className(arrClass)
			if name == "" {
				name = "java.lang.Object[]"
			}
			cs := p.arrayStat(name)
			cs.count++
			b := arrShallow(int64(n)*int64(id), int64(id))
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
			if sz == 0 || int64(n)*sz > remaining { // bad element type or desync
				p.desynced = true
				return fmt.Errorf("desync: prim-array (type=%d n=%d) exceeds %d left in segment", etype, n, remaining)
			}
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
			b := arrShallow(int64(n)*sz, int64(id))
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
			// an unrecognized sub-record tag means we're reading field/element
			// bytes as if they were a tag — i.e. desynced. Stop; don't invent data.
			p.desynced = true
			return fmt.Errorf("unknown heap sub-record 0x%02x (parse desync)", sub)
		}
		if err != nil {
			return err
		}
		if remaining < 0 { // any record that overran the segment → desync
			p.desynced = true
			return fmt.Errorf("desync: read %d bytes past the segment", -remaining)
		}
	}
	return nil
}

// objShallow / arrShallow size an object the way the JVM/MAT do: an 8-byte mark
// word + a klass pointer (idSize), plus field/element bytes, padded to 8-byte
// alignment. Arrays carry an extra 4-byte length. Far closer than a flat header
// guess, and identical across the common compressed-oops layouts.
func align8(n int64) int64 { return (n + 7) &^ 7 }
func objShallow(fieldBytes, idSize int64) int64 {
	return align8(8 + idSize + fieldBytes)
}
func arrShallow(elemBytes, idSize int64) int64 {
	return align8(8 + idSize + 4 + elemBytes)
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
	fmt.Fprintf(&b, "heap histogram (shallow size) — %s across %s objects (top consumers first)\n",
		fmtSize(h.totalBytes), humanCount(h.totalObjs))
	if h.desynced {
		// A wrong-but-confident histogram is the worst outcome. If the record
		// stream desynced (an unfamiliar hprof variant), say so loudly — these
		// numbers are partial and possibly skewed; MAT is the source of truth.
		b.WriteString("  ⚠ parse ended early — this hprof has a record shape jdebug doesn't fully recognize.\n")
		b.WriteString("    the numbers below are PARTIAL and may be skewed; trust Eclipse MAT for this dump.\n")
	}
	if h.truncated {
		// Be honest: this is the START of the file, not a random sample, and not
		// the whole heap. A leak living past the walked window is invisible here.
		scope := fmtSize(analyzeHprofLimit)
		if h.fileBytes > 0 {
			scope = fmt.Sprintf("%s of a %s dump", fmtSize(analyzeHprofLimit), fmtSize(h.fileBytes))
		}
		b.WriteString("  ⚠ partial: walked the first " + scope + " — a hint from the start of the file, NOT the whole heap.\n")
		b.WriteString("    a leak outside this window won't show; open the .hprof in Eclipse MAT for the real answer.\n")
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

	b.WriteString("\n" + heapVerdict(h))
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

func readU8(r *bufio.Reader) (uint64, error) {
	var b [8]byte
	if _, err := readFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b[:]), nil
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
