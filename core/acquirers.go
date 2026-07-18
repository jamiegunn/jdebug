package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The three capture tiers as pipeline Acquirers. Tiers only fetch bytes —
// validation and manifest bookkeeping belong to the pipeline (capture.go),
// which is what makes the v1 asymmetry (F1/F5) unrepresentable.

// Info is the tier's progress logger (v1's `info` lines). Nil-safe.
type Info func(format string, a ...any)

func (f Info) p(format string, a ...any) {
	if f != nil {
		f(format, a...)
	}
}

// --- tier 1: actuator -------------------------------------------------------

// ActuatorAcquirer captures via Spring Boot actuator over the pod's own
// HTTP client. Kind: "threads" (text or JSON) or "heap".
type ActuatorAcquirer struct {
	Kind string // threads | heap
	JSON bool   // threads only: Spring's structured format
	Base string // e.g. http://localhost:8080/actuator
	Auth string // bearer:VAR | basic:U:P ("" = none)
	Name string // artifact filename override ("" = tier default)
	Log  Info
}

func (a ActuatorAcquirer) Meta() Meta {
	name, url := "threads-actuator.txt", a.Base+"/threaddump"
	if a.Kind == "heap" {
		name, url = "heap-actuator.hprof", a.Base+"/heapdump"
	} else if a.JSON {
		name = "threads-actuator.json"
	}
	if a.Name != "" {
		name = a.Name
	}
	return Meta{Name: name, Tier: "actuator", Command: "<curl-or-wget> " + url + " (in the pod)"}
}

func (a ActuatorAcquirer) Acquire(ctx context.Context, c Cluster, t Resolved, destPath string) (int64, error) {
	url, accept := a.Base+"/threaddump", "text/plain"
	if a.Kind == "heap" {
		url, accept = a.Base+"/heapdump", ""
	} else if a.JSON {
		accept = "application/json"
	}
	a.Log.p("  $ kubectl -n %s exec %s -c %s -- sh -c '<curl-or-wget> %s' > %s", t.Namespace, t.Pod, t.Container, url, destPath)
	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, f, "sh", "-c", PodFetchScript(url, accept, a.Auth)); err != nil {
		// classify: secured vs absent vs wedged — the ONE precise next action
		code, _ := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, "sh", "-c", PodHTTPStatusScript(url, a.Auth))
		code = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, code)
		return 0, fmt.Errorf("%w\n%s", err, explainActuatorFail(code))
	}
	return 0, nil
}

func explainActuatorFail(code string) string {
	switch code {
	case "401", "403":
		return "  secured (HTTP " + code + "): the actuator needs credentials.\n" +
			"    → set auth (bearer:ENV_VAR or basic:USER_VAR:PASS_VAR, naming the pod's OWN env vars),\n" +
			"    → or skip HTTP entirely: --via jattach"
	case "404":
		return "  not found (HTTP 404): nothing is served at this path.\n" +
			"    → the actuator may be disabled, on a different base path, or behind management.server.port.\n" +
			"    → fix the URL (--actuator-base), or skip HTTP: --via jattach"
	case "", "000":
		return "  no HTTP reply: the app isn't serving (wedged, still starting, or actuator off).\n" +
			"    → skip HTTP entirely: --via jattach"
	default:
		return "  the actuator returned HTTP " + code + ".\n    → try a no-HTTP route: --via jattach"
	}
}

// --- tier 2: jattach ---------------------------------------------------------

// JattachAcquirer captures through the vendored jattach binary: installed
// into the pod (checksum-verified first), aimed at the JVM PID found in
// /proc. Kind: "threads" or "heap". JcmdRun (below) shares the plumbing.
type JattachAcquirer struct {
	Kind       string
	VendorDir  string // <kit>/vendor/jattach
	Binary     string // explicit --binary/$JATTACH_BINARY override ("" = vendored)
	RemotePath string // default /tmp/jattach
	Log        Info
	// RecordArtifact is called when jattach is staged into the pod
	// (owned=true) or found pre-existing (owned=false) — cleanup bookkeeping.
	RecordArtifact func(owned bool, path, note string)

	// pendingRemote: set by a heap Acquire — where the in-pod copy still
	// lives. Cleaned up by CleanupRemote after the pipeline validates, or
	// left in place (with the path in the error) for a manual retry.
	pendingRemote string
}

