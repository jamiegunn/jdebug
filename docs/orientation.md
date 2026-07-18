---
title: Orientation (for a new contributor or LLM session)
nav_order: 15
---

# jdebug — orientation

A self-contained brief on what jdebug is, why it exists, how it's built, and
how to work on it. Read this first when starting fresh.

## What it is

**jdebug is an incident companion for debugging JVM applications running in
Kubernetes** (and, in a reduced mode, JVMs on a plain host). It captures and
explains evidence — pod status, logs, health, memory anatomy, thread dumps,
heap dumps, JVM command output, and the kubernetes-layer context around the
pod — and turns live state into concrete next actions.

The audience it is designed for is a **junior SRE or on-call engineer** who
knows the *symptom* ("the app is OOMKilling / crash-looping / slow") but not
necessarily the *tools* (`jcmd`, `jattach`, heap vs RSS, HPA, actuator). Every
surface is built to reduce panic and teach cause→effect while staying
operationally useful. It is **diagnostic-first**: it never changes cluster
state without an explicit, spelled-out confirmation.

## The core intentions (the "why" behind every decision)

These are the load-bearing principles. When in doubt, they win.

1. **Symptom-first, not tool-first.** The hero action is the guided-diagnosis
   wizard (`w`): "what are you seeing?" → it runs the right captures. A junior
   should be able to act without knowing which JVM tool to reach for.
2. **Safe by default; disruptive actions are loud.** Almost everything is
   read-only. The few state-changing actions — heap dump / `snapshot --heap`
   (pause the JVM), `restart` (re-roll the deployment), `kill` (delete a pod),
   `log-level` (change log volume) — each require a confirm and print a full
   risk brief first. This must never regress.
3. **No silent targeting.** The user always knows which cluster / namespace /
   pod / container a command will touch. A readiness gate hides the capture
   tools until the target is valid.
4. **No silent degradation — explain every failure.** A missing/forbidden
   kubectl call, absent metrics-server, secured actuator, or missing jattach
   is *explained in plain language with the next action*, never flattened into
   a blank or a stack trace. A denied read is `UNKNOWN`, never treated as
   "fine" or "nothing there". This is the single most repeated review demand —
   and not yet fully met: some TUI dashboard panel reads still render denied
   values as blank/zero (see "Known gaps" below).
5. **Plain language first, jargon paired.** Every command answers "what
   question does this answer?" Terms like selector/actuator/jcmd get inline
   glosses; the glossary is `?`, the per-command transparency cards are `.`.
6. **Evidence is never lost.** Every capture lands under `dumps/` and every
   command's output is tee'd to a session log. Captures are organized
   `dumps/pods/<pod>/<timestamp>/<file>` and browsable in the UI.
7. **Two layers, both covered.** jdebug spans the **JVM layer** (memory,
   threads, heap, jcmd) *and* the **kubernetes/pod layer** (limits, probes,
   exit codes, cgroup memory, HPA, ReplicaSets, security posture, lifecycle).

## Architecture (how it's built)

**The dispatcher routes; the engines do the work.** `jdebug` (bash) routes
verbs. Capture verbs run through the **v2 Go core** (`core/`) when a built or
vendored `jdebug-core` is present, with the v1 bash tiers (`capture/*.sh`) as
the `JDEBUG_V1` fallback; the observe/lifecycle verbs are still bash. See
[architecture](architecture) for the migration ledger — it is mid-migration, and
that document is the honest status record.

```
jdebug                      the verb router (bash). `jdebug <verb> [args]`.
jdebug-local                a single-file in-pod CLI (mode 2/3, no kubectl)
lib/common.sh               shared helpers: target config, kubectl error
                            explainer, pod_fetch (curl/wget-in-pod), session dir
core/                       the v2 capture engine (Go, stdlib-only): typed
                            destructive-target gate, validate-before-announce
                            pipeline, manifest provenance, thread-dump parser
tools/core/                 vendored, hash-verified jdebug-core binaries
capture/*.sh                the v1 capture tiers: actuator (HTTP), jattach
                            (attach protocol), jdk (debug container)
observe/*.sh                analysis + pod-layer verbs + lifecycle:
                            analyze, memory-report, tail-logs, set-log-level,
                            snapshot, why, security, topology, lifecycle
tui/                        the Go Bubble Tea frontend (the only menu/wizard)
vendor/tui/                 the vendored, hash-verified Go TUI binaries
vendor/jattach/             the vendored, hash-verified jattach binaries
docs/                       the published docs site (GitHub Pages / Jekyll)
tests/                      the mock suite + live-JVM + kind suites + pty driver
```

**One interactive frontend.** The Go TUI (`tui/`, Bubble Tea + lipgloss +
x/ansi only) is the only menu/wizard — the old bash menu was removed
([architecture](architecture), Phase 0b). `jdebug` runs a local dev build when present,
else the vendored binary for your platform — after verifying it against
`vendor/tui/SHA256SUMS`. No TUI available → every command still works from
the CLI, and error messages give the CLI route alongside any menu key they
mention.

### The CLI verbs

Observe (read-only): `status` `why` `security` `topology` `health` `top`
`memory` `metrics` `logs` (`--previous` for a crashed container) `analyze`
`dumps` `doctor`.
Capture (evidence → `dumps/`): `threads` `heap --confirm` `jcmd` `snapshot`
(`--heap`).
State-changing (guarded, need `--confirm`): `restart` (re-roll deployment),
`kill` (delete pod), `log-level`.
Setup: `install-jattach`, `push-local`.

