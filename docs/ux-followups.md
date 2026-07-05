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

## Actuator credentials — SHIPPED

The target editor gains an **auth** field (`k`). It stores only a REFERENCE to
the pod's own credential env vars — `bearer:ENV_VAR` or
`basic:USER_VAR:PASS_VAR` — never a secret value. Because the actuator is
called from *inside* the pod (`kubectl exec … curl localhost:…`), `pod_fetch`
emits the auth header with a literal `$ENV_VAR` that the pod's shell expands:
the secret is read in the pod and never touches jdebug's config or the
operator's machine. The prompt explains the usual source (a Kubernetes Secret
mounted as env — verify with `T`, then `env | grep -i actuator`) and does NOT
guess a default password. When actuator fetches fail, the capture scripts point
users at this setup or at the no-HTTP jattach route.

401-vs-absent detection — SHIPPED. A failed actuator fetch now probes the HTTP
status (`pod_http_status`, a `curl -w %{http_code}` / busybox `wget -S` snippet)
and `explain_actuator_fail` names the precise next action: `401/403` → set auth
(or jattach), `404` → wrong path/disabled (fix the URL, or jattach), no reply →
app wedged (jattach). A 200 that isn't a dump is run through `classify_capture`.

Remaining refinement: pre-fill the likely env-var name by reading the pod spec's
`env` in the target editor's auth field.

## Operator incident workflows

Product directions to turn the launcher into an incident companion. Each
should bias the wizard, dashboard, and NEXT toward the checks that matter and
must never make destructive changes automatically.

- **Incident modes** — explicit starting points (down / slow / restarting /
  memory / CPU / deployed / not-sure) that pre-select a wizard flow and reorder
  NEXT. Entry point: a top-level pick in `openWizard`, and a `mode` field that
  weights `suggestions()`.
