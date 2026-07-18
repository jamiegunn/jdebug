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
// the waits-for graph and detecting cycles, which works identically on
// every format we can parse.

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
}

var (
	reThreadHead = regexp.MustCompile(`^"([^"]+)"`)
	reState      = regexp.MustCompile(`java\.lang\.Thread\.State:\s+([A-Z_]+)`)
	reWaiting    = regexp.MustCompile(`-\s+waiting to lock <([^>]+)>(.*)`)
	reLocked     = regexp.MustCompile(`-\s+locked <([^>]+)>`)
	reFrame      = regexp.MustCompile(`^\s*at\s+(\S+)`)
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
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Java-level deadlock") {
			d.JstackBanner = true
			cur = nil
			continue
		}
		if m := reThreadHead.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			d.Threads = append(d.Threads, ThreadInfo{Name: m[1]})
			cur = &d.Threads[len(d.Threads)-1]
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case reState.MatchString(line):
			cur.State = reState.FindStringSubmatch(line)[1]
		case reWaiting.MatchString(line):
			m := reWaiting.FindStringSubmatch(line)
			cur.WaitsFor = m[1]
			cur.WaitsDesc = "waiting to lock <" + m[1] + ">" + strings.TrimRight(m[2], " ")
		case reLocked.MatchString(line):
			cur.Holds = append(cur.Holds, reLocked.FindStringSubmatch(line)[1])
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
}

// LockContention is one lock and how many threads block on it.
type LockContention struct {
	Desc  string // "waiting to lock <id> (a Cls)"
	Count int
}

// Analyze computes the verdict.
func (d ThreadDump) Analyze() Analysis {
	a := Analysis{Total: len(d.Threads), JstackBanner: d.JstackBanner}
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
		if t.State != "BLOCKED" && t.LockOwner == "" {
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

	say(fmt.Sprintf("%d threads — %d RUNNABLE · %d BLOCKED · %d WAITING · %d TIMED_WAITING",
		a.Total, a.Runnable, a.Blocked, a.Waiting, a.TimedWaiting))
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
