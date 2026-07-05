# jdebug-tui — the Go (Bubble Tea) frontend

The interactive menu for the jdebug kit, implemented with
[Bubble Tea](https://github.com/charmbracelet/bubbletea) +
[Lipgloss](https://github.com/charmbracelet/lipgloss). It has **feature
parity with the classic bash menu** (`ui/tui.sh`): same keys, same screens,
same readiness gating, same remembered target, same session log.

## Architecture: frontend only

This binary **draws and handles keys — nothing else**. Every action shells
out to the tested bash CLI (`jdebug`) or the in-pod tool (`jdebug-local`).
Capture logic, tier auto-degrade, error translation, and safety gates all
live in the scripts; if the CLI's behavior changes, this frontend inherits it.

Two consequences worth knowing:

- It is a **full-screen (altscreen) dashboard**, but commands execute via
  `tea.ExecProcess`, which drops back to your normal screen: output streams
  to your real terminal and scrollback, tees to `dumps/session-<ts>.log`,
  prints a ✓/✗ verdict, and pauses for a key before the dashboard resumes.
- It reads and writes the same `~/.config/jdebug/target`, so you can switch
  between the frontends freely (`JDEBUG_CLASSIC=1` forces bash).

The dashboard's right column is the **live TARGET panel** (`panel.go`):
pod phase/restarts/last-exit, memory & CPU vs limits (kubectl top + pod
spec), HPA state, and JVM heap used/max read through the pod's actuator —
fetched asynchronously, refreshed every 20 s and after every command. Below
it, the **NEXT box** converts that data into suggested key presses, so the
app brings the mental model instead of demanding one.

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
| `menu.go` | header/status line, gate panels, main menu + two-column layout, action key dispatch, tier/jcmd/level picks |
| `panel.go` | live TARGET panel data fetch/render + the NEXT suggestion engine |
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

- Keys are case-sensitive and mirror the bash menu exactly: `H` (heap) and
  `M` (mode) are deliberately shifted; `g` opens the target editor.
- Disruptive actions confirm by pressing the **same key again**.
- Model methods use value receivers and return the mutated copy — standard
  Elm-architecture style; the only escape hatch is `confirmThen/Else`
  callbacks, which receive `*model` for the post-confirm mutation.
- New screens: add a `screen` constant, a `View` branch, a key handler, and
  a `-render` case + parity assertion in the kit suite.
