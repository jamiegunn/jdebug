package core

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCluster: the test double. Each field scripts one behavior.
type fakeCluster struct {
	pods    []string
	podsErr error
}

func (f fakeCluster) ExecPod(_ context.Context, _, _, _ string, w io.Writer, _ ...string) error {
	return nil
}
func (f fakeCluster) PodsMatching(_ context.Context, _, _ string) ([]string, error) {
	return f.pods, f.podsErr
}
func (f fakeCluster) CopyFromPod(_ context.Context, _, _, _, _, _ string) error { return nil }

// fakeAcquirer writes canned bytes and declares an expected size.
type fakeAcquirer struct {
	meta Meta
	data []byte
	size int64 // declared expected size (0 = unknown)
	fail error
}

func (a fakeAcquirer) Meta() Meta { return a.meta }
func (a fakeAcquirer) Acquire(_ context.Context, _ Cluster, _ Resolved, w io.Writer) (int64, error) {
	if a.fail != nil {
		return 0, a.fail
	}
	_, err := w.Write(a.data)
	return a.size, err
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
