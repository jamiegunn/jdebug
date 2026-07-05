---
title: Diagnosis playbooks
nav_order: 8
---

# Diagnosis playbooks

These are the recipes the wizard (`w`) runs for you, written out — the same
sequences work from the CLI. Each ends with which tool analyzes the evidence.

## Pod OOMKilled / restarting on memory

**Question one: is the memory in the Java heap, or somewhere else?**

```sh
jdebug memory
```

- **heap ≈ container limit** → heap pressure or a leak. Take a heap dump
  (`jdebug heap --confirm` — pauses the app) and open it in **Eclipse MAT →
  Leak Suspects**.
- **heap low, RSS high** → the memory is off-heap: metaspace, direct buffers,
  thread stacks, or native. Run `jdebug jcmd "VM.native_memory summary"`
  (needs `-XX:NativeMemoryTracking=summary` in the JVM flags) and look for
  the growing category.

The memory report itemizes every pool and prints an **Unaccounted** line —
if that is large *and growing*, suspect a native leak (JNI, direct buffers).

## Slow / hung / high latency

```sh
jdebug threads     # safe, instant
jdebug health
```

Run `jdebug analyze` — it flags deadlocks, blocked threads, the most-contended
locks, and hot frames automatically — then open the dump in
[VisualVM](https://visualvm.github.io/) (free, runs locally; dumps never leave
your machine). Things to look for by hand: many threads `BLOCKED`/`WAITING` on
the same lock or connection pool (database pool exhaustion looks like dozens
of threads waiting in `getConnection`), and any `DOWN` component in health —
a failing dependency makes the app look sick when it's actually the victim.

## High CPU / autoscaler adding pods

```sh
jdebug threads && sleep 5 && jdebug threads
jdebug metrics process.cpu.usage
```

Diff the two dumps: a stack that is `RUNNABLE` in **both** is your hot loop.
For hard cases, record a profile:

```sh
jdebug jcmd "JFR.start duration=60s filename=/tmp/rec.jfr"
# …60s later the pod has /tmp/rec.jfr — kubectl cp it out, open in JDK Mission Control
```

## Memory creeping up (suspected leak)

A leak = objects that survive and accumulate. The proof is two heap dumps:

1. Note the number: `jdebug metrics jvm.memory.used`
2. Baseline: `jdebug heap --confirm`
3. Let the app take traffic; watch the metric grow
4. Second dump: `jdebug heap --confirm`
5. Eclipse MAT → open both → *compare to another heap dump* (dominator trees).
   Whatever grew is your leak, with the reference chain that keeps it alive.

## GC pauses climbing

```sh
jdebug jcmd "GC.heap_info"
jdebug memory
jdebug metrics jvm.gc.pause     # COUNT + TOTAL_TIME — note it, wait, re-run
```

If TOTAL_TIME grows fast while the heap stays near-full, the collector is
thrashing: allocation pressure or a leak. Follow the leak playbook.

## Not sure — capture everything

```sh
jdebug snapshot            # + --heap --confirm to include an hprof
```

One bundle: describe, health, metrics, threads, memory report, and the jcmd
set (GC, VM.flags, code cache, classloaders, NMT). Hand it to a colleague or
analyze offline — production is touched once.

## Crash-looping / CrashLoopBackOff

The pod dies right at (or shortly after) startup and kubernetes backs off
between retries. Two questions: how often, and what did it say on the way
down?

```sh
jdebug status            # RESTARTS count + the events k8s recorded
jdebug logs --previous   # the PREVIOUS container's last lines
```

The crash reason is almost always in the previous container's final lines:

- **`OutOfMemoryError` / exit 137** → it's memory — run the OOM playbook
  above (wizard flow 1).
- **A stack trace** → the failing class names the culprit; startup config or
  a missing dependency is the usual story.
- **Nothing useful in the logs** → the events from `status` carry the
  kubernetes-side reasons: image pull failures, failed probes killing the
  container, scheduling problems.

## Reading the analyzers

All recommended tools are free and run locally — evidence never has to leave
your machine:

| evidence | tool (local install) | first click |
|---|---|---|
| anything captured | `jdebug analyze` (built in) | the ⚠ findings |
| `threads/*.txt` | [VisualVM](https://visualvm.github.io/) | File → Load, check the thread states |
| `heap/*.hprof` | [Eclipse MAT](https://eclipse.dev/mat/) | *Leak Suspects* report |
| `heap/*.hprof` (two) | Eclipse MAT | *compare to another heap dump* |
| `*.jfr` | [JDK Mission Control](https://openjdk.org/projects/jmc/) | Method Profiling flame view |
| `snapshot-*/` | a text editor | `memory-report.txt`, then `threads.txt` |
