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
- `tui/render_demo.go` - canned render states for visual review.
- `tui/main_test.go` - interaction and layout tests.
- `ui/tui.sh` - bash fallback menu; keep behavior and copy aligned where practical.

## Current UX Assessment

Overall, the UX is strong for junior SREs. The tool already does the most important thing well: it starts from symptoms and turns live context into concrete next actions.

Notable strengths:

- The main menu has a clear `START HERE` guided diagnosis entry.
- The wizard asks “what are you seeing?” instead of asking which JVM tool the user wants.
- Target setup is gated and checklist-driven.
- The target editor explains Kubernetes fields inline.
- Heap dumps are clearly marked as disruptive and require confirmation.
- The live panel turns target state into suggested key presses.
- Help includes a glossary, first workflow, hidden keys, and safety rules.
- Documentation repeatedly states that evidence stays local and captures are saved.
- Recent additions move in the right operator-workflow direction: `workload` exposes Deployment/ReplicaSet/HPA/service context, and `re-roll` / `kill pod` expose recovery-oriented actions through hard confirmation gates.

Current post-scan notes from the latest code:

- `observe/lifecycle.sh` does a good job explaining what `restart` and `kill` do, their risks, and their next steps after execution.
- The TUI now shows `W workload`, `R re-roll`, and `K kill pod`, which makes the tool more useful during real incidents.
- The help screen and safety wording have not fully caught up: it still says everything is read-only except heap dumps and verbosity, but `R` and `K` are also state-changing.
- `re-roll` currently wraps awkwardly in rendered dashboard/menu output because the description and risk text are too long for the row.
- The live panel still summarizes important data without exposing where each data point came from or which command gathered it.

## Known UX Issues To Address

### 1. Documentation key mismatch

`docs/getting-started.md` says the menu glossary is on `h`, but the TUI uses `h` for health and `?` for help/glossary.

Fix the docs to say `?` opens help/glossary.

### 2. Risk depends too much on color

Rows use colored dots for safe/caution/disruptive. This is useful, but the meaning is weaker in `NO_COLOR`, low-color terminals, screenshots, and color-blind contexts.

Improve by adding text for non-safe risk where possible, for example:

- `● caution`
- `● pauses app`
- `● changes logging`

Keep the menu compact, but do not make risk depend only on color.

### 3. Some labels are still expert-first

Consider renaming or pairing expert terms with plain-language names:

- `jcmd` -> `JVM tools` or `JVM cmd`
- `top` -> `CPU/memory`
- `selector` -> `app label` where onboarding matters
- `actuator` -> `app health URL` where onboarding matters

Expert terms can remain in descriptions or help text, but the first visible label should be understandable under stress.

### 4. High-CPU wizard flow needs a real interval

The high-CPU flow says two thread dumps should be captured a few seconds apart, but the implementation queues two `threads` captures back-to-back.

Improve this flow so the second dump is meaningfully separated. Acceptable approaches:

- Prompt the user to wait and press a key before the second dump.
- Add a deliberate short delay if it fits the TUI command model.
- Capture once, explain why to wait, then queue the second capture after confirmation.

Do not silently run two immediate captures while saying they are separated.

### 5. First-run path could be even more obvious

The TUI already emphasizes `w` guided diagnosis. For a true junior flow, make “Not sure? Press w” even harder to miss in first-run or gated states.

Potential improvements:

- In the ready menu, make the wizard line visually dominant and plain.
- In help, put `w` as the first recommended action.
- In docs, recommend `jdebug` then `w` earlier and more explicitly.

### 6. Dense dashboard may overwhelm first-time users

The wide dashboard is powerful, but it shows many panes at once: menu, target live, trends, next, pods, events, captures, and logs.

Do not remove this power-user layout, but consider whether first-time users should get a clearer “incident checklist” hierarchy:

1. What is happening?
2. What should I press next?
3. What evidence was captured?
4. What details are available if needed?

### 7. Commands should be runnable by key or visible label

The keyboard shortcut model is good, but users should not have to remember that `s` means status or `h` means health. They should be able to select either the shortcut or the visible command label and run the same action.

Design goal: every command row should support both:

- Pressing the shortcut key, such as `s`.
- Selecting the command label, such as `Status`, then running it.

This should apply consistently across all visible menu commands, including quick checks, captures, advanced actions, local-mode actions, and any picker-like command list.

### 8. Live panel details should be explorable

The live panel currently summarizes important signals such as last exit, autoscale, memory, CPU, and JVM heap. These summaries should be clickable or otherwise selectable so the user can drill into the underlying explanation.

Specific examples:

- `last exit` should open details about the previous termination reason, exit code if available, related events, and what command to run next.
- Autoscale should open HPA details, scaling rules, current/max replicas, target metrics, and any failure condition.
- Memory and CPU should open a short explanation of what layer the metric comes from and what it means.

### 9. Autoscale metadata is too thin

The dashboard currently reports autoscale as a replica count, for example `4 replicas`. This is useful but incomplete under incident pressure.

Improve autoscale display and drill-down to show:

- Current replicas and max replicas, for example `4/4` or `4/6`.
- Min replicas if space allows.
- Whether the Deployment's desired replica count collides with or fights the HPA-managed count.
- Whether the HPA is healthy and able to scale.
- If autoscale cannot start or cannot compute metrics, say so plainly, for example `autoscale failing - no metrics` or `autoscale failing - no rules`.
- A detail view with the HPA conditions and the raw reason/message translated into plain language.

### 10. Resource metrics and JVM metrics need clearer separation

The dashboard shows memory, CPU, and JVM heap near each other. A junior user may not know whether memory and CPU are pod/container resource metrics or JVM-internal metrics.

Consider splitting the live panel into two explicit groups:

- Resource usage: pod/container CPU and memory from Kubernetes metrics and resource limits.
- JVM usage: heap, non-heap, GC, and JVM route used (`actuator`, `jattach`, or `jcmd`).

The labels should answer “where did this number come from?” without requiring the user to know Kubernetes metrics-server or actuator internals.

### 11. JVM heap fallback behavior should be explicit

The live panel labels heap values with the route used, such as `via actuator`. Confirm and preserve graceful fallback behavior when actuator heap metrics are unavailable.

Desired behavior:

- Try actuator first when configured and reachable.
- Gracefully fall back to jattach or jcmd where possible.
- Show the route used next to the value.
- If no route works, show why in plain language and suggest the next action, such as staging jattach or fixing actuator settings.

### 12. Actuator credentials need a guided setup path

The current UX appears to assume unauthenticated localhost actuator access. Many real Spring Boot apps secure actuator endpoints, so retarget/settings should guide users through authenticated access.

Add a design and implementation plan for actuator credentials:

- Retarget/settings should include actuator auth state, not just actuator URL.
- Support a safe way to provide credentials or tokens without storing secrets carelessly.
- Explain where credentials usually come from in common Spring Boot deployments.
- If credentials are unavailable, clearly offer non-actuator routes such as jattach.
- Avoid guessing or exposing secrets. If a password is generated, hashed, injected from a Kubernetes Secret, or printed during startup, tell the user how to verify the source rather than assuming a default.

Security note: do not ask another LLM to invent default actuator passwords. Spring Security defaults vary by configuration and version; in many apps the generated password appears in logs only in specific local/dev setups, while production credentials are commonly externalized through environment variables, config, or Kubernetes Secrets.

### 13. Command and data transparency needs a first-class UI pattern

Users should be able to see what command produced a data point or what command will run before they execute an action. This should be available without requiring them to run the command first and scroll through output.

Add small indicators next to command rows and important live-panel data. The indicator can be a compact symbol, selectable marker, or inline hint, but it should open an interstitial/detail view that answers:

- What command will run, or what command/data source produced this value.
- Why the command or value is useful.
- What it can and cannot prove.
- Whether it is safe, state-changing, app-pausing, or likely to expose sensitive data.
- What alternatives exist when the route is blocked, for example actuator vs jattach vs jdk.
- What permissions or dependencies it needs, such as RBAC, metrics-server, actuator, jattach, or python3.

Examples:

- `status` detail: runs `jdebug status`; uses Kubernetes pod status and events; safe/read-only; good first check for restarts and scheduling failures.
- `memory` detail: runs `jdebug memory`; reconciles Kubernetes/container RSS with JVM heap/non-heap; requires metrics and python3; alternatives include `jdebug-local memory` inside the pod.
- `threads` detail: runs `jdebug threads`; captures thread state; safe; alternatives are actuator, jattach, and jdk route.
- `heap` detail: runs `jdebug heap --confirm`; pauses the JVM; may contain user data; alternatives are MAT analysis after capture or memory reports before capture.
- `re-roll` detail: runs `jdebug restart --confirm`; maps to `kubectl rollout restart`; restarts every pod in the Deployment; alternative is `kill pod` for one sick replica.
- `last exit` detail: comes from pod container status; explain previous termination reason, exit code if available, related events, and the next command to run.
- `mem` / `cpu` detail: comes from Kubernetes metrics-server via `kubectl top`; it is pod/container resource usage, not JVM heap.
- `jvm heap` detail: comes from actuator metrics or jcmd fallback; label the active route and what failed if no route worked.

This transparency layer should be discoverable from both keyboard and mouse. Avoid requiring hover-only behavior, because terminal hover support is inconsistent.

### 14. Safety copy must include all state-changing actions

The TUI now exposes `R re-roll` and `K kill pod`. These are useful recovery actions, but they change cluster state. Help text, footer legends, risk labels, docs, and tests should treat them as state-changing alongside heap dumps and log-level changes.

Required updates:

- Help safety rules should mention `R` and `K` explicitly.
- Risk text should avoid implying only heap pauses the app.
- `R` and `K` should remain shifted-key actions with second-key confirmation.
- Any selected-label command flow must preserve the same confirmations.
- The row copy should fit without awkward wrapping at supported dashboard widths.

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

Handle these in small commits or patches:

1. Fix `docs/getting-started.md` help key mismatch from `h` to `?`.
2. Review all menu/help/docs references to `h`, `?`, `d`, `a`, `g`, and `M` for consistency.
3. Add non-color risk text for caution/disruptive actions while keeping safe rows compact.
4. Update wizard high-CPU flow so the two thread dumps are actually separated.
5. Review expert labels in `tui/menu.go`, `tui/editor.go`, and `docs/tui.md`; prefer plain-language labels first.
6. Add row selection support so commands can be run by shortcut key or selected visible label.
7. Add drill-down behavior for live-panel signals, starting with last exit and autoscale.
8. Expand autoscale metadata to show current/max replicas, health, HPA failures, and Deployment/HPA replica conflicts.
9. Split or relabel live metrics into Resource usage and JVM usage groups.
10. Verify JVM heap fallback from actuator to jattach/jcmd and make route/failure state visible.
11. Design actuator credential setup in retarget/settings without unsafe secret storage or guessed defaults.
12. Add an operator workflow design pass for incident modes, evidence chains, runbook cards, timeline, blocked-by view, and escalation summary.
13. Add command/data transparency indicators and detail interstitials for command rows and live-panel data.
14. Update help/docs/risk copy for state-changing actions: `R re-roll`, `K kill pod`, verbosity changes, and heap dumps.
15. Tighten long row copy so `re-roll` and other destructive rows do not wrap awkwardly in supported menu/dashboard widths.
16. Run render checks and tests after changes.

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

Add or update tests around the UX contracts, not just rendering. The strongest tests should assert that junior-operator affordances keep working as the UI changes.

Suggested TUI test coverage:

- Command rows can run by shortcut key and by selected visible label.
- Row selection works for quick checks, captures, advanced actions, and local-mode actions.
- Disruptive selected-label actions still require the same confirmation behavior as shortcut actions.
- Help/glossary key consistency: `?` opens help, `h` runs health, and docs/rendered help do not imply otherwise.
- Risk text remains visible when `NO_COLOR=1` or styling is stripped.
- High-CPU wizard does not run two thread captures back-to-back without a wait, prompt, or confirmation step.
- Live-panel drill-down opens details for `last exit` and autoscale/HPA.
- Autoscale render covers healthy HPA, maxed HPA, missing metrics, missing rules, and Deployment/HPA replica conflict states.
- Resource metrics and JVM metrics render under distinct labels or sections.
- JVM heap render covers actuator success, actuator failure with jattach/jcmd fallback, and no-route failure with a clear next action.
- Actuator auth settings render without exposing secrets, and secured-actuator failures point to credential setup or jattach fallback.
- Blocked-by states render actionable explanations for RBAC, metrics-server, secured actuator, missing jattach, no selector, and no previous logs.
- NEXT suggestions are severity sorted when multiple signals are present.
- Escalation summary includes target, symptom/workflow, findings, commands run, captures, blocked checks, and sensitive-evidence warnings.
- Command and data transparency indicators expose command provenance, data source, why it matters, risks, alternatives, and dependencies before execution or drill-down.
- New state-changing actions such as `re-roll` and `kill pod` are documented in help/safety copy and retain hard confirmation gates.

Suggested docs/test-suite checks:

- Rendered menu/help/docs agree on key names and command names.
- Canned render states exist for new detail views, blocked-by views, autoscale states, and escalation summary.
- Width/frame tests include the new selectable rows and runbook/detail overlays.
- Bash fallback tests either assert feature parity or explicitly document where the Go TUI has richer interaction than bash.

## Acceptance Criteria

A good follow-up change should satisfy these checks:

- A new user can identify the recommended first action without reading docs.
- Help/glossary keys are consistent across TUI, bash menu, README, and docs.
- Dangerous actions are identifiable without relying only on color.
- The high-CPU wizard does not misrepresent back-to-back thread dumps as separated samples.
- Every visible command can be run by shortcut key or by selecting its command label.
- Live-panel summary fields that imply deeper context, especially last exit and autoscale, have a discoverable detail path.
- Autoscale shows enough metadata to answer whether the app is at max replicas, can scale, and is fighting Deployment replicas.
- Pod/container resource metrics are visually and textually distinct from JVM metrics.
- JVM heap values show which route supplied them and what to do when no route works.
- Actuator authentication has an explicit guided setup path and does not rely on guessed default credentials.
- Operator workflow ideas are either implemented or captured as explicit follow-up tasks with clear UX entry points.
- NEXT suggestions prioritize severity and, where possible, explain their evidence chain and confidence.
- The UI can produce a concise escalation summary from the current session state and captured evidence.
- Blocked checks are shown as actionable blocked states, not generic failures.
- Command/data transparency indicators are visible and open detail views that show source command/API, purpose, risks, alternatives, and dependencies.
- State-changing actions such as `re-roll` and `kill pod` are represented accurately in help, risk copy, confirmations, and layout tests.
- Target setup errors explain the next action in plain language.
- Existing tests pass, especially TUI interaction and render tests.
- The bash fallback remains behaviorally aligned with the Go TUI where relevant.

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