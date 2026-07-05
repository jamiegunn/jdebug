# LLM Handoff: Junior-SRE UX Review for jdebug

## Purpose

Use this brief to continue improving `jdebug`'s UI/UX for a junior SRE or on-call engineer with limited Kubernetes and JVM diagnostic knowledge.

The goal is not to make the tool less powerful. The goal is to make the first 10 minutes of an incident safer, clearer, and more action-oriented for someone who knows symptoms before they know tools.

## Product Context

`jdebug` is a JVM diagnostics kit for Kubernetes and local JVMs. It captures and analyzes evidence such as pod status, logs, health, memory reports, thread dumps, heap dumps, JVM command output, and snapshot bundles.

The project has two interactive frontends:

- Go Bubble Tea TUI: `tui/`
- Bash fallback menu: `ui/tui.sh`

Both frontends shell out to the tested CLI scripts. The UI should guide users toward safe checks first, explain the target, and clearly warn before any disruptive action.

## Primary Audience

Design for a junior SRE who may know:

- The app is slow, restarting, OOMKilled, crash-looping, or unhealthy.
- Basic shell usage.
- Basic `kubectl` familiarity, but not deep Kubernetes internals.

Do not assume they already understand:

- `selector`, `namespace`, `container`, `HPA`, `RSS`, `actuator`, `jattach`, `jcmd`, heap vs non-heap memory, or thread dump interpretation.

## UX Principles To Preserve

Preserve these existing strengths:

- Symptom-first entry: the wizard should remain the safest starting point.
- Readiness gates: do not show capture actions until the target is valid.
- Plain-language labels: every command should answer “what question does this answer?”
- Risk visibility: safe, caution, and disruptive actions must be clear.
- Evidence safety: every output and capture should be easy to find later.
- No silent targeting: users must always know which cluster, pod, and container a command will touch.
- No silent degradation: missing RBAC, metrics-server, actuator, or jattach should be explained plainly.

## Important Files

Start here:

- `docs/tui.md` - full interactive menu documentation.
- `docs/getting-started.md` - first-session onboarding.
- `docs/playbooks.md` - diagnosis recipes mirrored by the wizard.
- `docs/troubleshooting.md` - plain-language failure explanations.
- `README.md` - top-level user promise.
- `tui/menu.go` - main menu copy, risk rows, footer, gate view.
- `tui/wizard.go` - symptom-based guided diagnosis flows.
- `tui/editor.go` - target editor and field explanations.
- `tui/help.go` - glossary, workflow, safety rules.
- `tui/panel.go` - live target panel and NEXT suggestions.
- `tui/captures.go` - captures browser and artifact viewing.
- `tui/spark.go` - trends sampling and sparkline rendering.
- `tui/render_demo.go` - canned render states for visual review.
- `tui/main_test.go` - interaction and layout tests.
- `observe/topology.sh` - workload topology command behind `W workload`.
- `ui/tui.sh` - bash fallback menu; keep behavior and copy aligned where practical.
- `docs/ux-followups.md` - current follow-up backlog for larger UX/product directions.

## Current UX Assessment

Overall, the UX is strong for junior SREs. The tool already does the most important thing well: it starts from symptoms and turns live context into concrete next actions.

Notable strengths now present in the code:

- The main menu has a clear `START HERE` guided diagnosis entry.
- The wizard asks “what are you seeing?” instead of asking which JVM tool the user wants.
- Target setup is gated and checklist-driven.
- The target editor explains Kubernetes fields inline.
- Heap dumps, re-rolls, and pod kills are marked as state-changing/disruptive and require confirmation.
- The live panel turns target state into suggested key presses.
- Help includes a glossary, first workflow, hidden keys, and safety rules.
- Documentation repeatedly states that evidence stays local and captures are saved.
- Recent additions move in the right operator-workflow direction: `workload` exposes Deployment/ReplicaSet/HPA/service context, and `re-roll` / `kill pod` expose recovery-oriented actions through hard confirmation gates.

Current post-scan notes from the latest code:

- Safety and correctness fixes have shipped: `?` is documented as help/glossary, risk rows include words not just color, long menu rows truncate instead of wrapping, and help now lists `H`, `x --heap`, `R`, `K`, and `v` accurately.
- The high-CPU wizard no longer fires two thread dumps back-to-back; the second dump is gated by a wait/confirm prompt.
- The live panel separates `resource · container, from kubectl top` from `jvm · inside the process`, shows richer autoscale state such as current/max/min and failure reasons, and points missing heap data toward jattach or route setup.
- NEXT suggestions are severity-ordered, and clicking the TARGET panel runs `why` as a pragmatic drill-down for last-exit, limits, probes, and autoscale context.
- Larger interaction/product ideas are captured in `docs/ux-followups.md`: click-to-run rows, full command/data transparency cards, actuator credentials, incident workflows, evidence chains, runbook cards, timeline, escalation summary, blocked-by view, and confidence levels.

## Remaining UX Issues To Address

### 1. Commands should be runnable by key or visible label

The keyboard shortcut model is good, but users should not have to remember that `s` means status or `h` means health. They should be able to select either the shortcut or the visible command label and run the same action.

Design goal: every command row should support both:

- Pressing the shortcut key, such as `s`.
- Selecting or clicking the command label, such as `Status`, then running it.

This should apply consistently across quick checks, captures, advanced actions, local-mode actions, and any picker-like command list. `docs/ux-followups.md` already proposes a row-to-y map that dispatches through the same key path so confirmation behavior cannot drift.

### 2. Full command and data transparency cards are still a follow-up

Some transparency has shipped: panel group headers name data provenance, heap rows show the active route or next action, command output prints `$ ...`, and panel click runs `why`. The full transparency-card layer is not implemented yet.

Users should be able to inspect a command or live-panel value before acting. A detail/interstitial should answer:

- What command will run, or what command/API produced this value.
- Why the command or value is useful.
- What it can and cannot prove.
- Whether it is safe, state-changing, app-pausing, or likely to expose sensitive data.
- What alternatives exist when the route is blocked, for example actuator vs jattach vs jdk.
- What permissions or dependencies it needs, such as RBAC, metrics-server, actuator, jattach, or python3.

Examples to cover first: `status`, `memory`, `threads`, `heap`, `re-roll`, `last exit`, `mem`, `cpu`, `autoscale`, and `jvm heap`.

This transparency layer should be discoverable from both keyboard and mouse. Avoid hover-only behavior because terminal hover support is inconsistent.

### 3. Actuator credentials need a guided setup path

The current UX still appears to assume unauthenticated localhost actuator access. Many real Spring Boot apps secure actuator endpoints, so retarget/settings should guide users through authenticated access.

Add a design and implementation plan for actuator credentials:

- Retarget/settings should include actuator auth state, not just actuator URL.
- Support a safe way to provide credentials or tokens without storing secrets carelessly.
- Store only a reference, such as an env var name or file path, not the secret value.
- Explain where credentials usually come from in common Spring Boot deployments.
- If credentials are unavailable, clearly offer non-actuator routes such as jattach.
- Avoid guessing or exposing secrets. If a password is generated, injected from a Kubernetes Secret, or printed during startup, tell the user how to verify the source rather than assuming a default.

Security note: do not ask another LLM to invent default actuator passwords. Spring Security defaults vary by configuration and version; in many apps the generated password appears in logs only in specific local/dev setups, while production credentials are commonly externalized through environment variables, config, or Kubernetes Secrets.

### 4. Autoscale detail should connect panel state to workload topology

The live panel now shows current/max/min replicas and whether HPA is failing or at max. The remaining gap is connecting that summary to the broader workload story:

- Whether Deployment `replicas:` is fighting HPA-managed scale.
- Which HPA conditions explain the state.
- Which workload object owns the target pod.
- Whether old ReplicaSets are still serving pods during a rollout.

`W workload` and `why` already explain much of this; the UX opportunity is making the autoscale panel/detail card point users there explicitly.

### 5. Dense dashboard may still overwhelm first-time users

The wide dashboard is powerful, but it shows many panes at once: menu, target live, trends, next, pods, events, captures, and logs. The compact layout already uses a clearer incident-checklist order, but first-time wide-dashboard users may still need stronger hierarchy.

