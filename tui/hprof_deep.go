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

// collInfo is what jdebug measures about ONE collection instance — the exact
// entry count read from its own `size`/`baseCount` field, and the length of its
// backing array (capacity). Together they give "N entries in a table sized for
// M" — the specific-collection size + wasted capacity MAT reports, not a
// heap-wide HashMap$Node proxy.
type collInfo struct {
	entries      int64
	entriesKnown bool
	tableNode    int32 // backing-array node (-1 until linked in pass 2)
	capacity     int64 // backing-array element count (buckets/slots), resolved after pass 2
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
	coll    map[int32]*collInfo // measured collections: node -> entries + capacity
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
		arrLen:  map[int32]int64{}, collOf: map[int32]*collInfo{},
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
	// mark which classes are JDK collections, so pass 2 can measure their exact
	// entry count + backing-array capacity (a HashMap's real size, not a proxy).
	p.collClassIDs = map[uint64]bool{}
	for cid := range p.layouts {
		if isCollectionType(p.classDisplayName(cid)) {
			p.collClassIDs[cid] = true
		}
	}
	// pass 2: edges
	p.g.succ = make([][]int32, len(p.g.shallow))
	p.g.pred = make([][]int32, len(p.g.shallow))
	if err := p.scan(path, 2); err != nil {
		return nil, nil, err
	}
	// resolve each measured collection's capacity from its backing-array length
	for _, ci := range p.collOf {
		if ci.tableNode >= 0 {
			ci.capacity = p.arrLen[ci.tableNode]
		}
	}
	p.g.coll = p.collOf
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
	// exact-collection sizing (#1/#2): backing-array lengths + per-collection stats
	arrLen       map[int32]int64     // object-array node -> element count (capacity)
	collOf       map[int32]*collInfo // collection node -> its measured size + backing array
	collClassIDs map[uint64]bool     // class ids whose display name is a JDK collection
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
				ix := p.node(oid, int64(n)*int64(id)+16, name)
				p.arrLen[ix] = int64(n) // capacity of any collection backed by this array
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
// object reference field. For JDK collections it ALSO reads (instead of
// discarding) the instance's own `size`/`baseCount` field and links its backing
// array — so we can report a specific collection's exact entry count and wasted
// capacity, not a heap-wide HashMap$Node estimate.
func (p *deepParser) parseBody(r *bufio.Reader, classID uint64, nbytes int, from int32, rem *int64) error {
	cl := p.layouts[classID]
	var chain []finfo
	if cl != nil {
		chain = cl.chain
	}
	var ci *collInfo
	if p.collClassIDs[classID] {
		ci = &collInfo{tableNode: -1}
	}
	read := 0
	for _, fd := range chain {
		if read >= nbytes {
			break
		}
		switch {
		case fd.typ == 2: // object reference
			ref, err := readIDN(r, p.idSize)
			if err != nil {
				return err
			}
			read += p.idSize
			to, ok := p.idOf[ref]
			if ok && ref != 0 {
				p.addEdge(from, to)
			}
			if ci != nil && ci.tableNode < 0 && ok && ref != 0 && isBackingArrayField(fd.name) {
				ci.tableNode = to
			}
		case ci != nil && !ci.entriesKnown && fd.typ == 10 && fd.name == "size": // int size
			v, err := readU4(r)
			if err != nil {
				return err
			}
			read += 4
			ci.entries = int64(int32(v))
			ci.entriesKnown = true
		case ci != nil && !ci.entriesKnown && fd.typ == 11 && fd.name == "baseCount": // ConcurrentHashMap
			v, err := readU8(r)
			if err != nil {
				return err
			}
			read += 8
			ci.entries = int64(v)
			ci.entriesKnown = true
		default:
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
	if ci != nil {
		p.collOf[from] = ci
	}
	*rem -= int64(nbytes)
	return nil
}

// isBackingArrayField names the field that holds a collection's backing array —
// the one whose length is the collection's capacity.
func isBackingArrayField(n string) bool {
	switch n {
	case "table", "elementData", "elements", "queue", "items":
		return true
	}
	return false
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
		if err := skip(id); err != nil { // static field name id
			return err
		}
		t, err := r.ReadByte()
		if err != nil {
			return err
		}
		*rem--
		// A class's OBJECT-typed static fields are GC roots (the loaded class
		// holds them). Following them is essential: a static cache/registry is
		// the single most common real leak, and without this edge everything it
		// holds looks like garbage. So in pass 2 we read the value and wire an
		// edge from the synthetic root to it, rather than discarding it.
		if pass == 2 && t == 2 {
			ref, err := readIDN(r, id)
			if err != nil {
				return err
			}
			*rem -= int64(id)
			if to, ok := p.idOf[ref]; ok && ref != 0 {
				p.addEdge(0, to)
			}
		} else if err := skip(basicTypeSize(t, id)); err != nil {
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

// --- leak-pattern recognition ------------------------------------------------

// classCounts tallies reachable instances per class name — the signal several
// leak SIGNATURES are read from (many ThreadLocalMap entries, many Finalizers,
// many classloaders), which a retained-size ranking alone doesn't surface.
func (g *heapGraph) classCounts() map[string]int64 {
	c := map[string]int64{}
	for i := 1; i < len(g.shallow); i++ {
		c[g.names[g.name[i]]]++
	}
	return c
}

// leakPattern is the difference between a dump and a diagnosis: it matches the
// retained-size ranking + instance counts against the handful of leak shapes
// that cause most real JVM OOMs, and returns a NAMED pattern with a concrete
// action — not "open it in MAT". found=false when nothing matches confidently.
func leakPattern(g *heapGraph, rows []retRow, reachable int64, h *heapHistogram) (title, detail, action string, found bool) {
	if len(rows) == 0 || reachable == 0 {
		return "", "", "", false
	}
	cnt := g.classCounts()
	sum := func(names ...string) int64 {
		var n int64
		for _, x := range names {
			n += cnt[x]
		}
		return n
	}
	pct := func(b int64) float64 { return float64(b) * 100 / float64(reachable) }

	// 1. ThreadLocal leak — entries never removed on POOLED threads (very common
	// with thread pools + ThreadLocal caches / MDC / SecurityContext).
	if tl := sum("java.lang.ThreadLocal$ThreadLocalMap$Entry"); tl >= 10_000 {
		return "ThreadLocal leak",
			fmt.Sprintf("%s ThreadLocalMap entries are still reachable — on a thread POOL, thread-locals set per request are never cleared, so they pile up.", humanCount(tl)),
			"call ThreadLocal.remove() in a finally (or a servlet filter / interceptor); never leave a set() on a pooled thread.", true
	}
	// 2. Finalizer backlog — the finalizer thread can't keep up, so Finalizer
	// refs (and everything they hold) pin the heap.
	if fz := sum("java.lang.ref.Finalizer"); fz >= 50_000 {
		return "finalizer backlog",
			fmt.Sprintf("%s java.lang.ref.Finalizer objects are queued — the finalizer thread is behind, pinning everything awaiting finalize().", humanCount(fz)),
			"stop relying on finalize() (deprecated). Use java.lang.ref.Cleaner or try-with-resources; check for a slow/blocked finalizer.", true
	}
	// 3. Classloader / redeploy leak — many live ClassLoaders (each pins its
	// classes + statics) is the signature of hot-redeploy leaks in app servers.
	var loaders int64
	for name, n := range cnt {
		if strings.HasSuffix(name, "ClassLoader") {
			loaders += n
		}
	}
	if loaders >= 50 {
		return "classloader / redeploy leak",
			fmt.Sprintf("%s live ClassLoaders are retained — each pins all of its classes and their statics. Classic hot-redeploy / hot-reload leak.", humanCount(loaders)),
			"find what holds the old ClassLoaders alive (a static registry, a ThreadLocal, a JDBC driver, a shutdown hook) and release it on undeploy.", true
	}

	// 4. unbounded collection / cache — the #1 JVM leak. Prefer an EXACT
	// measurement of the biggest collection we sized (its own entry count +
	// backing-array capacity) over the old heap-wide HashMap$Node proxy.
	for _, rw := range rows {
		rp := pct(rw.ret)
		if rp < 25 {
			break // rows are sorted by retained desc — nothing above the bar left
		}
		ci, ok := g.coll[rw.ix]
		if !ok {
			continue
		}
		name := g.names[g.name[rw.ix]]
		return "unbounded collection / cache", collDetail(name, rw.ret, rp, ci), collAction(ci), true
	}

	// 5b. fallback when we couldn't size the specific collection: the old
	// name/heap-wide-proxy heuristic still catches the shape.
	top := rows[0]
	topName := g.names[g.name[top.ix]]
	topPct := pct(top.ret)
	entries := sum(
		"java.util.HashMap$Node", "java.util.concurrent.ConcurrentHashMap$Node",
		"java.util.LinkedHashMap$Entry", "java.util.Hashtable$Entry", "java.util.TreeMap$Entry")
	if topPct >= 25 && (isCollectionType(topName) || entries >= 500_000) {
		via := topName
		if !isCollectionType(topName) {
			via = fmt.Sprintf("%s (holding ~%s map entries)", topName, humanCount(entries))
		}
		return "unbounded collection / cache",
			fmt.Sprintf("a %s retains %s (%.0f%% of the reachable heap)%s — a collection that grows without a ceiling is the #1 JVM leak.",
				via, fmtSize(top.ret), topPct, ternary(entries >= 500_000, fmt.Sprintf(", and there are ~%s map entries heap-wide", humanCount(entries)), "")),
			"bound it: a max size + eviction (Caffeine/Guava LoadingCache), a TTL, or an LRU — and confirm removal actually happens. Its path to GC roots is above; that field is the cache.", true
	}
	if topPct >= 30 && !isFrameworkClass(topName) {
		return "single dominant object",
			fmt.Sprintf("one %s retains %s (%.0f%% of the reachable heap) — most of the memory hangs off this one object.", topName, fmtSize(top.ret), topPct),
			"the reference chain above names the field that anchors it — break that reference (clear/scope it) or bound what it accumulates.", true
	}

	// 6. Duplicate strings — the same char[]/byte[] content held many times over.
	// Checked LAST, as a fallback: when no single holder dominates but a big share
	// of the heap is identical String content, that IS the diagnosis. (When a
	// collection/object already dominated above, that's the better headline.)
	if h != nil && h.dupWaste > 0 {
		dupPct := float64(h.dupWaste) * 100 / float64(reachable)
		if dupPct >= 20 || (h.dupWaste >= 16<<20 && dupPct >= 10) {
			return "duplicate strings",
				fmt.Sprintf("~%s is wasted on %s repeated char[]/byte[] value(s) (%.0f%% of the reachable heap) — identical String content is held many times instead of once.",
					fmtSize(h.dupWaste), humanCount(h.dupGroups), dupPct),
				"deduplicate: intern hot values (String.intern or a canonicalizing cache), or enable -XX:+UseStringDeduplication on G1.", true
		}
	}
	return "", "", "", false
}

// collDetail renders the EXACT size of a specific leaking collection: how many
// entries it holds, and how big its backing table is (capacity) — so "wasted
// capacity" (a HashMap sized for 1M holding 3) is visible, the way MAT shows it,
// rather than a heap-wide guess.
func collDetail(name string, ret int64, pctOfHeap float64, ci *collInfo) string {
	base := fmt.Sprintf("a %s retains %s (%.0f%% of the reachable heap)", name, fmtSize(ret), pctOfHeap)
	if !ci.entriesKnown {
		if ci.capacity > 0 {
			return fmt.Sprintf("%s — its backing table is sized for %s slots. A collection that grows without a ceiling is the #1 JVM leak.",
				base, humanCount(ci.capacity))
		}
		return base + " — a collection that grows without a ceiling is the #1 JVM leak."
	}
	if ci.capacity <= 0 {
		return fmt.Sprintf("%s — it holds %s entries. A collection that grows without a ceiling is the #1 JVM leak.",
			base, humanCount(ci.entries))
	}
	fill := float64(ci.entries) * 100 / float64(ci.capacity)
	s := fmt.Sprintf("%s — it holds %s entries in a table sized for %s (%.0f%% full)",
		base, humanCount(ci.entries), humanCount(ci.capacity), fill)
	// wasted capacity: a big, mostly-empty table is memory spent on slack.
	if ci.capacity >= 1024 && fill < 25 {
		s += fmt.Sprintf(", so ~%s of that table is empty slack", humanCount(ci.capacity-ci.entries))
	}
	return s + ". A collection that grows without a ceiling is the #1 JVM leak."
}

// collAction gives the fix, split by whether the collection is also oversized.
func collAction(ci *collInfo) string {
	if ci.entriesKnown && ci.capacity >= 1024 {
		if fill := float64(ci.entries) * 100 / float64(ci.capacity); fill < 25 {
			return "two issues: it's OVERSIZED (a table mostly empty — presize correctly or drop a bad initialCapacity), and if entries only ever climb, BOUND it — max size + eviction (Caffeine/Guava), a TTL, or an LRU. Its path to GC roots is above."
		}
	}
	return "bound it: a max size + eviction (Caffeine/Guava LoadingCache), a TTL, or an LRU — and confirm removal actually happens. Its path to GC roots is above; that field is the cache."
}

// isCollectionType reports whether a class name is a JDK collection/map/array
// container — the usual body of a "growing cache" leak.
func isCollectionType(n string) bool {
	switch n {
	case "java.util.HashMap", "java.util.concurrent.ConcurrentHashMap",
		"java.util.LinkedHashMap", "java.util.TreeMap", "java.util.Hashtable",
		"java.util.ArrayList", "java.util.LinkedList", "java.util.Vector",
		"java.util.HashSet", "java.util.LinkedHashSet", "java.util.TreeSet",
		"java.util.concurrent.ConcurrentLinkedQueue", "java.util.ArrayDeque",
		"java.util.HashMap$Node[]", "java.util.concurrent.ConcurrentHashMap$Node[]":
		return true
	}
	return strings.HasPrefix(n, "java.util.") && (strings.HasSuffix(n, "[]") || strings.Contains(n, "Map") || strings.Contains(n, "List") || strings.Contains(n, "Set"))
}

// --- render ------------------------------------------------------------------

func analyzeHprofDeep(path string, h *heapHistogram) (out string, err error) {
	// same contract as analyzeHprof: a corrupt dump yields a message, not a
	// Go stack trace (the caller prints "(retained-size pass skipped: …)").
	defer func() {
		if rec := recover(); rec != nil {
			out, err = "", fmt.Errorf("corrupt heap dump (deep pass aborted: %v)", rec)
		}
	}()
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
	fmt.Fprintf(&b, "heap analysis — retained size (what each object actually KEEPS alive)\n")
	fmt.Fprintf(&b, "reachable %s across %s objects · unreachable (garbage, not yet GC'd) %s\n",
		fmtSize(reachable), humanCount(int64(len(rows))), fmtSize(garbage))

	// LEAD with the diagnosis: a named leak pattern + a concrete fix, when one
	// matches. This is the "usable, not a dump" part — the holders + paths below
	// are the evidence for it.
	if title, detail, action, found := leakPattern(g, rows, reachable, h); found {
		b.WriteString("\n⟶ LEAK PATTERN — " + title + "\n")
		b.WriteString("  " + detail + "\n")
		b.WriteString("  → fix: " + action + "\n")
	}

	// accumulation point (single-dump growth evidence): of everything the top
	// holder dominates, which class piles up the most. One dump can't watch it
	// grow, but "N instances of X accumulate under one holder" is the same
	// signature MAT's leak-suspect report keys on — an automatic answer, not a
	// hand-off to a two-dump diff.
	if len(rows) > 0 {
		ch := dominatorChildren(idom)
		if cls, n := g.accumulationPoint(rows[0].ix, ch); n >= 1000 && cls != "" {
			hint := ""
			if isFrameworkClass(cls) {
				hint = "  (a JDK/framework type — trace it to the app object that owns them)"
			}
			fmt.Fprintf(&b, "\n⟶ ACCUMULATION POINT — %s instances of %s pile up under the top holder.\n"+
				"  that one growing set is the leak's body%s.\n", humanCount(n), cls, hint)
		}
	}

	b.WriteString("\ntop retained holders (retained | shallow | class):\n")
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

	// when no pattern matched, still give a plain-language read of the shape
	if _, _, _, found := leakPattern(g, rows, reachable, h); !found {
		b.WriteString("\n" + deepVerdict(rows, g, ret, reachable) + "\n")
	}
	b.WriteString("\nnext: this is ONE dump — to prove the set is growing over TIME, take a\n")
	b.WriteString("second dump under load and diff (jdebug fills in both paths for you):\n")
	b.WriteString("  jdebug analyze --diff <before.hprof> <after.hprof>\n")
	b.WriteString("(Eclipse MAT still adds OQL queries and side-by-side dump diffs.)")
	return b.String(), nil
}

// dominatorChildren builds the child adjacency of the dominator tree from idom.
func dominatorChildren(idom []int32) [][]int32 {
	ch := make([][]int32, len(idom))
	for v := 1; v < len(idom); v++ {
		d := idom[v]
		if d >= 0 && int(d) != v {
			ch[d] = append(ch[d], int32(v))
		}
	}
	return ch
}

// accumulationPoint returns the most frequent class among all objects DOMINATED
// by holder (its retained set) and that class's count — the accumulating type
// that makes the holder heavy. Iterative DFS of the dominator subtree so a deep
// heap can't blow the Go stack.
func (g *heapGraph) accumulationPoint(holder int32, ch [][]int32) (cls string, count int64) {
	counts := map[int32]int64{} // name index -> count
	stack := []int32{holder}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range ch[v] {
			counts[g.name[c]]++
			stack = append(stack, c)
		}
	}
	var bestIx int32 = -1
	for ix, n := range counts {
		if bestIx < 0 || n > count || (n == count && ix < bestIx) {
			bestIx, count = ix, n
		}
	}
	if bestIx < 0 {
		return "", 0
	}
	return g.names[bestIx], count
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
