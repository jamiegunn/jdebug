package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Thread-dump parsing and analysis — the v2 replacement for analyze.sh's
// greps. The point (audit finding F4): jstack/jcmd dumps carry a
// "Found one Java-level deadlock" banner, but Spring Boot actuator dumps do
// NOT run deadlock detection — a grep for the banner false-negatives on the
// DEFAULT capture tier. Here deadlocks are found structurally, by building
// the waits-for graph and detecting cycles, from BOTH lock syntaxes:
// monitors ("- waiting to lock <id>" / "- locked <id>") and
// java.util.concurrent ("- parking to wait for <id>" / the
// "Locked ownable synchronizers:" list) — juc locks are the common case in
// modern Spring code, and a deadlock certified healthy is worse than no tool.
//
// KNOWN LIMITS, stated so nobody trusts this past what it can see:
//   - virtual threads (JDK 21+) never appear in ThreadMXBean/actuator dumps;
//     Render warns when the dump shows signs of a virtual-thread app.
//   - the JDK-21 `jcmd Thread.dump_to_file` plain format uses different
//     headers; Render refuses (0 threads) rather than blessing it blind.

// ThreadInfo is one thread's parsed state.
type ThreadInfo struct {
	Name      string
	State     string   // RUNNABLE | BLOCKED | WAITING | TIMED_WAITING | NEW | TERMINATED
	Frames    []string // stack frames, outermost first (as printed)
	WaitsFor  string   // lock id this thread is blocked on ("" = none)
	WaitsDesc string   // the raw "waiting to lock <id> (a Cls)" text
	Holds     []string // lock ids this thread holds
	LockOwner string   // owning thread's NAME when the format says so (JSON)
}

// ThreadDump is a parsed capture.
type ThreadDump struct {
	Threads      []ThreadInfo
	JstackBanner bool // the dump itself declared a deadlock (jstack/jcmd)
	// PlainFormatHeads counts `#N "name"`-style headers — the JDK-21
	// `jcmd Thread.dump_to_file` plain format this parser does NOT read.
	// Total==0 with PlainFormatHeads>0 means "wrong format", not "no threads".
	PlainFormatHeads int
}

var (
	reThreadHead = regexp.MustCompile(`^"([^"]+)"`)
	reState      = regexp.MustCompile(`java\.lang\.Thread\.State:\s+([A-Z_]+)`)
	reWaiting    = regexp.MustCompile(`-\s+waiting to lock <([^>]+)>(.*)`)
	// juc syntax: LockSupport.park on an AQS sync (ReentrantLock & friends).
	// The thread is WAITING, not BLOCKED, and no "waiting to lock" appears.
	reParking = regexp.MustCompile(`-\s+parking to wait for\s+<([^>]+)>(.*)`)
	reLocked  = regexp.MustCompile(`-\s+locked <([^>]+)>`)
	// a held juc lock appears as a bare "- <id>" line under the
	// "Locked ownable synchronizers:" section
	reSyncHeld  = regexp.MustCompile(`^\s*-\s+<([^>]+)>`)
	reFrame     = regexp.MustCompile(`^\s*at\s+(\S+)`)
	rePlainHead = regexp.MustCompile(`^#\d+\s+"`)
)

// ParseThreadDump auto-detects format: Spring actuator JSON when the first
// non-space byte is '{', else jstack/jcmd/actuator-text.
func ParseThreadDump(r io.Reader) (ThreadDump, error) {
	br := bufio.NewReader(r)
	for {
		b, err := br.Peek(1)
		if err != nil {
			return ThreadDump{}, fmt.Errorf("empty or unreadable dump: %w", err)
		}
		if b[0] == ' ' || b[0] == '\n' || b[0] == '\t' || b[0] == '\r' {
			_, _ = br.ReadByte()
			continue
		}
		if b[0] == '{' {
			return parseJSONDump(br)
		}
		return parseTextDump(br)
	}
}