- **Evidence chains — SHIPPED.** NEXT rows now show the short cause→effect
  behind a recommendation (`likely  OOMKilled last restart → mem 94% of limit →
  w flow 1`). `suggestionRows()` returns structured rows (`{conf, msg, ev, key}`)
  and `suggestions()` renders them; each render site clips to its column width.
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
- **Blocked-by view — SHIPPED.** `b` opens a BLOCKED-BY overlay in both
  frontends. `blockers()` (Go) reads the live signals — cluster reachability,
  selector, pinned pod, RBAC (Forbidden replies via `forbiddenRe`),
  metrics-server, actuator — and lists each currently-blocked check as an
  operator state paired with the least-privilege permission, setup step, or
  fallback route (a dead cluster short-circuits as the one root blocker). The
  bash side mirrors the same catalog and echoes the live gate checks. Reachable
  even while the target gate is up (that's when it matters most).
- **Confidence levels — SHIPPED.** `likely / possible / unknown` prefixes lead
  each NEXT row (coloured by certainty) so a junior knows which warnings are
  certain: a named OOM/crash-loop is `likely`, a blind autoscaler is `unknown`.

These stay diagnostic-first. Recovery guidance (scale up, roll back, loosen a
probe, raise a limit) should be explanation or copy-paste unless a strongly
confirmed remediation flow is designed — the pattern set by `re-roll` and
`kill pod` (hard confirm + full risk brief).

## Runtime context / app wiring

**Goal:** answer “what is this app, what exposes it, what config is it running
with, and what dependencies might be miswired?” without making the user jump
between pod specs, Services, ConfigMaps, Secrets, and JVM commands.

**Entry point:** expand `jdebug topology` or add a sibling read-only verb such
as `jdebug context`. Organize the output into scan-friendly sections:

- Owner and rollout: Deployment/ReplicaSet revision, ready/updated/available
  replicas, strategy, image tag/digest, command/args, rollout status.
- Autoscale: HPA current/min/max, ScalingActive state, metric failures, and
  Deployment `replicas:` vs HPA ownership conflicts.
- Services and ports: Services selecting the pod, Service type, ClusterIP,
  external IP/hostname, ports, targetPorts, named ports, endpoint readiness,
  and whether the selected pod is in endpoints.
- Probes: readiness/liveness/startup probe path/port/type, timeouts, failure
  thresholds, and recent probe failures.
- Environment: container env, `envFrom`, ConfigMap/Secret references, JVM env
  (`JAVA_TOOL_OPTIONS`, `JAVA_OPTS`, `JDK_JAVA_OPTIONS`), active Spring
  profiles, timezone, and proxy variables.
- JVM runtime: Java version, `jcmd VM.flags`, heap/GC flags, system properties
  where safe, NMT/JFR availability.
- Volumes and storage: mounts, PVCs, emptyDir/tmpfs, ConfigMap/Secret volumes,
  read-only flags, and memory-backed mounts that can contribute to OOMs.
- Routes and policy: Ingress/Gateway/mesh routes and NetworkPolicy where
  discoverable.

Every section should print or link to the command/API used to gather it. Secret
values must be redacted; show names/keys/references only.

## Dependency-aware checks: Valkey / Redis-compatible

**Goal:** when env/config suggests Valkey or Redis-compatible clients, surface
the configuration that commonly explains connectivity and cluster routing
failures.

Useful safe clues/checks:

- Client host/port/db/SSL settings from env/config, with secrets redacted.
- `cluster-enabled`.
- `cluster-announce-hostname`, `cluster-announce-ip`,
  `cluster-announce-port`, `cluster-announce-tls-port`,
  `cluster-announce-bus-port`.
- `bind`, `protected-mode`, `port`, `tls-port`.
- ACL / `requirepass` / `masterauth` presence, redacted.
- `replica-announce-ip`, `replica-announce-port`.
- `appendonly`, `maxmemory`, `maxmemory-policy`, `timeout`, `tcp-keepalive`,
  `client-output-buffer-limit`.
- `cluster-node-timeout`, `cluster-require-full-coverage`,
  `cluster-migration-barrier`.

Wrong announce settings are especially useful to flag because they create the
classic “works inside the pod, clients fail from elsewhere” incident shape.

## Captures browser redesign

**Goal:** make captured evidence easy to trust and navigate, especially after
retargeting to another pod.

Design points:

- Show the current scope: selected pod, all pods, current session, or a
  drilled-in timestamp folder.
- Reset or explicitly prompt when the selected pod changes but the browser is
  pinned to a previous pod/session.
- Add explicit refresh and “last refreshed” state.
- Add filters/tabs: current pod, all pods, snapshots, threads, heaps, logs,
  recent.
- Show capture type, route/source, pod, timestamp, size, and recommended next
  action.
- Make `a analyzes current view` precise.
- Support keyboard selection in addition to click-to-open.
- Preserve sensitive-evidence warnings for heaps and logs.

## Invalid heap capture recovery — SHIPPED

When a `.hprof` is actually an actuator error page, login response, JSON error,
or empty/truncated download, the tool now explains it as a capture-route problem
rather than sending the user toward Eclipse MAT.

- Capture time still validates `JAVA PROFILE` magic and leaves bad files for
  inspection; a 200 that isn't a dump is run through `classify_capture`
  (`lib/common.sh`) and named (HTML login/error page · JSON error · empty).
- `analyze` (`observe/analyze.sh`) classifies the bad file and prints exact
  recovery — set auth (`k`), `jdebug heap --via jattach --confirm`, `--via jdk`,
  or fix the actuator URL — instead of MAT-oriented next steps.
- The captures browser (`captures.go`) marks invalid `.hprof` files with `⚠` in
  a warn colour, `capHint` says "not a heap dump — a explains", and viewing one
  shows the classification + recovery (`classifyHead` mirrors the CLI).

Test fixtures cover bad magic, HTML login, JSON error, empty, and valid HPROF
(bash `run-tests.sh` + Go `hprof_test.go`).

## Trends transparency

**Goal:** make the trends pane understandable without reading source code.

The UI should explain:

- One sample is added per panel refresh, currently about every 20 seconds.
- Values are point-in-time samples from Kubernetes/JVM reads, not averages
  computed by jdebug.
- `mem` means container memory percentage of limit.
- `cpu` means Kubernetes CPU usage scaled against the limit when available.
- `rst` means restart count markers; `▲` means the restart count increased at
  that sample.
- History is capped at `histCap` samples, roughly 30 minutes at 20 seconds per
  sample.
- Gaps mean missing metrics or unknown values.

Prefer `restarts` over `rst` where width allows, and provide a trends detail
card or inline legend.

## Idle/background activity transparency

**Goal:** answer “what is the TUI doing in the background while I am just
looking at it?” and let the operator control that activity.

Current shape to expose:

- Live logs refresh about every 5 seconds.
- Target/panel/dashboard reads refresh around every 20 seconds and may burst
  several Kubernetes reads.
- Actuator heap metric reads touch the app/JVM when actuator works.
- `jcmd GC.heap_info` fallback is read-only but heavier and should not be
  repeated in quiet mode.

Controls to add:

- Visible status such as `idle refresh: logs 5s · target 20s · actuator heap 20s`.
- Background probes summary: `kubectl logs, pod/top/hpa, actuator metrics`.
- Pause/resume background refresh.
- Manual refresh once.
- Slow down / speed up intervals.
- Quiet mode: disable logs and JVM probes, keep manual refresh.

Classify cost/risk in the transparency card: low-cost Kubernetes reads,
medium-cost logs/top, app/JVM-touching actuator metrics, heavier JVM-touching
`jcmd` fallback.