Do not remove this power-user layout, but consider whether first-time users should get a clearer “incident checklist” hierarchy:

1. What is happening?
2. What should I press next?
3. What evidence was captured?
4. What details are available if needed?

### 6. Workload topology needs app wiring and clearer organization

`W workload` is valuable, but the output should be reorganized so the pieces are not jumbled together. A junior SRE should be able to scan it as a structured inventory of how the app is owned, exposed, configured, and connected.

Recommended sections:

- **Owner and rollout:** Deployment/ReplicaSet revision, ready/updated/available replicas, strategy, image tag/digest, command/args, rollout status.
- **Autoscale:** HPA current/min/max, whether it is active, metric failures, and whether Deployment `replicas:` fights HPA.
- **Services and ports:** Services selecting the pod, Service type, ClusterIP/external IP/hostname, ports, targetPorts, named ports, endpoint readiness, and whether the selected pod is included in endpoints.
- **Probes:** readiness/liveness/startup probe type, path/port, delays, timeouts, failure thresholds, and recent probe failures.
- **Environment:** container env vars, envFrom ConfigMaps/Secrets by reference, JVM env such as `JAVA_TOOL_OPTIONS`, `JAVA_OPTS`, `JDK_JAVA_OPTIONS`, `SPRING_PROFILES_ACTIVE`, timezone, and proxy vars.
- **JVM runtime:** Java version, JVM flags from `jcmd VM.flags`, heap/GC flags, system properties where safe, and whether NMT/JFR are available.
- **Volumes and storage:** volumes, mounts, read-only flags, PVC names/status, emptyDir/tmpfs memory-backed volumes, ConfigMap/Secret mounts, and which mounts may affect memory or credentials.
- **Secrets and config references:** names and keys only, never secret values. Show where sensitive config comes from without printing it.
- **Network policy and ingress/gateway:** NetworkPolicy exposure, Ingress/Gateway/mesh routes that point at the Service where discoverable.

Every section should include the `kubectl` or JVM command/API used to gather it, or link to a transparency card. Redact sensitive values by default.

### 7. Runtime context should detect common dependencies such as Valkey

Add a read-only runtime context/app wiring workflow that answers: “What is this app connected to, and what configuration assumptions might be wrong?”

For Valkey/Redis-compatible dependencies, useful safe checks and config clues include:

- Detected host/port/db/SSL settings from env/config, with secrets redacted.
- `cluster-enabled`.
- `cluster-announce-hostname`, `cluster-announce-ip`, `cluster-announce-port`, `cluster-announce-tls-port`, and `cluster-announce-bus-port`.
- `bind`, `protected-mode`, `port`, and `tls-port`.
- ACL/`requirepass` presence without printing credentials.
- `masterauth`, redacted.
- `replica-announce-ip` and `replica-announce-port`.
- `appendonly`, `maxmemory`, `maxmemory-policy`, `timeout`, `tcp-keepalive`, and `client-output-buffer-limit`.
- `cluster-node-timeout`, `cluster-require-full-coverage`, and `cluster-migration-barrier`.

The operator value is high because wrong cluster announce settings often produce “works inside the pod but clients fail” incidents. Keep this dependency-aware layer extensible so future checks can cover databases, Kafka, HTTP dependencies, service mesh, and cloud metadata.

### 8. Captures browser needs a navigation redesign

The captures pane is powerful but hard to navigate. It also may not feel reliable when changing pods because it keeps browse state (`capsCwd`) unless explicitly reset.

Redesign goals:

- Make the current scope obvious: selected pod, all pods, current session, or a drilled-in timestamp folder.
- Reset or clearly prompt when changing pods if the browser is still pinned to the previous pod/session.
- Add an explicit refresh action and visible “last refreshed” time.
- Provide filter tabs or segments: current pod, all pods, snapshots, threads, heaps, logs, recent.
- Show capture type, source route, pod, timestamp, size, and recommended next action.
- Make `a analyzes current view` explicit, including what “current view” means.
- Keep click-to-open, but also support keyboard selection for accessibility.
- Preserve evidence safety warnings, especially for heap dumps and logs.

### 9. Invalid heap captures need a clearer recovery path