func parseTextDump(r io.Reader) (ThreadDump, error) {
	var d ThreadDump
	var cur *ThreadInfo
	inSyncList := false // inside a "Locked ownable synchronizers:" section
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Java-level deadlock") {
			d.JstackBanner = true
			cur = nil
			continue
		}
		if rePlainHead.MatchString(line) {
			d.PlainFormatHeads++
			continue
		}
		if m := reThreadHead.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			d.Threads = append(d.Threads, ThreadInfo{Name: m[1]})
			cur = &d.Threads[len(d.Threads)-1]
			inSyncList = false
			continue
		}
		if cur == nil {
			continue
		}
		if strings.Contains(line, "Locked ownable synchronizers") {
			inSyncList = true
			continue
		}
		switch {
		case reState.MatchString(line):
			cur.State = reState.FindStringSubmatch(line)[1]
		case reWaiting.MatchString(line):
			m := reWaiting.FindStringSubmatch(line)
			cur.WaitsFor = m[1]
			cur.WaitsDesc = "waiting to lock <" + m[1] + ">" + strings.TrimRight(m[2], " ")
		case reParking.MatchString(line):
			// juc: parked on an AQS sync. Conditions/queues park too, but
			// their sync object is held by no one, so no graph edge forms —
			// only an exclusively-held lock (in someone's ownable-synchronizer
			// list) can complete a cycle. No false positives from idle pools.
			m := reParking.FindStringSubmatch(line)
			if cur.WaitsFor == "" {
				cur.WaitsFor = m[1]
				cur.WaitsDesc = "parking to wait for <" + m[1] + ">" + strings.TrimRight(m[2], " ")
			}
		case reLocked.MatchString(line):
			cur.Holds = append(cur.Holds, reLocked.FindStringSubmatch(line)[1])
		case inSyncList && reSyncHeld.MatchString(line):
			cur.Holds = append(cur.Holds, reSyncHeld.FindStringSubmatch(line)[1])
		case reFrame.MatchString(line):
			cur.Frames = append(cur.Frames, reFrame.FindStringSubmatch(line)[1])
		}
	}
	return d, sc.Err()
}

// The actuator JSON shapes we care about (java.lang.management.ThreadInfo).
type jsonDump struct {
	Threads []struct {
		ThreadName    string `json:"threadName"`
		ThreadState   string `json:"threadState"`
		LockName      string `json:"lockName"`
		LockOwnerName string `json:"lockOwnerName"`
		StackTrace    []struct {
			ClassName  string `json:"className"`
			MethodName string `json:"methodName"`
		} `json:"stackTrace"`
		LockedMonitors []struct {
			IdentityHashCode int64  `json:"identityHashCode"`
			ClassName        string `json:"className"`
		} `json:"lockedMonitors"`
	} `json:"threads"`
}

func parseJSONDump(r io.Reader) (ThreadDump, error) {
	var jd jsonDump
	if err := json.NewDecoder(r).Decode(&jd); err != nil {
		return ThreadDump{}, fmt.Errorf("not a parseable actuator JSON thread dump: %w", err)
	}
	var d ThreadDump
	for _, t := range jd.Threads {
		ti := ThreadInfo{Name: t.ThreadName, State: t.ThreadState, LockOwner: t.LockOwnerName}
		if t.LockName != "" && (t.ThreadState == "BLOCKED" || strings.HasPrefix(t.ThreadState, "WAITING")) {
			ti.WaitsFor = t.LockName
			ti.WaitsDesc = "waiting to lock <" + t.LockName + ">"
		}
		for _, m := range t.LockedMonitors {
			ti.Holds = append(ti.Holds, fmt.Sprintf("%s@%d", m.ClassName, m.IdentityHashCode))
		}
		for _, f := range t.StackTrace {
			ti.Frames = append(ti.Frames, f.ClassName+"."+f.MethodName)
		}
		d.Threads = append(d.Threads, ti)
	}
	return d, nil
}

// --- analysis ----------------------------------------------------------------

// idleNativeRe: NIO event-loop / selector / accept threads report RUNNABLE
// while parked in the kernel — idle, not load. Same set as v1's analyze.sh.
var idleNativeRe = regexp.MustCompile(`EPoll|KQueue|/Net\.|SelectorImpl|NioEventLoop|socketAccept|socketRead0|PlainSocketImpl|FileDispatcherImpl|SocketDispatcher|accept0|poll0|Poller`)

// Analysis is the structured verdict on one dump.
type Analysis struct {
	Total, Runnable, Blocked, Waiting, TimedWaiting int
	// DeadlockCycles: each cycle as thread names in waits-for order,
	// closing back on the first (["a","b","a"]). Found structurally —
	// works on actuator dumps that carry no jstack banner.
	DeadlockCycles [][]string
	JstackBanner   bool
	ContendedLocks []LockContention // most-contended first
	HotFrameCount  int
	HotFrame       string
	IdleRunnable   int
	// PlainFormatHeads: the dump looks like JDK-21 `jcmd Thread.dump_to_file`
	// plain format (which this parser does not read) — refuse, don't bless.
	PlainFormatHeads int
	// VirtualApp: the dump shows signs of a virtual-thread app (unparker
	// thread / VirtualThread frames). ThreadMXBean/actuator dumps NEVER
	// include virtual threads, so this dump is structurally incomplete.
	VirtualApp bool
}

