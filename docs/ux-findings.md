# UX Findings For jdebug

## Goal

Evaluate `jdebug` UI/UX for whether a junior SRE can understand it, get context quickly, and become useful with minimal Kubernetes/JVM knowledge.

## Summary Verdict

`jdebug` is already very strong for junior SRE usability. The core UX model is right: symptom-driven guidance, target safety gates, plain-language output explanations, automatic evidence capture, and "what next?" recommendations.

The main improvement area is cognitive load. The TUI still exposes several Kubernetes/JVM implementation terms early (`selector`, `actuator`, `jcmd`, `HPA`, `RSS`, `NMT`, `JFR`) before a junior user necessarily has a mental model. The next build should make the first-run experience more symptom-first and progressively disclose advanced tools.

Overall UX rating: approximately 8/10 for learnability and 8.5/10 for incident usefulness.

## Highest Priority Build Items

### 1. Fix "Not Sure" Wizard Flow

Current issue:
The wizard's `Not sure -- capture everything` flow asks whether to include a heap dump. If the user declines the heap dump, the flow effectively completes without running a safe capture.

Desired behavior:
Declining heap should still run a non-disruptive snapshot.

Recommended flow:

```text
Not sure -- capture everything
1. Run safe snapshot without heap.
2. Ask: include heap dump too? This pauses the JVM.
3. If yes, run heap capture or snapshot with heap.
4. End with: press `a` to analyze the bundle.
```

Implementation target:
`tui/wizard.go`

Likely change:
Split wizard flow 6 into a safe `snapshot` step plus an optional heap step.

### 2. Make The Main TUI More Symptom-First

Current issue:
The wizard is excellent, but the main menu still presents tool names first:

```text
status
health
top
memory
threads
jcmd
heap
snapshot
logs
verbosity
```

For junior SREs, the primary mental model is usually:

```text
app is down
pod is restarting
memory is high
app is slow
CPU is high
logs look bad
not sure
```

Desired behavior:
Make `w guided diagnosis` visually dominant and clearly the default first action.

Recommended UI direction:

```text
START HERE
  w  guided diagnosis    choose symptom; safest path for new users

QUICK CHECKS
  s  status              is the pod running or restarting?
  h  health              is a dependency down?
  m  memory              is the pod near OOM?
  l  logs                what did the app say?

CAPTURE EVIDENCE
  t  threads             safe snapshot of what code is doing
  x  bundle              safe offline evidence bundle
  H  heap                pauses app; only for memory leaks

ADVANCED
  j  jcmd                advanced JVM commands
  v  log level           change runtime logging
```

Implementation targets:
- `tui/menu.go`
- `ui/tui.sh`
- `docs/tui.md`
- `docs/getting-started.md`

### 3. Add Plain-Language Labels Beside Jargon

Current issue:
Terms like `selector`, `actuator`, `jcmd`, `HPA`, `RSS`, `NMT`, and `JFR` appear in the UI/docs. Help explains them, but users may not open help during an incident.

Desired behavior:
Where these terms appear in primary UI, add short explanations inline.

Examples:

```text
selector    app=payments       label that finds your app pods
actuator    :8080/actuator     Spring Boot admin endpoint
jcmd        advanced JVM tools
HPA         autoscaling status
RSS         total container memory
NMT         native memory tracking
JFR         JVM flight recording/profile
```

Implementation targets:
- `tui/editor.go`
- `tui/panel.go`
- `tui/help.go`
- `ui/tui.sh`
- `docs/getting-started.md`

### 4. Improve Compact/Medium Terminal Layout

Current issue:
Rendered TUI output at moderate widths wraps awkwardly. The menu and live target panel can become visually dense, which is risky for junior users under pressure.

Observed examples:
- Long target header wraps.
- Chooser descriptions wrap mid-phrase.
- Menu and target panel compete for attention.

Desired behavior:
For smaller or medium terminal widths, prefer a simplified vertical "incident checklist" layout instead of a dense dashboard.

Recommended compact order:

```text
1. Target status
2. NEXT recommendation
3. Guided diagnosis
4. Quick checks
5. Captures
6. Logs / advanced
```

