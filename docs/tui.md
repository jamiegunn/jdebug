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
  jcmd (quick-pick of the five useful commands), `0` snapshot (everything in one bundle).
- **LOGS** — live tail from every replica; runtime log-level changes (level is a one-key pick).
- **MORE** — `h` help/glossary · `c` check setup (doctor) · `d` view captures ·
  `i` stage jattach · `p` push in-pod tool · `t` target · `m` mode · `q` quit.

## Keys act instantly

Navigation is single-keypress — no Enter. The only deliberate inputs are
**confirmations** (destructive actions like heap dumps, and quitting — both
ask y/N) and **free-text fields** (a namespace nobody enumerated, a custom
actuator URL). After a command's output, any key returns to the menu.

## The header tells you everything

Mode, kube context **with a live ✓/✗ reachability indicator**, namespace,
selector, container, pinned pod, and actuator URL — you always know exactly
what a keypress will hit. An empty selector shows as
`<any pod — press t to narrow to your app>` rather than silently meaning "whatever".

## Targeting (`t`) — the field editor

`t` opens an editor where **each field is one keypress**, edited in place:

```
TARGET — press a letter to change a field · Enter/b back to the menu
 c  context     ddk3s
 n  namespace   payments
 s  selector    app=payments
 p  pod         <auto: first match>
 o  container   app
 a  actuator    http://localhost:8080/actuator
```

Everything the cluster can enumerate opens a **live dropdown** — pick by
number, single keypress:

- `c` — your kube contexts (switching runs `kubectl config use-context`,
  confirmed first because it changes your default everywhere)
- `n` — namespaces, listed from the cluster
- `s` — selectors **built from the `app` labels actually on pods** in the
  namespace, plus an explicit *any pod* option; `t` types any label expression
- `p` — matching pods with phase and restart counts, so you can pin the
  sick replica instead of silently getting the first
- `o` — containers read from the **pinned pod's** spec (pick the pod first;
  the container list follows it)

Free text remains available everywhere — and when permissions don't allow
enumerating (e.g. you can't list namespaces), the dropdown says so and drops
straight to a typed prompt.

Selections are **remembered between sessions** (`~/.config/jdebug/target` —
delete to forget). A pinned pod that has since died is detected at startup
and falls back to auto with a visible notice.

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
