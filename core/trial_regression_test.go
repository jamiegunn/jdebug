package core

// Regression tests for the adversarial-review findings (the "trial"):
//   I.2  success paths must be real (Artifact.Path, not reconstructed)
//   I.3  garbage / wrong-format dumps must never read "nothing alarming"
//   I.4  java.util.concurrent (ReentrantLock) deadlocks must be detected
//   I.5  the snapshot manifest's hashes must match the on-disk files
//   I.1  the JSON threads capture must have a JSON-aware validator
//        + virtual-thread incompleteness must carry a caveat

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

// --- I.4: juc deadlock detection ------------------------------------------

// an actuator-TEXT dump (no jstack banner) of a textbook two-thread
// ReentrantLock deadlock: WAITING + "parking to wait for" + the held lock
// listed under "Locked ownable synchronizers".
const jucDeadlockText = `2026-07-18 10:00:00
Full thread dump OpenJDK 64-Bit Server VM (21.0.2+13 mixed mode):

"worker-1" #12 prio=5 os_prio=0 tid=0x1 nid=0x1 waiting on condition  [0x0a]
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(java.base@21.0.2/Native Method)
	- parking to wait for  <0x000000071a3f1e40> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)
	at java.util.concurrent.locks.LockSupport.park(java.base@21.0.2/LockSupport.java:221)
	at com.example.Deadlock.lambda$main$0(Deadlock.java:20)

   Locked ownable synchronizers:
	- <0x000000071a3f1e70> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)

"worker-2" #13 prio=5 os_prio=0 tid=0x2 nid=0x2 waiting on condition  [0x0b]
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(java.base@21.0.2/Native Method)
	- parking to wait for  <0x000000071a3f1e70> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)
	at java.util.concurrent.locks.LockSupport.park(java.base@21.0.2/LockSupport.java:221)
	at com.example.Deadlock.lambda$main$1(Deadlock.java:28)

   Locked ownable synchronizers:
	- <0x000000071a3f1e40> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)
`

func TestJucDeadlockDetected(t *testing.T) {
	d, err := ParseThreadDump(strings.NewReader(jucDeadlockText))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Analyze()
	if len(a.DeadlockCycles) == 0 {
		t.Fatal("a ReentrantLock deadlock (parking + ownable synchronizers) must be detected — WAITING-not-BLOCKED is the juc signature")
	}
	var out strings.Builder
	if flags := a.Render(&out); flags == 0 {
		t.Fatal("Render must flag the juc deadlock")
	}
	if strings.Contains(out.String(), "nothing alarming") {
		t.Fatalf("a deadlocked dump must never read 'nothing alarming': %s", out.String())
	}
}

// idle pool threads park on queues/conditions nobody "holds" — no edge, no
// false deadlock. The fix must not turn every quiet thread pool into a P1.
func TestIdleParkingIsNotADeadlock(t *testing.T) {
	idle := `Full thread dump OpenJDK 64-Bit Server VM (21.0.2+13 mixed mode):

"pool-1-thread-1" #10 prio=5 tid=0x1 nid=0x1 waiting on condition  [0x0a]
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(java.base@21.0.2/Native Method)
	- parking to wait for  <0x00000007000000a0> (a java.util.concurrent.SynchronousQueue$TransferStack)
	at java.util.concurrent.locks.LockSupport.park(java.base@21.0.2/LockSupport.java:221)

"pool-1-thread-2" #11 prio=5 tid=0x2 nid=0x2 waiting on condition  [0x0b]
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(java.base@21.0.2/Native Method)
	- parking to wait for  <0x00000007000000a0> (a java.util.concurrent.SynchronousQueue$TransferStack)
	at java.util.concurrent.locks.LockSupport.park(java.base@21.0.2/LockSupport.java:221)
`
	d, err := ParseThreadDump(strings.NewReader(idle))
	if err != nil {
		t.Fatal(err)
	}
	if a := d.Analyze(); len(a.DeadlockCycles) != 0 {
		t.Fatalf("idle queue parking must not read as a deadlock: %v", a.DeadlockCycles)
	}
}

// --- I.3: garbage and wrong formats must be refused, not blessed ----------

