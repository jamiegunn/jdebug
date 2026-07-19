---
title: In-pod & bare metal (jdebug-local)
nav_order: 6
---

# jdebug-local — diagnostics from inside the box

`jdebug` drives a pod from *outside* via kubectl. When you only have a shell
*inside* the container (a `kubectl exec -it` session, a debug sidecar,
`nsenter` from the node) — or the JVM isn't in Kubernetes at all —
**`jdebug-local`** runs the same captures against `localhost` and `/proc`.
It is a single POSIX-`sh` file: it runs under busybox `ash` on a stock
JRE-alpine image with nothing added, and never touches the network beyond
localhost.

## Getting it in

```sh
jdebug push-local                                    # kubectl cp → <pod>:/tmp/jdebug-local
# or: kubectl cp jdebug-local <ns>/<pod>:/tmp/jdebug-local -c app
# or: paste the file into the pod shell
```

Then, inside:

```sh
sh /tmp/jdebug-local help          # full help; `help <cmd>` for per-command detail
sh /tmp/jdebug-local memory
sh /tmp/jdebug-local threads > /tmp/threads.txt
sh /tmp/jdebug-local snapshot --heap --confirm
```

## Commands

| command | does | needs |
|---|---|---|
| `health` | actuator health, per subsystem | curl or wget in the container |
| `metrics [name]` | metric names, or one live value | same |
| `memory` | container RSS (cgroup v1/v2) vs JVM heap/non-heap/threads | same |
| `threads` | jstack-format thread dump → stdout | same; auto-falls back to jattach |
| `heap --confirm` | hprof → `$OUT_DIR` — **pauses the JVM** | same; auto-falls back to jattach |
| `jcmd "<cmd>"` | full jcmd surface | jattach staged at `/tmp/jattach` |
| `snapshot [--heap --confirm]` | offline bundle in `$OUT_DIR`, tarred | best-effort per section; `--heap` pauses the JVM and requires `--confirm` |
| `dumps` | list captures **with a ready-to-paste extraction command** | — |

## Getting evidence out

`jdebug-local` fills the extraction command in for you: inside a pod it knows
its own pod name (hostname) and namespace (the serviceaccount mount), so
after a heap dump it prints something like

```
pull it to your machine — run this OUTSIDE the pod:
    kubectl -n payments cp payments-7d9f4b-x2k1p:/tmp/heap-20260704T181530Z.hprof ./heap-20260704T181530Z.hprof
```

On bare metal it just prints the path — the file is already on your machine.
When jdebug drove it over SSH (it sets `JDEBUG_SSH_BACK` to the `user@host` it
used), it instead prints the exact `scp user@host:… .` to pull the capture back
to your machine.

## Environment

| var | default | meaning |
|---|---|---|
| `ACTUATOR_BASE` | `http://localhost:8080/actuator` | also `-a/--actuator-base` |
| `JATTACH_BIN` | `/tmp/jattach` | also `--jattach-bin` |
| `JVM_PID` | auto: pgrep java → `/proc` comm scan → libjvm map scan; **errors if none found** (no PID-1 guess) | set it when several JVMs run on the box |
| `OUT_DIR` | `/tmp` | where dumps and bundles land |
| `JDEBUG_SSH_BACK` | *(unset)* | set by jdebug to the `user@host` when it runs this script over SSH, so capture hints print the `scp` to pull files back |
| `JDEBUG_REQUIRE_DATA_ACK` | *(unset)* | when set, a heap dump requires `JDEBUG_DATA_ACK=1` (governance opt-in for regulated environments) |
| `JDEBUG_DATA_ACK` | *(unset)* | acknowledges the heap-dump data-handling notice when governance is required |

The `jdk` tier is not available in-pod (it needs `kubectl debug` from
outside); actuator, jattach, and the memory report all work.
