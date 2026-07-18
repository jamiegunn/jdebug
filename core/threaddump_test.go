package core

import (
	"strings"
	"testing"
)

// The F4 scenario: a REAL deadlock captured via the DEFAULT tier. Actuator
// text dumps carry no "Found one Java-level deadlock" banner — v1's grep
// said "nothing alarming". The lock graph must find it anyway.
const deadlockNoBanner = `Full thread dump mock JVM
"txn-1" #10
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.Transfer.debit(Transfer.java:10)
	- waiting to lock <0xB> (a java.lang.Object)
	- locked <0xA> (a java.lang.Object)
"txn-2" #11
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.Transfer.credit(Transfer.java:20)
	- waiting to lock <0xA> (a java.lang.Object)
	- locked <0xB> (a java.lang.Object)
"idle-1" #12
   java.lang.Thread.State: WAITING (parking)
	at jdk.internal.misc.Unsafe.park(Native Method)
`

func TestDeadlockFoundWithoutBanner(t *testing.T) {
	d, err := ParseThreadDump(strings.NewReader(deadlockNoBanner))
	if err != nil {
		t.Fatal(err)
	}
	if d.JstackBanner {
		t.Fatal("fixture must not carry the banner — that is the point")
	}
	a := d.Analyze()
	if len(a.DeadlockCycles) != 1 {
		t.Fatalf("lock-graph must find the cycle (F4), got %v", a.DeadlockCycles)
	}
	cyc := strings.Join(a.DeadlockCycles[0], "→")
	if !strings.Contains(cyc, "txn-1") || !strings.Contains(cyc, "txn-2") {
		t.Fatalf("cycle must name both threads: %s", cyc)
	}
	var out strings.Builder
	flags := a.Render(&out)
	if flags == 0 || !strings.Contains(out.String(), "DEADLOCK detected") {
		t.Fatalf("render must flag the deadlock:\n%s", out.String())
	}
	if strings.Contains(out.String(), "nothing alarming") {
		t.Fatal("a deadlocked dump must never get the all-clear")
	}
}

