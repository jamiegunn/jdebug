package main

// hprof_deep.go — the deep pass: build the object reference graph, compute a
// dominator tree, and from it the RETAINED size of each object (how much memory
// would be freed if it went away). Retained size + the dominator tree is what
// actually names a leak — "one object keeps 300 MB alive" — which the shallow
// histogram can't. This is the expensive pass (holds the whole graph in memory),
// so it's opt-in: `jdebug-tui -analyze-heap -deep <file>` / `jdebug analyze --deep`.
//
// Two file passes: (1) collect class layouts + assign every instance/array a
// node with its shallow size + GC roots; (2) parse instance/object-array bodies
// for references → edges. Then Cooper-Harvey-Kennedy dominators (simple, correct
// on cyclic graphs) and a bottom-up retained-size sum.

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// object graphs bigger than this won't fit comfortably in memory — bail to MAT
// rather than risk an OOM on the operator's laptop.
const maxDeepObjects = 8_000_000

type finfo struct {
	name string
	typ  byte
}

type classLayout struct {
	superID uint64
	fnameID []uint64 // own instance-field name ids (parallel to ftype)
	ftype   []byte   // own instance-field type codes, in HPROF order
	chain   []finfo  // own + all superclass fields (names resolved after pass 1)
}

type heapGraph struct {
	shallow []int64 // node 0 is a synthetic super-root (shallow 0)
	name    []int32 // class-name index per node
	names   []string
	succ    [][]int32
	pred    [][]int32
}

// --- build -------------------------------------------------------------------

func buildHeapGraph(path string) (*heapGraph, *deepParser, error) {
	// pass 1: classes, nodes (shallow + name), roots
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	p := &deepParser{
		idOf: map[uint64]int32{}, layouts: map[uint64]*classLayout{},
		strs: map[uint64]string{}, classNameID: map[uint64]uint64{},
		nameIdx: map[string]int32{},
	}
	p.g = &heapGraph{names: []string{"<GC roots>"}, shallow: []int64{0}, name: []int32{0}}
	p.nameIdx["<GC roots>"] = 0
	if err := p.scan(path, 1); err != nil {
		return nil, nil, err
	}
	if len(p.g.shallow) > maxDeepObjects {
		return nil, nil, fmt.Errorf("heap too large for the in-memory deep pass (%s objects) — use Eclipse MAT", humanCount(int64(len(p.g.shallow))))
	}
	// finalize field chains (own + supers) for instance parsing
	for id, cl := range p.layouts {
		cl.chain = p.fieldChain(id, map[uint64]bool{})
	}
	// pass 2: edges
	p.g.succ = make([][]int32, len(p.g.shallow))
	p.g.pred = make([][]int32, len(p.g.shallow))
	if err := p.scan(path, 2); err != nil {
		return nil, nil, err
	}
	// resolve GC-root ids → nodes and wire them as edges from the synthetic
	// root (node 0), de-duped
	seen := map[int32]bool{}
	for _, rid := range p.rootRaw {
		if ix, ok := p.idOf[rid]; ok && !seen[ix] {
			seen[ix] = true
			p.addEdge(0, ix)
		}
	}
	return p.g, p, nil
}

func (p *deepParser) fieldChain(classID uint64, guard map[uint64]bool) []finfo {
	cl := p.layouts[classID]
	if cl == nil || guard[classID] {
		return nil
	}
	guard[classID] = true
	out := make([]finfo, 0, len(cl.ftype))
	for i, t := range cl.ftype {
		name := ""
		if s, ok := p.strs[cl.fnameID[i]]; ok {
			name = s
		}
		out = append(out, finfo{name: name, typ: t})
	}
	if cl.superID != 0 {
		out = append(out, p.fieldChain(cl.superID, guard)...)
	}
	return out
}

type deepParser struct {
	idSize      int
	g           *heapGraph
	idOf        map[uint64]int32
	layouts     map[uint64]*classLayout
	strs        map[uint64]string
	classNameID map[uint64]uint64
	nameIdx     map[string]int32
	rootRaw     []uint64 // GC-root object ids, resolved to nodes after pass 1
	// pass 3 (path-label recovery): which edges we need field names for
	wantFrom map[int32]map[int32]bool
	labels   map[[2]int32]string
}

