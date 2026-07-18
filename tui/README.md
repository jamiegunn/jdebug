# jdebug-tui — the Go (Bubble Tea) frontend

The interactive menu for the jdebug kit, implemented with
[Bubble Tea](https://github.com/charmbracelet/bubbletea) +
[Lipgloss](https://github.com/charmbracelet/lipgloss). It is the sole
interactive frontend — it grew out of, and replaced, the retired bash menu
(`ui/tui.sh`): same keys, same screens, same readiness gating, same
remembered target, same session log.

## Architecture: frontend only

This binary **draws and handles keys — nothing else**. Every action shells
out to the tested bash CLI (`jdebug`) or the in-pod tool (`jdebug-local`).
Capture logic, tier auto-degrade, error translation, and safety gates all
live in the scripts; if the CLI's behavior changes, this frontend inherits it.

Two consequences worth knowing:

- It is a **full-screen (altscreen) dashboard** and commands **stream into
  it**: every run (quick reads and the long ones alike) pipes its output
  live into the bottom pane, replacing the log tail while it's held — the
  menu stays interactive, `esc` stops/dismisses, ↑↓ scrolls, the title
  carries the ✓/✗ verdict (`output.go`). Terminals too small for the strip
  get the same stream in a full-screen view. The **wizard** streams its
  narrated steps through the same pane (`wizard.go` → `startPane`) — nothing
  drops to a raw terminal takeover anymore. Every run appends the same
  `$ cmd … ✓/✗` transcript block to `dumps/session-<ts>.log`.
- It reads and writes the same `~/.config/jdebug/target` the CLI uses, so the
  TUI and `jdebug <verb>` always agree on which pod you're aimed at. (This Go
  TUI is the only frontend now — the old bash menu has been retired.)

The layout scales in three tiers (`layout.go`): compact (<104 cols), the
classic menu + 38-col TARGET LIVE sidebar (104–139), and the full grid
(≥140×34, which now **fills the whole terminal width** in three columns — a
slightly wider menu, then two even columns) — menu | TARGET LIVE + NEXT |
PODS + WORKLOAD. The bottom is a **tabbed work
area** — WORK / LOGS / EVENTS / CAPTURES / TRENDS (click a tab or tab/shift-tab)
— filling the remaining height. The LOGS tab polls
`kubectl logs --tail=200` every 5 s (errors and stack traces red, warnings
amber; `f` expands it full-screen with j/k scrollback); events and captures
refresh on the 20 s tick; trend samples piggyback on
every panel fetch. The **NEXT box** converts live data into suggested key
presses, so the app brings the mental model instead of demanding one.
Every fixed-height frame is exact: overlay screens (confirm, jcmd, …)
budget their extra lines so the header never scrolls off.

## Build / run / test

```sh
make tui            # from the repo root; builds tui/jdebug-tui
jdebug              # the CLI prefers the built binary automatically
cd tui && go test ./...   # unit tests (interaction contracts)
```

The kit's main suite (`tests/run-tests.sh`) also builds it, runs `go vet` +
`go test`, asserts screen parity via the `-render` flag, and drives a full
interactive session on a real pty (`tests/pty-drive.py`).

## File map

| file | what it owns |
|---|---|
| `main.go` | flags, program bootstrap, root model + screen state machine, confirm helper |
| `palette.go` | adaptive Lipgloss styles (dark + light variants per token) |
| `backend.go` | kit/config/dumps paths, target load/save (bash-compatible), kubectl enumeration, readiness probes |
| `exec.go` | `ExecProcess` command runner with session-log tee; local jattach staging |
| `menu.go` | header/status line, gate panels, main menu, action key dispatch, tier/jcmd/level picks |
| `layout.go` | size tiers, column/row math, tier-2 grid assembly, overlay budgeting |
| `panel.go` | live TARGET panel data fetch/render + the NEXT suggestion engine |
| `pods.go` | PODS pane: selector/namespace pod list, click-to-retarget, wheel scroll |
| `logs.go` | live log tail: 5 s poll, severity classifier, focus mode |
| `events.go` | kubernetes events pane for the target pod |
| `captures.go` | dumps/ browser pane (name/size/age) |
| `spark.go` | sample ring + sparkline/restart-marker rendering |
| `output.go` | streaming output pane: pipe-fed runner, strip + full-screen views, scroll/stop keys |
| `editor.go` | target editor (`g`), generic picker + text input widgets |
| `wizard.go` | the six guided-diagnosis flows as step queues |
| `chooser.go` | the where-is-the-JVM opening screen (+ `u` self-test) |
| `help.go` | glossary / workflow / safety-rules screen (`?`) |
| `render_demo.go` | `-render <screen>` — canned-state renders for tests, no tty/kubectl needed |

## Theming

Colors are `lipgloss.AdaptiveColor` pairs — GitHub-dark values on dark
backgrounds, GitHub-light on light ones. Detection is automatic (the OSC-11
background query); override with `JDEBUG_THEME=light` or `JDEBUG_THEME=dark`.
Terminals without truecolor degrade to the nearest 256/16 colors; `NO_COLOR`
and non-tty output strip styling entirely.

## Conventions

- Keys are case-sensitive: `H` (heap) and `M` (mode) are deliberately
  shifted; `g` opens the target editor.
- Disruptive actions confirm by pressing the **same key again**.
- Model methods use value receivers and return the mutated copy — standard
  Elm-architecture style; the only escape hatch is `confirmThen/Else`
  callbacks, which receive `*model` for the post-confirm mutation.
- New screens: add a `screen` constant, a `View` branch, a key handler, and
  a `-render` case + parity assertion in the kit suite.