func TestRenderRefusesGarbage(t *testing.T) {
	d, err := ParseThreadDump(strings.NewReader("10:00 INFO started\n10:01 WARN pool nearly full\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Analyze()
	var out strings.Builder
	flags := a.Render(&out)
	if flags == 0 {
		t.Fatal("0 parsed threads must raise a flag, not exit clean")
	}
	if strings.Contains(out.String(), "nothing alarming") {
		t.Fatalf("garbage must never read 'nothing alarming': %s", out.String())
	}
}

func TestRenderNamesJDK21PlainFormat(t *testing.T) {
	plain := `#1 "main" java.lang.Thread.State: RUNNABLE
#96 "tomcat-handler-1" virtual
      java.base/jdk.internal.misc.Unsafe.park(Native Method)
#97 "tomcat-handler-2" virtual
`
	d, err := ParseThreadDump(strings.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Analyze()
	var out strings.Builder
	if flags := a.Render(&out); flags == 0 {
		t.Fatal("the unreadable JDK-21 plain format must raise a flag")
	}
	if !strings.Contains(out.String(), "Thread.dump_to_file") {
		t.Fatalf("the refusal must NAME the format so the operator knows the capture may be fine: %s", out.String())
	}
}

// --- virtual-thread incompleteness caveat ---------------------------------

func TestVirtualThreadAppCarriesCaveat(t *testing.T) {
	vt := `Full thread dump OpenJDK 64-Bit Server VM (21.0.2+13 mixed mode):

"VirtualThread-unparker" #20 daemon prio=5 tid=0x1 nid=0x1 waiting on condition  [0x0a]
   java.lang.Thread.State: TIMED_WAITING (parking)
	at jdk.internal.misc.Unsafe.park(java.base@21.0.2/Native Method)

"main" #1 prio=5 tid=0x2 nid=0x2 runnable  [0x0b]
   java.lang.Thread.State: RUNNABLE
	at com.example.App.main(App.java:10)
`
	d, err := ParseThreadDump(strings.NewReader(vt))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Analyze()
	if !a.VirtualApp {
		t.Fatal("the unparker thread is the virtual-thread tell — must be detected")
	}
	var out strings.Builder
	a.Render(&out)
	if !strings.Contains(out.String(), "STRUCTURALLY INCOMPLETE") {
		t.Fatalf("a virtual-thread app's platform dump must carry the incompleteness caveat: %s", out.String())
	}
	if strings.Contains(out.String(), "nothing alarming") {
		t.Fatalf("an incomplete dump must not read 'nothing alarming': %s", out.String())
	}
}

// --- I.1: JSON capture validator ------------------------------------------

func TestValidateThreadDumpJSON(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	os.WriteFile(good, []byte(`{"threads":[{"threadName":"main","threadState":"RUNNABLE"}]}`), 0o600)
	if v := ValidateThreadDumpJSON(good, 0); !v.OK {
		t.Fatalf("a valid Spring JSON thread dump must validate: %s", v.Reason)
	}
	errBody := filepath.Join(dir, "err.json")
	os.WriteFile(errBody, []byte(`{"timestamp":"2026-07-18","status":401,"error":"Unauthorized"}`), 0o600)
	if v := ValidateThreadDumpJSON(errBody, 0); v.OK {
		t.Fatal("a Spring error body has no threads and must NOT validate")
	}
	login := filepath.Join(dir, "login.html")
	os.WriteFile(login, []byte("<html><title>Please sign in</title></html>"), 0o600)
	if v := ValidateThreadDumpJSON(login, 0); v.OK {
		t.Fatal("an HTML login page must NOT validate as a JSON dump")
	}
}

// --- I.2: the printed path must be the real path --------------------------

// slowAcquirer forces the capture across a second boundary — the exact
// condition under which the old CapturedAt-reconstructed path went stale.
type slowAcquirer struct{ fakeAcquirer }

func (a slowAcquirer) Acquire(ctx context.Context, c Cluster, t Resolved, destPath string) (int64, error) {
	// sleep past the next wall-clock second so CapturedAt's second differs
	// from the session directory's timestamp
	now := time.Now()
	time.Sleep(time.Until(now.Truncate(time.Second).Add(1100 * time.Millisecond)))
	return a.fakeAcquirer.Acquire(ctx, c, t, destPath)
}

func TestArtifactPathIsRealEvenAcrossSecondBoundary(t *testing.T) {
	st := &Store{Root: t.TempDir()}
	pipe := Pipeline{Cluster: fakeCluster{}, Store: st}
	acq := slowAcquirer{fakeAcquirer{
		meta: Meta{Name: "threads-actuator.txt", Tier: "actuator"},
		data: []byte("Full thread dump OpenJDK\n\"main\" #1\n   java.lang.Thread.State: RUNNABLE\n"),
	}}
	art, err := pipe.Run(context.Background(), acq, Resolved{Target: Target{Namespace: "ns", Pod: "p1"}}, ValidateThreadDump)
	if err != nil {
		t.Fatal(err)
	}
	if art.Path == "" {
		t.Fatal("Artifact.Path must be set — callers print it")
	}
	if _, serr := os.Stat(art.Path); serr != nil {
		t.Fatalf("the reported path must exist on disk: %v", serr)
	}
	// and the old reconstruction would have been wrong (proves the bug class)
	reconstructed := filepath.Join(st.Root, "pods", "p1", art.CapturedAt.Format("20060102T150405Z"), art.Name)
	if reconstructed == art.Path {
		t.Log("boundary not crossed this run (timing) — Path contract still verified above")
	} else if _, serr := os.Stat(reconstructed); serr == nil {
		t.Fatal("reconstructed path should not exist when the boundary was crossed")
	}
}

// --- I.5: manifest hashes must match the on-disk files, even for failures --

func TestSnapshotManifestHashesMatchDiskForFailedSections(t *testing.T) {
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
		t.Fatal("this setup must produce failed sections")
	}
	sess := &Session{Dir: res.Dir}
	m, err := sess.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range m.Artifacts {
		p := filepath.Join(res.Dir, a.Name)
		st, serr := os.Stat(p)
		if serr != nil {
			t.Fatalf("%s: %v", a.Name, serr)
		}
		if st.Size() != a.Bytes {
			t.Fatalf("%s: manifest says %d bytes, disk has %d — the evidence chain must verify against its own files", a.Name, a.Bytes, st.Size())
		}
		sum, herr := fileSHA256(p)
		if herr != nil {
			t.Fatal(herr)
		}
		if sum != a.SHA256 {
			t.Fatalf("%s: manifest sha256 %s != on-disk %s", a.Name, a.SHA256, sum)
		}
	}
}

// --- snapshot must not record a login page as ✔ health.json ----------------

func TestSnapshotRejectsLoginPageAsHealth(t *testing.T) {
	kit := t.TempDir()
	fc := fakeCluster{pods: []string{"p1"}, exec: func(argv []string, w io.Writer) error {
		script := strings.Join(argv, " ")
		if strings.Contains(script, "/health") {
			io.WriteString(w, "<html><title>Please sign in</title><body>login</body></html>")
			return nil // HTTP 200 from curl's point of view — no -f on health
		}
		if strings.Contains(script, "/threaddump") {
			io.WriteString(w, "Full thread dump OpenJDK\n\"main\" #1\n")
			return nil
		}
		io.WriteString(w, "{}")
		return nil
	}}
	st := &Store{Root: t.TempDir()}
	cfg := Config{KitRoot: kit, DumpsRoot: st.Root, ActuatorBase: "http://x/actuator"}
	r, _ := Resolve(context.Background(), fc, Target{Namespace: "ns"})
	ct, _ := r.Confirm()
	res, err := Snapshot(context.Background(), fc, Pipeline{Cluster: fc, Store: st}, cfg, ct, SnapshotOpts{NoJattach: true})
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{Dir: res.Dir}
	m, _ := sess.Read()
	for _, a := range m.Artifacts {
		if a.Name == "health.json" && a.Verdict.OK {
			t.Fatal("a login page must never be recorded as ✔ health.json")
		}
	}
	if res.Failed == 0 {
		t.Fatal("the login-page health section must count as failed")
	}
}