func (p *deepParser) nameIndex(n string) int32 {
	if i, ok := p.nameIdx[n]; ok {
		return i
	}
	i := int32(len(p.g.names))
	p.g.names = append(p.g.names, n)
	p.nameIdx[n] = i
	return i
}

func (p *deepParser) classDisplayName(classID uint64) string {
	if sid, ok := p.classNameID[classID]; ok {
		if s, ok := p.strs[sid]; ok {
			return javaClassName(s)
		}
	}
	return "unknown"
}

// node returns the index for an object id, creating it on first sight (pass 1).
func (p *deepParser) node(id uint64, shallow int64, name string) int32 {
	if ix, ok := p.idOf[id]; ok {
		return ix
	}
	ix := int32(len(p.g.shallow))
	p.idOf[id] = ix
	p.g.shallow = append(p.g.shallow, shallow)
	p.g.name = append(p.g.name, p.nameIndex(name))
	return ix
}

func (p *deepParser) addEdge(from, to int32) {
	p.g.succ[from] = append(p.g.succ[from], to)
	p.g.pred[to] = append(p.g.pred[to], from)
}

func (p *deepParser) scan(path string, pass int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	ver, err := readCString(r)
	if err != nil || !strings.HasPrefix(ver, "JAVA PROFILE") {
		return fmt.Errorf("not an hprof file")
	}
	id32, err := readU4(r)
	if err != nil {
		return err
	}
	p.idSize = int(id32)
	if _, err := discard(r, 8); err != nil {
		return err
	}
	for {
		tag, err := r.ReadByte()
		if err != nil {
			break
		}
		if _, err := discard(r, 4); err != nil {
			break
		}
		length, err := readU4(r)
		if err != nil {
			break
		}
		switch {
		case pass == 1 && tag == 0x01: // STRING
			id, _ := readIDN(r, p.idSize)
			buf := make([]byte, int(length)-p.idSize)
			readFull(r, buf)
			p.strs[id] = string(buf)
		case pass == 1 && tag == 0x02: // LOAD_CLASS
			readU4(r)
			cid, _ := readIDN(r, p.idSize)
			readU4(r)
			nid, _ := readIDN(r, p.idSize)
			p.classNameID[cid] = nid
		case tag == 0x0C || tag == 0x1C: // HEAP_DUMP[_SEGMENT]
			if err := p.walk(r, int64(length), pass); err != nil {
				return nil // stop cleanly on a snag; partial graph is still useful
			}
		default:
			if _, err := discard(r, int(length)); err != nil {
				return nil
			}
		}
	}
	return nil
}

