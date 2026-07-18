package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCluster: the test double. exec is a scriptable hook keyed on the
// argv joined with spaces; copyFrom writes canned bytes to the local path.
type fakeCluster struct {
	pods     []string
	podsErr  error
	podJSON  []byte
	exec     func(argv []string, w io.Writer) error
	copyFrom func(remote, local string) error
	copyTo   func(local, remote string) error
}

func (f fakeCluster) ExecPod(_ context.Context, _, _, _ string, w io.Writer, argv ...string) error {
	if f.exec != nil {
		return f.exec(argv, w)
	}
	return nil
}
func (f fakeCluster) PodsMatching(_ context.Context, _, _ string) ([]string, error) {
	return f.pods, f.podsErr
}
func (f fakeCluster) CopyFromPod(_ context.Context, _, _, _ string, remote, local string) error {
	if f.copyFrom != nil {
		return f.copyFrom(remote, local)
	}
	return nil
}
func (f fakeCluster) CopyToPod(_ context.Context, _, _, _ string, local, remote string) error {
	if f.copyTo != nil {
		return f.copyTo(local, remote)
	}
	return nil
}
func (f fakeCluster) Debug(_ context.Context, _, _, _, _, _, _ string) error { return nil }
func (f fakeCluster) DescribePod(_ context.Context, _, _ string, w io.Writer) error {
	fmt.Fprintln(w, "Name: mock-pod")
	return nil
}
func (f fakeCluster) PodJSON(_ context.Context, _, _ string) ([]byte, error) {
	if f.podJSON != nil {
		return f.podJSON, nil
	}
	return []byte(`{"spec":{"containers":[]}}`), nil
}
func (f fakeCluster) PodLogs(_ context.Context, _, _, _ string, _ bool, _ io.Writer) error {
	return nil
}

// fakeAcquirer writes canned bytes and declares an expected size.
type fakeAcquirer struct {
	meta Meta
	data []byte
	size int64 // declared expected size (0 = unknown)
	fail error
}

func (a fakeAcquirer) Meta() Meta { return a.meta }
func (a fakeAcquirer) Acquire(_ context.Context, _ Cluster, _ Resolved, destPath string) (int64, error) {
	if a.fail != nil {
		return 0, a.fail
	}
	if err := os.WriteFile(destPath, a.data, 0o600); err != nil {
		return 0, err
	}
	return a.size, nil
}

// --- target resolution: the F8 rule as a type --------------------------------

func TestResolveExplicitPod(t *testing.T) {
	r, err := Resolve(context.Background(), fakeCluster{}, Target{Namespace: "ns", Pod: "pod-x"})
	if err != nil || !r.Explicit || r.Pod != "pod-x" {
		t.Fatalf("explicit pod: %+v err=%v", r, err)
	}
	if _, err := r.Confirm(); err != nil {
		t.Fatalf("explicit pod must be confirmable: %v", err)
	}
}

func TestResolveNoPods(t *testing.T) {
	_, err := Resolve(context.Background(), fakeCluster{pods: nil}, Target{Namespace: "ns"})
	if !errors.Is(err, ErrNoPods) {
		t.Fatalf("want ErrNoPods, got %v", err)
	}
}

func TestResolveSingleMatchConfirms(t *testing.T) {
	r, err := Resolve(context.Background(), fakeCluster{pods: []string{"only"}}, Target{Namespace: "ns"})
	if err != nil || r.Pod != "only" {
		t.Fatalf("single match: %+v err=%v", r, err)
	}
	if _, err := r.Confirm(); err != nil {
		t.Fatalf("unambiguous match must be confirmable: %v", err)
	}
}

func TestResolveAmbiguousRefusesConfirm(t *testing.T) {
	r, err := Resolve(context.Background(), fakeCluster{pods: []string{"a", "b", "c"}}, Target{Namespace: "ns"})
	if err != nil {
		t.Fatalf("resolve itself must succeed (non-destructive callers auto-pick): %v", err)
	}
	if r.Pod != "a" || r.Explicit || len(r.Matches) != 3 {
		t.Fatalf("auto-pick surface: %+v", r)
	}
	if _, err := r.Confirm(); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous match must NOT confirm (F8) — got %v", err)
	}
}

