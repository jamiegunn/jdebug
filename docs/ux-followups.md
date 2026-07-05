---
title: UX follow-ups
nav_order: 12
---

# UX follow-ups

Design directions from the junior-SRE UX review that are captured here with
concrete entry points rather than implemented yet. The review's correctness
and safety items (honest safety copy, colour-free risk text, no-wrap rows, the
CPU-flow interval, the Resource/JVM panel split, richer autoscale, severity-
sorted NEXT, panel click-to-drill-in) already shipped.

## Click-to-run menu rows

**Goal:** every visible command runs by clicking its label, not only by key.

**Entry point:** extend `handleMouse` with a `menuRowHit(x, y)` that maps a
click in the menu column to the action on that row, then dispatches the same
key path (so confirms for `H`/`R`/`K` still fire). Build the row→y map while
rendering `remoteBody`/`localBody` so geometry can't drift from the layout.
Test: clicking a safe row runs it; clicking `H`/`R`/`K` opens the same
second-key confirm as the shortcut.

## Command & data transparency cards

**Goal:** before running (or when drilling into a value), show what command
runs, its data source/API, why it's useful, what it can't prove, its risk,
alternatives, and dependencies (RBAC / metrics-server / actuator / jattach /
python3).

**Entry point:** a `commandInfo` registry keyed by action, and a detail screen
(`scDetail`) opened by a dedicated key (e.g. `.`) or by clicking a per-row
indicator. The content already exists in scattered form — the `why`/`security`
verbs, the capture-tier docs, `explain_kubectl_error` — so the card is a
presentation layer over known facts. Panel signals reuse the same card:
`last exit` → termination reason + exit-code meaning + next command;
`autoscale` → HPA conditions in plain language. (Today clicking the panel runs
`why`, which already narrates most of this.)

## Actuator credentials

**Goal:** stop assuming unauthenticated localhost actuator. Secured endpoints
are common in production.

**Design (no guessed defaults, no careless secret storage):**

- Retarget/settings gains an *actuator auth* field alongside the URL: none /
  bearer token / basic. Store only a reference (env var name or a path), never
  the secret value, in the shared target config.
- At call time, read the secret from the referenced env var / file so it never
  lands on disk via jdebug.
- Explain where the credential usually comes from — a Kubernetes Secret
  mounted into the pod, an env var, or (in some local/dev setups only) the
  generated password printed to the app log at startup. **Do not assume a
  default password**; tell the user how to verify the source.
- When actuator auth is missing or wrong, the health/metrics failures already
  route through `explain_kubectl_error`-style messaging — extend it to say
  "secured actuator (401/403) — provide credentials in settings, or capture
  via jattach (needs no HTTP)".

## Operator incident workflows

Product directions to turn the launcher into an incident companion. Each
should bias the wizard, dashboard, and NEXT toward the checks that matter and
must never make destructive changes automatically.

- **Incident modes** — explicit starting points (down / slow / restarting /
  memory / CPU / deployed / not-sure) that pre-select a wizard flow and reorder
  NEXT. Entry point: a top-level pick in `openWizard`, and a `mode` field that
  weights `suggestions()`.
- **Evidence chains** — show the short cause→effect behind a recommendation
  (`OOMKilled → mem 94% of limit → w flow 1`). Entry point: `suggestions()`
  returns a chain, not just a line.
- **Runbook cards** — the transparency card, specialised per signal (what it
  means / why / check first / safe cmd / risky cmd / what to tell the next
  person). Good first cards: last exit, HPA, container memory, JVM heap, probe
  failures, CrashLoopBackOff, secured actuator.
- **Incident timeline** — order the pod's events + the operator's captures
  (created → pulled → started → probe failed → OOM → restarted → HPA scaled →
  captured threads/heap). Entry point: a verb over `kubectl get events
  --sort-by` merged with the session log's capture timestamps.
- **What changed** — image + imageID, restart/rollout time, rollout history,
  events since last restart, previous logs, probe failures, HPA vs Deployment
  replicas. Much of this is already in `topology` + `logs --previous`; the
  workflow names the question.
- **Escalation summary** — one key builds a handoff: target, symptom/workflow,
  findings + confidence, commands run (from the session log), captures + paths,
  blocked checks + why, suggested next action, and a sensitive-evidence
  warning. Entry point: a verb that reads the session log + current panel
  state.
- **Blocked-by view** — surface a failed check as an operator state (blocked by
  RBAC / metrics-server / secured actuator / missing jattach / no selector /
  no previous logs), each with the least-privilege fix or fallback route. The
  building block (`explain_kubectl_error`) exists; this aggregates it into a
  view.
- **Confidence levels** — `likely / possible / unknown` prefixes on NEXT so a
  junior knows which warnings are certain.

These stay diagnostic-first. Recovery guidance (scale up, roll back, loosen a
probe, raise a limit) should be explanation or copy-paste unless a strongly
confirmed remediation flow is designed — the pattern set by `re-roll` and
`kill pod` (hard confirm + full risk brief).