func (p *deepParser) walk(r *bufio.Reader, length int64, pass int) error {
	id := p.idSize
	rem := length
	skip := func(n int) error {
		if n < 0 {
			return fmt.Errorf("neg skip")
		}
		if _, err := discard(r, n); err != nil {
			return err
		}
		rem -= int64(n)
		return nil
	}
	rID := func() (uint64, error) { v, e := readIDN(r, id); rem -= int64(id); return v, e }
	rU4 := func() (uint32, error) { v, e := readU4(r); rem -= 4; return v, e }
	rootIDs := func(extra int) error { // a root record: first field is the root object id
		v, e := rID()
		if e != nil {
			return e
		}
		if pass == 1 {
			p.rootRaw = append(p.rootRaw, v) // resolved to a node after pass 1
		}
		return skip(extra)
	}
	for rem > 0 {
		sub, err := r.ReadByte()
		if err != nil {
			return err
		}
		rem--
		switch sub {
		case 0x21: // INSTANCE_DUMP: id, u4 stack, id class, u4 nbytes, [body]
			oid, e := rID()
			if e != nil {
				return e
			}
			if _, e := rU4(); e != nil {
				return e
			}
			cid, e := rID()
			if e != nil {
				return e
			}
			nb, e := rU4()
			if e != nil {
				return e
			}
			if pass == 1 {
				p.node(oid, int64(nb)+int64(id)*2, p.classDisplayName(cid))
				if err := skip(int(nb)); err != nil {
					return err
				}
			} else if pass == 2 {
				from := p.idOf[oid]
				if err := p.parseBody(r, cid, int(nb), from, &rem); err != nil {
					return err
				}
			} else { // pass 3: recover field-name labels for wanted edges
				from := p.idOf[oid]
				if p.wantFrom[from] != nil {
					if err := p.parseBodyLabeled(r, cid, int(nb), from, &rem); err != nil {
						return err
					}
				} else if err := skip(int(nb)); err != nil {
					return err
				}
			}
		case 0x22: // OBJ_ARRAY_DUMP: id, u4 stack, u4 n, id arrClass, [n ids]
			oid, e := rID()
			if e != nil {
				return e
			}
			if _, e := rU4(); e != nil {
				return e
			}
			n, e := rU4()
			if e != nil {
				return e
			}
			ac, e := rID()
			if e != nil {
				return e
			}
			if pass == 1 {
				name := p.classDisplayName(ac)
				if name == "unknown" {
					name = "java.lang.Object[]"
				}
				p.node(oid, int64(n)*int64(id)+16, name)
				if err := skip(int(n) * id); err != nil {
					return err
				}
			} else if pass == 2 {
				from := p.idOf[oid]
				for i := uint32(0); i < n; i++ {
					ref, e := rID()
					if e != nil {
						return e
					}
					if to, ok := p.idOf[ref]; ok && ref != 0 {
						p.addEdge(from, to)
					}
				}
			} else { // pass 3: label wanted array element edges
				from := p.idOf[oid]
				tos := p.wantFrom[from]
				if tos == nil {
					if err := skip(int(n) * id); err != nil {
						return err
					}
				} else {
					for i := uint32(0); i < n; i++ {
						ref, e := rID()
						if e != nil {
							return e
						}
						if to, ok := p.idOf[ref]; ok && tos[to] {
							if _, seen := p.labels[[2]int32{from, to}]; !seen {
								p.labels[[2]int32{from, to}] = "[]"
							}
						}
					}
				}
			}
		case 0x23: // PRIM_ARRAY_DUMP: id, u4 stack, u4 n, u1 type, [n*sz]
			oid, e := rID()
			if e != nil {
				return e
			}
			if _, e := rU4(); e != nil {
				return e
			}
			n, e := rU4()
			if e != nil {
				return e
			}
			et, e := r.ReadByte()
			if e != nil {
				return e
			}
			rem--
			sz := int64(basicTypeSize(et, id))
			if pass == 1 {
				p.node(oid, int64(n)*sz+16, primArrayName[et])
			}
			if err := skip(int(int64(n) * sz)); err != nil {
				return err
			}
		case 0x20: // CLASS_DUMP
			if err := p.classDump(r, &rem, pass); err != nil {
				return err
			}
		case 0xFF, 0x05, 0x07: // ROOT_UNKNOWN / STICKY_CLASS / MONITOR_USED: id
			if err := rootIDs(0); err != nil {
				return err
			}
		case 0x01: // ROOT_JNI_GLOBAL: id, id
			if err := rootIDs(id); err != nil {
				return err
			}
		case 0x02, 0x03, 0x08: // JNI_LOCAL / JAVA_FRAME / THREAD_OBJECT: id, u4, u4
			if err := rootIDs(8); err != nil {
				return err
			}
		case 0x04, 0x06: // NATIVE_STACK / THREAD_BLOCK: id, u4
			if err := rootIDs(4); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown sub 0x%02x", sub)
		}
	}
	return nil
}

// parseBody walks an instance's field chain, adding an edge for every non-null
// object reference field.
func (p *deepParser) parseBody(r *bufio.Reader, classID uint64, nbytes int, from int32, rem *int64) error {
	cl := p.layouts[classID]
	var chain []finfo
	if cl != nil {
		chain = cl.chain
	}
	read := 0
	for _, fd := range chain {
		if read >= nbytes {
			break
		}
		if fd.typ == 2 { // object reference
			ref, err := readIDN(r, p.idSize)
			if err != nil {
				return err
			}
			read += p.idSize
			if to, ok := p.idOf[ref]; ok && ref != 0 {
				p.addEdge(from, to)
			}
		} else {
			s := basicTypeSize(fd.typ, p.idSize)
			if _, err := discard(r, s); err != nil {
				return err
			}
			read += s
		}
	}
	if read < nbytes { // trailing bytes (chain incomplete) — skip them
		if _, err := discard(r, nbytes-read); err != nil {
			return err
		}
	}
	*rem -= int64(nbytes)
	return nil
}