// --- validators: F1's truncation and route-vs-heap confusion -----------------

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateHprofGood(t *testing.T) {
	data := []byte("JAVA PROFILE 1.0.2\x00rest-of-heap-bytes")
	v := ValidateHprof(writeTemp(t, data), int64(len(data)))
	if !v.OK {
		t.Fatalf("valid hprof rejected: %s", v.Reason)
	}
}

func TestValidateHprofTruncated(t *testing.T) {
	data := []byte("JAVA PROFILE 1.0.2\x00partial")
	v := ValidateHprof(writeTemp(t, data), int64(len(data))+1000)
	if v.OK || !strings.Contains(v.Reason, "TRUNCATED") {
		t.Fatalf("truncated hprof must fail with TRUNCATED, got %+v", v)
	}
}

func TestValidateHprofLoginPage(t *testing.T) {
	v := ValidateHprof(writeTemp(t, []byte("<!DOCTYPE html><html><title>Please sign in</title>login</html>")), 0)
	if v.OK || !strings.Contains(v.Reason, "login") {
		t.Fatalf("login page must be classified, got %+v", v)
	}
}

func TestValidateThreadDump(t *testing.T) {
	good := ValidateThreadDump(writeTemp(t, []byte("Full thread dump OpenJDK\n\"main\" #1")), 0)
	if !good.OK {
		t.Fatalf("valid dump rejected: %s", good.Reason)
	}
	bad := ValidateThreadDump(writeTemp(t, []byte("{\"error\":\"attach refused\"}")), 0)
	if bad.OK || !strings.Contains(bad.Reason, "Full thread dump") {
		t.Fatalf("non-dump must fail with marker reason, got %+v", bad)
	}
}

// --- pipeline: validation is unskippable, manifests are truthful -------------

func pipe(t *testing.T) (Pipeline, *Store) {
	t.Helper()
	st := &Store{Root: t.TempDir()}
	return Pipeline{Cluster: fakeCluster{}, Store: st}, st
}

func TestPipelineStoresValidCapture(t *testing.T) {
	p, st := pipe(t)
	data := []byte("JAVA PROFILE 1.0.2\x00heap")
	acq := fakeAcquirer{meta: Meta{Name: "heap.hprof", Tier: "jattach"}, data: data, size: int64(len(data))}
	r, _ := Resolve(context.Background(), fakeCluster{pods: []string{"p1"}}, Target{Namespace: "ns"})
	ct, _ := r.Confirm()
	art, err := p.RunDestructive(context.Background(), acq, ct, ValidateHprof)
	if err != nil {
		t.Fatalf("valid capture errored: %v", err)
	}
	if !art.Verdict.OK || art.Tier != "jattach" || art.Bytes != int64(len(data)) || art.SHA256 == "" {
		t.Fatalf("artifact record wrong: %+v", art)
	}
	// provenance is in the manifest, not just the return value
	sess, _ := st.Session("p1", time.Now().UTC())
	m, err := sess.Read()
	if err != nil || len(m.Artifacts) != 1 || m.Artifacts[0].SHA256 != art.SHA256 {
		t.Fatalf("manifest roundtrip: %+v err=%v", m, err)
	}
}

func TestPipelineRefusesInvalidCapture(t *testing.T) {
	p, st := pipe(t)
	acq := fakeAcquirer{meta: Meta{Name: "heap.hprof", Tier: "actuator"}, data: []byte("<html>login</html>")}
	r, _ := Resolve(context.Background(), fakeCluster{pods: []string{"p1"}}, Target{Namespace: "ns"})
	ct, _ := r.Confirm()
	art, err := p.RunDestructive(context.Background(), acq, ct, ValidateHprof)
	if err == nil {
		t.Fatal("invalid capture must return an error")
	}
	if art.Verdict.OK {
		t.Fatal("verdict must record the failure")
	}
	// the bad file is KEPT for inspection, and the manifest says it's bad
	sess, _ := st.Session("p1", time.Now().UTC())
	m, _ := sess.Read()
	if len(m.Artifacts) != 1 || m.Artifacts[0].Verdict.OK {
		t.Fatalf("manifest must record the invalid capture: %+v", m)
	}
	if _, serr := os.Stat(filepath.Join(sess.Dir, "heap.hprof")); serr != nil {
		t.Fatal("invalid capture file must be kept for inspection")
	}
}

