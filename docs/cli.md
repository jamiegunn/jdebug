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
| `jdebug why [pod]` | kubernetes-layer deep-dive: requests/limits, QoS, probes, exit-code meanings, cgroup memory beyond the JVM, HPA scaling rules and the classic replicas-vs-HPA fight | every finding explained in plain language; degrades loudly when metrics-server/RBAC block a check |
| `jdebug security [pod]` | pod security posture: live-verified uid (root?), privilege/capabilities, read-only rootfs, host namespaces, service-account token exposure, NetworkPolicy reachability | each ⚠ names its one-line fix; unknowns are flagged, never assumed safe |
| `jdebug topology [pod]` | the workload tree: Deployment → its ReplicaSets (current + old revisions) → pods, plus HPA and routing Services | flags old ReplicaSets still running pods (mid/stuck rollout) and the replicas-vs-HPA fight; explains what's current vs stale |
| `jdebug workload [pod]` | `topology` + `why` in one scroll — the rollout tree, then the pod deep-dive (limits, probes, exit codes, autoscaling). This is what the TUI's `W` key runs | run `topology` or `why` alone if you only want one half |
| `jdebug context [pod]` | how the app is wired: Services/Endpoints that expose it, env & config refs, probes, volumes, and dependencies (Valkey/Redis) | secret **values** redacted — names/keys/refs only |
| `jdebug health` | actuator health, per subsystem + liveness/readiness | DOWN component = failing dependency |
| `jdebug top` | `kubectl top pods` + HPA state | needs metrics-server |
| `jdebug memory` | container RSS vs JVM heap/non-heap, reconciled per pool | needs `python3` on the host; refuses to print a misleading table if metrics fail |
| `jdebug metrics [name]` | list JVM/process/system metric names, or one live value | re-run to trend, e.g. `jvm.gc.pause` |
| `jdebug logs` | stream logs from all matching replicas | requires a selector; uses `stern` if installed |
| `jdebug what-changed [pod]` | answers "did something change?" — image/imageID, restart & rollout times, events since the last restart, `--previous` logs, probe failures, HPA-vs-Deployment replicas | composed from data you already have; read-only |
| `jdebug timeline [pod]` | merges `kubectl get events` with the session's capture timestamps into one chronological view | read-only |
| `jdebug escalate [pod]` | builds a handoff brief from session state: target, symptom, findings, commands run, captures + paths, blocked checks, suggested next action, sensitive-evidence warning | read-only; redaction on |

## Capture — evidence files → `dumps/`

| command | does | risk |
|---|---|---|
| `jdebug threads [--via t]` | thread dump | safe, instant |
| `jdebug heap --confirm [--via t]` | heap dump (hprof) | **pauses the JVM** — seconds on small heaps, minutes on multi-GB |
| `jdebug jcmd "<cmd>"` | any jcmd via jattach (`GC.heap_info`, `VM.native_memory summary`, `JFR.start …`) | mostly safe; individual jcmds vary |
| `jdebug snapshot [--heap --confirm]` | one offline bundle: describe + **why** + **security** + health + threads + memory + jcmd set (+ optional hprof) | safe unless `--heap` |

With no `--via`, capture **auto-degrades** `actuator → jattach → jdk`,
announcing each fallback. Force one tier with `--via actuator|jattach|jdk`.

## Runtime changes

| command | does | risk |
|---|---|---|
| `jdebug log-level <logger> <LEVEL>` | change a logger live on **every** replica | adds log volume; warns on `ROOT` + `DEBUG/TRACE`; not persistent across restarts; lowercase levels accepted |
| `jdebug restart [pod] --confirm` | **re-roll** the owning Deployment (`kubectl rollout restart`), then watch it | rolling restart of every pod — explains the downtime/state risk; refuses without `--confirm`; RBAC/ownership failures explained |
| `jdebug kill [pod] --confirm` | **delete** a pod (`kubectl delete pod`) | graceful SIGTERM → grace → SIGKILL; managed pods respawn, unmanaged ones don't (it says which); refuses without `--confirm` |

## Setup & evidence

| command | does |
|---|---|
| `jdebug doctor` | pre-incident checkup: host tools, captures dir, jattach cache, cluster, target pods, actuator — ✓/!/✗ with fixes, non-zero exit on blockers |
| `jdebug fetch-heap [dir] [pod]` | retrieve the hprof the JVM wrote **on crash** (`-XX:+HeapDumpOnOutOfMemoryError`) — the only heap that exists for an OOMKilled pod; explains the setup when it finds nothing | read-only; size-verified copy; needs the v2 engine (`make core`) |
| `jdebug dumps` | list every capture with per-type analysis instructions |
| `jdebug analyze [path]` | first-pass triage of every capture: thread-state histogram (idle NIO/epoll selectors named, not mistaken for a busy loop), deadlocks, contended locks, real hot frames, DOWN health components, OOM-risk %, invalid dumps — with the right deep tool named per finding |
| `jdebug analyze --deep <heap>` | heap dump **retained size** (dominator tree) + **path to GC roots** — which objects keep memory alive and why. No Eclipse MAT needed |
| `jdebug analyze --diff [before after]` | diff two heap dumps to see what **grew** (leak hunting); with no args it auto-picks the two newest valid dumps |
| `jdebug cleanup` | prune old/empty captures and stale remote artifacts from `dumps/` |
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