func (j *JattachAcquirer) Meta() Meta {
	if j.Kind == "heap" {
		return Meta{Name: "heap-jattach.hprof", Tier: "jattach", Command: "jattach <pid> dumpheap"}
	}
	return Meta{Name: "threads-jattach.txt", Tier: "jattach", Command: "jattach <pid> jcmd 'Thread.print -l'"}
}

func (j *JattachAcquirer) remote() string {
	if j.RemotePath != "" {
		return j.RemotePath
	}
	return "/tmp/jattach"
}

func (j *JattachAcquirer) Acquire(ctx context.Context, c Cluster, t Resolved, destPath string) (int64, error) {
	pid, err := j.prepare(ctx, c, t)
	if err != nil {
		return 0, err
	}
	if j.Kind == "heap" {
		ts := time.Now().UTC().Format("20060102T150405Z")
		remoteDump := "/tmp/heap-jattach-" + ts + ".hprof"
		j.Log.p("running jattach dumpheap (PAUSES JVM)")
		if err := c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, io.Discard, j.remote(), pid, "dumpheap", remoteDump); err != nil {
			return 0, fmt.Errorf("jattach dumpheap failed: %w", err)
		}
		sizeStr, serr := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, "sh", "-c", "wc -c < '"+remoteDump+"'")
		expected, _ := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64)
		if expected <= 0 {
			// The truncation gate needs the in-pod size. If we can't read it,
			// say so loudly — ValidateHprof will fall back to magic-only, which
			// does NOT catch a silently-truncated hprof (finding F1).
			j.Log.p("⚠ could not read the in-pod dump size (%v) — truncation check degraded to hprof-magic only", serr)
		}
		j.Log.p("copying %s -> %s", remoteDump, destPath)
		if err := c.CopyFromPod(ctx, t.Namespace, t.Pod, t.Container, remoteDump, destPath); err != nil {
			return 0, fmt.Errorf("kubectl cp failed: %w (the dump is still in the pod at %s)", err, remoteDump)
		}
		// The remote copy is removed only AFTER the pipeline validates —
		// see CleanupRemote; here we hand back the expected size so the
		// validator can catch kubectl cp truncation.
		j.pendingRemote = remoteDump
		return expected, nil
	}
	j.Log.p("running jattach jcmd 'Thread.print -l' on PID %s", pid)
	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, f, j.remote(), pid, "jcmd", "Thread.print -l"); err != nil {
		return 0, fmt.Errorf("jattach thread dump failed: %w", err)
	}
	return 0, nil
}

// prepare installs jattach (verified) if needed and finds the JVM PID.
func (j *JattachAcquirer) prepare(ctx context.Context, c Cluster, t Resolved) (string, error) {
	// already present?
	if err := c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, io.Discard, "test", "-x", j.remote()); err == nil {
		j.Log.p("jattach already present at %s (in %s)", j.remote(), t.Pod)
		if j.RecordArtifact != nil {
			j.RecordArtifact(false, j.remote(), "jattach (already in the pod)")
		}
	} else if err := j.install(ctx, c, t); err != nil {
		return "", err
	}
	pid, err := FindJVMPID(ctx, c, t)
	if err != nil {
		return "", err
	}
	j.Log.p("JVM PID inside pod: %s", pid)
	return pid, nil
}

func (j *JattachAcquirer) install(ctx context.Context, c Cluster, t Resolved) error {
	local := j.Binary
	if local != "" {
		if _, err := os.Stat(local); err != nil {
			return fmt.Errorf("--binary path not found: %s", local)
		}
		j.Log.p("installing jattach from local file: %s", local)
	} else {
		arch, err := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, "uname", "-m")
		if err != nil {
			return fmt.Errorf("cannot read pod arch: %w", err)
		}
		var suffix string
		switch arch {
		case "x86_64", "amd64":
			suffix = "x64"
		case "aarch64", "arm64":
			suffix = "arm64"
		default:
			return fmt.Errorf("unsupported pod arch: %s (provide --binary instead)", arch)
		}
		local = filepath.Join(j.VendorDir, "jattach-linux-"+suffix)
		if _, err := os.Stat(local); err != nil {
			return fmt.Errorf("no vendored jattach for arch %q at %s — provide --binary", arch, local)
		}
		// Integrity gate: verify against SHA256SUMS BEFORE the binary ships
		// into a production pod (the v1 F2 remediation, structural here).
		if err := verifySums(j.VendorDir, filepath.Base(local)); err != nil {
			return err
		}
		j.Log.p("using vendored jattach (verified): %s", local)
	}
	if err := c.CopyToPod(ctx, t.Namespace, t.Pod, t.Container, local, j.remote()); err != nil {
		return fmt.Errorf("kubectl cp of jattach failed: %w (no tar in the pod?)", err)
	}
	if err := c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, io.Discard, "chmod", "+x", j.remote()); err != nil {
		return err
	}
	if j.RecordArtifact != nil {
		j.RecordArtifact(true, j.remote(), "jattach")
	}
	// liveness probe: no output at all = exec/libc broke
	out, _ := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, j.remote())
	if out == "" {
		return fmt.Errorf("jattach binary produced no output inside the pod (libc/arch mismatch?) — provide --binary")
	}
	j.Log.p("jattach installed and working")
	return nil
}