The three **capture tiers** (auto-selected, safest-first) are actuator (HTTP
to the app's `/actuator`), jattach (a small vendored helper that speaks the
JVM attach protocol — needs no HTTP, ships in this repo), and jdk (a temporary
debug container). This is why "no actuator" is never fatal.

### The Go TUI, at a glance

A full-screen dashboard that scales by terminal size (`layout.go` tiers):
compact incident-checklist → menu+sidebar → a full grid (menu | live TARGET
panel + sparkline trends + NEXT suggestions | PODS/WORKLOAD | a tabbed
WORK/LOGS/EVENTS/CAPTURES/TRENDS bottom pane). Key pieces:

- `menu.go` — sections (START HERE / QUICK CHECKS / CAPTURE EVIDENCE /
  ADVANCED), risk rows (word + colour), the readiness gate, **click-to-run**
  (`menuRowClick` dispatches a row click through the same key path as a
  keypress, confirms preserved).
- `panel.go` — the live TARGET panel (resource-vs-JVM grouped, autoscale
  cur/max/failing, heap route) and the **severity-ordered NEXT** engine.
- `wizard.go` — the guided-diagnosis flows; they stream into the dashboard's
  output pane (you never leave the main page).
- `output.go` — the streaming command pane: commands stream in place (no raw
  bash takeover); `esc` stops/dismisses, `C` copies, wheel scrolls.
- `captures.go` — the navigable dumps browser (drill folders, view a file in
  the pane, contextual `a` analyzes it).
- `hprof.go` — a from-scratch HPROF reader → heap class histogram
  (`jdebug-tui -analyze-heap`), no external deps.
- `detail.go` — the transparency cards (`.` / right-click): per command, what
  runs · source · why · risk · needs · alternatives.
- `pods.go` — the clickable PODS pane (click to retarget) + panel drill-down.
- `editor.go` — the target editor, including the actuator **auth** field (`k`)
  that stores a *reference* to the pod's credential env vars, never a secret.

## How to work on it

**Run everything:** `tests/run-tests.sh` from the repo root. It runs the Go
unit tests for BOTH modules (core — including the adversarial-review
regression suite — and the TUI), bash CLI cases (driven by
`tests/mocks/kubectl`, a case-statement fake), a real-pty drive of the built
TUI, and a `gofmt` check. Shellcheck runs as a separate, advisory CI job
(`.github/workflows/tests.yml`), not in the suite. As of this writing the
suite is ~393 assertions and must be green. Note its limits honestly: it
proves messages, gates, and capture plumbing against a **mock** kubectl; the
live-JVM (`tests/live/`) and kind (`tests/integration/`) suites prove real
transport/JVM behavior but run manually, not in CI (see
[architecture](architecture), Phase 5).

**Gotchas learned the hard way:**
- `tests/mocks/kubectl` matches **first-case-wins** — anchor patterns
  carefully. `-o json` vs `-o jsonpath` swallowing has bitten more than once;
  the pod-json branch is end-anchored for this reason.
- The Go TUI has a fixed-height frame on the dashboard tier — every screen must
  render exactly `height` rows; overlay screens budget the lines they append.
  `TestDashboardFrameExact` guards this at 200×50.
- Render screens for visual review: `cd tui && go run . -render <menu|compact|
  dashboard|wizard|help|gate|detail|output|focus>`.
- CI has a separate `docs` (GitHub Pages) job that occasionally fails at the
  *deploy* step ("Deployment failed, try again later") even when the Jekyll
  build succeeds — that's a Pages-infrastructure flake, not a content problem;
  re-run it.

**Where the design record lives:** `docs/ux-followups.md` is the per-item UX
status record (most items there are marked SHIPPED, with the remaining
refinements listed per item); `docs/roadmap.md` holds the larger unshipped
ideas; [architecture](architecture) is the v2-migration status ledger. When these
disagree with each other or with the code, the code wins — fix the doc.

## What's done vs. what's next

**Done and test-pinned:** the JVM + pod-layer verbs; organized + browsable
captures with in-app viewing and a Go heap histogram; guarded lifecycle
actions (ambiguous-match refusal on every destructive verb); workload
topology; the symptom-first menu with click-to-run and per-command
transparency cards; the severity-sorted NEXT engine and panel drill-down;
honest safety copy and colour-free risk; secured-actuator credential
references; the operator-workflow layer (incident modes, evidence chains,
runbook cards, timeline, what-changed, escalation summary, blocked-by view,
confidence levels — see `docs/ux-followups.md`); and plain-language failure
explanation across the CLI's kubectl calls.

**Known gaps and open work** (tracked in `docs/roadmap.md` and
`docs/ux-followups.md`): secret redaction in session logs; `--dry-run`;
multi-pod fan-out; the TUI's process handling (a cancelled stream can leave
its kubectl running) and some dashboard reads that render unknowns as
blank/zero under RBAC denial; the kind/real-cluster suite is not yet wired
into CI. Anything executable stays behind the `restart`/`kill` pattern (hard
confirm + full risk brief).

## The one-paragraph version

jdebug turns a Kubernetes JVM incident into a guided, safe, evidence-producing
workflow for someone who knows the symptom but not the tools. It's a bash CLI
dispatcher over a Go capture engine (with v1 bash tiers as fallback) plus one
interactive Go TUI. Its non-negotiables: start from symptoms, gate the target,
make disruptive actions loud, explain every failure in plain language, and
never lose evidence. Work on it by running `tests/run-tests.sh`, keeping the
docs honest about what is and isn't proven, and preserving those principles.
