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

## 3. Point it at your app — the menu insists

You can't fire a capture at the wrong thing: the tools stay hidden behind a
✓/✗ checklist until the cluster answers **and** you've picked a specific pod
and container. The panel tells you exactly which key fixes each ✗; Enter (or
`g`) opens the target editor — each field is one keypress, and everything the
cluster can enumerate is a live dropdown:

- **c context** — your kube contexts (switching is confirmed first)
- **n namespace** — listed from the cluster
- **s selector** — built from the `app` labels actually on the pods there
- **o container / p pod** — read from the pod spec; the pod picker shows
  restart counts (*the restarting one is usually the sick one*)

The header's one-line status always shows exactly what you're pointed at,
with a live green/red reachability dot. Menu keys act instantly — no Enter;
every key is a letter from the action's own name, risk shows as a colored
dot per row, and the one dangerous action (`H` heap) confirms by asking for
a second `H`.

## 4. Look around — all safe, read-only

| key | shows | how to read it |
|---|---|---|
| `s` status | pods, restarts, events | the output ends with what CrashLoopBackOff, OOMKilled, and Pending mean |
| `h` health | the app's own health checks | a DOWN component names the failing dependency — chase that system first |
| `o` top | CPU + memory per pod | memory near the limit = OOM risk |
| `m` memory | container total vs JVM heap/non-heap | the classic "heap is fine but the pod died" gap, explained |

## 5. Or just describe the symptom

Press `w`. The wizard asks what you're seeing — OOMKilled, slow/hung,
high CPU, memory creeping, GC pauses, or "not sure" — then runs the right
capture sequence, explains each result as it lands, and names the analysis
tool to open next. Anything that could hurt the app asks first.

## 6. Find your evidence

Press `d` (or run `jdebug dumps`) to list every capture newest-first with
instructions per file type, and `a` (`jdebug analyze`) for a built-in
first-pass triage — deadlocks, blocked pools, DOWN components, OOM risk.
For deeper digs the recommended tools are free **local installs** —
[VisualVM](https://visualvm.github.io/) for thread dumps, Eclipse MAT for
heap dumps — so evidence never leaves your machine. Your whole session (every
command and its output) is also transcribed to `dumps/session-<timestamp>.log`,
so nothing you saw on screen is ever lost.

## The three keys to remember

- `w` — guided diagnosis. When in doubt, start here.
- `?` — help: the glossary, the workflow, the safety rules, the hidden keys.
- `d` — everything you've captured and what to do with it (`a` analyzes it all).
