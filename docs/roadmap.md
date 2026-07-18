---
title: Roadmap
nav_order: 17
---

# Roadmap

Ideas that fit the tool's principles — explain everything, safe by default,
evidence you can't lose. Roughly ordered by leverage; none are promises.

## Recently shipped

- **v2 Go capture engine** (`core/`) — typed destructive-target confirmation,
  a validate-before-announce pipeline, `manifest.json` provenance (bytes +
  sha256 + verdict per artifact), a structural thread-dump analyzer (monitor
  **and** `java.util.concurrent` deadlock cycles), `fetch-heap`, and
  `JDEBUG_TIMEOUT` + clean Ctrl-C handling; vendored per-platform in
  `tools/core/` with hash proof, `JDEBUG_V1=1` forces the bash tiers
- **Checksum-pinned jattach** — vendored in `vendor/jattach/` with SHA256SUMS
  verified before every install; tampered/missing binaries refuse to ship
  into a pod (was listed under Hardening below)
- **Secured actuators** — `ACTUATOR_AUTH=bearer:ENV_VAR` /
  `basic:USER_VAR:PASS_VAR` referencing the pod's OWN env vars (the secret
  never leaves the pod), settable in the TUI target editor (`k`). Shipped in
  this reference-based form rather than the header-pass-through sketched
  under Reach below
- **Go (Bubble Tea) frontend** — `make tui`; the only interactive frontend,
  driving the same tested CLI; binaries are vendored per-platform in
  `vendor/tui/` with hash proof (SHA256SUMS), kept fresh by the git hooks
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

- **`jdebug bundle-offline`** — one command that packages jdebug-local +
  arch-matched jattach binaries + docs into a tarball for air-gapped
  clusters and jump boxes.
- **Secret redaction** — scrub bearer tokens / passwords that appear in
  command output before they reach the session log. **Until this ships,
  session logs record output verbatim — treat them like the captures.**
- **`--dry-run` everywhere** — print every kubectl/HTTP call a command
  *would* run, execute nothing. Doubles as a training mode.
- **Timeout budget, finished** — `JDEBUG_TIMEOUT` shipped for the v2 engine
  (env var, opt-in). Still to do: a `--timeout` flag, coverage of the v1
  bash tiers, and reporting partial results as partial.
- **darwin-amd64 vendored binaries** — Intel Macs currently need a local
  `make core` / `make tui` (only darwin-arm64 + linux are vendored).
- **TUI process handling** — kill the whole process group on cancel so a
  stopped stream can't leave `kubectl` running (see ux-followups' known gaps).

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

- **Secured actuators, header form** — the env-var-reference form shipped
  (see Recently shipped); a raw header pass-through
  (`--actuator-header 'Authorization: Bearer …'`) with masking everywhere
  remains unbuilt.
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