func TestPipelineRequiresValidator(t *testing.T) {
	p, _ := pipe(t)
	acq := fakeAcquirer{meta: Meta{Name: "x", Tier: "t"}}
	r, _ := Resolve(context.Background(), fakeCluster{pods: []string{"p1"}}, Target{Namespace: "ns"})
	if _, err := p.Run(context.Background(), acq, r, nil); err == nil {
		t.Fatal("nil validator must be refused — validation is not optional")
	}
}

func TestPipelineAcquireFailureLeavesNothing(t *testing.T) {
	p, st := pipe(t)
	acq := fakeAcquirer{meta: Meta{Name: "th.txt", Tier: "actuator"}, fail: errors.New("exec: pod gone")}
	r, _ := Resolve(context.Background(), fakeCluster{pods: []string{"p1"}}, Target{Namespace: "ns"})
	if _, err := p.Run(context.Background(), acq, r, ValidateThreadDump); err == nil {
		t.Fatal("acquire failure must propagate")
	}
	sess, _ := st.Session("p1", time.Now().UTC())
	if _, serr := os.Stat(filepath.Join(sess.Dir, "th.txt")); !os.IsNotExist(serr) {
		t.Fatal("a failed acquire must not leave a half-written artifact (audit axis D)")
	}
}

func TestSessionDirIsOwnerOnly(t *testing.T) {
	st := &Store{Root: t.TempDir()}
	sess, err := st.Session("p1", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sess.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("session dir must be owner-only (heap dumps hold prod data), got %v", info.Mode().Perm())
	}
}

// --- jattach acquirer: install verification + truncation detection -----------