When `jdebug analyze <heap.hprof>` finds bad HPROF magic, the output currently identifies the file as invalid and suggests retrying with `--via jattach`. That is directionally right, but the UX should make the likely cause and next action more concrete, especially because old invalid `.hprof` files may remain in `dumps/` for inspection.

Improve invalid heap handling across capture, analyze, and captures browser:

- At capture time, continue validating HPROF magic before calling a file usable.
- If an actuator heap download returns an error page, JSON error, HTML login page, 401/403, 404, or truncated content, classify that probable cause where possible.
- In `analyze`, avoid generic “heap questions in Eclipse MAT” next steps for invalid heaps; MAT cannot use a bad HPROF.
- Show a short preview or classification of the first bytes/content type when safe, for example `looks like HTML error page`, `looks like JSON actuator error`, or `empty/truncated file`.
- Suggest exact recovery commands:
  - secured/disabled actuator: configure actuator credentials or use `jdebug heap --via jattach --confirm`.
  - app too wedged to serve HTTP: use `--via jattach` or `--via jdk`.
  - route/base path wrong: check actuator URL in target settings.
- Mark invalid heap captures visibly in the captures browser so users do not try to open them in MAT.
- Preserve the invalid file for inspection, but keep it out of “valid evidence” summaries unless explicitly included.

### 10. Trends need labels that explain sampling and meaning

The current trends section is not self-explanatory. It should answer:

- Sampling cadence: one sample per panel refresh, currently about every 20 seconds.
- Whether values are instantaneous samples or averages. Current memory/CPU values are point-in-time samples from Kubernetes metrics, not time-window averages computed by jdebug.
- What each label means: `mem` is container memory percentage of limit, `cpu` is Kubernetes CPU usage scaled against limit when available, and `rst` means restart count markers.
- What `▲` means: restart count increased at that sample.
- How much history is shown: up to `histCap` samples, roughly 30 minutes at 20 seconds per sample.
- What gaps mean: missing metrics or unknown values.

Consider renaming `rst` to `restarts` where width allows, and add a trends detail card or legend.

### 11. Idle background activity should be visible and controllable

The TUI is not zero-touch while open. The fastest idle loop is the live log refresh, about every 5 seconds. Other panel/dashboard refreshes happen around every 20 seconds and can issue several read-only calls in a burst.

Current background activity to make transparent:

- `kubectl logs --tail=...` about every 5 seconds in the live log pane.
- `kubectl get pod`, `kubectl top pod`, `kubectl get hpa`, events, pods, and captures refreshes around the panel/dashboard tick.
- Actuator heap metric reads every panel refresh when actuator is available.
- `jcmd GC.heap_info` fallback if actuator heap metrics fail and a JVM route exists.

Add visible controls and status:

- `idle refresh: logs 5s · target 20s · actuator heap 20s`.
- `background probes: kubectl logs, pod/top/hpa, actuator metrics`.
- Pause/resume background refresh.
- Manual refresh once.
- Slow down / speed up refresh intervals.
- Quiet mode: disable logs and JVM probes, keep manual refresh.

Classify background work by cost/risk:

- Low cost: Kubernetes reads such as pod, HPA, services, events.
- Medium cost: `kubectl top` and logs polling.
- App/JVM touching: actuator metrics.
- Heavier JVM touching: repeated `jcmd` fallback.

For junior-SRE transparency, the UI should answer: “while I’m just looking at this screen, what is it doing in the background?”

### 12. Completed items should stay covered by regression tests

Do not re-open these as current work unless a regression appears:

- `?` help/glossary documentation is fixed.
- Risk labels no longer rely only on color.
- High-CPU wizard has a real user-controlled interval between thread dumps.
- Resource/JVM live-panel grouping has shipped.
- Autoscale current/max/min/failing state has shipped.
- JVM heap route/fallback text has shipped.
- `R`/`K` safety copy and row wrapping have shipped.

## Operator Workflows To Consider

Use these as product directions for turning the TUI from a command launcher into an incident companion. They should stay grounded in evidence the tool can actually collect, and they should avoid making destructive changes automatically.

### Incident modes

Offer explicit workflows for common operator starting points:

