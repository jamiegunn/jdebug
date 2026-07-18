package core

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// fetch-heap — the close of audit finding F7. Every live-capture tier needs
// a running, exec-able container, but the most common JVM incident (the
// OOMKilled crash loop) doesn't offer one at the moment that matters. The
// only heap that exists for the OOM itself is the one the JVM wrote on its
// way down (-XX:+HeapDumpOnOutOfMemoryError -XX:HeapDumpPath=<volume>).
// This retrieves it: from the restarted container (an emptyDir survives
// container restarts for the pod's lifetime) or from a sibling sharing the
// volume — size-verified through the same pipeline as every other capture.

// HeapDumpHint is what the pod spec says about on-crash dumps.
type HeapDumpHint struct {
	FlagSet  bool   // -XX:+HeapDumpOnOutOfMemoryError visible in env
	DumpPath string // -XX:HeapDumpPath=... value ("" = unset → JVM cwd)
}

// InspectHeapDumpConfig reads the pod spec's env (JAVA_TOOL_OPTIONS et al)
// for the on-crash dump flags. Flags baked into the image's entrypoint are
// invisible here — callers should say so rather than claim certainty.
func InspectHeapDumpConfig(ctx context.Context, c Cluster, ns, pod string) (HeapDumpHint, error) {
	raw, err := c.PodJSON(ctx, ns, pod)
	if err != nil {
		return HeapDumpHint{}, err
	}
	var spec struct {
		Spec struct {
			Containers []struct {
				Env []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return HeapDumpHint{}, fmt.Errorf("unparseable pod spec: %w", err)
	}
	var h HeapDumpHint
	for _, ct := range spec.Spec.Containers {
		for _, e := range ct.Env {
			if strings.Contains(e.Value, "HeapDumpOnOutOfMemoryError") {
				h.FlagSet = true
			}
			if i := strings.Index(e.Value, "HeapDumpPath="); i >= 0 {
				rest := e.Value[i+len("HeapDumpPath="):]
				if j := strings.IndexAny(rest, " \t"); j >= 0 {
					rest = rest[:j]
				}
				h.DumpPath = rest
			}
		}
	}
	return h, nil
}

// FoundDump is one on-crash hprof discovered in the pod.
type FoundDump struct {
	Path  string
	Bytes int64
	MTime int64 // unix seconds — newest wins
}

// FindHeapDumps searches the pod for *.hprof files. Search order: the
// spec-declared HeapDumpPath first, then the conventional spots. Depth is
// bounded — this must stay cheap on a hurting pod.
func FindHeapDumps(ctx context.Context, c Cluster, t Resolved, hint HeapDumpHint, extraDirs []string) ([]FoundDump, error) {
	// an EXPLICIT path from the operator means "search exactly here" —
	// no defaults mixed in
	dirs := []string{}
	if len(extraDirs) > 0 {
		dirs = append(dirs, extraDirs...)
	} else {
		if hint.DumpPath != "" {
			// HeapDumpPath may be a file or a directory — search its directory
			d := hint.DumpPath
			if strings.HasSuffix(d, ".hprof") {
				d = filepath.Dir(d)
			}
			dirs = append(dirs, d)
		}
		dirs = append(dirs, "/tmp", "/dumps", "/heap-dumps", "/var/log")
	}
	seen := map[string]bool{}
	uniq := dirs[:0]
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			uniq = append(uniq, d)
		}
	}
	// one find, bounded depth: emit mtime + size per file. `stat -c %Y` covers
	// both GNU coreutils and busybox (alpine); when stat is absent/unsupported
	// mtime falls back to 0 and the sort degrades to size, never erroring.
	script := "for d in " + strings.Join(uniq, " ") + `; do
  [ -d "$d" ] || continue
  find "$d" -maxdepth 3 -name '*.hprof' -type f 2>/dev/null | while read -r f; do
    mt="$(stat -c %Y "$f" 2>/dev/null || echo 0)"
    printf '%s\t%s\t%s\n' "$mt" "$(wc -c < "$f" | tr -d ' ')" "$f"
  done
done`
	out, err := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, "sh", "-c", script)
	if err != nil && out == "" {
		return nil, fmt.Errorf("could not search the pod for dumps: %w", err)
	}
	var found []FoundDump
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		mtime, _ := strconv.ParseInt(parts[0], 10, 64)
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		if parts[2] != "" {
			found = append(found, FoundDump{Path: parts[2], Bytes: size, MTime: mtime})
		}
	}
	// The dump from THIS crash is the NEWEST one — sort by mtime first so a
	// stale-but-larger dump from an earlier OOM never wins. Fall back to
	// largest (a full on-crash heap) then name when the pod's stat gave us no
	// mtime (all zero); repeated OOMs suffix _pid/_n names.
	sort.Slice(found, func(i, j int) bool {
		if found[i].MTime != found[j].MTime {
			return found[i].MTime > found[j].MTime
		}
		if found[i].Bytes != found[j].Bytes {
			return found[i].Bytes > found[j].Bytes
		}
		return found[i].Path > found[j].Path
	})
	return found, nil
}