// LockContention is one lock and how many threads block on it.
type LockContention struct {
	Desc  string // "waiting to lock <id> (a Cls)"
	Count int
}

// Analyze computes the verdict.
func (d ThreadDump) Analyze() Analysis {
	a := Analysis{Total: len(d.Threads), JstackBanner: d.JstackBanner, PlainFormatHeads: d.PlainFormatHeads}
	for _, t := range d.Threads {
		if strings.Contains(t.Name, "VirtualThread") {
			a.VirtualApp = true
			break
		}
		for _, fr := range t.Frames {
			if strings.Contains(fr, "java.lang.VirtualThread") || strings.Contains(fr, "jdk.internal.vm.Continuation") {
				a.VirtualApp = true
				break
			}
		}
		if a.VirtualApp {
			break
		}
	}
	lockWaiters := map[string][]int{} // lock id → waiting thread indexes
	lockDesc := map[string]string{}
	frameCount := map[string]int{}
	for i, t := range d.Threads {
		switch t.State {
		case "RUNNABLE":
			a.Runnable++
		case "BLOCKED":
			a.Blocked++
		case "WAITING":
			a.Waiting++
		case "TIMED_WAITING":
			a.TimedWaiting++
		}
		if t.WaitsFor != "" && t.State == "BLOCKED" {
			lockWaiters[t.WaitsFor] = append(lockWaiters[t.WaitsFor], i)
			lockDesc[t.WaitsFor] = t.WaitsDesc
		}
		if t.State == "RUNNABLE" && len(t.Frames) > 0 {
			fr := t.Frames[0]
			// strip the "java.base@21.0.11/" module prefix, like v1's awk
			if i := strings.Index(fr, "/"); i > 0 && strings.Contains(fr[:i], "@") {
				fr = fr[i+1:]
			}
			if idleNativeRe.MatchString(fr) {
				a.IdleRunnable++
			} else {
				frameCount[fr]++
			}
		}
	}
	for id, ws := range lockWaiters {
		a.ContendedLocks = append(a.ContendedLocks, LockContention{Desc: lockDesc[id], Count: len(ws)})
	}
	sort.Slice(a.ContendedLocks, func(i, j int) bool {
		if a.ContendedLocks[i].Count != a.ContendedLocks[j].Count {
			return a.ContendedLocks[i].Count > a.ContendedLocks[j].Count
		}
		return a.ContendedLocks[i].Desc < a.ContendedLocks[j].Desc
	})
	for fr, n := range frameCount {
		if n > a.HotFrameCount || (n == a.HotFrameCount && fr < a.HotFrame) {
			a.HotFrameCount, a.HotFrame = n, fr
		}
	}
	a.DeadlockCycles = d.deadlockCycles()
	return a
}

// deadlockCycles builds the waits-for graph (thread → thread) and returns
// every distinct cycle. Edges come from lock ownership ("- locked <id>" /
// lockedMonitors) or, in JSON, the explicit lockOwnerName.
func (d ThreadDump) deadlockCycles() [][]string {
	holder := map[string]int{} // lock id → holding thread index
	byName := map[string]int{}
	for i, t := range d.Threads {
		byName[t.Name] = i
		for _, l := range t.Holds {
			holder[l] = i
		}
	}
	next := map[int]int{} // waits-for edges
	for i, t := range d.Threads {
		// An edge exists when the format tells us WHAT the thread waits on
		// (monitor "waiting to lock", juc "parking to wait for", or JSON's
		// lockName) — juc waiters are WAITING, not BLOCKED, so gating on
		// BLOCKED alone made every ReentrantLock deadlock invisible. Idle
		// pool/condition parking creates no edge: nobody HOLDS those objects.
		if t.WaitsFor == "" && t.LockOwner == "" {
			continue
		}
		if t.LockOwner != "" {
			if j, ok := byName[t.LockOwner]; ok && j != i {
				next[i] = j
				continue
			}
		}
		if t.WaitsFor != "" {
			if j, ok := holder[t.WaitsFor]; ok && j != i {
				next[i] = j
			}
		}
	}
	var cycles [][]string
	seen := map[int]bool{}
	for start := range next {
		if seen[start] {
			continue
		}
		path := []int{}
		onPath := map[int]int{}
		cur, ok := start, true
		for ok {
			if pos, hit := onPath[cur]; hit {
				cyc := path[pos:]
				names := make([]string, 0, len(cyc)+1)
				for _, i := range cyc {
					names = append(names, d.Threads[i].Name)
					seen[i] = true
				}
				names = append(names, d.Threads[cyc[0]].Name)
				cycles = append(cycles, names)
				break
			}
			if seen[cur] {
				break
			}
			onPath[cur] = len(path)
			path = append(path, cur)
			cur, ok = next[cur]
		}
		for _, i := range path {
			seen[i] = true
		}
	}
	sort.Slice(cycles, func(i, j int) bool { return strings.Join(cycles[i], "→") < strings.Join(cycles[j], "→") })
	return cycles
}