func vendorFixture(t *testing.T, tamper bool) string {
	t.Helper()
	dir := t.TempDir()
	bin := []byte("#!/bin/sh\necho usage\n")
	if err := os.WriteFile(filepath.Join(dir, "jattach-linux-x64"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	sum, _ := fileSHA256sum(filepath.Join(dir, "jattach-linux-x64"))
	if tamper {
		sum = strings.Repeat("0", 64)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sum+"  jattach-linux-x64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// jattachExecScript scripts the pod-side sequence: no jattach yet, arch
// x86_64, PID discovery finds 42, jattach probe prints usage, thread dump
// prints a real marker.
func jattachExecScript(argv []string, w io.Writer) error {
	joined := strings.Join(argv, " ")
	switch {
	case strings.Contains(joined, "test -x"):
		return errors.New("not present")
	case strings.Contains(joined, "uname"):
		fmt.Fprintln(w, "x86_64")
	case strings.Contains(joined, "/proc"):
		fmt.Fprintln(w, "42")
	case strings.Contains(joined, "Thread.print"):
		fmt.Fprintln(w, "Full thread dump OpenJDK")
	case strings.HasSuffix(joined, "/tmp/jattach"):
		fmt.Fprintln(w, "Usage: jattach <pid>")
	}
	return nil
}

func TestJattachThreadsEndToEnd(t *testing.T) {
	p, _ := pipe(t)
	fc := fakeCluster{pods: []string{"p1"}, exec: jattachExecScript}
	p.Cluster = fc
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	acq := &JattachAcquirer{Kind: "threads", VendorDir: vendorFixture(t, false)}
	art, err := p.Run(context.Background(), acq, r, ValidateThreadDump)
	if err != nil {
		t.Fatalf("jattach threads: %v", err)
	}
	if !art.Verdict.OK || art.Tier != "jattach" {
		t.Fatalf("artifact: %+v", art)
	}
}

func TestJattachRefusesTamperedVendorBinary(t *testing.T) {
	p, _ := pipe(t)
	fc := fakeCluster{pods: []string{"p1"}, exec: jattachExecScript}
	p.Cluster = fc
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	acq := &JattachAcquirer{Kind: "threads", VendorDir: vendorFixture(t, true)}
	_, err := p.Run(context.Background(), acq, r, ValidateThreadDump)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered vendored binary must refuse with a checksum error, got %v", err)
	}
}

func TestJattachHeapTruncationCaught(t *testing.T) {
	p, _ := pipe(t)
	full := []byte("JAVA PROFILE 1.0.2\x00full-heap-bytes-here")
	fc := fakeCluster{
		pods: []string{"p1"},
		exec: func(argv []string, w io.Writer) error {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "test -x"):
				return nil // jattach already in the pod
			case strings.Contains(joined, "/proc"):
				fmt.Fprintln(w, "42")
			case strings.Contains(joined, "wc -c"):
				fmt.Fprintln(w, len(full)) // pod-side size: the truth
			}
			return nil
		},
		copyFrom: func(remote, local string) error {
			return os.WriteFile(local, full[:len(full)-5], 0o600) // cp truncates
		},
	}
	p.Cluster = fc
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	ct, _ := r.Confirm()
	acq := &JattachAcquirer{Kind: "heap"}
	_, err := p.RunDestructive(context.Background(), acq, ct, ValidateHprof)
	if err == nil || !strings.Contains(err.Error(), "TRUNCATED") {
		t.Fatalf("truncated kubectl cp must fail validation (F1), got %v", err)
	}
	if acq.RemoteDumpPath() == "" {
		t.Fatal("the in-pod copy's path must be retained for a retry")
	}
}

// --- actuator acquirer: pod_fetch parity -------------------------------------

func TestPodFetchScriptParity(t *testing.T) {
	s := PodFetchScript("http://x/y", "text/plain", "")
	for _, want := range []string{"command -v curl", "wget -qO-", "Accept: text/plain"} {
		if !strings.Contains(s, want) {
			t.Fatalf("pod_fetch parity: missing %q in %s", want, s)
		}
	}
	auth := PodFetchScript("http://x/y", "", "bearer:MGMT_TOKEN")
	if !strings.Contains(auth, `Authorization: Bearer $MGMT_TOKEN`) {
		t.Fatalf("bearer auth must reference the pod env var unexpanded: %s", auth)
	}
	basic := PodFetchScript("http://x/y", "", "basic:U:P")
	if !strings.Contains(basic, `-u "$U:$P"`) {
		t.Fatalf("basic auth must reference pod env vars: %s", basic)
	}
}

func TestActuatorAcquirerWritesBody(t *testing.T) {
	p, _ := pipe(t)
	fc := fakeCluster{pods: []string{"p1"}, exec: func(argv []string, w io.Writer) error {
		fmt.Fprintln(w, "Full thread dump mock JVM")
		return nil
	}}
	p.Cluster = fc
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	art, err := p.Run(context.Background(),
		ActuatorAcquirer{Kind: "threads", Base: "http://localhost:8080/actuator"}, r, ValidateThreadDump)
	if err != nil || !art.Verdict.OK || art.Name != "threads-actuator.txt" {
		t.Fatalf("actuator threads: %+v err=%v", art, err)
	}
}

// --- config: the saved-target file is parsed, never executed -----------------

func TestSavedTargetParsed(t *testing.T) {
	dir := t.TempDir()
	content := "# written by jdebug's target editor\n" +
		"SAVED_NAMESPACE=payments\nSAVED_SELECTOR=''\nSAVED_CONTAINER=app\n" +
		"SAVED_ACTUATOR=http://localhost:9001/manage\nSAVED_POD=pod-b\n"
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JDEBUG_CONFIG_DIR", dir)
	t.Setenv("JDEBUG_NAMESPACE", "")
	os.Unsetenv("JDEBUG_NAMESPACE")
	saved := loadSavedTarget()
	if saved["SAVED_NAMESPACE"] != "payments" || saved["SAVED_SELECTOR"] != "" || saved["SAVED_ACTUATOR"] != "http://localhost:9001/manage" {
		t.Fatalf("saved target parse: %+v", saved)
	}
}

func TestSavedTargetTamperIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target"),
		[]byte("SAVED_NAMESPACE=$(touch /tmp/pwned-core)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JDEBUG_CONFIG_DIR", dir)
	if saved := loadSavedTarget(); saved != nil {
		t.Fatalf("tampered file must be ignored entirely, got %+v", saved)
	}
	if _, err := os.Stat("/tmp/pwned-core"); err == nil {
		t.Fatal("command substitution must never execute")
	}
}

