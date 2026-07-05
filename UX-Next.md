# UX Next: Interstitial Consistency

## Summary

The current Bubble Tea TUI supports interstitials well, but the app uses several different interstitial patterns at once. Each behavior makes sense locally, but the overall model can feel inconsistent to an operator under pressure.

The goal is to make temporary UI states predictable: detail cards, confirmations, pickers, help, blocked-by views, output, and mouse interactions should share a small set of rules.

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

### 2. Inline overlays

Use inline overlays for small, temporary decisions:

- Confirmations
- Capture route picker
- Log-level picker
- Short command pickers

These should render over the screen that launched them.

Examples:

- Confirm launched from menu -> menu + confirm.
- Confirm launched from editor -> editor + confirm.
- Confirm launched from detail card -> detail + confirm, if that flow exists.

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

## Suggested Tests

Add tests for the interaction contract, not just rendering:

- Confirmation launched from menu renders over menu and returns to menu on cancel.
- Confirmation launched from editor renders over editor and returns to editor on cancel.
- Confirmation launched from picker/detail, if supported, renders over that source screen.
- `esc` cancels/back-outs consistently across confirm, via, detail, blocked, picker, input, output, and wizard.
- Random unrelated keys do not dismiss full-screen interstitials unless the screen explicitly documents that behavior.
- Left-click and keyboard shortcut use the same command path and confirmation gates.
- Right-click opens the same detail card as keyboard detail access.
- TARGET panel `click -> why` remains visibly labeled if it runs immediately.

## Why This Matters

The app is becoming an incident companion, not just a command launcher. As the UI gains transparency cards, blocked-by views, workload/runtime context, captures browsing, and recovery actions, predictable interstitial behavior becomes part of the safety model.

Junior SREs should not have to learn a new dismiss/run rule for every temporary screen.