// CleanupRemote deletes the in-pod heap file after a VALIDATED capture.
func (j *JattachAcquirer) CleanupRemote(ctx context.Context, c Cluster, t Resolved) {
	if j.pendingRemote == "" {
		return
	}
	_ = c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, io.Discard, "rm", "-f", j.pendingRemote)
}

// RemoteDumpPath exposes where the in-pod copy still lives (retry hint).
func (j *JattachAcquirer) RemoteDumpPath() string { return j.pendingRemote }

// verifySums checks file (inside dir) against dir/SHA256SUMS.
func verifySums(dir, file string) error {
	sums := filepath.Join(dir, "SHA256SUMS")
	b, err := os.ReadFile(sums)
	if err != nil {
		return fmt.Errorf("missing %s — refusing to install an unverified binary (or pass --binary)", sums)
	}
	var want string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == file {
			want = f[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no entry for %s in %s — refusing to install unverified", file, sums)
	}
	got, err := fileSHA256sum(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("vendored jattach FAILED its checksum — refusing to install it into the pod (expected %s, got %s)", want, got)
	}
	return nil
}

func fileSHA256sum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FindJVMPID locates the JVM inside the pod: comm=="java" first, then
// anything mapping libjvm (custom launchers) — PID 1 is the pause sandbox
// under shareProcessNamespace, never assume it.
func FindJVMPID(ctx context.Context, c Cluster, t Resolved) (string, error) {
	script := `
for p in $(ls /proc 2>/dev/null | grep -E "^[0-9]+$"); do
    if [ "$(cat /proc/$p/comm 2>/dev/null)" = "java" ]; then echo "$p"; exit 0; fi
done
for p in $(ls /proc 2>/dev/null | grep -E "^[0-9]+$"); do
    if grep -q libjvm "/proc/$p/maps" 2>/dev/null; then echo "$p"; exit 0; fi
done
exit 1`
	pid, err := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, "sh", "-c", script)
	pid = strings.TrimSpace(pid)
	if err != nil || pid == "" || !isDigits(pid) {
		return "", fmt.Errorf("no JVM found inside pod %s container %s (no 'java' process, nothing maps libjvm)", t.Pod, t.Container)
	}
	return pid, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// JcmdRun proxies one jcmd command through jattach to stdout (no artifact).
func JcmdRun(ctx context.Context, c Cluster, t Resolved, j *JattachAcquirer, command string, w io.Writer) error {
	pid, err := j.prepare(ctx, c, t)
	if err != nil {
		return err
	}
	j.Log.p("running jcmd '%s' on PID %s", command, pid)
	return c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, w, j.remote(), pid, "jcmd", command)
}

// --- tier 3: jdk (ephemeral debug container) ---------------------------------

// JDKAcquirer is the last resort: kubectl debug attaches a JDK image, finds
// the JVM PID from /proc, hand-shakes the HotSpot attach protocol across
// the container boundary, and runs jstack/jmap.
type JDKAcquirer struct {
	Kind  string // threads | heap
	Image string // default eclipse-temurin:21-jdk-alpine
	Log   Info
}

func (j JDKAcquirer) image() string {
	if j.Image != "" {
		return j.Image
	}
	if v := os.Getenv("JDK_DEBUG_IMAGE"); v != "" {
		return v
	}
	if v := os.Getenv("JDEBUG_JDK_IMAGE"); v != "" {
		return v
	}
	return "eclipse-temurin:21-jdk-alpine"
}