// Render prints the analysis in v1 analyze.sh's exact voice: "    " lines,
// "    ⚠ " flags. Returns the number of flags raised.
func (a Analysis) Render(w io.Writer) int {
	flags := 0
	say := func(s string) { fmt.Fprintf(w, "    %s\n", s) }
	flag := func(s string) { fmt.Fprintf(w, "    ⚠ %s\n", s); flags++ }

	// 0 parsed threads is NEVER "nothing alarming" — it means this file is
	// not a thread dump this parser can read. Refuse to bless it.
	if a.Total == 0 {
		if a.PlainFormatHeads > 0 {
			flag(fmt.Sprintf("unreadable FORMAT, not an empty JVM: this looks like a JDK-21 `jcmd Thread.dump_to_file` plain dump (%d thread headers seen) — this parser doesn't read that format", a.PlainFormatHeads))
			say("the capture may be fine; the analyzer isn't. Open the file directly, or recapture: jdebug threads")
		} else {
			flag("0 threads parsed — this does not look like a thread dump (garbage, truncated, or an unsupported format)")
			say("treat this as a FAILED capture: recapture (jdebug threads) and inspect the file before trusting any tool's read of it")
		}
		return flags
	}
	say(fmt.Sprintf("%d threads — %d RUNNABLE · %d BLOCKED · %d WAITING · %d TIMED_WAITING",
		a.Total, a.Runnable, a.Blocked, a.Waiting, a.TimedWaiting))
	if a.VirtualApp {
		flag("this dump is STRUCTURALLY INCOMPLETE: the app uses VIRTUAL threads, which never appear in actuator/ThreadMXBean dumps — the request-handling threads are invisible here")
		say("full picture (JDK 21+): jdebug jcmd 'Thread.dump_to_file -format=json /tmp/vt.json' — then read it directly")
	}
	switch {
	case len(a.DeadlockCycles) > 0:
		for _, cyc := range a.DeadlockCycles {
			flag(fmt.Sprintf("DEADLOCK detected — lock cycle: %s (found by lock-graph analysis, so this works on actuator dumps too — they carry no jstack banner)",
				strings.Join(cyc, " → ")))
		}
	case a.JstackBanner:
		flag("DEADLOCK detected — open the 'Found one Java-level deadlock' section of the file")
	}
	if a.Blocked > 0 {
		flag(fmt.Sprintf("%d thread(s) BLOCKED — lock contention. Most-contended locks:", a.Blocked))
		for i, l := range a.ContendedLocks {
			if i >= 3 {
				break
			}
			fmt.Fprintf(w, "      %d %s\n", l.Count, l.Desc)
		}
	}
	if a.HotFrame != "" && a.HotFrameCount >= 3 {
		flag(fmt.Sprintf("hot frame: %d× %s — that many threads truly running the same code is a real hot spot; profile it",
			a.HotFrameCount, a.HotFrame))
	}
	if a.IdleRunnable > 0 {
		say(fmt.Sprintf("%d RUNNABLE thread(s) are event-loop/selector threads parked in native I/O (epoll/kqueue) — idle, not load", a.IdleRunnable))
	}
	if flags == 0 {
		say("nothing alarming — mostly waiting/idle threads is normal for a pool-based app")
	}
	say("deeper: open the file in VisualVM (free, runs locally — visualvm.github.io)")
	return flags
}