// parseBodyLabeled (pass 3) walks an instance's fields and records, for each
// reference that lands on a wanted child, the FIELD NAME that points to it —
// so a path-to-GC-roots can read "kept via field <name>".
func (p *deepParser) parseBodyLabeled(r *bufio.Reader, classID uint64, nbytes int, from int32, rem *int64) error {
	cl := p.layouts[classID]
	var chain []finfo
	if cl != nil {
		chain = cl.chain
	}
	tos := p.wantFrom[from]
	read := 0
	for _, fd := range chain {
		if read >= nbytes {
			break
		}
		if fd.typ == 2 {
			ref, err := readIDN(r, p.idSize)
			if err != nil {
				return err
			}
			read += p.idSize
			if to, ok := p.idOf[ref]; ok && ref != 0 && tos[to] {
				key := [2]int32{from, to}
				if _, seen := p.labels[key]; !seen {
					p.labels[key] = fd.name
				}
			}
		} else {
			s := basicTypeSize(fd.typ, p.idSize)
			if _, err := discard(r, s); err != nil {
				return err
			}
			read += s
		}
	}
	if read < nbytes {
		if _, err := discard(r, nbytes-read); err != nil {
			return err
		}
	}
	*rem -= int64(nbytes)
	return nil
}

// recoverPathLabels re-reads the file once to fill in field-name labels for the
// given set of (from,to) node edges — a tiny set (the edges on a few paths).
func (p *deepParser) recoverPathLabels(path string, want map[[2]int32]bool) map[[2]int32]string {
	p.wantFrom = map[int32]map[int32]bool{}
	for e := range want {
		if e[0] == 0 { // root edges are labelled "GC root" without a re-read
			continue
		}
		m := p.wantFrom[e[0]]
		if m == nil {
			m = map[int32]bool{}
			p.wantFrom[e[0]] = m
		}
		m[e[1]] = true
	}
	p.labels = map[[2]int32]string{}
	if len(p.wantFrom) > 0 {
		p.scan(path, 3)
	}
	return p.labels
}

func (p *deepParser) classDump(r *bufio.Reader, rem *int64, pass int) error {
	id := p.idSize
	skip := func(n int) error {
		if _, err := discard(r, n); err != nil {
			return err
		}
		*rem -= int64(n)
		return nil
	}
	u2 := func() (int, error) {
		var b [2]byte
		if _, err := readFull(r, b[:]); err != nil {
			return 0, err
		}
		*rem -= 2
		return int(uint16(b[0])<<8 | uint16(b[1])), nil
	}
	cid, err := readIDN(r, id) // class object id
	if err != nil {
		return err
	}
	*rem -= int64(id)
	if err := skip(4); err != nil { // stack serial
		return err
	}
	superID, err := readIDN(r, id)
	if err != nil {
		return err
	}
	*rem -= int64(id)
	if err := skip(5*id + 4); err != nil { // loader, signers, protdomain, 2 reserved, instance size
		return err
	}
	cp, err := u2()
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
		*rem--
		if err := skip(basicTypeSize(t, id)); err != nil {
			return err
		}
	}
	sf, err := u2()
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
		*rem--
		if err := skip(basicTypeSize(t, id)); err != nil {
			return err
		}
	}
	inf, err := u2()
	if err != nil {
		return err
	}
	nameIDs := make([]uint64, 0, inf)
	types := make([]byte, 0, inf)
	for i := 0; i < inf; i++ {
		nid, err := readIDN(r, id) // field name id (kept, so edges can be labelled)
		if err != nil {
			return err
		}
		*rem -= int64(id)
		t, err := r.ReadByte()
		if err != nil {
			return err
		}
		*rem--
		nameIDs = append(nameIDs, nid)
		types = append(types, t)
	}
	if pass == 1 {
		p.layouts[cid] = &classLayout{superID: superID, fnameID: nameIDs, ftype: types}
	}
	return nil
}

// --- dominators (Cooper-Harvey-Kennedy) --------------------------------------