- App is down.
- App is slow.
- App is restarting.
- Memory problem.
- CPU problem.
- Deployment just happened.
- Not sure.

Each mode should bias the dashboard, wizard, and NEXT suggestions toward the checks that matter most for that scenario. For example, `Deployment just happened` should prioritize rollout status, image/version, recent events, previous logs, config/env changes where feasible, probes, and restart reasons.

### Confidence levels for recommendations

NEXT suggestions should communicate confidence, not just urgency. Examples:

- `likely: memory limit hit`
- `possible: liveness probe too aggressive`
- `unknown: metrics-server missing`

This helps junior SREs understand that not every warning carries the same certainty.

### Evidence chains

When the UI recommends an action, show the short evidence chain behind it.

Example:

```text
OOMKilled last restart -> memory 94% of limit -> press w flow 1
```

This teaches cause and effect while keeping the next step operational.

### Runbook cards

Clickable or selectable dashboard details should open small runbook cards. Each card should answer:

- What this means.
- Why it usually happens.
- What to check first.
- Safe command to run.
- Risky command, if any.
- What to tell the next person.

Good first cards: last exit, autoscale/HPA, resource memory, JVM heap, probe failures, CrashLoopBackOff, and secured actuator.

### Incident timeline

Add a timeline view that orders what happened and what the operator did. Useful entries include:

- Pod created.
- Image pulled.
- Container started.
- Probe failed.
- OOMKilled or exited.
- Restarted.
- HPA scaled or failed to scale.
- User captured threads, heap, snapshot, or logs.

Chronology is often the missing context for junior operators.

### What changed workflow

Add a workflow for recent-change investigations. It should check or summarize:

- Current image and image ID.
- Restart time and rollout timing.
- Rollout history if available.
- New events since the last restart.
- Previous logs after startup.
- Probe failures.
- HPA and Deployment desired replicas.
- Config/env differences where feasible and safe.

Even when the tool cannot diff everything, naming `What changed?` as a workflow helps the user ask the right question.

### Escalation summary

Add a one-key handoff summary for asking a senior SRE or developer for help. It should include:

- Target cluster, namespace, pod, container, and mode.
- Symptom selected or workflow used.
- Key findings and confidence levels.
- Commands run.
- Captures created and where they were saved.
- Blocked checks and why they were blocked.
- Suggested next action.
- Sensitive evidence warning when heap dumps, logs, tokens, or environment data may be involved.

This is especially valuable for junior SREs because knowing what context to include is part of the hard part.

### Blocked-by view

When a check cannot run, show it as an operator state rather than a generic failure. Examples:

- Blocked by RBAC.
- Blocked by missing metrics-server.
- Blocked by secured actuator.
- Blocked by missing jattach.
- Blocked by no selector.
- Blocked by no previous logs.

For each blocked state, show the least-privilege permission, setup step, or fallback route that would unblock the workflow.

### Severity sorting

When several signals are bad at once, sort NEXT suggestions by operational severity:

1. App unavailable or CrashLoopBackOff.
2. OOMKilled or restart storm.
3. HPA maxed, failed, or blind.
4. Probe failures.
5. High resource pressure.
6. Missing observability or blocked checks.

This prevents the dashboard from feeling noisy when an incident has multiple symptoms.

### Recovery-oriented guidance

The tool should stay diagnostic-first, but it can suggest recovery options without executing risky changes automatically. Examples:

- Scale up deployment.
- Roll back rollout.
- Loosen liveness probe.
- Increase memory limit.
- Disable DEBUG or TRACE logging.
- Restart one pod.

These should be explanation-only or copy-paste commands unless an explicit, strongly confirmed remediation flow is designed later.

The current `re-roll` and `kill pod` actions are examples of recovery-oriented guidance becoming executable. Preserve their hard confirmation gates and explanatory output; add pre-execution transparency so users understand the exact command, scope, and alternatives before confirming.

## Suggested Implementation Tasks

Prioritize these remaining items in small commits or patches:

