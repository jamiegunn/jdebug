---
title: Command reference
nav_order: 4
---

# Command reference

Every command takes `-n/--namespace`, `-l/--selector`, `--container <name>`,
`--actuator-base <url>`, `-h/--help`, and (where a single pod matters) an
optional trailing pod name. `jdebug -V` prints the version.

## Triage — safe, read-only

| command | does | notes |
|---|---|---|
| `jdebug status` | pods, restarts, recent events | output ends with a "how to read this" key |
| `jdebug health` | actuator health, per subsystem + liveness/readiness | DOWN component = failing dependency |
| `jdebug top` | `kubectl top pods` + HPA state | needs metrics-server |
| `jdebug memory` | container RSS vs JVM heap/non-heap, reconciled per pool | needs `python3` on the host; refuses to print a misleading table if metrics fail |
| `jdebug metrics [name]` | list JVM/process/system metric names, or one live value | re-run to trend, e.g. `jvm.gc.pause` |
| `jdebug logs` | stream logs from all matching replicas | requires a selector; uses `stern` if installed |

## Capture — evidence files → `dumps/`

| command | does | risk |
|---|---|---|
| `jdebug threads [--via t]` | thread dump | safe, instant |
| `jdebug heap --confirm [--via t]` | heap dump (hprof) | **pauses the JVM** — seconds on small heaps, minutes on multi-GB |
| `jdebug jcmd "<cmd>"` | any jcmd via jattach (`GC.heap_info`, `VM.native_memory summary`, `JFR.start …`) | mostly safe; individual jcmds vary |
| `jdebug snapshot [--heap --confirm]` | one offline bundle: describe + health + threads + memory + jcmd set (+ optional hprof) | safe unless `--heap` |

With no `--via`, capture **auto-degrades** `actuator → jattach → jdk`,
announcing each fallback. Force one tier with `--via actuator|jattach|jdk`.

## Runtime changes

| command | does | risk |
|---|---|---|
| `jdebug log-level <logger> <LEVEL>` | change a logger live on **every** replica | adds log volume; warns on `ROOT` + `DEBUG/TRACE`; not persistent across restarts; lowercase levels accepted |

## Setup & evidence

| command | does |
|---|---|
| `jdebug doctor` | pre-incident checkup: host tools, captures dir, jattach cache, cluster, target pods, actuator — ✓/!/✗ with fixes, non-zero exit on blockers |
| `jdebug dumps` | list every capture with per-type analysis instructions |
| `jdebug analyze [path]` | first-pass triage of every capture: thread-state histogram, deadlocks, contended locks, hot frames, DOWN health components, OOM-risk %, invalid dumps — with the right deep tool named per finding |
| `jdebug install-jattach` | pre-stage the jattach binary in the pod |
| `jdebug push-local` | copy the in-pod tool (`jdebug-local`) to `<pod>:/tmp` |
| `jdebug wizard` | jump straight into guided diagnosis |

## Exit codes

| code | meaning |
|---|---|
| 0 | success |
| 1 | the operation itself failed (message says why) |
| 2 | target problem (e.g. no pod matched, missing `--confirm` in `jdebug-local`) |
| 3 | environment problem (cluster unreachable, actuator absent, jattach missing) |
| 64 | usage error — bad command, flag, or argument |
| 127 | a required host command is missing |

## Behavior guarantees

- The exact `kubectl`/HTTP command being run is printed (`$ …` lines) before
  it runs — every capture doubles as a copy-pasteable cookbook.
- When several pods match and none was named, the choice is announced and the
  alternatives listed. Nothing is targeted silently.
- Captured files are validated (`Full thread dump` marker, hprof
  `JAVA PROFILE` magic); a wrong-looking capture fails loudly and the file is
  kept for inspection.
- Every cluster-touching command checks connectivity first and translates
  failures into plain language (see [Troubleshooting](troubleshooting)) —
  you should never see a raw x509 stack trace.
