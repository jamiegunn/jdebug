---
title: Getting started
nav_order: 3
---

# Getting started

A first session, end to end. No prior Kubernetes or JVM knowledge is assumed
— the tool defines its own terms (`?` in the menu opens a full glossary).

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

- **k auth** — for a *secured* actuator, name the pod's own credential env
  vars (`bearer:ENV_VAR` or `basic:USER_VAR:PASS_VAR`). jdebug stores only the
  reference, never the secret — it's read inside the pod at call time. No
  actuator auth? jattach captures need no HTTP at all
- **c context** — which cluster kubectl talks to (switching is confirmed first)
- **n namespace** — the app's "folder" in the cluster, listed live
- **s selector** — the label (like `app=payments`) that finds your app's pods.
  Suggestions come from the labels actually on the pods (your selected pod's
  own labels first), show how many pods each matches, and stick to stable
  workload keys — rollout internals like `pod-template-hash` are never
  offered. Nothing is ever auto-picked: you always confirm
- **o container / p pod** — a pod is one running copy of the app; the container
  is the app's box inside it. Both are read from the pod spec, and the pod
  picker shows restart counts (*the restarting one is usually the sick one*)

Each field carries the same plain-language explanation inline in the editor,
so nobody has to leave the screen to decode a term mid-incident.

Locked-down cluster? When RBAC forbids listing, the editor says so plainly
("Can't list pods in payments with your current RBAC") and drops straight to
typed input — an access denial is never dressed up as "nothing to list".

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
