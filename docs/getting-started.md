---
title: Getting started
nav_order: 3
---

# Getting started

A first session, end to end. No prior Kubernetes or JVM knowledge is assumed
— the tool defines its own terms (`h` in the menu is a full glossary).

## 1. Check your setup

```sh
jdebug doctor
```

Fix anything marked ✗ before continuing — each line says how.

## 2. Open the menu

```sh
jdebug
```

The first question is **where the JVM is**:

1. **Remote** — you're on your laptop, reaching a pod via kubectl (the usual case; *not sure? pick this*)
2. **In-pod** — you already have a shell inside the container
3. **Bare metal** — the JVM runs on this very machine

## 3. Point it at your app

Press `t` for the target editor — each field is one keypress, and everything
the cluster can enumerate is a live dropdown:

- **c context** — your kube contexts (switching is confirmed first)
- **n namespace** — listed from the cluster
- **s selector** — built from the `app` labels actually on the pods there
- **o container / p pod** — read from the pod spec; the pod picker shows
  restart counts (*the restarting one is usually the sick one*)

The header always shows exactly what you're pointed at, plus a live
✓/✗ cluster-reachability indicator. Menu keys act instantly — no Enter;
only confirmations (heap dumps, quitting) ask for a deliberate y.

## 4. Look around — all safe, read-only

| key | shows | how to read it |
|---|---|---|
| `1` status | pods, restarts, events | the output ends with what CrashLoopBackOff, OOMKilled, and Pending mean |
| `2` health | the app's own health checks | a DOWN component names the failing dependency — chase that system first |
| `3` top | CPU + memory per pod | memory near the limit = OOM risk |
| `4` memory | container total vs JVM heap/non-heap | the classic "heap is fine but the pod died" gap, explained |

## 5. Or just describe the symptom

Press `w`. The wizard asks what you're seeing — OOMKilled, slow/hung,
high CPU, memory creeping, GC pauses, or "not sure" — then runs the right
capture sequence, explains each result as it lands, and names the analysis
tool to open next. Anything that could hurt the app asks first.

## 6. Find your evidence

Press `d` (or run `jdebug dumps`). Every capture is listed newest-first with
instructions per file type — thread dumps go to [fastthread.io](https://fastthread.io),
heap dumps to Eclipse MAT's *Leak Suspects*. Your whole session (every command
and its output) is also transcribed to `dumps/session-<timestamp>.log`, so
nothing you saw on screen is ever lost.

## The three keys to remember

- `w` — guided diagnosis. When in doubt, start here.
- `h` — help: the glossary, the workflow, the safety rules.
- `d` — everything you've captured and what to do with it.