1. Add click/select-to-run menu rows so every visible command can run by shortcut key or selected label.
2. Build the command/data transparency-card layer described in `docs/ux-followups.md`.
3. Add actuator credential setup in retarget/settings without unsafe secret storage or guessed defaults.
4. Make autoscale drill-down connect panel state to workload topology, HPA conditions, and Deployment/HPA replica conflicts.
5. Reorganize `W workload` into clear sections and add service/ports, image, probes, env/config references, volumes/PVCs, secrets references, and the commands used to gather each section.
6. Add a runtime context/app wiring workflow with dependency-aware checks, starting with Valkey/Redis-compatible configuration clues.
7. Redesign the captures browser around clear scope, refresh state, filters, keyboard selection, and pod-change behavior.
8. Improve invalid heap capture handling in capture, analyze, and captures browser.
9. Explain trends sampling cadence, meaning, history window, gaps, and restart markers in the UI.
10. Add idle/background activity transparency and controls: refresh status, pause/resume, manual refresh, interval controls, and quiet mode.
11. Productize operator workflows from `docs/ux-followups.md`: incident modes, evidence chains, runbook cards, timeline, What changed, escalation summary, blocked-by view, and confidence levels.
12. Reassess first-time wide-dashboard hierarchy after the above interactions exist.
13. Run render checks and tests after changes.

Already shipped and regression-covered; do not duplicate this work unless tests reveal a regression:

- `?` help/glossary docs fix.
- Color-independent risk labels.
- Row truncation/no-wrap behavior.
- Honest safety copy for `H`, `x --heap`, `R`, `K`, and `v`.
- High-CPU wizard wait/confirm before dump #2.
- Resource/JVM live-panel grouping.
- HPA current/max/min/failing display.
- JVM heap route/next-action text.
- TARGET panel click-to-`why` drill-down.
- Severity-sorted NEXT suggestions.

## Validation Commands

From the repo root:

```sh
tests/run-tests.sh
```

For focused TUI validation:

```sh
cd tui
go test ./...
go run . -render menu
go run . -render compact
go run . -render dashboard
go run . -render wizard
go run . -render help
go run . -render gate
```

Useful width check for rendered screens:

```sh
cd tui
for screen in chooser wizard help gate compact menu dashboard; do
  printf '%s ' "$screen"
  go run . -render "$screen" \
    | perl -pe 's/\e\[[0-9;]*m//g' \
    | awk '{ if (length($0)>max) max=length($0) } END { print max }'
done
```

## Test Adjustments To Add

Add or update tests around the remaining UX contracts, not just rendering. The strongest tests should assert that junior-operator affordances keep working as the UI changes.

Suggested TUI test coverage:

- Command rows can run by shortcut key and by selected visible label.
- Row selection works for quick checks, captures, advanced actions, and local-mode actions.
- Disruptive selected-label actions still require the same confirmation behavior as shortcut actions.
- Command and data transparency indicators/cards expose command provenance, data source, why it matters, risks, alternatives, and dependencies before execution or drill-down.
- Autoscale transparency covers HPA conditions, maxed HPA, missing metrics/rules, and Deployment/HPA replica conflict states.
- Workload topology tests cover section order, Services/ports, image, probes, env/config references, volumes/PVCs, Secret references with values redacted, and displayed source commands.
- Runtime context tests detect Valkey/Redis-style configuration safely and redact credentials.
- Captures browser tests cover selected-pod scope, pod-change reset/prompt behavior, refresh state, filters, keyboard navigation, and `a analyzes current view` semantics.
- Invalid heap tests cover actuator error-page/JSON/login/truncated files, analyze recovery wording, captures-browser invalid markers, and exclusion from valid-evidence summaries.
- Trends tests cover the legend/copy for 20-second samples, point-in-time values, `rst`/restart markers, history window, and missing metrics gaps.
- Idle activity tests cover visible refresh cadence, pause/resume, manual refresh, quiet mode, and no repeated `jcmd` fallback when quieted.
- Actuator auth settings render without exposing secrets, and secured-actuator failures point to credential setup or jattach fallback.
- Blocked-by states render actionable explanations for RBAC, metrics-server, secured actuator, missing jattach, no selector, and no previous logs.
- Escalation summary includes target, symptom/workflow, findings, commands run, captures, blocked checks, and sensitive-evidence warnings.
- Incident-mode selection changes NEXT ordering and wizard defaults without hiding safety gates.

