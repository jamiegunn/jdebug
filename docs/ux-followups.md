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

## Click-to-run menu rows — SHIPPED

Every visible command now runs by clicking its label as well as by key.
`menuRowClick` reads the clicked line's menu column, extracts the action key,
and dispatches through `menuKey` — the same path as a keypress, so confirms
for `H`/`R`/`K` still fire. Tier-agnostic (parses the rendered column rather
than mapping geometry). The footer says "press a key or click a row" on wide
terminals. Remaining refinement: extend the same affordance to picker-like
lists (jcmd picks, log-level).

## Command & data transparency cards — SHIPPED

`.` (or right-click a row) opens the transparency cards (`scDetail`): every
runnable command has a card giving what runs (`$ …`), the data source/API, why
it's useful, its risk (safe / PAUSES the JVM / state-changing / sensitive),
what it needs (kubectl / metrics-server / actuator / jattach / python3), and
the alternatives when a route is blocked. Right-click anchors the clicked
command's card first. A test asserts every menu action has a card.

Remaining refinement: per-panel-signal cards (`last exit` → termination reason
+ exit-code meaning; `autoscale` → HPA conditions). Today left-clicking the
panel runs `why`, which narrates most of this in one pass.

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
