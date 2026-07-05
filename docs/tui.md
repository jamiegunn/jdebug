---
title: Interactive menu (TUI)
nav_order: 5
---

# The interactive menu

`jdebug` with no arguments opens the menu. It is the recommended way in:
every action is labeled with what it answers and how risky it is, and the
wizard encodes the diagnostic playbooks so you don't have to remember them.

## Layout

- **▶ w GUIDED DIAGNOSIS** — describe the symptom, it runs the right captures. Start here.
- **LOOK AROUND** — status / health / top / memory. All safe, read-only.
- **CAPTURE EVIDENCE** — threads (safe · instant), heap (**⚠ pauses the app**),
  jcmd (quick-pick of the five useful commands), snapshot (everything in one bundle).
- **LOGS** — live tail from every replica; runtime log-level changes.
- **MORE** — `h` help/glossary · `c` check setup (doctor) · `d` view captures ·
  `i` stage jattach · `p` push in-pod tool · `t` target · `m` mode · `q` quit.

## The header tells you everything

Mode, kube context **with a live ✓/✗ reachability indicator**, namespace,
selector, container, pinned pod, and actuator URL — you always know exactly
what a keypress will hit. An empty selector shows as
`<any pod — press t to narrow to your app>` rather than silently meaning "whatever".

## Targeting (`t`)

One screen walks through context (numbered picker; switching runs
`kubectl config use-context` only after an explicit confirmation), namespace,
selector, container, actuator URL — and then, if several pods match, a pod
picker with phase and restart counts so you can pin the sick replica.

## Output is never lost

- The screen clears **once**, at startup. After that everything scrolls —
  results stay above the next menu and in your terminal's scrollback.
- Every command's output is also transcribed to
  `dumps/session-<timestamp>.log`. The path is shown at every pause and on quit.
- A **failed** action pauses just like a successful one — the error stays on
  screen until you press Enter, with a ✗ line pointing at the explanation.
- Ctrl-C stops a streaming command (like logs) and returns to the menu;
  bare Enter redraws instead of quitting; `q` quits and prints the transcript path.

## Modes

The opening question is *where is the JVM?*

1. **Remote** — drives `kubectl exec` from your machine (full feature set)
2. **In-pod** — you have a shell inside the container; drives `jdebug-local`
3. **Bare metal** — a JVM on this host, no Kubernetes; also `jdebug-local`

The wizard, help, capture browser, and jattach staging work in every mode;
kubectl-only steps (status, top) are skipped in local modes *with an
explanation*, never silently.