func TestUnquoteBash(t *testing.T) {
	cases := map[string]string{
		"plain":     "plain",
		"''":        "",
		"'a b'":     "a b",
		`app\ =\ x`: "app = x",
		"$'a\\nb'":  "a\nb",
	}
	for in, want := range cases {
		if got := unquoteBash(in); got != want {
			t.Fatalf("unquoteBash(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- snapshot orchestration ---------------------------------------------------

func TestSnapshotBundle(t *testing.T) {
	// stub kit: the observe reporters are tiny scripts writing canned output
	kit := t.TempDir()
	if err := os.MkdirAll(filepath.Join(kit, "observe"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"why.sh", "security.sh", "memory-report.sh"} {
		if err := os.WriteFile(filepath.Join(kit, "observe", s),
			[]byte("#!/bin/sh\necho \"stub "+s+" output\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fc := fakeCluster{pods: []string{"p1"}, exec: func(argv []string, w io.Writer) error {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "/health"):
			fmt.Fprint(w, `{"status":"UP"}`)
		case strings.Contains(joined, "/metrics"):
			fmt.Fprint(w, `{"names":["jvm.memory.used"]}`)
		case strings.Contains(joined, "/threaddump"):
			fmt.Fprint(w, "Full thread dump mock JVM\n")
		}
		return nil
	}}
	st := &Store{Root: t.TempDir()}
	pipe := Pipeline{Cluster: fc, Store: st}
	cfg := Config{KitRoot: kit, DumpsRoot: st.Root, ActuatorBase: "http://localhost:8080/actuator"}
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	ct, _ := r.Confirm()

	res, err := Snapshot(context.Background(), fc, pipe, cfg, ct, SnapshotOpts{NoJattach: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Captured != 7 {
		t.Fatalf("expected 7 clean sections, got %+v", res)
	}
	// the marker that makes `jdebug dumps` call it a bundle
	if _, err := os.Stat(filepath.Join(res.Dir, ".snapshot")); err != nil {
		t.Fatal(".snapshot marker missing")
	}
	// every section is in the manifest with a verdict
	sess := &Session{Dir: res.Dir}
	m, err := sess.Read()
	if err != nil || len(m.Artifacts) != 7 {
		t.Fatalf("manifest must record all sections: %d, err=%v", len(m.Artifacts), err)
	}
	for _, a := range m.Artifacts {
		if !a.Verdict.OK || a.SHA256 == "" {
			t.Fatalf("section %s: %+v", a.Name, a.Verdict)
		}
	}
	// spot-check content parity
	b, _ := os.ReadFile(filepath.Join(res.Dir, "threads.txt"))
	if !strings.Contains(string(b), "Full thread dump") {
		t.Fatalf("threads.txt content: %q", b)
	}
}

func TestSnapshotFailedSectionIsHonest(t *testing.T) {
	kit := t.TempDir() // no observe/ scripts → those sections fail
	fc := fakeCluster{pods: []string{"p1"}, exec: func(argv []string, w io.Writer) error {
		return errors.New("exec: connection refused")
	}}
	st := &Store{Root: t.TempDir()}
	cfg := Config{KitRoot: kit, DumpsRoot: st.Root, ActuatorBase: "http://x/actuator"}
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	ct, _ := r.Confirm()
	res, err := Snapshot(context.Background(), fc, Pipeline{Cluster: fc, Store: st}, cfg, ct, SnapshotOpts{NoJattach: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed == 0 {
		t.Fatal("failures must be counted")
	}
	// a failed section's file says CAPTURE FAILED — never an empty file
	// masquerading as evidence; and the manifest verdict agrees
	b, _ := os.ReadFile(filepath.Join(res.Dir, "health.json"))
	if !strings.Contains(string(b), "CAPTURE FAILED") {
		t.Fatalf("failed section must say so in-file: %q", b)
	}
	sess := &Session{Dir: res.Dir}
	m, _ := sess.Read()
	bad := 0
	for _, a := range m.Artifacts {
		if !a.Verdict.OK {
			bad++
		}
	}
	if bad != res.Failed {
		t.Fatalf("manifest verdicts (%d bad) must match the failure count (%d)", bad, res.Failed)
	}
}

// --- fetch-heap (F7): on-crash dump retrieval ---------------------------------

func TestInspectHeapDumpConfig(t *testing.T) {
	fc := fakeCluster{podJSON: []byte(`{"spec":{"containers":[{"env":[
		{"name":"JAVA_TOOL_OPTIONS","value":"-XX:+HeapDumpOnOutOfMemoryError -XX:HeapDumpPath=/dumps -Xmx256m"}
	]}]}}`)}
	h, err := InspectHeapDumpConfig(context.Background(), fc, "ns", "p1")
	if err != nil || !h.FlagSet || h.DumpPath != "/dumps" {
		t.Fatalf("hint: %+v err=%v", h, err)
	}
	bare := fakeCluster{}
	h2, _ := InspectHeapDumpConfig(context.Background(), bare, "ns", "p1")
	if h2.FlagSet {
		t.Fatal("no env → flag must read unset")
	}
}

func TestFetchHeapEndToEnd(t *testing.T) {
	heap := []byte("JAVA PROFILE 1.0.2\x00oncrash-heap-bytes")
	fc := fakeCluster{
		pods: []string{"p1"},
		exec: func(argv []string, w io.Writer) error {
			if strings.Contains(strings.Join(argv, " "), "find") {
				// mtime \t size \t path — an OLDER but LARGER dump from a prior
				// OOM, plus the NEWER (smaller) dump from this crash. The newer
				// one must win, proving mtime beats size.
				fmt.Fprintf(w, "100\t%d\t/dumps/old-big.hprof\n", len(heap)+9999)
				fmt.Fprintf(w, "200\t%d\t/dumps/java_pid1.hprof\n", len(heap))
			}
			return nil
		},
		copyFrom: func(remote, local string) error {
			if remote != "/dumps/java_pid1.hprof" {
				return errors.New("wrong remote: " + remote)
			}
			return os.WriteFile(local, heap, 0o600)
		},
	}
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	dumps, err := FindHeapDumps(context.Background(), fc, r,
		HeapDumpHint{FlagSet: true, DumpPath: "/dumps"}, nil)
	if err != nil || len(dumps) != 2 || dumps[0].Path != "/dumps/java_pid1.hprof" || dumps[0].Bytes != int64(len(heap)) {
		t.Fatalf("find (newest must sort first): %+v err=%v", dumps, err)
	}
	st := &Store{Root: t.TempDir()}
	art, err := Pipeline{Cluster: fc, Store: st}.Run(context.Background(),
		FetchHeapAcquirer{Remote: dumps[0]}, r, ValidateHprof)
	if err != nil || !art.Verdict.OK || art.Tier != "on-crash" {
		t.Fatalf("fetch: %+v err=%v", art, err)
	}
}

func TestFetchHeapTruncationCaught(t *testing.T) {
	heap := []byte("JAVA PROFILE 1.0.2\x00full-heap")
	fc := fakeCluster{
		pods: []string{"p1"},
		copyFrom: func(_, local string) error {
			return os.WriteFile(local, heap[:len(heap)-4], 0o600) // cp truncates
		},
	}
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	st := &Store{Root: t.TempDir()}
	_, err := Pipeline{Cluster: fc, Store: st}.Run(context.Background(),
		FetchHeapAcquirer{Remote: FoundDump{Path: "/dumps/x.hprof", Bytes: int64(len(heap))}}, r, ValidateHprof)
	if err == nil || !strings.Contains(err.Error(), "TRUNCATED") {
		t.Fatalf("truncated on-crash fetch must fail validation, got %v", err)
	}
}

func TestExplainNoDumpsGuidance(t *testing.T) {
	noFlag := ExplainNoDumps(HeapDumpHint{})
	if !strings.Contains(noFlag, "HeapDumpOnOutOfMemoryError") || !strings.Contains(noFlag, "emptyDir") {
		t.Fatalf("missing-flag guidance must include the setup:\n%s", noFlag)
	}
	flagged := ExplainNoDumps(HeapDumpHint{FlagSet: true, DumpPath: "/dumps"})
	if !strings.Contains(flagged, "REPLACED") || !strings.Contains(flagged, "logs --previous") {
		t.Fatalf("flag-set guidance must cover pod replacement:\n%s", flagged)
	}
}