func (j JDKAcquirer) Meta() Meta {
	if j.Kind == "heap" {
		return Meta{Name: "heap-jdk.hprof", Tier: "jdk", Command: "kubectl debug + jmap -dump:live"}
	}
	return Meta{Name: "threads-jdk.txt", Tier: "jdk", Command: "kubectl debug + jstack -l"}
}

const jdkAttachPreamble = `
JPID=""
for p in /proc/[0-9]*/comm; do
    [ "$(cat "$p" 2>/dev/null)" = "java" ] && { JPID="${p#/proc/}"; JPID="${JPID%/comm}"; break; }
done
if [ -z "$JPID" ]; then
    for m in /proc/[0-9]*/maps; do
        grep -q libjvm "$m" 2>/dev/null && { JPID="${m#/proc/}"; JPID="${JPID%/maps}"; break; }
    done
fi
[ -n "$JPID" ] || { echo "ERROR: no JVM visible in the shared PID namespace" >&2; exit 1; }
SOCK="/proc/$JPID/root/tmp/.java_pid$JPID"
if [ ! -S "$SOCK" ]; then
    touch "/proc/$JPID/root/tmp/.attach_pid$JPID" \
        || { echo "ERROR: cannot reach the JVM /tmp via /proc/$JPID/root (need root in the debug container)" >&2; exit 1; }
    kill -QUIT "$JPID"
    n=0; while [ $n -lt 50 ] && [ ! -S "$SOCK" ]; do sleep 0.2; n=$((n+1)); done
fi
[ -S "$SOCK" ] || { echo "ERROR: JVM never opened the attach socket ($SOCK)" >&2; exit 1; }
ln -sf "$SOCK" "/tmp/.java_pid$JPID"
`

func (j JDKAcquirer) Acquire(ctx context.Context, c Cluster, t Resolved, destPath string) (int64, error) {
	debugName := fmt.Sprintf("%s-%d", map[string]string{"heap": "jmap", "threads": "jstack"}[j.Kind], time.Now().Unix())
	if j.Kind == "threads" {
		script := jdkAttachPreamble + `exec jstack -l "$JPID"`
		j.Log.p("thread dump via ephemeral JDK container (pod=%s image=%s)", t.Pod, j.image())
		if err := c.Debug(ctx, t.Namespace, t.Pod, t.Container, debugName, j.image(), script); err != nil {
			return 0, fmt.Errorf("kubectl debug failed: %w", err)
		}
		f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		return 0, followDebugLogs(ctx, c, t, debugName, f)
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	remoteDump := "/tmp/heap-jdk-" + ts + ".hprof"
	script := jdkAttachPreamble + `exec jmap -dump:live,format=b,file=` + remoteDump + ` "$JPID"`
	j.Log.p("heap dump via ephemeral JDK container (pod=%s image=%s) — PAUSES the JVM", t.Pod, j.image())
	if err := c.Debug(ctx, t.Namespace, t.Pod, t.Container, debugName, j.image(), script); err != nil {
		return 0, fmt.Errorf("kubectl debug failed: %w", err)
	}
	var logbuf strings.Builder
	if err := followDebugLogs(ctx, c, t, debugName, &logbuf); err != nil {
		return 0, err
	}
	if !strings.Contains(logbuf.String(), "Heap dump file created") {
		return 0, fmt.Errorf("jmap did not report success:\n%s", strings.TrimSpace(logbuf.String()))
	}
	sizeStr, serr := ExecPodCapture(ctx, c, t.Namespace, t.Pod, t.Container, "sh", "-c", "wc -c < '"+remoteDump+"'")
	expected, _ := strconv.ParseInt(strings.TrimSpace(sizeStr), 10, 64)
	if expected <= 0 {
		j.Log.p("⚠ could not read the in-pod dump size (%v) — truncation check degraded to hprof-magic only", serr)
	}
	if err := c.CopyFromPod(ctx, t.Namespace, t.Pod, t.Container, remoteDump, destPath); err != nil {
		return 0, fmt.Errorf("kubectl cp failed: %w (the dump is still in the pod at %s)", err, remoteDump)
	}
	_ = c.ExecPod(ctx, t.Namespace, t.Pod, t.Container, io.Discard, "rm", "-f", remoteDump)
	return expected, nil
}

// followDebugLogs retries `logs -f` while the ephemeral container starts.
func followDebugLogs(ctx context.Context, c Cluster, t Resolved, container string, w io.Writer) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = c.PodLogs(ctx, t.Namespace, t.Pod, container, true, w); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("could not read the debug container's output: %w", err)
}
