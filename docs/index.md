---
title: Home
nav_order: 1
---

# jdebug — JVM debug kit for Kubernetes

Capture and analyze JVM diagnostics from a pod **without a JDK in the image**,
and without assuming any prior Kubernetes or JVM background — the tool
explains what it is doing, what the output means, and what to do next, every
step of the way.

```
jdebug            # interactive menu — press w for guided diagnosis
jdebug doctor     # check everything works BEFORE you need it
```

## Why it exists

Production Java images are usually JRE-only: no `jstack`, no `jmap`, no
`jcmd`. When a pod is OOMKilled at 2am, the standard tools aren't there.
jdebug gets the same evidence anyway, using three capture routes in order of
preference (see [Capture tiers](capture-tiers)):

| Tier | Route | Needs |
|---|---|---|
| 1 | **actuator** — ask the app itself over HTTP | Spring Boot actuator endpoints |
| 2 | **jattach** — ~80 KB helper binary placed in the pod | same-uid exec into the pod |
| 3 | **jdk** — temporary JDK debug container | cluster allows ephemeral containers |

With no tier forced, captures **auto-degrade** — try tier 1, announce the
fallback, try tier 2, then tier 3 — so one command works across wildly
different clusters and policies.

## What it deliberately is

- **Explains itself.** Every command prints the exact `kubectl` line it runs
  (copy-paste it yourself next time), interprets its own output
  ("how to read this"), and names the next step and the right analysis tool.
- **Safe by default.** Everything is read-only except heap dumps (which pause
  the JVM) and log-level changes — both are labeled in the menu, and anything
  destructive asks first, every time.
- **Evidence you can't lose.** Captures land in one `dumps/` directory, every
  interactive session is transcribed to a log, and `jdebug dumps` lists what
  you have with per-file analysis instructions.
- **Guided.** `jdebug wizard` asks what you're seeing — OOMKilled, slow,
  high CPU, creeping memory, GC pauses — and runs the right capture sequence
  for that symptom. The menu's `h` key is a full glossary.

## Start here

1. [Install](install) — one symlink, three requirements
2. [Getting started](getting-started) — your first session, end to end
3. [Diagnosis playbooks](playbooks) — symptom → captures → analysis
4. [Troubleshooting](troubleshooting) — every error message, decoded