Regression tests to keep or strengthen:

- Help/glossary key consistency: `?` opens help, `h` runs health, and docs/rendered help do not imply otherwise.
- Risk text remains visible when `NO_COLOR=1` or styling is stripped.
- High-CPU wizard does not run two thread captures back-to-back without a wait, prompt, or confirmation step.
- Resource metrics and JVM metrics render under distinct labels or sections.
- JVM heap render covers actuator success, actuator failure with jattach/jcmd fallback, and no-route failure with a clear next action.
- NEXT suggestions are severity sorted when multiple signals are present.
- State-changing actions such as `re-roll` and `kill pod` are documented in help/safety copy and retain hard confirmation gates.

Suggested docs/test-suite checks:

- Rendered menu/help/docs agree on key names and command names.
- Canned render states exist for new detail views, blocked-by views, autoscale states, and escalation summary.
- Width/frame tests include the new selectable rows and runbook/detail overlays.
- Bash fallback tests either assert feature parity or explicitly document where the Go TUI has richer interaction than bash.

## Acceptance Criteria

A good new follow-up change should satisfy the relevant checks below:

- Every visible command can be run by shortcut key or by selecting its command label.
- Live-panel summary fields that imply deeper context have a discoverable detail path; TARGET panel click-to-`why` exists now, and transparency cards should add finer-grained detail.
- Autoscale drill-down connects current/max/failing panel state to HPA conditions, workload topology, and Deployment/HPA conflicts.
- Workload topology is organized into readable sections and includes Services/ports, image, probes, env/config references, volumes/PVCs, Secret references, and source commands with sensitive values redacted.
- Runtime context/app wiring identifies relevant dependency configuration, including Valkey/Redis-compatible config where detectable, without printing secrets.
- Captures navigation makes current scope, refresh state, selected pod, filters, and analysis target obvious.
- Invalid heap captures are detected early, classified when possible, marked in the captures browser, and paired with exact retry/configuration guidance instead of MAT-oriented next steps.
- Trends explain sample cadence, point-in-time semantics, labels, restart markers, history window, and missing-data gaps.
- Idle background activity is visible and controllable, including pause/resume, manual refresh, interval controls, and quiet mode.
- Actuator authentication has an explicit guided setup path and does not rely on guessed default credentials.
- Operator workflow ideas are either implemented or captured as explicit follow-up tasks with clear UX entry points.
- NEXT suggestions, where touched, explain their evidence chain and confidence.
- The UI can produce a concise escalation summary from the current session state and captured evidence.
- Blocked checks are shown as actionable blocked states, not generic failures.
- Command/data transparency indicators are visible and open detail views that show source command/API, purpose, risks, alternatives, and dependencies.
- Existing tests pass, especially TUI interaction and render tests.
- The bash fallback remains behaviorally aligned with the Go TUI where relevant.

Regression criteria to preserve:

- A new user can identify the recommended first action without reading docs.
- Help/glossary keys are consistent across TUI, bash menu, README, and docs.
- Dangerous actions are identifiable without relying only on color.
- The high-CPU wizard does not misrepresent back-to-back thread dumps as separated samples.
- Pod/container resource metrics are visually and textually distinct from JVM metrics.
- JVM heap values show which route supplied them and what to do when no route works.
- State-changing actions such as `re-roll` and `kill pod` are represented accurately in help, risk copy, confirmations, and layout tests.
- Target setup errors explain the next action in plain language.

## Tone And Copy Guidance

Use short, direct operational language:

- Prefer “is the pod restarting?” over “inspect workload lifecycle state.”
- Prefer “pauses app” over “disruptive.”
- Prefer “app health URL” before “actuator” when introducing the concept.
- Prefer “one running copy of the app” before “pod” when teaching.

Avoid marketing language. This is an incident tool. Copy should reduce panic, not add personality for its own sake.

## Do Not Break

- Heap dump confirmation behavior.
- Target readiness gating.
- Session log/transcript behavior.
- Shared target config compatibility with the bash frontend.
- Existing CLI command behavior unless a task explicitly requires it.
- The ability to use the tool in low-dependency or locked-down environments.