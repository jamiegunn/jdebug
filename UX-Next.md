# UX Next: Interstitial Consistency

## Implementation status — SHIPPED

- **Confirm-over-source bug** — fixed: `askConfirm2` records `confirmBase`; a
  confirmation now renders over the screen that launched it (menu → menu, editor
  → editor), and declining returns there.
- **Interstitial consistency** — every full-screen/overlay interstitial now has
  a TITLE and a visible dismiss hint (esc/back/cancel/any-key); pickers (via /
  jcmd / level) gained titles + intent. Enforced by
  `TestInterstitialsHaveTitleAndDismiss`.
- **Actuator auth interstitial** — `k` opens a guided `ACTUATOR AUTH` screen
  (formats + examples + how-to-find + jattach fallback; stores a reference only).
- **Bottom work-area tabs** — WORK / LOGS / EVENTS tab strip; events are back
  from the right column; tab/shift-tab switch; a launched command auto-selects
  WORK.
- **Remote artifact lifecycle** — the CLI records what it staged in a pod
  (jattach / jdebug-local) with ownership; `jdebug cleanup [--confirm]` removes
  only session-staged files (never pre-existing, never local dumps/); the TUI
  shows a footer indicator + a `u` REMOTE ARTIFACTS screen + a quit-time mention.

Remaining refinement (not blocking): extend `f`-expand to the WORK/EVENTS tabs
(today it expands LOGS), and a per-work-item history list in the WORK tab.

## Summary

The current Bubble Tea TUI supports interstitials well, but the app uses several different interstitial patterns at once. Each behavior makes sense locally, but the overall model can feel inconsistent to an operator under pressure.

The goal is to make temporary UI states predictable: detail cards, confirmations, pickers, help, blocked-by views, output, and mouse interactions should share a small set of rules.

Every interstitial should answer three questions before the operator acts:

- **Where am I?** A clear title.
- **Why am I here?** A one-sentence intent/description.
- **What will happen if I continue?** The exact command(s), data source, or state change that will run.

## Current Patterns

### Appended overlays

These render by appending content under `menuView()`:

- `scConfirm`: confirmation message under the menu.
- `scVia`: actuator / jattach / jdk route picker under the menu.
- `scJcmd`: JVM command picker under the menu.
- `scLevel`: log-level picker under the menu.

### Full replacement screens

These replace the menu entirely:

- `scHelp`
- `scDetail`
- `scBlocked`
- `scEditor`
- `scPicker`
- `scInput`
- `scWizard`

### Mixed output behavior

Command output can appear as:

- A full-screen output view.
- A dashboard bottom pane.
- A stream that temporarily replaces live logs.

This is useful, but it adds a separate mental model from the other interstitials.

### Mixed mouse behavior

Mouse behavior is useful but inconsistent by region:

- Left-click menu row: run command.
- Right-click menu row: open detail card.
- Click TARGET panel: run `why` immediately.
- Click capture: drill/open file.
- Click pod: retarget.

The actions are reasonable, but they do not all follow one “inspect first / act second” language.

### Mixed dismiss behavior

Dismiss/accept behavior differs by screen:

- Help: any key returns.
- Blocked-by: any key returns.
- Detail cards: `q`, `esc`, `enter`, or `.` returns.
- Picker: unrelated key keeps current and returns.
- Via picker: unrelated key cancels.
- Confirm: wrong key cancels.
- Editor: `esc` saves target and returns.

These local decisions are defensible, but they can feel unpredictable as one product.

## Concrete Bug/Risk

`scConfirm` always renders over `menuView()`:

```go
case scConfirm:
    return m.menuView() + "\n  " + cWarn.Render(m.confirmMsg) + " "
```

That means a confirmation launched from another screen can visually yank the user back to the menu. For example, a context-switch confirmation from the target editor belongs to the editor flow, but the confirmation renders as menu + confirm.

The confirmation should render over the screen that launched it, not always over the menu.

