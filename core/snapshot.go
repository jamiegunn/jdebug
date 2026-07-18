package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Snapshot — the one-shot offline-analysis bundle (v1 observe/snapshot.sh),
// orchestrated by the core. Sections are best-effort: a failed capture is
// noted inside its own file and the rest continues; the manifest records
// every section's verdict, so "which parts of this bundle can I trust" is
// answerable later (something v1's bundle could not tell you).
//
// During migration the kubernetes-layer sections (why/security/memory)
// still run the v1 bash reporters — they are observe-side and retire in
// Phase 3; the capture-side sections are all core-native.

// SnapshotOpts configures one bundle run.
type SnapshotOpts struct {
	WantHeap  bool // include heap.hprof — PAUSES the JVM (caller enforces --confirm)
	NoJattach bool // skip the jattach jcmd sections
	Log       Info
	// JattachFactory builds the acquirer used for the jcmd sections (lets
	// the CLI wire vendor dir / --binary / artifact recording once).
	JattachFactory func(kind string) *JattachAcquirer
}

// SnapshotResult summarizes a bundle run.
type SnapshotResult struct {
	Dir      string
	Captured int
	Failed   int
}

// Snapshot captures the bundle for a CONFIRMED target — the whole bundle is
// treated as destructive-capable (its --heap form pauses the JVM), so the
// ambiguity rule applies to all of it, exactly like v1 after the F8 fix.
func Snapshot(ctx context.Context, c Cluster, pipe Pipeline, cfg Config, t Confirmed, o SnapshotOpts) (SnapshotResult, error) {
	r := t.Resolved()
	ts := time.Now().UTC()
	var sess *Session
	var err error
	if pipe.OutDir != "" {
		sess, err = pipe.Store.SessionAt(pipe.OutDir, r.Pod, ts)
	} else {
		sess, err = pipe.Store.Session(r.Pod, ts)
	}
	if err != nil {
		return SnapshotResult{}, err
	}
	// the marker that makes `jdebug dumps` call this a snapshot bundle
	if err := os.WriteFile(filepath.Join(sess.Dir, ".snapshot"), nil, 0o600); err != nil {
		return SnapshotResult{}, err
	}
	res := SnapshotResult{Dir: sess.Dir}
	o.Log.p("snapshot of pod %s → %s", r.Pod, sess.Dir)

	// stepV: run one section; validate (when a validator is given) so a 401
	// login page or marker-less "thread dump" can't record as ✔; hash/stat
	// only AFTER any failure-note overwrite, so the manifest always describes
	// the file as it exists on disk — a manifest whose hashes fail against
	// its own files is the inverse of provenance.
	stepV := func(name, what string, validate Validator, fill func(w *os.File) error) {
		path := filepath.Join(sess.Dir, name)
		f, ferr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if ferr != nil {
			res.Failed++
			return
		}
		serr := fill(f)
		_ = f.Close()
		if serr == nil && validate != nil {
			if v := validate(path, 0); !v.OK {
				serr = fmt.Errorf("captured content failed validation: %s", v.Reason)
			}
		}
		art := Artifact{Name: name, Tier: "snapshot", Command: what, CapturedAt: time.Now().UTC(), Path: path}
		if serr != nil {
			res.Failed++
			// the failure note lives IN the file, like v1 — an empty file
			// must never masquerade as a clean capture
			_ = os.WriteFile(path, []byte("CAPTURE FAILED: "+what+"\n--- error ---\n"+serr.Error()+"\n"), 0o600)
			art.Verdict = Verdict{OK: false, Reason: serr.Error()}
			o.Log.p("  ✘ %s  (%s) — failed, details inside", name, what)
		} else {
			res.Captured++
			art.Verdict = Verdict{OK: true}
			o.Log.p("  ✔ %s  (%s)", name, what)
		}
		if st, err := os.Stat(path); err == nil {
			art.Bytes = st.Size()
		}
		art.SHA256, _ = fileSHA256(path)
		_ = sess.Append(art)
	}
	step := func(name, what string, fill func(w *os.File) error) { stepV(name, what, nil, fill) }

	execPodTo := func(w *os.File, script string) error {
		return c.ExecPod(ctx, r.Namespace, r.Pod, r.Container, w, "sh", "-c", script)
	}
	// kit bash reporters (observe side — retire in Phase 3)
	kitScript := func(w *os.File, rel string) error {
		cmd := exec.CommandContext(ctx, filepath.Join(cfg.KitRoot, rel),
			"-n", r.Namespace, "-l", r.Selector, "--container", r.Container, r.Pod)
		cmd.Stdout = w
		var errb strings.Builder
		cmd.Stderr = &errb
		if err := cmd.Run(); err != nil {
			first := strings.SplitN(strings.TrimSpace(errb.String()), "\n", 2)[0]
			return fmt.Errorf("%s: %s", rel, first)
		}
		return nil
	}

	step("pod.txt", "kubectl describe pod", func(w *os.File) error {
		return c.DescribePod(ctx, r.Namespace, r.Pod, w)
	})
	step("why.txt", "pod deep-dive (limits/probes/exit codes/HPA)", func(w *os.File) error {
		return kitScript(w, "observe/why.sh")
	})
	step("security.txt", "pod security posture", func(w *os.File) error {
		return kitScript(w, "observe/security.sh")
	})
	// health: NO -f — a DOWN health is a 503 WITH the diagnostic body, and
	// that body is exactly what an incident snapshot needs. But it DOES get
	// auth (like every other section) and a content check: a 401 login page
	// must never be recorded "✔ health.json".
	validateHealth := func(path string, _ int64) Verdict {
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return Verdict{false, "unreadable: " + rerr.Error()}
		}
		if !strings.Contains(string(b), `"status"`) {
			return Verdict{false, "doesn't look like actuator health output — " + classifyHead(path)}
		}
		return Verdict{OK: true}
	}
	stepV("health.json", "actuator health", validateHealth, func(w *os.File) error {
		return execPodTo(w, fmt.Sprintf(
			`if command -v curl >/dev/null 2>&1; then curl -sS %s'%s/health'; else wget -qO- %s'%s/health'; fi`,
			podAuthFlags("curl", cfg.ActuatorAuth), cfg.ActuatorBase,
			podAuthFlags("wget", cfg.ActuatorAuth), cfg.ActuatorBase))
	})
	step("metrics.json", "actuator metrics index", func(w *os.File) error {
		return execPodTo(w, PodFetchScript(cfg.ActuatorBase+"/metrics", "", cfg.ActuatorAuth))
	})
	stepV("threads.txt", "actuator threaddump (text)", ValidateThreadDump, func(w *os.File) error {
		return execPodTo(w, PodFetchScript(cfg.ActuatorBase+"/threaddump", "text/plain", cfg.ActuatorAuth))
	})
	step("memory-report.txt", "memory anatomy", func(w *os.File) error {
		return kitScript(w, "observe/memory-report.sh")
	})

	if !o.NoJattach && o.JattachFactory != nil {
		jat := func(name, cmd string) {
			step(name, "jcmd "+cmd, func(w *os.File) error {
				return JcmdRun(ctx, c, r, o.JattachFactory(""), cmd, w)
			})
		}
		jat("gc-heap-info.txt", "GC.heap_info")
		jat("vm-flags.txt", "VM.flags")
		jat("codecache.txt", "Compiler.codecache")
		jat("classloaders.txt", "VM.classloader_stats")
		jat("nmt-summary.txt", "VM.native_memory summary")
	} else if o.NoJattach {
		o.Log.p("  - skipping jattach sections (--no-jattach)")
	}

	if o.WantHeap {
		o.Log.p("  capturing heap dump via actuator (PAUSES the JVM)...")
		heapPipe := pipe
		heapPipe.OutDir = sess.Dir // the bundle IS the session
		acq := ActuatorAcquirer{Kind: "heap", Base: cfg.ActuatorBase, Auth: cfg.ActuatorAuth, Log: o.Log, Name: "heap.hprof"}
		if art, err := heapPipe.RunDestructive(ctx, acq, t, ValidateHprof); err != nil {
			res.Failed++
			o.Log.p("  ✘ heap.hprof — actuator heapdump failed (try: jdebug heap --via jattach --confirm)")
		} else {
			res.Captured++
			o.Log.p("  ✔ heap.hprof (%d bytes)", art.Bytes)
		}
	}

	o.Log.p("snapshot complete: %s  (%d captured, %d failed)", sess.Dir, res.Captured, res.Failed)
	return res, nil
}
