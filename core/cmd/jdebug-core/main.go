// jdebug-core — the v2 engine's CLI. The bash dispatcher (jdebug) routes the
// capture verbs here when this binary is built (make core); JDEBUG_V1=1
// forces the bash implementations. Exit codes match v1 exactly:
//
//	0  capture succeeded
//	1  every tier failed (cascade), a forced tier failed, an analyzed file
//	   was unreadable as a dump, or fetch-heap found nothing to fetch
//	2  target resolution failed (no pod / ambiguous destructive match)
//	64 usage error or a missing --confirm gate
//
// (No other codes: 3 is the DISPATCHER's "environment problem" and must not
// be reused here — `jdebug fetch-heap; echo $?` has one meaning per code.)
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	core "github.com/jamiegunn/jdebug/core"
)

func errf(format string, a ...any) { fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...) }
func infof(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "["+time.Now().Format("15:04:05")+"] "+format+"\n", a...)
}

type opts struct {
	verb      string
	via       string // "" = auto-cascade
	confirm   bool
	json      bool
	binary    string
	jcmdArg   string
	pod       string
	wantHeap  bool // snapshot --heap
	noJattach bool // snapshot --no-jattach
	cfg       core.Config
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: jdebug-core {threads|heap|jcmd} [--via actuator|jattach|jdk] [--confirm]
                   [--json] [--binary <jattach>] [-n ns] [-l selector] [--container c]
                   [--actuator-base url] [pod] -- jcmd also takes a command string`)
}

func parse(args []string) (opts, int) {
	o := opts{cfg: core.LoadConfig()}
	if len(args) == 0 {
		usage()
		return o, 64
	}
	o.verb = args[0]
	rest := args[1:]
	var pos []string
	for i := 0; i < len(rest); i++ {
		next := func() (string, bool) {
			if i+1 >= len(rest) {
				errf("%s needs a value", rest[i])
				return "", false
			}
			i++
			return rest[i], true
		}
		switch rest[i] {
		case "-n", "--namespace":
			v, ok := next()
			if !ok {
				return o, 64
			}
			o.cfg.Target.Namespace = v
		case "-l", "--selector":
			v, ok := next()
			if !ok {
				return o, 64
			}
			o.cfg.Target.Selector = v
		case "--container":
			v, ok := next()
			if !ok {
				return o, 64
			}
			o.cfg.Target.Container = v
		case "--actuator-base":
			v, ok := next()
			if !ok {
				return o, 64
			}
			o.cfg.ActuatorBase = v
		case "--via":
			v, ok := next()
			if !ok {
				return o, 64
			}
			o.via = v
		case "--binary":
			v, ok := next()
			if !ok {
				return o, 64
			}
			o.binary = v
		case "--confirm":
			o.confirm = true
		case "--json":
			o.json = true
		case "--heap":
			o.wantHeap = true
		case "--no-jattach":
			o.noJattach = true
		case "-h", "--help":
			usage()
			return o, -1 // handled: exit 0
		default:
			pos = append(pos, rest[i])
		}
	}
	switch o.verb {
	case "jcmd":
		if len(pos) > 0 {
			o.jcmdArg = pos[0]
			pos = pos[1:]
		}
		if o.jcmdArg == "" {
			errf("jcmd action requires a command string (e.g. 'GC.heap_info')")
			return o, 64
		}
	case "diff-memory":
		// two positionals: before, after — stashed in jcmdArg + pod
		if len(pos) > 0 {
			o.jcmdArg = pos[0]
			pos = pos[1:]
		}
	case "fetch-heap":
		// optional first positional: an in-pod path/dir to search; a second
		// positional (if any) is the pod name, same as other verbs
		if len(pos) > 0 && strings.HasPrefix(pos[0], "/") {
			o.jcmdArg = pos[0]
			pos = pos[1:]
		}
	}
	if len(pos) > 0 {
		o.pod = pos[0]
	}
	return o, 0
}

func main() {
	o, rc := parse(os.Args[1:])
	if rc == -1 {
		return
	}
	if rc != 0 {
		os.Exit(rc)
	}
	switch o.verb {
	case "threads", "heap", "jcmd", "snapshot", "fetch-heap":
	case "analyze-threads":
		// offline: parse + analyze one dump file (text or actuator JSON);
		// prints analyze.sh-voice lines, then "__FLAGS__ N" for the caller.
		if o.pod == "" {
			errf("analyze-threads needs a dump file")
			os.Exit(64)
		}
		f, err := os.Open(o.pod)
		if err != nil {
			errf("%v", err)
			os.Exit(2)
		}
		defer f.Close()
		d, err := core.ParseThreadDump(f)
		if err != nil {
			errf("%v", err)
			os.Exit(1)
		}
		a := d.Analyze()
		n := a.Render(os.Stdout)
		fmt.Printf("__FLAGS__ %d\n", n)
		// 0 parsed threads = this was not a readable thread dump; a clean
		// exit here would let scripts mistake garbage for health (I.3).
		if a.Total == 0 {
			os.Exit(1)
		}
		return
	case "diff-memory":
		// offline: growth between two jdebug memory reports (F9)
		if o.pod == "" || o.jcmdArg == "" {
			errf("diff-memory needs two memory-report files: <before> <after>")
			os.Exit(64)
		}
		before, err := parseMemFile(o.jcmdArg) // first positional
		if err != nil {
			errf("before: %v", err)
			os.Exit(1)
		}
		after, err := parseMemFile(o.pod) // second positional
		if err != nil {
			errf("after: %v", err)
			os.Exit(1)
		}
		core.DiffMemReports(before, after, os.Stdout)
		return
	default:
		errf("unknown command: %s", o.verb)
		usage()
		os.Exit(64)
	}
	if o.via != "" && o.via != "actuator" && o.via != "jattach" && o.via != "jdk" {
		errf("unknown --via '%s' (actuator|jattach|jdk)", o.via)
		os.Exit(64)
	}
	// The confirm gate fires ONCE, before any tier — a missing flag must
	// never read as a triple tier failure (v1 finding F3).
	if o.verb == "heap" && !o.confirm {
		errf("heap dumps pause the JVM (destructive in production). Re-run with --confirm.")
		os.Exit(64)
	}
	if o.verb == "snapshot" && o.wantHeap && !o.confirm {
		errf("--heap pauses the JVM (destructive in production). Add --confirm.")
		os.Exit(64)
	}

	o.cfg.Announce()
	// Ctrl-C (or a dispatcher SIGTERM) cancels the context: the running
	// kubectl child is killed and the error path still prints its retry
	// hints (e.g. where an in-pod heap copy survives). A second Ctrl-C
	// force-kills (NotifyContext stops relaying once canceled).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Optional global budget so no capture can hang an incident call:
	// JDEBUG_TIMEOUT accepts Go durations ("90s", "5m"). Unset/0 = no limit
	// (multi-GB heap dumps legitimately take minutes — do not guess one).
	if v := os.Getenv("JDEBUG_TIMEOUT"); v != "" && v != "0" {
		if d, derr := time.ParseDuration(v); derr == nil && d > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		} else {
			errf("ignoring invalid JDEBUG_TIMEOUT=%q (want a duration like 90s or 5m)", v)
		}
	}
	cluster := core.Kubectl{}

	t := o.cfg.Target
	t.Pod = o.pod
	resolved, err := core.Resolve(ctx, cluster, t)
	if err != nil {
		errf("%v — pass -n/-l", err)
		os.Exit(2)
	}
	// record the RESOLVED pod so artifact bookkeeping (remote-artifacts.tsv)
	// can name it — cleanup needs the pod column to remove staged binaries.
	o.cfg.Target.Pod = resolved.Pod
	// destructive verbs refuse to guess among replicas (F8, typed)
	var confirmed core.Confirmed
	if o.verb == "heap" || o.verb == "snapshot" {
		confirmed, err = resolved.Confirm()
		if err != nil {
			errf("%d pods match and this operation PAUSES the JVM — refusing to guess which replica.", len(resolved.Matches))
			errf("  name the pod explicitly (e.g. the restarting one). Matching pods:")
			for _, p := range resolved.Matches {
				fmt.Fprintln(os.Stderr, "    "+p)
			}
			os.Exit(2)
		}
	} else if len(resolved.Matches) > 1 && !resolved.Explicit {
		infof("%d pods match — using %s. If you meant another (e.g. the restarting one), add its name:", len(resolved.Matches), resolved.Pod)
		for _, p := range resolved.Matches {
			fmt.Fprintln(os.Stderr, "           "+p)
		}
	}

	store := &core.Store{Root: o.cfg.DumpsRoot}
	pipe := core.Pipeline{Cluster: cluster, Store: store, OutDir: o.cfg.OutDir}

	if o.verb == "jcmd" {
		j := o.newJattach("")
		if err := core.JcmdRun(ctx, cluster, resolved, j, o.jcmdArg, os.Stdout); err != nil {
			errf("%v", err)
			os.Exit(1)
		}
		return
	}

	if o.verb == "fetch-heap" {
		// F7: retrieve the heap the JVM wrote on its way down. Read-only,
		// non-destructive — works on the restarted container or a sibling
		// sharing the dump volume. o.jcmdArg (first positional) may name a
		// remote path or directory to search explicitly.
		hint, herr := core.InspectHeapDumpConfig(ctx, cluster, resolved.Namespace, resolved.Pod)
		if herr != nil {
			infof("couldn't read the pod spec for HeapDumpPath (%v) — searching the usual spots", herr)
		}
		var extra []string
		if o.jcmdArg != "" {
			extra = append(extra, o.jcmdArg)
		}
		dumps, ferr := core.FindHeapDumps(ctx, cluster, resolved, hint, extra)
		if ferr != nil {
			errf("%v", ferr)
			os.Exit(1)
		}
		if len(dumps) == 0 {
			errf("%s", core.ExplainNoDumps(hint))
			// 1, not 3: the dispatcher already uses 3 for "environment
			// problem" (cluster unreachable) — one meaning per exit code.
			os.Exit(1)
		}
		infof("found %d on-crash dump(s); fetching the newest:", len(dumps))
		for _, d := range dumps {
			fmt.Fprintf(os.Stderr, "    %10d  %s\n", d.Bytes, d.Path)
		}
		art, err := pipe.Run(ctx, core.FetchHeapAcquirer{Remote: dumps[0], Log: infof}, resolved, core.ValidateHprof)
		if err != nil {
			errf("%v", err)
			os.Exit(1)
		}
		infof("wrote %s (%d bytes, verified: hprof magic + size match)", art.Path, art.Bytes)
		infof("this is the heap AT THE MOMENT OF THE OOM — analyze: jdebug analyze, or Eclipse MAT 'Leak Suspects'")
		return
	}

	if o.verb == "snapshot" {
		res, err := core.Snapshot(ctx, cluster, pipe, o.cfg, confirmed, core.SnapshotOpts{
			WantHeap: o.wantHeap, NoJattach: o.noJattach, Log: infof,
			JattachFactory: func(kind string) *core.JattachAcquirer { return o.newJattach(kind) },
		})
		if err != nil {
			errf("%v", err)
			os.Exit(1)
		}
		infof("next steps:")
		infof("  threads.txt      → VisualVM (free, runs locally — your dumps never leave your machine)")
		infof("  heap.hprof       → Eclipse MAT: ParseHeapDump.sh heap.hprof org.eclipse.mat.api:suspects")
		infof("  memory-report.txt→ compare against an earlier snapshot: jdebug analyze --diff <old> <new>")
		if res.Failed > 0 {
			os.Exit(1)
		}
		return
	}

	runTier := func(tier string) error {
		acq, validate := o.acquirer(tier)
		var art core.Artifact
		var err error
		if o.verb == "heap" {
			art, err = pipe.RunDestructive(ctx, acq, confirmed, validate)
		} else {
			art, err = pipe.Run(ctx, acq, resolved, validate)
		}
		if err != nil {
			// a validated-then-failed jattach heap leaves the in-pod copy
			// behind for a retry — say where
			if j, ok := acq.(*core.JattachAcquirer); ok && j.RemoteDumpPath() != "" {
				errf("the in-pod copy is still at %s:%s — retry:", resolved.Pod, j.RemoteDumpPath())
				errf("  kubectl -n %s cp %s:%s <local> -c %s", resolved.Namespace, resolved.Pod, j.RemoteDumpPath(), resolved.Container)
			}
			return err
		}
		// success — the epilogue v1 users know. art.Path is the file's REAL
		// location (the pipeline's session dir); reconstructing it from
		// CapturedAt printed a nonexistent path whenever the capture crossed
		// a second boundary — i.e. on essentially every heap dump.
		path := art.Path
		if path == "" { // defensive: older Artifact without Path
			path = filepath.Join(o.cfg.DumpsRoot, "pods", resolved.Pod, art.Name)
		}
		if o.verb == "heap" {
			infof("wrote %s (%d bytes, verified: hprof magic + size match)", path, art.Bytes)
			infof("analyze: Eclipse MAT → File → Open Heap Dump → 'Leak Suspects' (or VisualVM)")
		} else {
			infof("wrote %s (%d bytes, validated)", path, art.Bytes)
			infof("analyze: open it in VisualVM (free, runs locally — visualvm.github.io) and look for deadlocks & blocked pools")
		}
		if j, ok := acq.(*core.JattachAcquirer); ok {
			j.CleanupRemote(ctx, cluster, resolved)
		}
		return nil
	}

	if o.via != "" {
		if err := runTier(o.via); err != nil {
			errf("%v", err)
			os.Exit(1)
		}
		return
	}
	// auto-cascade actuator → jattach → jdk, announcing each fallback
	if err := runTier("actuator"); err == nil {
		return
	} else {
		errf("%v", err)
	}
	infof("⚠ actuator tier failed — auto-falling back to jattach (needs no actuator)…")
	if err := runTier("jattach"); err == nil {
		return
	} else {
		errf("%v", err)
	}
	infof("⚠ jattach tier failed — auto-falling back to an ephemeral JDK container…")
	if err := runTier("jdk"); err == nil {
		return
	} else {
		errf("%v", err)
	}
	errf("all capture tiers failed (actuator → jattach → jdk); see the errors above.")
	os.Exit(1)
}

func (o opts) newJattach(kind string) *core.JattachAcquirer {
	vendor := os.Getenv("JATTACH_VENDOR_DIR")
	if vendor == "" {
		vendor = filepath.Join(o.cfg.KitRoot, "vendor", "jattach")
	}
	binary := o.binary
	if binary == "" {
		binary = os.Getenv("JATTACH_BINARY")
	}
	return &core.JattachAcquirer{
		Kind:       kind,
		VendorDir:  vendor,
		Binary:     binary,
		RemotePath: os.Getenv("JATTACH_REMOTE_PATH"),
		Log:        infof,
		RecordArtifact: func(owned bool, path, note string) {
			recordArtifact(o.cfg, owned, path, note)
		},
	}
}

func (o opts) acquirer(tier string) (core.Acquirer, core.Validator) {
	validate := core.ValidateThreadDump
	if o.verb == "heap" {
		validate = core.ValidateHprof
	} else if o.json && tier == "actuator" {
		// Spring's JSON thread dump has no "Full thread dump" marker — the
		// text validator would fail EVERY valid JSON capture and blame the
		// actuator for it. JSON gets a JSON-aware validator.
		validate = core.ValidateThreadDumpJSON
	}
	switch tier {
	case "actuator":
		return core.ActuatorAcquirer{Kind: o.verb, JSON: o.json, Base: o.cfg.ActuatorBase, Auth: o.cfg.ActuatorAuth, Log: infof}, validate
	case "jattach":
		return o.newJattach(o.verb), validate
	default:
		return core.JDKAcquirer{Kind: o.verb, Log: infof}, validate
	}
}

func parseMemFile(path string) (map[string]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return core.ParseMemReport(f)
}

// recordArtifact appends to the same remote-artifacts.tsv v1's cleanup
// command reads: owned \t ns \t pod \t container \t path \t note, deduped.
// The POD column must be real — cleanup runs `kubectl exec <pod>` on it; an
// empty pod strands the staged binary in production forever. And the dedup
// key must include ns+pod, or staging into a SECOND pod goes unrecorded.
func recordArtifact(cfg core.Config, owned bool, path, note string) {
	mf := filepath.Join(cfg.DumpsRoot, "remote-artifacts.tsv")
	_ = os.MkdirAll(cfg.DumpsRoot, 0o700)
	key := "\t" + cfg.Target.Namespace + "\t" + cfg.Target.Pod + "\t" + cfg.Target.Container + "\t" + path + "\t"
	if b, err := os.ReadFile(mf); err == nil && strings.Contains(string(b), key) {
		return
	}
	ownedStr := "0"
	if owned {
		ownedStr = "1"
	}
	f, err := os.OpenFile(mf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\t%s\n", ownedStr, cfg.Target.Namespace, cfg.Target.Pod, cfg.Target.Container, path, note)
}