## Proposed Interstitial Policy

### 1. Full-screen screens

Use full-screen replacement views for larger, self-contained contexts:

- Help
- Wizard
- Target editor
- Captures browser
- Detail cards
- Blocked-by view
- Runtime context/workload detail views

Each should show a consistent footer:

```text
esc/q back
enter run/select        # only where applicable
```

Each full-screen interstitial should include:

- A title, for example `ACTUATOR AUTH`, `COMMAND DETAILS`, `BLOCKED BY`, or `CAPTURES`.
- A short intent line, for example `Choose how jdebug should authenticate to secured actuator endpoints.`
- The action that will happen on accept, including exact command(s) where applicable.
- A risk/source/dependency summary when the action touches the pod, JVM, cluster API, credentials, or local files.

### 2. Inline overlays

Use inline overlays for small, temporary decisions:

- Confirmations
- Capture route picker
- Log-level picker
- Short command pickers

These should render over the screen that launched them.

Each inline overlay should include:

- A title or label, not just a bare prompt.
- A one-line intent, for example `Choose the capture route for this heap dump.`
- The command that will be executed if confirmed, or the config/state that will change.
- The cancel path, usually `esc cancels` or `any other key cancels` for same-key confirmations.

Examples:

- Confirm launched from menu -> menu + confirm.
- Confirm launched from editor -> editor + confirm.
- Confirm launched from detail card -> detail + confirm, if that flow exists.

Example confirmation copy:

```text
RE-ROLL DEPLOYMENT

Intent: restart every pod in the owning Deployment.
Will run: jdebug restart --confirm
Risk: state-changing; in-flight requests and in-memory state on old pods are lost.

Press R again to confirm · esc cancels
```

### 3. Detail before action

Standardize a consistent inspect/run model:

- `.` or right-click: explain/detail.
- `enter` or left-click: run/select primary action.
- `esc`: back/cancel.

Avoid unlabeled cases where clicking a summary immediately runs a command. If a click does run a command, label it clearly, for example `click -> why`.

### 4. Mouse semantics

Use one mouse language everywhere possible:

- Left-click: select/run primary action.
- Right-click: explain/detail.
- Wheel: scroll pane under pointer.
- Click outside an actionable region: no-op.

Confirmations must never be bypassed by mouse actions. Click-to-run should dispatch through the same key path as keyboard shortcuts.

### 5. Dismiss semantics

Standardize dismiss behavior:

- `esc` always backs out or cancels.
- `q` backs out for non-editing interstitials.
- `enter` accepts/runs/selects only where the screen says it does.
- Random keys should generally do nothing, rather than sometimes dismissing.

Exceptions are acceptable, but the screen must say so explicitly.

### 6. Confirmation source screen

Store enough state to render confirmation over the source screen.

Possible approach:

- Keep `m.prev` for return behavior.
- Add a helper that renders the previous/source screen without dispatching back into `scConfirm`.
- Or store a `confirmBase screen` separately from `prev`.

Acceptance rule:

- A confirmation launched from `scEditor` visually preserves the editor underneath.
- A confirmation launched from `scMenu` visually preserves the menu underneath.
- Declining returns to the source screen with state intact.
- Accepting runs the same command/callback as today.

## Actuator Auth Interstitial Needs Clearer Guidance

The target editor currently exposes actuator auth as a compact field (`k auth`) and an input prompt such as:

```text
actuator auth ref — bearer:ENV_VAR  or  basic:USER_VAR:PASS_VAR
```

That is technically correct, but it is confusing for an operator. It does not explain what value goes there, where to find it, or why jdebug wants an environment-variable reference instead of the secret itself.

### UX problem

An operator trying to secure actuator access needs answers to four questions:

- What auth formats are supported?
- Do I paste the token/password, or the name of an environment variable?
- How do I find the right environment variable or Secret reference?
- What should I do if I cannot find credentials?

The current one-line prompt tries to answer too much at once and does not provide examples.

