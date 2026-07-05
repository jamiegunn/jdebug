---
title: Interactive menu (TUI)
nav_order: 5
---

# The interactive menu

`jdebug` with no arguments opens the menu. It is the recommended way in:
every action is labeled with what it answers and how risky it is, and the
wizard encodes the diagnostic playbooks so you don't have to remember them.

## Two frontends, one contract

The menu ships in two implementations with identical keys, screens, gating,
config, and session-log behavior:

- **Go (Bubble Tea)** — the preferred frontend. Build once with `make tui`
  (needs a Go toolchain); `jdebug` automatically prefers the built binary at
  `tui/jdebug-tui`. Richer rendering, real text input, arrow-key pickers.
  Runs inline (no alternate screen), and commands execute with your real
  terminal — output stays in scrollback and the session log, exactly like
  the classic menu. Every action shells out to the same tested bash CLI.
- **bash (classic)** — the zero-dependency fallback; always available.
  Force it with `JDEBUG_CLASSIC=1`.

Both read and write the same remembered target (`~/.config/jdebug/target`),
so you can switch between them freely.

## Layout

The main screen is a two-line header (title + one glanceable status line), a
boxed **guided diagnosis** hero row, three verb-named sections with hairline
rules, a footer with navigation keys and a risk legend, and a live `❯` prompt:

```
 jvm debug kit                                        remote · kubectl → pod
 ● ddk3s  ·  debug-demo / app · …c6c4b5769-s9jdg  ·  :8080/actuator  ·  [g] retarget  [M] mode
 ─────────────────────────────────────────────────────────────────────────

 ▎▸ w  guided diagnosis — describe the symptom, it runs the right captures

 INSPECT  read-only ──────────────────────────────────────────────────────
   s   status      pods up? restarts, recent events                      ●
   h   health      app checks — db, queue, disk                          ●
   o   top         CPU + memory per pod, autoscaler                      ●
   m   memory      container total vs JVM heap/non-heap                  ●

 CAPTURE  saves to dumps/ · [d] browse ───────────────────────────────────
   t   threads     what every thread is doing now                        ●
   j   jcmd        advanced JVM — GC, JFR, native                        ●
   H   heap        every object, for leak hunting             ● pauses app
   x   snapshot    everything in one offline bundle                      ●

 LOGS ────────────────────────────────────────────────────────────────────
   l   logs        live stream from every replica                        ●
   v   verbosity   change log level, no restart                          ●

 ─────────────────────────────────────────────────────────────────────────
 more  [a] analyze  [c] check setup  [?] help  [q] quit   ●●● safe / caution / disruptive

 ❯ █
```

Every key is a **letter mnemonic from the action's own name** — no numbered
items. Risk is a colored dot down the right edge (green safe, yellow caution,
red disruptive); **heap is the only row with inline text** (`pauses app`, red),
so the one dangerous action is the loudest thing on screen.

The palette is GitHub-dark truecolor with a 16-color fallback (`NO_COLOR`
strips everything), **readability-tuned**: the spec's literal grey ramp reads
as mud on real terminals, so every text tier is lifted about two steps
(descriptions `#b6c2cf`, dim `#9ea7b1`, faint `#8b949e`) and the keys, command
names, and section labels are bold. The hierarchy survives; the squinting
doesn't. The panel fills the terminal up to 120 columns (min 78) — the
description column flexes, risk dots stay pinned to the right edge. Font
*size* is your terminal's setting (⌘+ / Ctrl+ in most emulators); the app
compensates with weight and contrast.

**Key collisions, resolved:** the spec's `t` (threads vs retarget) and `m`
(memory vs mode) clashes are settled as **`g` = target editor** and
**`M` = mode switch** — actions keep the lowercase letters, navigation moves
to `g`/shift. **`H` (heap) is deliberately capital**: lowercase `h` is health,
and the shift is a friction signal for the one app-pausing action.

Utility keys not shown in the footer (a deliberate deviation to keep it to one
line): `i` stage jattach, `p` push in-pod tool, `g` target, `M` mode, `d`
browse — all listed on the `?` help screen. Typed subcommands at the prompt
are not supported in this implementation (single-key only); the jcmd
quick-pick's `t` option accepts any free-typed jcmd string.

## Keys act instantly

Navigation is single-keypress — no Enter. The only deliberate inputs are
**confirmations** and **free-text fields** (a namespace nobody enumerated, a
custom actuator URL). After a command's output, any key returns to the menu.

Disruptive actions use **press-the-same-key-again** confirmation: `H` (heap)
prints *"heap dump pauses the app while it runs — press H again to confirm,
any other key cancels"* and only fires on the second `H`. Quitting asks y/N.

## The tools stay hidden until the target is ready

A capture can never be fired at nothing or at the wrong thing: the action
menu only appears once the target is verified —

- **remote:** cluster answering **and** a specific pod pinned **and** the
  container actually present in that pod's spec
- **in-pod / bare metal:** at least one working route to the JVM (actuator
  answering, or jattach staged)

Until then the menu shows a checklist panel with ✓/✗ per requirement and the
exact key to press for each missing piece (Enter opens the target editor
directly). Readiness is re-checked live — if the pinned pod dies mid-session,
the tools lock again with an explanation instead of failing captures.

The mode chooser (first screen) also offers `u` — run the kit's own test
suite (~10 s, mocked, touches nothing of yours) to prove the install works.

## The header tells you everything — in one line

Line 1: title + mode. Line 2: a single status line — a **live reachability
dot** (green = cluster answering, red + "unreachable — [c] explains why"),
the kube context, `namespace / container · pod` (long pod names truncate to
their unique tail), the actuator port/path, and the `[g] retarget [M] mode`
hints. You always know exactly what a keypress will hit.

## Targeting (`g`) — the field editor

`g` opens an editor where **each field is one keypress**, edited in place:

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
