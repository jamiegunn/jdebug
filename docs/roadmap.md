---
title: Roadmap
nav_order: 14
---

# Roadmap

Ideas that fit the tool's principles — explain everything, safe by default,
evidence you can't lose. Roughly ordered by leverage; none are promises.

## Recently shipped

- **Go (Bubble Tea) frontend** — `make tui`; same keys/screens/config as the
  classic bash menu, richer rendering, drives the same tested CLI; bash menu
  remains the zero-dependency fallback (`JDEBUG_CLASSIC=1`)
- **Readiness gate** — the menu hides its tools until the cluster answers and
  a real pod + container are selected (per mode), with a ✓/✗ checklist
- **`jdebug analyze`** — built-in first-pass triage of every capture type
- **`jdebug doctor`** — pre-incident checkup of host, cluster, target, actuator
- **Remembered target** — selections persist between sessions
  (`~/.config/jdebug/target`), with stale-pod detection
- **Single-keypress TUI** — instant keys, in-place target editor, live
  dropdowns (contexts, namespaces, selectors from real pod labels, containers
  from the pod spec, pods with restart counts), RBAC-safe typed fallback
- **Session transcript** — every command + output saved under `dumps/`
- **Robust JVM discovery** — `libjvm` map scan catches custom launchers
  (jwebserver, jlink images), not just processes named `java`
- **Local-only analysis stance** — every recommended tool (VisualVM, Eclipse
  MAT, JDK Mission Control) is a free local install; dumps never leave your
  machine
- **Self-tests from the UI** — `u` on the first screen runs the mocked suite

## Hardening

- **Checksum-pinned jattach** — verify SHA-256 per version/arch before
  caching or copying anything into a pod; refuse tampered binaries.
- **`jdebug bundle-offline`** — one command that packages jdebug-local +
  arch-matched jattach binaries + docs into a tarball for air-gapped
  clusters and jump boxes.
- **Secret redaction** — scrub bearer tokens / passwords that appear in
  command output before they reach the session log.
- **`--dry-run` everywhere** — print every kubectl/HTTP call a command
  *would* run, execute nothing. Doubles as a training mode.
- **Timeout budget** — a global `--timeout` so no capture can hang an
  incident call; partial results are reported as partial.

## Diagnosis power

- **`jdebug profile [30s|60s|5m]`** — one-command JFR workflow: start the
  recording, wait with a progress line, pull the `.jfr` out, print the JMC
  open instructions. (Stretch: async-profiler staging for flamegraphs, same
  pattern as jattach.)
- **`jdebug watch <metric>`** — sample a metric on an interval with a
  delta column and a terminal sparkline; `--until`/`--for` bounds. Turns
  "run it twice and compare" into a first-class trend view.
- **`jdebug diff threads <a> <b>`** — align two thread dumps and show which
  stacks persist RUNNABLE (the hot-loop workflow, automated).
- **Baseline & compare** — `jdebug snapshot --baseline` stores a named
  snapshot; later `jdebug compare <baseline>` diffs memory pools, thread
  counts, and GC totals with growth highlighted.
- **GC log capture** — fetch/enable GC logging where possible and summarize
  pause distribution without external tools.
- **Headless MAT integration** — when Eclipse MAT is installed, run
  `ParseHeapDump` + Leak Suspects automatically after a heap capture and
  print the top suspects inline.

## Reach

- **Secured actuators** — token/header pass-through
  (`--actuator-header 'Authorization: Bearer …'` / env), with the value
  masked in every echo, log, and transcript.
- **Multi-pod fan-out** — `--all-pods` runs a capture on every matching
  replica concurrently and files the results per pod; essential when only
  one replica of many is sick and you don't know which.
- **Event correlation** — annotate captures with the pod's recent k8s events
  (OOMKilled, probe failures) so evidence carries its own timeline.
- **JSON output** — `--json` on triage commands for scripting and bots.
- **Windows/WSL** support notes and testing.

## Ease of use

- **Profiles** — the remembered target (shipped) covers one app; profiles
  generalize it: `~/.config/jdebug/profiles/payments.conf` capturing
  namespace/selector/actuator/context; `jdebug -p payments memory`; the menu
  lists profiles on the target screen.
- **Shell completions** for bash/zsh (commands, flags, jcmd strings,
  profile names).
- **First-run tour** — on the very first launch, a 60-second interactive
  walk-through of the menu, wizard, and `d`/`h`/`c` keys.
- **`jdebug report`** — render a snapshot bundle into a single shareable
  Markdown/HTML incident report: what was captured, key numbers, hints fired.
- **Packaging** — Homebrew formula and a single-file release artifact so
  "install" is one command everywhere.
- **Session replay** — `jdebug session <log>` pretty-prints a transcript
  back with the same ✓/✗ structure, for handoffs and postmortems.

## Quality bar for any of the above

Every addition ships with: mock-driven tests for its messages and gates, a
docs page section, a `doctor` check if it adds an environment dependency,
and the same explain-why-then-fix error style as the rest of the kit.