### Proposed screen

Make actuator auth a full interstitial or guided sub-screen from the target editor, not just a single input prompt.

Suggested layout:

```text
ACTUATOR AUTH

Intent: choose how jdebug authenticates to secured actuator endpoints.

jdebug stores a REFERENCE, not the secret value.
The token/password should already exist inside the pod as an environment variable
or mounted secret. jdebug asks the pod to expand it when making the actuator call.

Will affect:
    future actuator calls such as health, metrics, heap via actuator, and log-level changes

Will save:
    a reference like bearer:MANAGEMENT_TOKEN or basic:ACTUATOR_USER:ACTUATOR_PASSWORD
    in ~/.config/jdebug/target

Choose one:

    1  none
         actuator is open on localhost, or you will use jattach instead

    2  bearer token from pod env var
         example: bearer:MANAGEMENT_TOKEN
         sends:   Authorization: Bearer $MANAGEMENT_TOKEN

    3  basic auth from pod env vars
         example: basic:ACTUATOR_USER:ACTUATOR_PASSWORD
         sends:   -u "$ACTUATOR_USER:$ACTUATOR_PASSWORD"

How to find candidates:

    safe:    W workload -> Environment / Secret references
    shell:   T terminal -> env | grep -Ei 'actuator|management|spring|token|password'
    k8s:     kubectl -n <ns> get deploy <name> -o yaml

Do not paste secret values into jdebug. Use env var names only.

esc back · enter save
```

### Examples to show in the UI

Bearer token:

```text
If the pod has:
    MANAGEMENT_TOKEN=<secret value>

Enter:
    bearer:MANAGEMENT_TOKEN
```

Basic auth:

```text
If the pod has:
    ACTUATOR_USER=<username>
    ACTUATOR_PASSWORD=<secret value>

Enter:
    basic:ACTUATOR_USER:ACTUATOR_PASSWORD
```

Spring-style env names the user might look for:

```text
MANAGEMENT_TOKEN
ACTUATOR_TOKEN
ACTUATOR_USER
ACTUATOR_PASSWORD
SPRING_SECURITY_USER_NAME
SPRING_SECURITY_USER_PASSWORD
MANAGEMENT_SERVER_PORT
MANAGEMENT_ENDPOINTS_WEB_EXPOSURE_INCLUDE
```

These are only search hints, not guaranteed names.

### Safe ways to obtain the reference

Prefer sources that reveal names and references, not values:

- Workload/runtime context view showing env var names and Secret/ConfigMap references.
- Deployment YAML showing `env`, `envFrom`, `secretKeyRef`, and `configMapKeyRef`.
- Pod shell `env` search only when the operator is allowed to inspect that pod.
- Team runbook or deployment chart values.

Do not encourage copying raw Secret values into jdebug config.

### Fallback guidance

If the operator cannot find actuator credentials, the screen should say:

```text
No actuator credentials? You can still capture JVM evidence without HTTP:
    threads: t -> jattach
    heap:    H -> jattach
    jcmd:    j
```

This keeps the user moving instead of trapping them in auth setup.

### Tests

Add tests for the actuator-auth interstitial:

- Opening `k auth` from the target editor shows examples for `bearer:ENV_VAR` and `basic:USER_VAR:PASS_VAR`.
- The screen explicitly says jdebug stores references, not secret values.
- The screen lists safe ways to find env var names.
- Saving persists only the reference string.
- Clearing auth sets the mode back to none.
- The fallback text mentions jattach routes when actuator credentials are unavailable.

## Remote Artifact Lifecycle Needs UI Treatment

Remote `jattach` is not zero-touch: in remote mode, the jattach tier stages a small executable inside the selected container when it is not already present.

Current behavior to make visible:

- Default remote path: `/tmp/jattach`.
- If missing, jdebug downloads or reuses a cached Linux `jattach` binary on the operator machine.
- jdebug copies it into the pod with `kubectl cp`.
- jdebug runs `chmod +x /tmp/jattach` inside the container.
- jattach heap dumps temporarily write a remote file such as `/tmp/heap-jattach-<ts>.hprof`, copy it back, then attempt to remove it.
- JDK heap tier similarly writes `/tmp/heap-jdk-<ts>.hprof`, copies it back, then attempts to remove it.
- Arbitrary `jcmd` commands can create remote files if the command includes a filename, for example `JFR.start filename=/tmp/rec.jfr`.
- `push-local` copies `jdebug-local` into the pod, commonly under `/tmp/jdebug-local`.

### UX problem

A junior operator may not realize the TUI copied anything into the pod. That matters in locked-down environments, read-only filesystems, security reviews, and cleanup expectations.

The UI should explicitly distinguish:

- **Read-only Kubernetes/API reads**.
- **App/JVM-touching reads** such as actuator and jcmd probes.
- **Remote filesystem writes** such as staging `/tmp/jattach`, pushing `jdebug-local`, heap/JFR output files, or chmod operations.
- **State-changing Kubernetes actions** such as `R re-roll` and `K kill pod`.

### Proposed UI additions

Add a visible remote-artifacts indicator when the session has staged or created anything in the pod:

```text
REMOTE ARTIFACTS
    /tmp/jattach                 staged by this session
    /tmp/heap-jattach-...hprof   copied back, cleanup pending
    /tmp/rec.jfr                 created by jcmd, not copied yet

enter cleanup · q/esc keep for now
```

Add this to transparency cards for jattach-backed commands:

```text
Will copy if missing:
    kubectl cp <cache>/jattach-linux-<arch> <pod>:/tmp/jattach -c <container>
    kubectl exec <pod> -c <container> -- chmod +x /tmp/jattach

Will run:
    kubectl exec <pod> -c <container> -- /tmp/jattach <pid> jcmd ...

Cleanup:
    remove /tmp/jattach on quit if jdebug staged it in this session
```

Add a footer/status hint when remote artifacts exist:

```text
remote artifacts: 1 staged · [u] cleanup · quit asks
```

### Cleanup on quit

On quit, if remote artifacts were staged or created in the current session, show a cleanup interstitial before exiting.

Suggested layout:

```text
CLEAN UP REMOTE ARTIFACTS

Intent: remove files jdebug copied or created inside the target pod during this session.

Will remove:
    /tmp/jattach                       staged by jdebug
    /tmp/heap-jattach-20260705.hprof   copied back successfully

Will not remove:
    files that existed before this session
    files not created by jdebug
    local evidence under dumps/

Will run:
    kubectl -n <ns> exec <pod> -c <container> -- rm -f <paths...>

Choose:
    y  clean up and quit
    n  quit and leave files
    esc back to app
```

### Important cleanup policy

Avoid deleting user-prestaged tools unexpectedly.

Track artifact ownership:

- If `/tmp/jattach` existed before this session, mark it `pre-existing` and do not remove it by default.
- If jdebug staged `/tmp/jattach` during this session, mark it `session-owned` and offer cleanup on quit.
- If a heap dump remote path was copied back successfully, remove it automatically or include it in cleanup.
- If a command-created file was not copied back, show it as `remote only` and ask before deleting.
- Never remove local `dumps/` evidence on quit.

### Failure handling

If cleanup fails, explain why and keep the exact command visible:

```text
cleanup failed: read-only filesystem / RBAC denied / pod replaced / container not running
manual cleanup:
    kubectl -n <ns> exec <pod> -c <container> -- rm -f /tmp/jattach
```

If the pod was replaced, say so plainly and do not treat it as a fatal app error.

### Tests

Add tests for remote artifact cleanup:

- Staging jattach records `/tmp/jattach` as session-owned when it did not exist before.
- Pre-existing `/tmp/jattach` is not removed by default.
- Remote heap/JFR paths created by jdebug are listed in the cleanup interstitial.
- Quit with session-owned artifacts opens cleanup before exiting.
- `y` runs the expected `kubectl exec rm -f ...` cleanup command.
- `n` quits without cleanup.
- `esc` returns to the app.
- Cleanup failures show the manual command and do not hide the transcript/local dumps.

## Bottom Work Area Tabs

Yes: the TUI can support tabs. Bubble Tea does not provide browser-like tabs as a primitive, but the app can render a tab strip with Lip Gloss and store active-tab state in the model. Keyboard and mouse events can switch tabs the same way the current dashboard switches between live logs and command output.

The bottom work area is a good candidate because it already has three competing jobs:

- Show command output while a selected action is running or held.
- Show live logs when no command output is active.
- Surface recent pod events without spending permanent right-column space on `EVENTS`.

### Proposed tab model

Replace the current binary bottom pane (`OUTPUT` while a command is active, otherwise `LIVE LOGS`) with a three-tab work area:

```text
 WORK 3  │  LOGS  │  EVENTS 2W                                  5s · tab/shift-tab switches
────────────────────────────────────────────────────────────────────────────────────────────
 ...active tab content...
```

Tabs:

- `WORK`: selected/running work items and their output transcript.
- `LOGS`: live log tail for the selected pod/container.
- `EVENTS`: recent Kubernetes events for the selected pod.

The selected tab should be visually obvious in monochrome and color. Use inverse/bold/underline or a bracketed label, not color alone.

### WORK tab

The `WORK` tab should answer: what did I ask jdebug to do, what is running now, and what evidence did it produce?

Content should include:

- The current running command, if any.
- Recent selected work items in this session, newest first.
- Status for each item: running, stopped, succeeded, failed.
- The transcript/output for the selected work item.
- Capture file path when a command produced evidence.

Suggested layout:

```text
 WORK 3  │  LOGS  │  EVENTS 2W                                  C copy · esc stops/dismisses
────────────────────────────────────────────────────────────────────────────────────────────
 ▶ heap dump       running     jdebug heap app-debug-demo-app-...
 ✓ status          12s ago     dumps/pods/.../status.txt
 ✗ health          2m ago      dependency check failed

 $ jdebug heap app-debug-demo-app-...
 ...streaming output...
```

Behavior:

- A newly launched command should switch to `WORK` automatically.
- If the operator manually switches away while a command is running, do not yank focus back on every chunk.
- `esc` in `WORK` stops a running command first; if nothing is running, it dismisses/clears the held output according to the existing output-pane rule.
- `C` copies the selected work item's transcript.
- `a` analyzes the selected capture/file when one exists.

### LOGS tab

The `LOGS` tab should keep the current live log behavior:

- Polls/tails the selected pod/container.
- Shows previous-container logs when the current container is crash-looping and current logs are unavailable.
- Supports scrollback, follow, and expanded focus mode.

Suggested title metadata:

```text
 WORK  │  LOGS*  │  EVENTS 2W                                  app · 5s ago · [f] expand
```

Use `*` or another non-color marker when the log tab has new error/warning lines while it is not active.

### EVENTS tab

The `EVENTS` tab should reuse the existing pod event fetch, but move it from the always-visible right column into the bottom work area.

Content should include:

- Recent events for the selected pod, newest first.
- Warning count in the tab label, for example `EVENTS 2W`.
- Age, type/severity, reason, and message.

Suggested layout:

```text
 WORK  │  LOGS  │  EVENTS 2W                                  pod events · 20s ago
────────────────────────────────────────────────────────────────────────────────────────────
 2m    Warning   BackOff      Back-off restarting failed container app in pod
 5m    Warning   Unhealthy    Liveness probe failed: HTTP probe failed with statuscode 503
 9m    Normal    Started      Started container app
```

When there are no events, keep the empty state plain:

```text
 - no recent pod events -
```

### Keyboard and mouse contract

Use a small, predictable tab contract:

- `tab`: next work-area tab.
- `shift-tab`: previous work-area tab.
- `1`, `2`, `3` only when the bottom work area has focus, or avoid number shortcuts to preserve command keys.
- Mouse click on a tab label switches tabs.
- Wheel scrolls the active bottom tab when the pointer is over the work area.
- `f` expands the active bottom tab, not only logs. For `WORK`, expand the transcript; for `EVENTS`, expand the event list.

Avoid stealing core menu shortcuts while focus is still on the main command menu. The bottom tab strip can be globally switchable with `tab`/`shift-tab`, but tab-specific actions should only apply when the active tab says they apply.

### State model

The implementation can be incremental:

- Add `workTab` state, for example `tabWork`, `tabLogs`, `tabEvents`.
- Keep existing `outState`, `logState`, and `events` data; do not merge their data models.
- Replace `bottomPane(w, h)` with a tab-aware renderer that delegates to `workPane`, `logPane`, or `eventsPane`.
- Keep `scOutput` as the fallback full-screen view for terminals without enough height, but make its content match the `WORK` tab.
- Add event/log unread markers only after the tab structure is stable.

### Interaction with the right column

This tab model supports the recent right-column change:

- Right column: target and Kubernetes object context (`PODS`, `WORKLOAD`, `CAPTURES`).
- Bottom work area: time-ordered operational evidence (`WORK`, `LOGS`, `EVENTS`).
- Middle panel: selected target status split by pod/container/JVM (`TARGET LIVE`, `resource · container`, `jvm · process`).

That separation should make scope clearer:

- Pod-specific: selected pod identity, phase/restarts, events, logs, captures.
- Container-specific: resource usage, limits, selected container logs.
- JVM/process-specific: heap, jcmd/jattach/actuator-derived values.
- Workload-specific: deployment/owner/replicas/service account/volumes.

### Tests

Add tests for bottom work-area tabs:

- Dashboard renders a tab strip with `WORK`, `LOGS`, and `EVENTS` when the bottom work area is visible.
- Launching a command switches the active tab to `WORK` and streams output there.
- Switching to `LOGS` keeps the command running but stops rendering command output in the active pane.
- `EVENTS` shows pod events and warning counts without reintroducing an always-visible right-column `EVENTS` pane.
- `tab` and `shift-tab` cycle tabs without triggering menu actions.
- Mouse click on a tab label switches tabs.
- Wheel scrolling over the bottom work area scrolls the active tab only.
- `f` expands the active tab and `esc` returns to the dashboard.
- Small terminals still fall back to full-screen output/log/event views without wrapping or hiding the active command.

## Suggested Tests

Add tests for the interaction contract, not just rendering:

- Every interstitial has a title and an intent/description line.
- Action interstitials show the command(s), data source, or state change that accepting will trigger.
- Confirmation launched from menu renders over menu and returns to menu on cancel.
- Confirmation launched from editor renders over editor and returns to editor on cancel.
- Confirmation launched from picker/detail, if supported, renders over that source screen.
- `esc` cancels/back-outs consistently across confirm, via, detail, blocked, picker, input, output, and wizard.
- Random unrelated keys do not dismiss full-screen interstitials unless the screen explicitly documents that behavior.
- Left-click and keyboard shortcut use the same command path and confirmation gates.
- Right-click opens the same detail card as keyboard detail access.
- TARGET panel `click -> why` remains visibly labeled if it runs immediately.
- Remote artifact interstitials show title, intent, copied/created paths, cleanup command, and keep/delete choices.
- Quit-time cleanup only removes session-owned remote artifacts, not pre-existing files or local dumps.

## Why This Matters

The app is becoming an incident companion, not just a command launcher. As the UI gains transparency cards, blocked-by views, workload/runtime context, captures browsing, and recovery actions, predictable interstitial behavior becomes part of the safety model.

Junior SREs should not have to learn a new dismiss/run rule for every temporary screen.