// dominators returns idom[] for every node reachable from the root (0); an
// unreachable node has idom == -1. Also returns each node's postorder number
// and the nodes in postorder (children before dominators).
func (g *heapGraph) dominators() (idom []int32, po []int32, order []int32) {
	n := len(g.shallow)
	po = make([]int32, n)
	for i := range po {
		po[i] = -1
	}
	// iterative DFS from the root to get a postorder
	type frame struct {
		node int32
		i    int
	}
	stack := []frame{{0, 0}}
	visiting := make([]bool, n)
	visiting[0] = true
	cnt := int32(0)
	for len(stack) > 0 {
		fr := &stack[len(stack)-1]
		if fr.i < len(g.succ[fr.node]) {
			w := g.succ[fr.node][fr.i]
			fr.i++
			if !visiting[w] {
				visiting[w] = true
				stack = append(stack, frame{w, 0})
			}
		} else {
			po[fr.node] = cnt
			order = append(order, fr.node)
			cnt++
			stack = stack[:len(stack)-1]
		}
	}
	idom = make([]int32, n)
	for i := range idom {
		idom[i] = -1
	}
	idom[0] = 0
	// reverse postorder = order reversed (root first)
	rpo := make([]int32, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		rpo = append(rpo, order[i])
	}
	intersect := func(a, b int32) int32 {
		for a != b {
			for po[a] < po[b] {
				a = idom[a]
			}
			for po[b] < po[a] {
				b = idom[b]
			}
		}
		return a
	}
	for changed := true; changed; {
		changed = false
		for _, b := range rpo {
			if b == 0 {
				continue
			}
			newIdom := int32(-1)
			for _, pr := range g.pred[b] {
				if idom[pr] == -1 {
					continue
				}
				if newIdom == -1 {
					newIdom = pr
				} else {
					newIdom = intersect(pr, newIdom)
				}
			}
			if newIdom != -1 && idom[b] != newIdom {
				idom[b] = newIdom
				changed = true
			}
		}
	}
	return idom, po, order
}

// retainedSizes sums shallow sizes up the dominator tree: retained[v] is the
// memory freed if v were collected (v plus everything only reachable through v).
func (g *heapGraph) retainedSizes(idom, order []int32) []int64 {
	ret := make([]int64, len(g.shallow))
	copy(ret, g.shallow)
	for _, v := range order { // postorder: children before their dominator
		if v == 0 || idom[v] == -1 {
			continue
		}
		ret[idom[v]] += ret[v]
	}
	return ret
}

// bfsParents returns, for each node, its parent on a SHORTEST path from the GC
// root (node 0) — the basis for "path to GC roots". -1 = unreachable.
func (g *heapGraph) bfsParents() []int32 {
	parent := make([]int32, len(g.shallow))
	for i := range parent {
		parent[i] = -1
	}
	parent[0] = 0
	q := []int32{0}
	for len(q) > 0 {
		v := q[0]
		q = q[1:]
		for _, w := range g.succ[v] {
			if parent[w] == -1 {
				parent[w] = v
				q = append(q, w)
			}
		}
	}
	return parent
}

