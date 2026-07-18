---
title: Evidence & data handling
nav_order: 9
---

# Evidence & data handling

## Where everything goes

All operator-side captures land under the kit's own `dumps/` directory —
never the directory you happened to run the command from:

```
dumps/
  pods/<pod>/<ts>/          one capture session: threads-<tier>.txt,
                            heap-<tier>.hprof, manifest.json (sha256 +
                            validation verdict per artifact); a `.snapshot`
                            marker means the session is a snapshot bundle
                            (describe, health, metrics, threads, memory, jcmd-*)
  session-<ts>.log          (transcript of every menu command + its output)
```

(Captures made by very old versions used a flat `dumps/threads/…`,
`dumps/heap/…` layout — `jdebug dumps` still lists those under
"older flat captures".)

`jdebug dumps` (or `d` in the menu) lists it all, newest first, with the
right analyzer for each file type — the dashboard's CAPTURES pane shows the
same per-artifact next step inline. Override per run with `$OUT_DIR`, move
the root with `$JDEBUG_DUMPS`. The directory is git-ignored.

| artifact | what to do with it |
|---|---|
| `threads/*.txt` | press `a` (analyze) — flags deadlocks, blocked pools, hot loops; deeper: open in VisualVM |
| `heap/*.hprof` | Eclipse MAT → "Leak Suspects" |
| `*.jfr` | JDK Mission Control |
| `snapshot-*/` | press `a` for a first pass; start reading at `memory-report.txt` and `threads.txt` |
| `session-*.log` | the timeline of what happened — everything you ran and what it printed |

In-pod captures (`jdebug-local`) go to the container's `/tmp`; `dumps` there
prints a **ready-to-paste `kubectl cp`** with the pod name and namespace
filled in automatically.

## Validation

Captures are checked, not assumed: thread dumps must contain the
`Full thread dump` marker, hprof files must start with the `JAVA PROFILE`
magic bytes. A capture that looks wrong fails loudly and the file is kept
for inspection — you'll never analyze a truncated error page by mistake.

## Treat dumps like production data

A heap dump is **everything the app had in memory** — which can include user
records, session tokens, credentials in flight. Expectations:

- keep dumps on machines with the same access controls as production data
- analyze **locally only** — every tool jdebug recommends (its own `analyze`,
  VisualVM, Eclipse MAT, JDK Mission Control) is a free local install; never
  upload dumps to web-based analyzers
- delete dumps when the investigation closes
- the session log records command *output* — the same considerations apply

## Impact expectations

| action | impact |
|---|---|
| status / health / top / metrics / memory / threads / logs / dumps / doctor | none — read-only |
| snapshot (without `--heap`) | none, plus a small static helper binary staged in `/tmp` for the jcmd sections (skippable: `--no-jattach`) |
| heap dump, `snapshot --heap` | **the JVM pauses** for the duration of the write — seconds on small heaps, minutes on multi-GB. Requires `--confirm` everywhere, plus an interactive y/N in the menu |
| log-level | log volume changes on every replica; not persistent across restarts |
| install-jattach / push-local | a file placed in the container's `/tmp`; gone on restart |
| tier 3 (jdk) | a terminated ephemeral container stays visible in the pod spec until restart — harmless |
