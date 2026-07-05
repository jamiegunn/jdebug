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

- **Incident modes — MOSTLY SHIPPED.** The wizard IS the symptom-first mode
  picker: OOM(1) · slow(2) · CPU(3) · leak(4) · GC(5) · not-sure(6) ·
  crash-loop(7) · **deploy-just-happened(8, new)**. Flow 8 runs `what-changed`
  → `timeline` → `logs --previous`. Remaining refinement: a `mode` field that
  also re-weights the dashboard `suggestions()` ordering (NEXT is already
  severity + confidence sorted, so this is incremental).
- **Evidence chains — SHIPPED.** NEXT rows now show the short cause→effect
  behind a recommendation (`likely  OOMKilled last restart → mem 94% of limit →
  w flow 1`). `suggestionRows()` returns structured rows (`{conf, msg, ev, key}`)
  and `suggestions()` renders them; each render site clips to its column width.
- **Runbook cards** — the transparency card, specialised per signal (what it
  means / why / check first / safe cmd / risky cmd / what to tell the next
  person). Good first cards: last exit, HPA, container memory, JVM heap, probe
  failures, CrashLoopBackOff, secured actuator.
- **Incident timeline — SHIPPED.** `jdebug timeline` (`observe/timeline.sh`,
  wizard flow 8) merges the pod's Kubernetes events with the capture directories
  under `dumps/pods/<pod>/` and prints them oldest→newest with a legend
  (⚠ warning · · normal · ⬇ a capture you took). Undated entries still show.
- **What changed — SHIPPED.** `jdebug what-changed` (`observe/what-changed.sh`,
  wizard flow 8) pulls the deploy-suspects into one place: spec image vs running
  imageID (digest), pod/rollout timing, restart reason + code + time, and
  Deployment `replicas:` vs HPA scale intent — with pointers to `logs
  --previous`, `timeline`, and `topology`.
- **Escalation summary — SHIPPED.** `jdebug escalate` (`E` in both frontends)
  builds a paste-ready handoff from the current target + live pod state + the
  session log + captures on disk: TARGET, FINDINGS with confidence
  (likely/possible/unknown, reusing the NEXT tiers), BLOCKED CHECKS (RBAC /
  metrics-server), COMMANDS ALREADY RUN (parsed from the newest
  `session-*.log`), CAPTURES with paths, a SUGGESTED NEXT action, and a
  sensitive-evidence warning when heap dumps/logs are present. Read-only;
  degrades to a minimal brief without python3.
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

## Runtime context / app wiring — SHIPPED

A new read-only verb `jdebug context` (`observe/context.sh`, reachable as `e` in
both frontends) answers "what is this app, what exposes it, what config is it
running with, and what dependencies might be miswired?" in one pass. It reads the
pod spec + Services/Endpoints + referenced ConfigMaps and prints scan-friendly
sections — **owner & rollout · services & ports (incl. endpoint membership) ·
probes · environment (JVM env, Spring profiles, tz, proxies, envFrom) · secret &
config references · volumes & storage (tmpfs/PVC/memory-backed flagged) ·
dependencies · Valkey/Redis** — each naming the command it used. Secret VALUES
are never printed: sensitive keys and secretKeyRef values show as
`<redacted>` / `← Secret name/key`. JVM live flags are pointed at
`jdebug jcmd 'VM.flags'` rather than probed, keeping `context` kubectl-only.

Remaining refinement (kept as ideas): external IP/hostname for LoadBalancer
Services, Ingress/Gateway/NetworkPolicy discovery. Original section spec:

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

## Dependency-aware checks: Valkey / Redis-compatible — SHIPPED

The `dependencies · Valkey / Redis` section of `jdebug context` surfaces both
client-side settings (from the app's `REDIS/VALKEY/LETTUCE/JEDIS` env, passwords
redacted) and server-side config found in mounted `redis.conf`-style ConfigMaps:
`cluster-enabled`, all `cluster-announce-*` / `replica-announce-*`,
`bind`/`protected-mode`/`port`/`tls-port`, `requirepass`/`masterauth`
(presence only, always `<redacted>`), `maxmemory*`, `appendonly`, and the
`cluster-node-timeout`/`require-full-coverage`/`migration-barrier` knobs. When
announce settings are present it flags the classic "works in the pod, clients
fail from elsewhere" footgun. Extensible for future deps (DB/Kafka/mesh).

Original clue list:

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

## Captures browser redesign — PARTIALLY SHIPPED

The reliability + clarity concerns are done; the fuller filter/keyboard redesign
is the remaining refinement.

Shipped:
- **Scope indicator** — the pane title names what you're looking at: `this pod`,
  `all pods`, or the drilled-in session path (`capsScope()`).
- **Pod-change reset** — switching pod (click) or committing a target change
  (editor) un-pins `capsCwd`, so the browser never sticks to the previous pod;
  the pod-click path also refetches immediately.
- **"Last refreshed" state** — the title shows `refreshed Ns ago`; `r` forces a
  refresh (via `refreshNow`), and each entry already shows type·size·age·next
  action, with invalid `.hprof` marked `⚠`.

Remaining refinement:
- Filter tabs (current pod / all / snapshots / threads / heaps / logs / recent).
- Keyboard selection (↑/↓/↵) alongside click — needs a focus mode like the log
  pane's `f`, or a dedicated full-screen captures screen.
- Per-entry route/source (which tier produced it) and a fully explicit
  "`a` analyzes: <the open file | the whole tree>" label.

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

## Trends transparency — SHIPPED

The TRENDS section now carries an inline legend (`spark.go`) —
`mem=%limit cpu=vs-limit ▲=restart · point-in-time, 1/20s` (a "collecting…"
variant until there are 2 samples) — and the help screen (`?`) adds a full
"TRENDS + WHAT THE SCREEN DOES WHILE IDLE" section spelling out point-in-time
(not averaged) semantics, the ~20s cadence, the ~30-min (`histCap`) window, and
that a gap in a sparkline is a missing metric sample.

## Idle/background activity transparency — SHIPPED

The panel now shows a live status line of what runs on its own
(`auto 20s · logs 5s · z quiets`), and a three-state background mode cycles with
`z`:

- **live** (default) — logs every 5s, pod/top/hpa/events/pods every 20s, plus the
  app/JVM-touching actuator heap probe (with its `jcmd GC.heap_info` fallback).
- **quiet** — stops log polling AND the JVM/actuator probe; the cheap kubectl
  reads stay, and the last-known heap/actuator status is held (`HeapSkipped`
  carry-forward) rather than blanked.
- **paused** — nothing runs automatically.

`r` does one full refresh on demand (works in every mode). The help screen
classifies the cost/risk of each probe (cheap kubectl vs medium logs/top vs
app/JVM-touching actuator + jcmd). Implemented in `main.go` (mode + tick gating +
`refreshNow`/`bgStatus`), `panel.go` (`fetchPanel(probeJVM)`), `menu.go` (`r`/`z`).