// pathFromRoot returns the node sequence root(0) … target (nil if unreachable).
func pathFromRoot(target int32, parent []int32) []int32 {
	if parent[target] == -1 {
		return nil
	}
	var rev []int32
	for v := target; ; v = parent[v] {
		rev = append(rev, v)
		if v == 0 {
			break
		}
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// --- render ------------------------------------------------------------------

func analyzeHprofDeep(path string) (string, error) {
	g, p, err := buildHeapGraph(path)
	if err != nil {
		return "", err
	}
	idom, _, order := g.dominators()
	ret := g.retainedSizes(idom, order)

	var reachable, garbage, total int64
	var rows []retRow
	for i := 1; i < len(g.shallow); i++ {
		total += g.shallow[i]
		if idom[i] == -1 {
			garbage += g.shallow[i]
			continue
		}
		reachable += g.shallow[i]
		rows = append(rows, retRow{int32(i), ret[i]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ret > rows[j].ret })

	var b strings.Builder
	fmt.Fprintf(&b, "retained-size analysis (dominator tree — what each object actually keeps alive)\n")
	fmt.Fprintf(&b, "reachable %s across %s objects · unreachable (garbage, not yet GC'd) %s\n\n",
		fmtSize(reachable), humanCount(int64(len(rows))), fmtSize(garbage))
	b.WriteString("top retained holders (retained | shallow | class):\n")
	top := rows
	if len(top) > 12 {
		top = top[:12]
	}
	for _, rw := range top {
		pct := 0.0
		if reachable > 0 {
			pct = float64(rw.ret) * 100 / float64(reachable)
		}
		fmt.Fprintf(&b, "  %9s (%4.1f%%)  %9s  %s\n", fmtSize(rw.ret), pct, fmtSize(g.shallow[rw.ix]), g.names[g.name[rw.ix]])
	}

	// path to GC roots — WHY the top holders are kept alive (with field names)
	if pathBlock := g.renderPaths(p, path, top, reachable); pathBlock != "" {
		b.WriteString("\n" + pathBlock)
	}

	b.WriteString("\n" + deepVerdict(rows, g, ret, reachable) + "\n")
	b.WriteString("that's retained size AND the reference chains MAT is for — no external tool needed.\n")
	b.WriteString("(Eclipse MAT still adds OQL and side-by-side dump diffs.)")
	return b.String(), nil
}

// renderPaths shows, for the biggest few retained holders, the shortest chain of
// references from a GC root — the "why is this alive?" that names a leak.
func (g *heapGraph) renderPaths(p *deepParser, path string, top []retRow, reachable int64) string {
	parent := g.bfsParents()
	// pick up to 3 distinct, meaningful holders that retain a real share
	var picks []int32
	seenName := map[string]bool{}
	for _, rw := range top {
		if reachable > 0 && float64(rw.ret)*100/float64(reachable) < 2 {
			break
		}
		nm := g.names[g.name[rw.ix]]
		if seenName[nm] || pathFromRoot(rw.ix, parent) == nil {
			continue
		}
		seenName[nm] = true
		picks = append(picks, rw.ix)
		if len(picks) == 3 {
			break
		}
	}
	if len(picks) == 0 {
		return ""
	}
	// collect the edges we need field-name labels for, then recover them
	want := map[[2]int32]bool{}
	paths := map[int32][]int32{}
	for _, t := range picks {
		pth := pathFromRoot(t, parent)
		paths[t] = pth
		for i := 0; i+1 < len(pth); i++ {
			want[[2]int32{pth[i], pth[i+1]}] = true
		}
	}
	labels := p.recoverPathLabels(path, want)

	var b strings.Builder
	b.WriteString("why the biggest holders are kept alive (shortest path from a GC root):\n")
	for _, t := range picks {
		pth := paths[t]
		if len(pth) > 14 { // keep very deep chains readable
			pth = append(append([]int32{}, pth[:7]...), pth[len(pth)-6:]...)
		}
		for i, node := range pth {
			name := "<GC roots>"
			if node != 0 {
				name = g.names[g.name[node]]
			}
			if i == 0 {
				b.WriteString("  " + name + "\n")
				continue
			}
			via := labels[[2]int32{pth[i-1], node}]
			if pth[i-1] == 0 {
				via = "GC root"
			}
			if via == "" {
				via = "→"
			} else {
				via = "→ " + via
			}
			fmt.Fprintf(&b, "  %s%s %s\n", strings.Repeat("  ", i), via, name)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

type retRow struct {
	ix  int32
	ret int64
}

func deepVerdict(rows []retRow, g *heapGraph, ret []int64, reachable int64) string {
	if len(rows) == 0 || reachable == 0 {
		return "verdict: nothing reachable to retain — the heap is empty or all garbage."
	}
	top := rows[0]
	pct := float64(top.ret) * 100 / float64(reachable)
	name := g.names[g.name[top.ix]]
	switch {
	case pct >= 40 && !isFrameworkClass(name):
		return fmt.Sprintf("verdict: LEAK SUSPECT — a single %s retains %s (%.0f%% of the reachable heap).\n"+
			"  That one object holds most of the memory. Open its 'Path to GC Roots' in MAT.", name, fmtSize(top.ret), pct)
	case pct >= 40:
		return fmt.Sprintf("verdict: one %s retains %s (%.0f%%) — but it's a framework/JDK type, so this is\n"+
			"  likely a big cache/pool by design. Confirm it's bounded rather than growing.", name, fmtSize(top.ret), pct)
	default:
		return "verdict: no single object dominates the heap — retained memory is spread out, which\n" +
			"  argues against one runaway leak. If it still grows, diff two dumps taken under load."
	}
}