// FetchHeapAcquirer copies one discovered dump out through the pipeline —
// size-verified like every heap capture (kubectl cp is the truncation path).
type FetchHeapAcquirer struct {
	Remote FoundDump
	Log    Info
}

func (f FetchHeapAcquirer) Meta() Meta {
	return Meta{
		Name:    "heap-oncrash-" + filepath.Base(f.Remote.Path),
		Tier:    "on-crash",
		Command: "kubectl cp " + f.Remote.Path + " (JVM-written on OOM)",
	}
}

func (f FetchHeapAcquirer) Acquire(ctx context.Context, c Cluster, t Resolved, destPath string) (int64, error) {
	f.Log.p("fetching on-crash dump %s (%d bytes) from %s", f.Remote.Path, f.Remote.Bytes, t.Pod)
	if err := c.CopyFromPod(ctx, t.Namespace, t.Pod, t.Container, f.Remote.Path, destPath); err != nil {
		return 0, fmt.Errorf("kubectl cp failed: %w (the dump is still in the pod at %s)", err, f.Remote.Path)
	}
	return f.Remote.Bytes, nil
}

// ExplainNoDumps is the guidance when the hunt comes up empty — turning the
// structural gap into setup instructions instead of a shrug.
func ExplainNoDumps(hint HeapDumpHint) string {
	var b strings.Builder
	b.WriteString("no on-crash heap dumps found in the pod.\n")
	if !hint.FlagSet {
		b.WriteString(`  why: -XX:+HeapDumpOnOutOfMemoryError is not in this pod's env, so the JVM
       writes NO dump when it dies — there is nothing to fetch, and the next
       OOM will be lost too. (Flags baked into the image aren't visible here;
       verify live with: jdebug jcmd VM.flags)
  fix: add to the deployment and let it crash once more — THEN this command
       has something to retrieve:
         env:
           - name: JAVA_TOOL_OPTIONS
             value: "-XX:+HeapDumpOnOutOfMemoryError -XX:HeapDumpPath=/dumps"
         (mount an emptyDir at /dumps so the file survives the container restart)`)
	} else {
		b.WriteString(`  the flag IS set`)
		if hint.DumpPath != "" {
			b.WriteString(" (HeapDumpPath=" + hint.DumpPath + ")")
		}
		b.WriteString(`, but no .hprof was found. Usual causes:
    · the pod was REPLACED (new name) since the crash — dumps on an emptyDir die
      with the POD, not the container. Check siblings/the replacement pod, or
      use a PVC for HeapDumpPath so dumps outlive rescheduling.
    · HeapDumpPath points somewhere unwritable — the JVM logs a write failure
      to stderr on its way down: jdebug logs --previous
    · the dump was already cleaned up.`)
	}
	return b.String()
}
