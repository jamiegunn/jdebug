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

## Not the JVM — the pod itself (`jdebug why`)

Not every "the app is broken" is inside the process. `jdebug why` reads the
kubernetes layer and explains each finding for someone who has never opened a
pod spec:

```sh
jdebug why
```

- **exit codes decoded** — 137 = SIGKILL (with `OOMKilled` = the kernel hit
  your memory *limit*; without it, something force-killed the container or a
  liveness probe gave up). 143 = SIGTERM, a *polite* shutdown (a deploy or a
  scale-down asked it to stop — not a crash). 1 = the app errored; its
  `--previous` logs have the exception.
- **requests vs limits** — requests are the scheduler's promise, limits are
  the hard ceiling. No memory limit → the container can starve its
  neighbours. No memory request → first to be evicted. The report says which.
- **probes** — a missing readiness probe means traffic hits the app before
  it's ready (startup 502s); an over-eager liveness probe *restarts a
  healthy-but-slow app*, which looks exactly like a crash loop but is fixed by
  loosening the probe, not touching the code.
- **memory beyond the heap** — the cgroup breakdown catches leaks no JVM tool
  sees: a growing file in a `tmpfs`/`emptyDir` volume counts against the
  memory limit and OOM-kills you with a "leak" that isn't in the heap at all.
- **HPA fights** — if the Deployment manifest pins `replicas:` while an HPA
  manages the same Deployment, every deploy resets the count and they fight.
  And an HPA whose metric source is missing (no metrics-server) is **blind** —
  `ScalingActive=False`, so it silently does nothing.

**No metrics-server?** `why`, `top`, and the panel all say so explicitly
rather than showing blanks — requests/limits still come from the spec, but
live usage genuinely doesn't exist and any CPU/memory HPA is inert.

## Is the pod safe? (`jdebug security`)

```sh
jdebug security
```

A plain-language posture audit — it *verifies the live uid* (root vs not) by
running `id` in the container rather than trusting the spec, then walks
privilege escalation, capabilities, read-only rootfs, host namespaces,
service-account token exposure (any code-exec in a pod with a mounted token
can call the k8s API), and NetworkPolicy reachability. Each ⚠ names a
one-line `securityContext`/manifest fix. Where RBAC blocks a check, it says
**UNKNOWN, not "fine"** — a denied read is never treated as a pass.

Both `why` and `security` are folded into `jdebug snapshot`, and their ⚠
findings surface in `jdebug analyze`'s summary alongside the JVM ones.

## Reading the analyzers

All recommended tools are free and run locally — evidence never has to leave
your machine:

| evidence (under `dumps/pods/<pod>/<ts>/`) | tool (local install) | first click |
|---|---|---|
| anything captured | `jdebug analyze` (built in) | the ⚠ findings |
| `threads-*.txt` | [VisualVM](https://visualvm.github.io/) | File → Load, check the thread states |
| `heap-*.hprof` | [Eclipse MAT](https://eclipse.dev/mat/) | *Leak Suspects* report |
| `heap-*.hprof` (two) | Eclipse MAT | *compare to another heap dump* |
| `*.jfr` | [JDK Mission Control](https://openjdk.org/projects/jmc/) | Method Profiling flame view |
| a snapshot bundle (`.snapshot` marker) | a text editor | `memory-report.txt`, then `threads.txt` |