func TestDeadlockFoundInActuatorJSON(t *testing.T) {
	// java.lang.management.ThreadInfo JSON, as /actuator/threaddump serves it
	js := `{"threads":[
	 {"threadName":"txn-1","threadState":"BLOCKED","lockName":"java.lang.Object@1a2b",
	  "lockOwnerName":"txn-2","stackTrace":[{"className":"com.example.Transfer","methodName":"debit"}]},
	 {"threadName":"txn-2","threadState":"BLOCKED","lockName":"java.lang.Object@3c4d",
	  "lockOwnerName":"txn-1","stackTrace":[{"className":"com.example.Transfer","methodName":"credit"}]},
	 {"threadName":"main","threadState":"RUNNABLE","stackTrace":[{"className":"com.example.App","methodName":"run"}]}
	]}`
	d, err := ParseThreadDump(strings.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	a := d.Analyze()
	if len(a.DeadlockCycles) != 1 {
		t.Fatalf("JSON deadlock via lockOwnerName must be found, got %+v", a.DeadlockCycles)
	}
	if a.Total != 3 || a.Blocked != 2 || a.Runnable != 1 {
		t.Fatalf("counts: %+v", a)
	}
}

func TestContentionWithoutDeadlockIsNotACycle(t *testing.T) {
	// two threads blocked on the SAME lock with no holder in the dump:
	// contention, not deadlock (the v1 suite's fixture shape)
	dump := `Full thread dump mock
"worker-1" #12
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.Db.get(Db.java:5)
	- waiting to lock <0x12345> (a java.lang.Object)
"worker-2" #13
   java.lang.Thread.State: BLOCKED (on object monitor)
	at com.example.Db.get(Db.java:5)
	- waiting to lock <0x12345> (a java.lang.Object)
`
	d, _ := ParseThreadDump(strings.NewReader(dump))
	a := d.Analyze()
	if len(a.DeadlockCycles) != 0 {
		t.Fatalf("shared contention must not read as a deadlock: %v", a.DeadlockCycles)
	}
	if len(a.ContendedLocks) != 1 || a.ContendedLocks[0].Count != 2 {
		t.Fatalf("contention: %+v", a.ContendedLocks)
	}
	var out strings.Builder
	a.Render(&out)
	if !strings.Contains(out.String(), "waiting to lock <0x12345>") {
		t.Fatalf("most-contended lock must be shown:\n%s", out.String())
	}
}

func TestIdleSelectorsAreNotHotFrames(t *testing.T) {
	dump := `Full thread dump mock
"reactor-http-epoll-1" #20
   java.lang.Thread.State: RUNNABLE
	at java.base@21.0.11/sun.nio.ch.EPoll.wait(Native Method)
"reactor-http-epoll-2" #21
   java.lang.Thread.State: RUNNABLE
	at java.base@21.0.11/sun.nio.ch.EPoll.wait(Native Method)
"reactor-http-epoll-3" #22
   java.lang.Thread.State: RUNNABLE
	at java.base@21.0.11/sun.nio.ch.EPoll.wait(Native Method)
`
	d, _ := ParseThreadDump(strings.NewReader(dump))
	a := d.Analyze()
	if a.IdleRunnable != 3 || a.HotFrame != "" {
		t.Fatalf("idle selectors misread: %+v", a)
	}
	var out strings.Builder
	flags := a.Render(&out)
	if flags != 0 || !strings.Contains(out.String(), "parked in native I/O") {
		t.Fatalf("idle dump must render calm:\n%s", out.String())
	}
}

func TestHotFrameFlagged(t *testing.T) {
	var b strings.Builder
	b.WriteString("Full thread dump mock\n")
	for i := 0; i < 4; i++ {
		b.WriteString("\"crunch-" + string(rune('0'+i)) + "\" #3" + string(rune('0'+i)) + "\n")
		b.WriteString("   java.lang.Thread.State: RUNNABLE\n")
		b.WriteString("\tat com.example.Hash.mine(Hash.java:1)\n")
	}
	d, _ := ParseThreadDump(strings.NewReader(b.String()))
	a := d.Analyze()
	if a.HotFrameCount != 4 || !strings.Contains(a.HotFrame, "Hash.mine") {
		t.Fatalf("hot frame: %+v", a)
	}
	var out strings.Builder
	if a.Render(&out); !strings.Contains(out.String(), "hot frame: 4×") {
		t.Fatalf("hot frame must be flagged:\n%s", out.String())
	}
}

// --- memory diff (F9) ---------------------------------------------------------

const memBefore = `== Pod pod-a ====================================================
  Container RSS         :    400.0 MiB  (cgroup memory.current)
  Container limit       :    512.0 MiB  (cgroup memory.max — what k8s OOM-kills on)

  JVM heap
    used                :    200.0 MiB
    committed           :    256.0 MiB
    max                 :    256.0 MiB
  JVM off-heap
    direct buffers      :     20.0 MiB  (NIO/Netty/Lettuce)
    thread stacks       :     40.0 MiB  (40 threads × ~1 MiB)
  Accounted             :    300.0 MiB  (heap + nonheap pools + direct + mapped + stacks)
  Unaccounted           :    100.0 MiB  (JVM internal overhead + native libs + allocator waste)
`

const memAfterNative = `== Pod pod-a ====================================================
  Container RSS         :    480.0 MiB  (cgroup memory.current)
  Container limit       :    512.0 MiB  (cgroup memory.max — what k8s OOM-kills on)

  JVM heap
    used                :    205.0 MiB
    committed           :    256.0 MiB
    max                 :    256.0 MiB
  JVM off-heap
    direct buffers      :     22.0 MiB  (NIO/Netty/Lettuce)
    thread stacks       :     40.0 MiB  (40 threads × ~1 MiB)
  Accounted             :    308.0 MiB  (heap + nonheap pools + direct + mapped + stacks)
  Unaccounted           :    172.0 MiB  (JVM internal overhead + native libs + allocator waste)
`

func TestMemDiffPointsAtNativeLeak(t *testing.T) {
	b, err := ParseMemReport(strings.NewReader(memBefore))
	if err != nil {
		t.Fatal(err)
	}
	a, err := ParseMemReport(strings.NewReader(memAfterNative))
	if err != nil {
		t.Fatal(err)
	}
	if b["Container RSS"] != 400.0 || a["Unaccounted"] != 172.0 {
		t.Fatalf("parse: before=%v after=%v", b, a)
	}
	var out strings.Builder
	DiffMemReports(b, a, &out)
	s := out.String()
	if !strings.Contains(s, "NATIVE leak") {
		t.Fatalf("80 MiB RSS growth with 72 MiB unaccounted must point at native:\n%s", s)
	}
	if !strings.Contains(s, "VM.native_memory") {
		t.Fatalf("next step must be NMT:\n%s", s)
	}
}

func TestMemDiffPointsAtHeap(t *testing.T) {
	b, _ := ParseMemReport(strings.NewReader(memBefore))
	a, _ := ParseMemReport(strings.NewReader(memBefore))
	a["Container RSS"] = 470.0
	a["used"] = 265.0
	var out strings.Builder
	DiffMemReports(b, a, &out)
	if !strings.Contains(out.String(), "the growth IS the heap") {
		t.Fatalf("heap-dominated growth must say so:\n%s", out.String())
	}
}

func TestMemDiffNoGrowth(t *testing.T) {
	b, _ := ParseMemReport(strings.NewReader(memBefore))
	a, _ := ParseMemReport(strings.NewReader(memBefore))
	var out strings.Builder
	DiffMemReports(b, a, &out)
	if !strings.Contains(out.String(), "no meaningful growth") {
		t.Fatalf("identical reports must give a defensible all-clear:\n%s", out.String())
	}
}