Implementation targets:
- `tui/layout.go`
- `tui/menu.go`
- `tui/panel.go`

### 5. Make Command Output End With A Beginner Summary

Current issue:
Many commands already include "how to read this," which is excellent. The next step is to make the last lines even more decision-oriented.

Desired output pattern:

```text
Bottom line:
  Memory is near the pod limit.
  JVM heap is not near max.
  This points to off-heap/native/container memory.

Next:
  Run wizard flow 1, or run:
  jdebug jcmd "VM.native_memory summary"
```

Apply this pattern especially to:
- `jdebug status`
- `jdebug top`
- `jdebug memory`
- `jdebug health`
- `jdebug analyze`
- `jdebug dumps`

Implementation targets:
- `jdebug`
- `observe/memory-report.sh`
- `observe/analyze.sh`
- capture scripts where relevant

### 6. Make Evidence Browser More Action-Oriented

Current issue:
Captures are listed with names, sizes, and age. This is useful, but juniors also need to know what to do with each file.

Desired behavior:
Display a per-artifact next step.

Examples:

```text
threads.txt       open with VisualVM or press `a`
heap.hprof        open with Eclipse MAT Leak Suspects
snapshot-/        press `a` for first-pass analysis
session.log       timeline of what happened
```

Implementation targets:
- `tui/captures.go`
- `jdebug dumps`
- `docs/evidence.md`

### 7. Add "Recommended Next Pick" Inside Target Editor

Current issue:
The readiness gate tells users what field is missing, but once inside the target editor, the user sees all fields equally.

Desired behavior:
When target setup is incomplete, show the next required action inside the editor.

Examples:

```text
Next: pick a namespace, then a pod.
Next: pick the pod with the highest restart count.
Next: pick the app container, usually `app`.
```

Implementation target:
- `tui/editor.go`

### 8. Make RBAC Failures Explicit In Target Enumeration

Current issue:
The target editor is intended to tolerate RBAC limits, but enumeration failures can be flattened into empty lists. Kubernetes may return explicit errors such as `forbidden: User ... cannot list resource "pods" in API group "" in the namespace ...`. If the UI discards that stderr and only sees no rows, a junior SRE may get misleading messages like "nothing to list" or "no pods match this target," even though the real issue is permissions.

Desired behavior:
Enumeration helpers should preserve whether a list is empty because there are genuinely no resources or because `kubectl` returned an error. RBAC denial should be shown plainly and should immediately offer a typed fallback.

Recommended messages:

```text
Can't list namespaces with your current RBAC.
Type the namespace name, or ask for permission to list namespaces.

Can't list pods in namespace payments.
You may still type a pod name if you know it.

Can't discover selectors because pods cannot be listed.
Type a selector such as app=payments, or pick a known pod by name.
```

Implementation targets:
- `tui/backend.go`
- `tui/editor.go`
- `ui/tui.sh`

Recommended implementation shape:

```text
Replace helpers that return only []string with a result type:
  items []string
  err   error
  forbidden bool

Do not discard stderr from kubectl enumeration commands.
Detect RBAC failures from non-zero exit plus messages containing:
  forbidden
  cannot list resource
  cannot get resource

Only say "no pods match" when kubectl succeeded and returned zero pods.
Say "cannot list pods" when kubectl failed.
```

### 9. Improve Selector Discovery Without Guessing Too Aggressively

Current issue:
Selector discovery currently focuses on `app` labels from pods in the selected namespace. That is useful but shallow. It does not use labels from an already selected pod, does not consider common Kubernetes app labels, and can hide RBAC errors behind `<any pod>`.

Desired behavior:
Selector suggestions should be conservative, explain where they came from, and never silently choose a selector for the user.

Recommended behavior:

```text
If a pod is selected:
  Try to read labels from that pod.
  Suggest stable app/workload labels from that pod first.

If no pod is selected:
  Try to list pods in the namespace.
  Derive selector candidates from common labels across pods.
  Include match counts when possible.

If pod listing is forbidden:
  Explain the RBAC limit.
  Offer typed selector input and typed pod input.
```

Prefer these label keys:

```text
app.kubernetes.io/name
app.kubernetes.io/instance
app
k8s-app
component
service
workload
```

Avoid suggesting rollout-specific labels unless the user explicitly asks for all labels:

```text
pod-template-hash
controller-revision-hash
statefulset.kubernetes.io/pod-name
pod-template-generation
```

Example picker:

```text
Selector -- suggestions from pod labels / namespace pods

  1  app.kubernetes.io/name=payments      matches 3 pods
  2  app=payments                         matches 3 pods
  3  component=api                        matches 3 pods
  4  <any pod>                            not recommended if namespace has many apps
  t  type a selector
```

Implementation targets:
- `tui/backend.go`
- `tui/editor.go`
- `ui/tui.sh`
- `docs/getting-started.md`

Wisdom check:
This is possible and wise if suggestions remain transparent and user-confirmed. It is not wise to auto-select inferred labels, because a bad selector can target the wrong workload, a stale ReplicaSet, or too many pods.

### 10. Add A Beginner/Advanced Display Split

Current issue:
Advanced tools are visible beside beginner-safe actions.

Desired behavior:
Keep advanced tools available, but visually secondary.

Beginner-first actions:
- `w` guided diagnosis
- `s` status
- `h` health
- `m` memory
- `l` logs
- `d` captures
- `a` analyze

Advanced actions:
- `j` jcmd
- `H` heap
- `v` verbosity/log level
- `i` stage jattach
- `p` push local tool

Implementation targets:
- `tui/menu.go`
- `ui/tui.sh`
- `tui/help.go`

## Strengths To Preserve

Do not regress these. They are central to the tool's UX quality.

### Symptom-Driven Wizard

The wizard in `tui/wizard.go` maps real incident symptoms to diagnostic flows:

```text
OOMKilled
slow/hung/high latency
high CPU
memory leak
GC pauses
not sure
CrashLoopBackOff
```

This is exactly right for junior SREs.

### Readiness Gate

The menu hides capture tools until the cluster, pod, and container are valid. This protects users from targeting the wrong app.

Implementation:
- `tui/menu.go`
- classic TUI in `ui/tui.sh`

### Live NEXT Suggestions

The target panel converts live status into action recommendations, e.g.:

```text
OOMKilled last restart -> w flow 1
memory 94% of limit -> m
CrashLoopBackOff -> w flow 7
```

Implementation:
- `tui/panel.go`

This is very strong and should be expanded, not removed.

### Safety Gates

Heap dumps are clearly marked as disruptive and require confirmation. This is essential.

Preserve:
- `H` as the heap key
- second-key confirmation
- "pauses app" language
- heap data sensitivity warning

### Evidence Preservation

The UX correctly saves captures under `dumps/`, keeps session logs, and explains local analysis tools like VisualVM and Eclipse MAT. This is a major operational strength.

## Suggested Build Order

1. Fix the "Not sure" wizard flow so safe snapshot always runs.
2. Rework main TUI grouping to emphasize guided diagnosis and quick checks.
3. Add inline plain-language explanations for jargon-heavy fields.
4. Improve compact layout so it behaves like an incident checklist.
5. Add beginner summaries to command output.
6. Make capture browser action-oriented.
7. Add target-editor next-step hints.
8. Make RBAC enumeration failures explicit instead of treating them as empty lists.
9. Improve selector discovery from selected pod labels and namespace pod labels.
10. Split beginner and advanced actions visually.

## Acceptance Criteria

A junior SRE should be able to do the following without external docs:

1. Start `jdebug`.
2. Pick the correct mode when unsure.
3. Select the correct target pod/container.
4. Understand whether the app is restarting, unhealthy, memory constrained, or crash-looping.
5. Use the wizard based on a symptom.
6. Avoid heap dumps unless they intentionally confirm the pause.
7. Find captured evidence.
8. Run first-pass analysis.
9. Know what external tool to open next, if needed.

## Recommended UX Principle

Prefer this structure everywhere:

```text
What is happening?
Why does it matter?
What should I press or run next?
What is risky?
Where is the evidence saved?
```

That is the right mental model for junior SRE usefulness during an incident.